package forge

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// The tests below drive GH through a fake `gh` placed first on PATH rather than
// through an injected function. GH's whole job is to build correct argv for a
// subprocess and interpret its exit code, so a seam that replaced exec would
// stub out precisely the behavior under test: that `--base` is omitted when
// empty, that a body travels on stdin and not in argv, that a non-zero exit is
// read as data rather than failure. The fake records argv and stdin, so those
// are assertable.
//
// The fake is a shell script, so these are unix-only. warden is unix-first
// (native git hooks, unix-socket attach; CI sets cross-platform: false), so
// this costs no real coverage — but skip rather than fail if someone runs the
// suite elsewhere.

const fakeGH = `#!/bin/sh
# Fake gh. Records argv (one line per invocation, NUL-free) and any stdin, then
# replays a canned exit code + stdout selected by the subcommand.
printf '%s\n' "$*" >> "$GH_LOG"
case "$2" in
view)
  [ -n "$GH_VIEW_OUT" ] && printf '%s\n' "$GH_VIEW_OUT"
  exit "${GH_VIEW_EXIT:-0}"
  ;;
create)
  [ -n "$GH_CREATE_OUT" ] && printf '%s\n' "$GH_CREATE_OUT"
  exit "${GH_CREATE_EXIT:-0}"
  ;;
comment)
  cat >> "$GH_STDIN_LOG"
  # --edit-last is the sticky-comment attempt; a repo with no prior comment
  # fails it and the caller retries without the flag.
  case "$*" in
  *--edit-last*) exit "${GH_EDIT_EXIT:-0}" ;;
  *) exit "${GH_POST_EXIT:-0}" ;;
  esac
  ;;
checks)
  [ -n "$GH_CHECKS_OUT" ] && printf '%s\n' "$GH_CHECKS_OUT"
  exit "${GH_CHECKS_EXIT:-0}"
  ;;
esac
exit 0
`

type ghFake struct {
	t        *testing.T
	logPath  string
	stdinLog string
}

// installFakeGH puts a fake `gh` first on PATH for the duration of the test and
// returns a handle for reading back what it was called with.
func installFakeGH(t *testing.T) *ghFake {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake gh is a shell script; warden is unix-first")
	}
	bin := t.TempDir()
	script := filepath.Join(bin, "gh")
	if err := os.WriteFile(script, []byte(fakeGH), 0o755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatalf("write fake gh: %v", err)
	}
	f := &ghFake{
		t:        t,
		logPath:  filepath.Join(bin, "argv.log"),
		stdinLog: filepath.Join(bin, "stdin.log"),
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("GH_LOG", f.logPath)
	t.Setenv("GH_STDIN_LOG", f.stdinLog)
	// Neutral defaults; each test overrides only what it exercises. Setting them
	// here (rather than relying on absence) keeps one test's env from leaking
	// into the next through the parent process.
	for _, k := range []string{
		"GH_VIEW_OUT", "GH_VIEW_EXIT", "GH_CREATE_OUT", "GH_CREATE_EXIT",
		"GH_EDIT_EXIT", "GH_POST_EXIT", "GH_CHECKS_OUT", "GH_CHECKS_EXIT",
	} {
		t.Setenv(k, "")
	}
	return f
}

// calls returns one entry per gh invocation, in order.
func (f *ghFake) calls() []string {
	f.t.Helper()
	b, err := os.ReadFile(f.logPath)
	if err != nil {
		return nil // never invoked
	}
	var out []string
	for _, l := range strings.Split(strings.TrimRight(string(b), "\n"), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func (f *ghFake) stdin() string {
	f.t.Helper()
	b, err := os.ReadFile(f.stdinLog)
	if err != nil {
		return ""
	}
	return string(b)
}

func TestAvailable(t *testing.T) {
	t.Run("gh on PATH", func(t *testing.T) {
		installFakeGH(t)
		if !NewGH(t.TempDir()).Available() {
			t.Error("Available() = false with gh on PATH, want true")
		}
	})

	t.Run("gh absent", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("unix-first")
		}
		// An empty PATH is the "gh not installed" case: PR creation is skipped
		// rather than failing the run.
		t.Setenv("PATH", t.TempDir())
		if NewGH(t.TempDir()).Available() {
			t.Error("Available() = true with empty PATH, want false")
		}
	})
}

