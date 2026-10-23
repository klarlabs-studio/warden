package scripts_test

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The GitLab CI/CD component cannot be run here, so these tests do the next
// best thing: interpolate the template exactly the way GitLab does (textually,
// `$[[ inputs.name ]]` → value) and execute the resulting job scripts. The
// real-binary scenarios live in e2e/gitlab_ci_test.go; these cover the
// template's own logic with a controlled verifier and no network.

const gitlabTemplatePath = "templates/verify.yml"

var inputRef = regexp.MustCompile(`\$\[\[\s*inputs\.([A-Za-z0-9_-]+)\s*\]\]`)

type glInput struct {
	Default *string  `yaml:"default"`
	Regex   string   `yaml:"regex"`
	Options []string `yaml:"options"`
}

type glJob struct {
	Script       []string `yaml:"script"`
	BeforeScript []string `yaml:"before_script"`
	Artifacts    struct {
		When    string   `yaml:"when"`
		Paths   []string `yaml:"paths"`
		Reports struct {
			Dotenv string `yaml:"dotenv"`
		} `yaml:"reports"`
	} `yaml:"artifacts"`
}

// loadGitLabTemplate splits the component into its spec header and body.
func loadGitLabTemplate(t *testing.T) (map[string]glInput, string) {
	t.Helper()
	raw, err := os.ReadFile(repoFile(t, gitlabTemplatePath))
	if err != nil {
		t.Fatal(err)
	}
	header, body, ok := strings.Cut(string(raw), "\n---\n")
	if !ok {
		t.Fatal("template has no `---` between spec header and body")
	}
	var spec struct {
		Spec struct {
			Inputs map[string]glInput `yaml:"inputs"`
		} `yaml:"spec"`
	}
	if err := yaml.Unmarshal([]byte(header), &spec); err != nil {
		t.Fatalf("spec header: %v", err)
	}
	if len(spec.Spec.Inputs) == 0 {
		t.Fatal("spec declares no inputs")
	}
	return spec.Spec.Inputs, body
}

// interpolateGitLab substitutes inputs the way GitLab does at pipeline
// creation: each reference becomes the given value, or the input's default.
func interpolateGitLab(t *testing.T, overrides map[string]string) map[string]glJob {
	t.Helper()
	inputs, body := loadGitLabTemplate(t)
	for name, val := range overrides {
		in, ok := inputs[name]
		if !ok {
			t.Fatalf("override for undeclared input %q", name)
		}
		if in.Regex != "" && !regexp.MustCompile(in.Regex).MatchString(val) {
			t.Fatalf("GitLab would reject input %s=%q (regex %s)", name, val, in.Regex)
		}
	}
	out := inputRef.ReplaceAllStringFunc(body, func(ref string) string {
		name := inputRef.FindStringSubmatch(ref)[1]
		if v, ok := overrides[name]; ok {
			return v
		}
		if in, ok := inputs[name]; ok && in.Default != nil {
			return *in.Default
		}
		t.Fatalf("reference to input %q, which has no value", name)
		return ""
	})
	jobs := map[string]glJob{}
	if err := yaml.Unmarshal([]byte(out), &jobs); err != nil {
		t.Fatalf("interpolated body: %v", err)
	}
	return jobs
}

func scriptItem(t *testing.T, items []string, marker string) string {
	t.Helper()
	for _, item := range items {
		if strings.Contains(item, "# warden: "+marker+"\n") {
			return item
		}
	}
	t.Fatalf("no script item marked %q", marker)
	return ""
}

func TestGitLabTemplate_InputsAreDeclaredAndUsed(t *testing.T) {
	inputs, body := loadGitLabTemplate(t)
	used := map[string]bool{}
	for _, m := range inputRef.FindAllStringSubmatch(body, -1) {
		used[m[1]] = true
		if _, ok := inputs[m[1]]; !ok {
			t.Errorf("body references undeclared input %q (GitLab refuses the pipeline)", m[1])
		}
	}
	for name, in := range inputs {
		if !used[name] {
			t.Errorf("input %q is declared but never used", name)
		}
		if in.Default == nil {
			t.Errorf("input %q has no default; every input is optional by design", name)
		}
	}
	if strings.Contains(body, "${{") {
		t.Error("GitHub expression syntax in a GitLab template")
	}
}

