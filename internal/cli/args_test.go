package cli

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A command must never silently answer a question other than the one asked.
//
// `warden verify <sha>` used to parse cleanly, verify HEAD, and print a PASS
// naming a commit the caller never mentioned. Every flag-parsing command here
// had the same hole. These cases pin the refusal for each of them, so a command
// added later without the guard fails here rather than in someone's CI log.
func TestCommands_RejectPositionalArguments(t *testing.T) {
	// A repo is needed so the failure under test is the ARGUMENT check and not an
	// earlier "not a git repo" error — the guard must fire before any work.
	gitRepo(t)

	cases := []struct {
		name string
		args []string
		// want is a fragment of the suggestion the message must carry.
		want string
	}{
		{"verify", []string{"verify", "abc123"}, "--commit abc123"},
		{"attest", []string{"attest", "abc123"}, "--commit abc123"},
		{"reattest", []string{"reattest", "abc123"}, "--commit abc123"},
		{"doctor", []string{"doctor", "main"}, "--branch main"},
		{"audit", []string{"audit", "main"}, "--branch main"},
		{"ci", []string{"ci", "stray"}, "no positional arguments"},
		{"import", []string{"import", "stray"}, "no positional arguments"},
		{"policy explain", []string{"policy", "explain", "stray"}, "no positional arguments"},
		{"axi verify", []string{"axi", "verify", "abc123"}, "--commit abc123"},
		{"axi verify-range", []string{"axi", "verify-range", "a..b"}, "--range a..b"},
		{"axi audit", []string{"axi", "audit", "main"}, "--branch main"},
		{"axi policy-explain", []string{"axi", "policy-explain", "x"}, "no positional arguments"},
		{"axi run-trigger", []string{"axi", "run-trigger", "x"}, "no positional arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, errb := run(tc.args...)
			if code != 2 {
				t.Errorf("%v: exit = %d, want 2 (a refusal, not an answer about something else)", tc.args, code)
			}
			if !strings.Contains(errb, tc.want) {
				t.Errorf("%v: stderr = %q, want it to contain %q", tc.args, errb, tc.want)
			}
		})
	}
}

// `warden init` is the one command a user runs before the repo is set up, so it
// is checked separately — it must refuse a stray argument without needing any
// prior state.
func TestInit_RejectsPositionalArguments(t *testing.T) {
	gitRepo(t)
	code, _, errb := run("init", "stray")
	if code != 2 || !strings.Contains(errb, "no positional arguments") {
		t.Errorf("init with a stray argument: code=%d err=%q", code, errb)
	}
}

// `fleet status` is the deliberate exception: its positional arguments ARE the
// repositories to survey. Guarding it would break the documented usage, so this
// asserts it still accepts them.
func TestFleetStatus_StillAcceptsPositionalPaths(t *testing.T) {
	dir := gitRepo(t)
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "placeholder"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ := run("fleet", "status", dir, other)
	if code == 2 {
		t.Fatalf("fleet status must accept positional paths, got exit 2: %q", out)
	}
	if !strings.Contains(out, "repos gated") {
		t.Errorf("fleet status did not survey the given paths: %q", out)
	}
}

// The suggestion exists to turn a refusal into a fix: the mistake is nearly
// always "I put the value where the flag goes".
func TestRejectExtraArgs_NamesTheLikelyFlag(t *testing.T) {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	var buf bytes.Buffer
	fs.SetOutput(&buf)
	if err := fs.Parse([]string{"deadbeef"}); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	if !rejectExtraArgs(fs, &stderr, "verify", "commit") {
		t.Fatal("a leftover argument must be rejected")
	}
	if got := stderr.String(); !strings.Contains(got, "--commit deadbeef") {
		t.Errorf("message must name the flag and echo the value, got %q", got)
	}
}

// With nothing left over it must stay out of the way — the guard runs on every
// invocation, including every correct one.
func TestRejectExtraArgs_SilentWhenThereIsNothingExtra(t *testing.T) {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if rejectExtraArgs(fs, &stderr, "verify", "commit") {
		t.Error("no positional arguments must not be a rejection")
	}
	if stderr.Len() != 0 {
		t.Errorf("nothing should be printed, got %q", stderr.String())
	}
}