func TestEnsurePRExisting(t *testing.T) {
	f := installFakeGH(t)
	t.Setenv("GH_VIEW_OUT", `{"url":"https://github.com/o/r/pull/7","number":7}`)

	got, err := NewGH(t.TempDir()).EnsurePR(context.Background(), "feat/x", "main")
	if err != nil {
		t.Fatalf("EnsurePR: %v", err)
	}
	want := domain.PRInfo{URL: "https://github.com/o/r/pull/7", Number: 7, Created: false}
	if got != want {
		t.Errorf("EnsurePR = %+v, want %+v", got, want)
	}
	// An existing PR must not trigger a create.
	if calls := f.calls(); len(calls) != 1 || !strings.Contains(calls[0], "pr view") {
		t.Errorf("calls = %q, want a single `pr view`", calls)
	}
}

func TestEnsurePRCreates(t *testing.T) {
	cases := []struct {
		name     string
		base     string
		viewOut  string
		viewExit string
		wantBase bool
	}{
		{
			name: "no existing PR", base: "main",
			viewExit: "1", wantBase: true,
		},
		{
			// `gh pr view` can exit 0 yet describe no PR. Trusting the exit code
			// alone would return a zero-valued PRInfo and never create anything.
			name: "view succeeds but reports no url", base: "main",
			viewOut: `{"url":"","number":0}`, wantBase: true,
		},
		{
			name: "view returns unparseable json", base: "main",
			viewOut: "not json at all", wantBase: true,
		},
		{
			// Empty base means "the forge's default branch": --base must be
			// omitted entirely, not passed empty.
			name: "empty base omits the flag", base: "",
			viewExit: "1", wantBase: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := installFakeGH(t)
			t.Setenv("GH_VIEW_OUT", c.viewOut)
			t.Setenv("GH_VIEW_EXIT", c.viewExit)
			t.Setenv("GH_CREATE_OUT", "Creating pull request for feat/x into main\nhttps://github.com/o/r/pull/12")

			got, err := NewGH(t.TempDir()).EnsurePR(context.Background(), "feat/x", c.base)
			if err != nil {
				t.Fatalf("EnsurePR: %v", err)
			}
			want := domain.PRInfo{URL: "https://github.com/o/r/pull/12", Created: true}
			if got != want {
				t.Errorf("EnsurePR = %+v, want %+v", got, want)
			}

			calls := f.calls()
			if len(calls) != 2 {
				t.Fatalf("calls = %q, want view then create", calls)
			}
			create := calls[1]
			if !strings.Contains(create, "pr create") || !strings.Contains(create, "--fill") {
				t.Errorf("create call = %q, want `pr create --fill`", create)
			}
			if gotBase := strings.Contains(create, "--base"); gotBase != c.wantBase {
				t.Errorf("--base present = %v, want %v (call %q)", gotBase, c.wantBase, create)
			}
		})
	}
}

func TestEnsurePRCreateFails(t *testing.T) {
	installFakeGH(t)
	t.Setenv("GH_VIEW_EXIT", "1")
	t.Setenv("GH_CREATE_EXIT", "1")

	got, err := NewGH(t.TempDir()).EnsurePR(context.Background(), "feat/x", "main")
	if err == nil {
		t.Fatal("EnsurePR = nil error on a failing `gh pr create`, want an error")
	}
	if got != (domain.PRInfo{}) {
		t.Errorf("EnsurePR = %+v on failure, want zero PRInfo", got)
	}
}

func TestCommentStickyEdit(t *testing.T) {
	f := installFakeGH(t)

	if err := NewGH(t.TempDir()).Comment(context.Background(), "feat/x", "gate passed"); err != nil {
		t.Fatalf("Comment: %v", err)
	}
	calls := f.calls()
	if len(calls) != 1 {
		t.Fatalf("calls = %q, want a single --edit-last comment", calls)
	}
	if !strings.Contains(calls[0], "--edit-last") {
		t.Errorf("call = %q, want --edit-last", calls[0])
	}
	// The body must travel on stdin: argv is visible in process listings and a
	// multi-line markdown body with shell metacharacters must not be re-quoted.
	if !strings.Contains(calls[0], "--body-file -") {
		t.Errorf("call = %q, want `--body-file -`", calls[0])
	}
	if got := f.stdin(); got != "gate passed" {
		t.Errorf("stdin = %q, want the comment body", got)
	}
}