// Every input lands inside a single-quoted shell string, so its regex is what
// keeps a value from closing the quote or the heredoc. GitLab evaluates input
// regexes with RE2, the same engine as Go's regexp.
func TestGitLabTemplate_InputRegexesFenceTheShell(t *testing.T) {
	inputs, _ := loadGitLabTemplate(t)
	hostile := []string{"'", "a'b", "x\ny", "$(id)", "`id`", "a;b", "a|b", "WARDEN_SCRIPT\n", "a\\b", "\"q\""}
	for name, in := range inputs {
		if name == "stage" || name == "image" {
			continue // YAML scalars, never inside a script
		}
		if in.Regex == "" && len(in.Options) == 0 {
			t.Errorf("input %q reaches a script without a regex or options", name)
			continue
		}
		if in.Regex == "" {
			continue
		}
		re := regexp.MustCompile(in.Regex)
		if !re.MatchString(*in.Default) {
			t.Errorf("input %q: default %q fails its own regex", name, *in.Default)
		}
		for _, v := range hostile {
			if re.MatchString(v) {
				t.Errorf("input %q accepts %q", name, v)
			}
		}
	}
	key := regexp.MustCompile(inputs["key"].Regex)
	for _, ok := range []string{"", "641e75b15812b7b6", "aaaa,bbbb", "RPi3V5DSSTpfFRW5bsnnz86LrxRrbkX2FVcx1rWCdE8=", "$WARDEN_TRUSTED_KEY", "${WARDEN_TRUSTED_KEY}"} {
		if !key.MatchString(ok) {
			t.Errorf("key regex rejects legitimate %q", ok)
		}
	}
}

// The default warden version must name a release that exists.
func TestGitLabTemplate_DefaultVersionIsReleased(t *testing.T) {
	inputs, _ := loadGitLabTemplate(t)
	ver := strings.TrimPrefix(*inputs["warden-version"].Default, "v")
	changelog, err := os.ReadFile(repoFile(t, "CHANGELOG.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(changelog), "\n## ["+ver+"]") {
		t.Fatalf("default warden-version %s is not a released version in CHANGELOG.md", ver)
	}
}

func TestGitLabTemplate_VerdictIsPublishedAsDotenvArtifact(t *testing.T) {
	job := interpolateGitLab(t, nil)["warden-verify"]
	if job.Artifacts.Reports.Dotenv != "warden-verify.env" || job.Artifacts.When != "always" ||
		!reflect.DeepEqual(job.Artifacts.Paths, []string{"warden-verify.env", "warden-plan.json"}) {
		t.Fatalf("verdict must be a dotenv report AND a plain artifact file, kept on failure: %+v", job.Artifacts)
	}
}

func requirePOSIX(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX job scripts")
	}
	for _, tool := range []string{"bash", "git", "sh"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}
}

func runItem(t *testing.T, dir, item string, env ...string) (string, int) {
	t.Helper()
	cmd := exec.Command("sh", "-c", item)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), code
}

// fakeCurl serves a non-cosign blob for the cosign download and fails every
// other request, logging what was asked for.
const fakeCurl = `#!/bin/sh
out=""; url=""
while [ $# -gt 0 ]; do
  case "$1" in -o) out="$2"; shift 2 ;; *) url="$1"; shift ;; esac
done
printf '%s\n' "$url" >> "$CURL_LOG"
case "$url" in */cosign-*) printf 'not cosign' > "$out"; exit 0 ;; esac
exit 22
`

func installFixture(t *testing.T) (binDir, curlLog string, env []string) {
	t.Helper()
	fixtures := t.TempDir()
	if err := os.WriteFile(filepath.Join(fixtures, "curl"), []byte(fakeCurl), 0o700); err != nil {
		t.Fatal(err)
	}
	binDir = t.TempDir()
	curlLog = filepath.Join(t.TempDir(), "curl.log")
	env = []string{
		"PATH=" + fixtures + string(os.PathListSeparator) + os.Getenv("PATH"),
		"WARDEN_BIN_DIR=" + binDir,
		"CURL_LOG=" + curlLog,
		"HOME=" + t.TempDir(),
	}
	return binDir, curlLog, env
}

// GitLab's regex is the first fence; the script's own guard is the second, for
// a GitLab that skips input validation or a template edited by hand.
func TestGitLabTemplate_InstallRefusesMalformedVersions(t *testing.T) {
	requirePOSIX(t)
	_, body := loadGitLabTemplate(t)
	install := ""
	for _, v := range []string{"latest", "1.2", "../../x/y", "0.35.0/../../evil", "v0.35.0 --x"} {
		t.Run(v, func(t *testing.T) {
			// Bypass interpolateGitLab's regex check on purpose: this is the
			// script guard under test.
			jobsYAML := inputRef.ReplaceAllStringFunc(body, func(ref string) string {
				if inputRef.FindStringSubmatch(ref)[1] == "warden-version" {
					return v
				}
				return ""
			})
			var jobs map[string]glJob
			if err := yaml.Unmarshal([]byte(jobsYAML), &jobs); err != nil {
				t.Fatal(err)
			}
			install = scriptItem(t, jobs["warden-verify"].Script, "install")
			binDir, curlLog, env := installFixture(t)
			out, code := runItem(t, t.TempDir(), install, env...)
			if code == 0 || !strings.Contains(out, "refusing version") {
				t.Fatalf("version %q not refused by the guard (exit %d): %s", v, code, out)
			}
			if _, err := os.Stat(curlLog); !os.IsNotExist(err) {
				t.Fatalf("version %q reached the network", v)
			}
			if _, err := os.Stat(filepath.Join(binDir, "warden")); !os.IsNotExist(err) {
				t.Fatal("a warden binary was installed")
			}
		})
	}
}

// cosign is the root of the install's trust chain, so a cosign that does not
// match the pinned digest must stop everything before warden is downloaded.
func TestGitLabTemplate_InstallRefusesTamperedCosign(t *testing.T) {
	requirePOSIX(t)
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("no pinned cosign for this platform")
	}
	install := scriptItem(t, interpolateGitLab(t, nil)["warden-verify"].Script, "install")
	binDir, curlLog, env := installFixture(t)
	out, code := runItem(t, t.TempDir(), install, env...)
	if code == 0 || !strings.Contains(out, "COSIGN DIGEST MISMATCH") || !strings.Contains(out, "NOTHING WAS VERIFIED") {
		t.Fatalf("tampered cosign accepted (exit %d): %s", code, out)
	}
	log, err := os.ReadFile(curlLog)
	if err != nil {
		t.Fatal(err)
	}
	if lines := strings.Split(strings.TrimSpace(string(log)), "\n"); len(lines) != 1 || !strings.Contains(lines[0], "sigstore/cosign/releases/download/v3.0.6/") {
		t.Fatalf("expected exactly the cosign download, got %q", lines)
	}
	if _, err := os.Stat(filepath.Join(binDir, "warden")); !os.IsNotExist(err) {
		t.Fatal("a warden binary was installed")
	}
}