func TestCommentFallsBackToFreshPost(t *testing.T) {
	f := installFakeGH(t)
	// No prior comment to edit: --edit-last fails and a fresh one is posted.
	t.Setenv("GH_EDIT_EXIT", "1")

	body := "gate passed\n\n| step | result |\n|---|---|\n| lint | ok |"
	if err := NewGH(t.TempDir()).Comment(context.Background(), "feat/x", body); err != nil {
		t.Fatalf("Comment: %v", err)
	}
	calls := f.calls()
	if len(calls) != 2 {
		t.Fatalf("calls = %q, want the edit attempt then a fresh post", calls)
	}
	if !strings.Contains(calls[0], "--edit-last") {
		t.Errorf("first call = %q, want the --edit-last attempt", calls[0])
	}
	if strings.Contains(calls[1], "--edit-last") {
		t.Errorf("second call = %q, want a fresh post without --edit-last", calls[1])
	}
	// Multi-line markdown must survive both attempts intact.
	if got := f.stdin(); got != body+body {
		t.Errorf("stdin = %q, want the body delivered to both attempts", got)
	}
}

func TestCommentReturnsErrorWhenBothAttemptsFail(t *testing.T) {
	installFakeGH(t)
	t.Setenv("GH_EDIT_EXIT", "1")
	t.Setenv("GH_POST_EXIT", "1")

	if err := NewGH(t.TempDir()).Comment(context.Background(), "feat/x", "body"); err == nil {
		t.Error("Comment = nil error when both attempts fail, want an error")
	}
}

func TestChecks(t *testing.T) {
	cases := []struct {
		name     string
		out      string
		exit     string
		want     domain.CIState
		wantPass int
		wantFail int
	}{
		{
			name: "all passing", out: `[{"state":"SUCCESS"},{"state":"SUCCESS"}]`,
			want: domain.CIPassing, wantPass: 2,
		},
		{
			// `gh pr checks` exits non-zero precisely when checks are failing or
			// pending — the case callers most need reported. Honoring the exit
			// code would turn every red branch into "no checks".
			name: "failing check exits non-zero", out: `[{"state":"SUCCESS"},{"state":"FAILURE"}]`,
			exit: "1", want: domain.CIFailing, wantPass: 1, wantFail: 1,
		},
		{
			name: "pending exits non-zero", out: `[{"state":"IN_PROGRESS"}]`,
			exit: "8", want: domain.CIPending,
		},
		{name: "no output", out: "", want: domain.CINone},
		{name: "unparseable output", out: "gh: not logged in", want: domain.CINone},
		{name: "empty array", out: `[]`, want: domain.CINone},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			installFakeGH(t)
			t.Setenv("GH_CHECKS_OUT", c.out)
			t.Setenv("GH_CHECKS_EXIT", c.exit)

			got, err := NewGH(t.TempDir()).Checks(context.Background(), "feat/x")
			if err != nil {
				t.Fatalf("Checks: %v", err)
			}
			if got.State != c.want {
				t.Errorf("Checks state = %s, want %s", got.State, c.want)
			}
			if got.Passed != c.wantPass || got.Failed != c.wantFail {
				t.Errorf("Checks = %+v, want passed=%d failed=%d", got, c.wantPass, c.wantFail)
			}
		})
	}
}

// TestRunsInRepoDir pins the reason GH carries a dir at all: gh resolves the
// remote and its auth from the working directory, so running it anywhere else
// would target whatever repo the developer's shell happened to be in.
func TestRunsInRepoDir(t *testing.T) {
	installFakeGH(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "cwd.txt")
	// Have the fake record its own working directory.
	t.Setenv("GH_CHECKS_OUT", "")
	script := "#!/bin/sh\npwd > " + marker + "\nexit 0\n"
	ghPath := filepath.Join(strings.SplitN(os.Getenv("PATH"), string(os.PathListSeparator), 2)[0], "gh")
	if err := os.WriteFile(ghPath, []byte(script), 0o755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatalf("rewrite fake gh: %v", err)
	}

	if _, err := NewGH(dir).Checks(context.Background(), "feat/x"); err != nil {
		t.Fatalf("Checks: %v", err)
	}
	b, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("fake gh recorded no cwd: %v", err)
	}
	// macOS reports /private/var for /var, so compare resolved paths.
	wantDir, _ := filepath.EvalSymlinks(dir)
	gotDir, _ := filepath.EvalSymlinks(strings.TrimSpace(string(b)))
	if gotDir != wantDir {
		t.Errorf("gh ran in %q, want the repo dir %q", gotDir, wantDir)
	}
}