// Downloads a real release and runs the full chain: pinned cosign,
// keyless signature over checksums.txt, archive digest. Opt-in, because it
// needs github.com and Sigstore's TUF repository. Defaults to the component
// version; release-preparation CI can name an already published version with
// WARDEN_GITLAB_INSTALL_VERSION, avoiding a dependency on its unpublished tag.
func TestGitLabTemplate_InstallRealRelease(t *testing.T) {
	if os.Getenv("WARDEN_GITLAB_INSTALL_NETWORK") == "" {
		t.Skip("set WARDEN_GITLAB_INSTALL_NETWORK=1 to install the real release")
	}
	requirePOSIX(t)
	inputs, _ := loadGitLabTemplate(t)
	version := os.Getenv("WARDEN_GITLAB_INSTALL_VERSION")
	if version == "" {
		version = *inputs["warden-version"].Default
	}
	install := scriptItem(t, interpolateGitLab(t, map[string]string{"warden-version": version})["warden-verify"].Script, "install")
	bin := t.TempDir()
	out, code := runItem(t, t.TempDir(), install, "WARDEN_BIN_DIR="+bin, "HOME="+t.TempDir())
	if code != 0 {
		t.Fatalf("install failed (exit %d): %s", code, out)
	}
	got, err := exec.Command(filepath.Join(bin, "warden"), "--version").CombinedOutput()
	if err != nil || !strings.Contains(string(got), strings.TrimPrefix(version, "v")) {
		t.Fatalf("installed binary reports %q (%v)", got, err)
	}
}

// verifyFixture is a GitLab-shaped checkout: a clone whose origin publishes a
// refs/notes/warden ref (unless withNotes is false), with a mock verifier.
type verifyFixture struct {
	t                  *testing.T
	checkout, bin, sha string
	argsFile           string
}

func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_AUTHOR_NAME=warden-ci", "GIT_AUTHOR_EMAIL=warden-ci",
		"GIT_COMMITTER_NAME=warden-ci", "GIT_COMMITTER_EMAIL=warden-ci")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newVerifyFixture(t *testing.T, withNotes bool) *verifyFixture {
	t.Helper()
	root := t.TempDir()
	remote, src, checkout := filepath.Join(root, "remote.git"), filepath.Join(root, "src"), filepath.Join(root, "checkout")
	gitRun(t, root, "init", "-q", "--bare", remote)
	gitRun(t, root, "init", "-q", src)
	gitRun(t, src, "commit", "-q", "--allow-empty", "-m", "c1")
	gitRun(t, src, "push", "-q", remote, "HEAD:refs/heads/main")
	if withNotes {
		gitRun(t, src, "notes", "--ref=warden", "add", "-m", "{}", "HEAD")
		gitRun(t, src, "push", "-q", remote, "refs/notes/warden")
	}
	gitRun(t, root, "clone", "-q", "--depth", "1", "--branch", "main", "file://"+remote, checkout)
	f := &verifyFixture{t: t, checkout: checkout, bin: t.TempDir(), sha: gitRun(t, checkout, "rev-parse", "HEAD")}
	f.argsFile = filepath.Join(t.TempDir(), "args")
	mock := "#!/bin/sh\nprintf '%s\\000' \"$@\" > \"$VERIFY_ARGS\"\nexit \"${VERIFY_STATUS:-0}\"\n"
	if err := os.WriteFile(filepath.Join(f.bin, "warden"), []byte(mock), 0o700); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *verifyFixture) run(overrides map[string]string, env ...string) (string, int) {
	f.t.Helper()
	item := scriptItem(f.t, interpolateGitLab(f.t, overrides)["warden-verify"].Script, "verify")
	env = append([]string{
		"WARDEN_BIN_DIR=" + f.bin,
		"CI_PROJECT_DIR=" + f.checkout,
		"VERIFY_ARGS=" + f.argsFile,
		"HOME=" + f.t.TempDir(),
	}, env...)
	return runItem(f.t, f.checkout, item, env...)
}

func (f *verifyFixture) verdict() map[string]string {
	f.t.Helper()
	file, err := os.Open(filepath.Join(f.checkout, "warden-verify.env"))
	if err != nil {
		f.t.Fatalf("no verdict file: %v", err)
	}
	defer func() { _ = file.Close() }()
	got := map[string]string{}
	sc := bufio.NewScanner(file)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			f.t.Fatalf("not dotenv: %q", sc.Text())
		}
		got[k] = v
	}
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, []string{"WARDEN_VALIDATED", "WARDEN_VERDICT", "WARDEN_VERIFIED_COMMIT"}) {
		f.t.Fatalf("verdict keys %v", keys)
	}
	return got
}

func (f *verifyFixture) args() []string {
	f.t.Helper()
	data, err := os.ReadFile(f.argsFile)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		f.t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00")
}

func TestGitLabTemplate_VerifyVerdicts(t *testing.T) {
	requirePOSIX(t)
	const pin = "641e75b15812b7b6"
	for _, tt := range []struct {
		name      string
		inputs    map[string]string
		env       []string
		notes     bool
		exit      int
		validated string
		verdict   string
		called    bool
	}{
		{"no key fails closed", nil, nil, true, 1, "false", "no-key", false},
		{"no key with run-checks", map[string]string{"on-unverified": "run-checks"}, nil, true, 0, "false", "no-key", false},
		{"separators only", map[string]string{"key": " , ,"}, nil, true, 1, "false", "no-key", false},
		{"validated", map[string]string{"key": pin}, nil, true, 0, "true", "validated", true},
		{"unverified", map[string]string{"key": pin}, []string{"VERIFY_STATUS=1"}, true, 1, "false", "unverified", true},
		{"unverified with run-checks", map[string]string{"key": pin, "on-unverified": "run-checks"}, []string{"VERIFY_STATUS=1"}, true, 0, "false", "unverified", true},
		{"verifier error is not a verdict", map[string]string{"key": pin, "on-unverified": "run-checks"}, []string{"VERIFY_STATUS=2"}, true, 2, "false", "error", true},
		{"remote publishes no notes", map[string]string{"key": pin}, nil, false, 1, "false", "no-notes", false},
		{"key from a variable", map[string]string{"key": "$PINNED_KEY"}, []string{"PINNED_KEY=" + pin}, true, 0, "true", "validated", true},
		{"variable that is empty", map[string]string{"key": "${PINNED_KEY}"}, []string{"PINNED_KEY="}, true, 1, "false", "no-key", false},
		{"commit from a variable", map[string]string{"key": pin, "commit": "$CI_COMMIT_SHA"}, nil, true, 0, "true", "validated", true},
		{"variable with shell in it", map[string]string{"key": "$PINNED_KEY"}, []string{"PINNED_KEY=$(touch injected)"}, true, 2, "false", "error", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := newVerifyFixture(t, tt.notes)
			env := append([]string{"CI_COMMIT_SHA=" + f.sha}, tt.env...)
			out, code := f.run(tt.inputs, env...)
			if code != tt.exit {
				t.Fatalf("exit %d, want %d: %s", code, tt.exit, out)
			}
			v := f.verdict()
			if v["WARDEN_VALIDATED"] != tt.validated || v["WARDEN_VERDICT"] != tt.verdict {
				t.Fatalf("verdict %v, want %s/%s: %s", v, tt.validated, tt.verdict, out)
			}
			// A configuration error found before the commit was resolved names
			// no commit at all — never a wrong one.
			wantCommit := f.sha
			if tt.verdict == "error" && !tt.called {
				wantCommit = ""
			}
			if v["WARDEN_VERIFIED_COMMIT"] != wantCommit {
				t.Fatalf("verdict names %q, want %q", v["WARDEN_VERIFIED_COMMIT"], wantCommit)
			}
			args := f.args()
			if !tt.called {
				if args != nil {
					t.Fatalf("verifier ran: %q", args)
				}
				return
			}
			want := []string{"verify", "--commit", f.sha, "--key", pin}
			if !reflect.DeepEqual(args, want) {
				t.Fatalf("arguments %q, want %q", args, want)
			}
			if _, err := os.Stat(filepath.Join(f.checkout, "injected")); !os.IsNotExist(err) {
				t.Fatal("input executed as shell")
			}
		})
	}
}

// A pipeline variable outranks a job variable in GitLab. The pinned literal
// is interpolated into the script, so no variable of any name can replace it.
func TestGitLabTemplate_PinnedLiteralsIgnoreVariables(t *testing.T) {
	requirePOSIX(t)
	f := newVerifyFixture(t, true)
	out, code := f.run(map[string]string{"key": "641e75b15812b7b6"},
		"WARDEN_TRUSTED_KEYS=attacker", "WARDEN_COMMIT=HEAD~5", "WARDEN_ON_UNVERIFIED=run-checks", "VERIFY_STATUS=1")
	if code != 1 {
		t.Fatalf("a variable changed the mode (exit %d): %s", code, out)
	}
	if args := f.args(); !reflect.DeepEqual(args, []string{"verify", "--commit", f.sha, "--key", "641e75b15812b7b6"}) {
		t.Fatalf("a variable changed the arguments: %q", args)
	}
}

func TestGitLabTemplate_RequirementsMustComeFromOutsideTheCheckout(t *testing.T) {
	requirePOSIX(t)
	f := newVerifyFixture(t, true)
	inside := filepath.Join(f.checkout, "requirements.json")
	if err := os.WriteFile(inside, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, code := f.run(map[string]string{"key": "641e75b15812b7b6", "requirements": "$REQ"}, "REQ="+inside)
	if code != 2 || !strings.Contains(out, "inside the checkout") || f.args() != nil {
		t.Fatalf("candidate-supplied requirements accepted (exit %d): %s", code, out)
	}
	// A symlink outside the checkout that points into it is still the candidate's.
	link := filepath.Join(t.TempDir(), "req.json")
	if err := os.Symlink(inside, link); err != nil {
		t.Fatal(err)
	}
	if out, code := f.run(map[string]string{"key": "641e75b15812b7b6", "requirements": "$REQ"}, "REQ="+link); code != 2 {
		t.Fatalf("symlinked candidate requirements accepted (exit %d): %s", code, out)
	}

	outside := filepath.Join(t.TempDir(), "protected requirements.json")
	if err := os.WriteFile(outside, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	image := "sha256:" + strings.Repeat("a", 64)
	out, code = f.run(map[string]string{
		"key": "641e75b15812b7b6", "requirements": "$REQ", "container-steps": "lint,test", "container-image-id": image,
	}, "REQ="+outside)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	want := []string{"verify", "--commit", f.sha, "--key", "641e75b15812b7b6", "--requirements", outside,
		"--container-steps", "lint,test", "--container-image-id", image}
	if args := f.args(); !reflect.DeepEqual(args, want) {
		t.Fatalf("arguments %q, want %q", args, want)
	}
}

// With GIT_STRATEGY: fetch the build directory is reused. A notes ref left by
// an earlier job must not stand in for what the remote publishes, and a
// tracked symlink named like the verdict file must not redirect the write.
func TestGitLabTemplate_VerifyIgnoresLeftovers(t *testing.T) {
	requirePOSIX(t)
	f := newVerifyFixture(t, false)
	gitRun(t, f.checkout, "notes", "--ref=warden", "add", "-m", "stale", "HEAD")
	target := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.WriteFile(target, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(f.checkout, "warden-verify.env")); err != nil {
		t.Fatal(err)
	}
	out, code := f.run(map[string]string{"key": "641e75b15812b7b6"})
	if code != 1 || f.verdict()["WARDEN_VERDICT"] != "no-notes" || f.args() != nil {
		t.Fatalf("stale local note was used (exit %d): %s", code, out)
	}
	if data, _ := os.ReadFile(target); string(data) != "untouched" {
		t.Fatal("verdict written through a symlink")
	}
}

func TestGitLabTemplate_SkipHelper(t *testing.T) {
	requirePOSIX(t)
	jobs := interpolateGitLab(t, nil)
	helper := scriptItem(t, jobs[".warden-skip-if-verified"].BeforeScript, "skip-if-verified")
	// The runner runs before_script and script in one shell; RAN marks script.
	program := helper + "\necho RAN-CHECKS\n"
	for _, tt := range []struct {
		name  string
		setup func(t *testing.T, dir, head string)
		skip  bool
	}{
		{"no verdict", func(*testing.T, string, string) {}, false},
		{"validated for this commit", func(t *testing.T, dir, head string) {
			writeVerdict(t, dir, "true", head)
		}, true},
		{"validated for another commit", func(t *testing.T, dir, _ string) {
			writeVerdict(t, dir, "true", strings.Repeat("0", 40))
		}, false},
		{"not validated", func(t *testing.T, dir, head string) {
			writeVerdict(t, dir, "false", head)
		}, false},
		{"verdict committed by the candidate", func(t *testing.T, dir, head string) {
			writeVerdict(t, dir, "true", head)
			gitRun(t, dir, "add", "warden-verify.env")
		}, false},
		{"verdict is a symlink", func(t *testing.T, dir, head string) {
			real := filepath.Join(t.TempDir(), "v.env")
			writeVerdict(t, filepath.Dir(real), "true", head)
			if err := os.Rename(filepath.Join(filepath.Dir(real), "warden-verify.env"), real); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, filepath.Join(dir, "warden-verify.env")); err != nil {
				t.Fatal(err)
			}
		}, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			gitRun(t, dir, "init", "-q")
			gitRun(t, dir, "commit", "-q", "--allow-empty", "-m", "c")
			head := gitRun(t, dir, "rev-parse", "HEAD")
			tt.setup(t, dir, head)
			// A forged variable must not matter: the helper reads the artifact.
			out, code := runItem(t, dir, program, "WARDEN_VALIDATED=true", "CI_COMMIT_SHA="+head)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, out)
			}
			if skipped := !strings.Contains(out, "RAN-CHECKS"); skipped != tt.skip {
				t.Fatalf("skipped=%v, want %v: %s", skipped, tt.skip, out)
			}
		})
	}
}

func writeVerdict(t *testing.T, dir, validated, sha string) {
	t.Helper()
	body := "WARDEN_VALIDATED=" + validated + "\nWARDEN_VERDICT=x\nWARDEN_VERIFIED_COMMIT=" + sha + "\n"
	if err := os.WriteFile(filepath.Join(dir, "warden-verify.env"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// min-profile is a fixed-text input: its regex admits only a known level with
// an optional version, the job re-checks it, and it reaches the verifier as one
// argument. A verifier that rejects the flag (exit 2) is a configuration error,
// never a verdict.
func TestGitLabTemplate_MinProfile(t *testing.T) {
	requirePOSIX(t)
	inputs, _ := loadGitLabTemplate(t)
	re := regexp.MustCompile(inputs["min-profile"].Regex)
	for _, ok := range []string{"", "permissive", "standard", "strict", "strict@1", "standard@12"} {
		if !re.MatchString(ok) {
			t.Errorf("min-profile regex rejects legitimate %q", ok)
		}
	}
	for _, bad := range []string{"paranoid", "strict@0", "strict@", "@1", "Strict", "strict@1@2", "strict ", "$MIN_PROFILE"} {
		if re.MatchString(bad) {
			t.Errorf("min-profile regex accepts %q", bad)
		}
	}

	const pin = "641e75b15812b7b6"
	f := newVerifyFixture(t, true)
	out, code := f.run(map[string]string{"key": pin, "min-profile": "strict@1"})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, out)
	}
	if args, want := f.args(), []string{"verify", "--commit", f.sha, "--key", pin, "--min-profile", "strict@1"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("arguments %q, want %q", args, want)
	}
	if v := f.verdict(); v["WARDEN_VALIDATED"] != "true" {
		t.Fatalf("verdict %v", v)
	}

	f = newVerifyFixture(t, true)
	out, code = f.run(map[string]string{"key": pin, "min-profile": "strict", "on-unverified": "run-checks"}, "VERIFY_STATUS=2")
	if v := f.verdict(); code != 2 || v["WARDEN_VALIDATED"] != "false" || v["WARDEN_VERDICT"] != "error" {
		t.Fatalf("a verifier that cannot check the profile must be a configuration error (exit %d, %v): %s", code, v, out)
	}

	f = newVerifyFixture(t, true)
	if _, code := f.run(map[string]string{"key": pin}); code != 0 {
		t.Fatalf("exit %d", code)
	}
	for _, a := range f.args() {
		if a == "--min-profile" {
			t.Fatal("an empty min-profile must not pass the flag")
		}
	}
}
