package cli

import (
	"bytes"
	"strings"
	"testing"
)

// TestCmdAttach_NoLiveRun pins the common case: someone types `warden attach`
// when nothing is running. That is not an error in the command — it is an answer
// — so it must name the state and say how to produce one, rather than surface
// the socket's connection failure.
func TestCmdAttach_NoLiveRun(t *testing.T) {
	repoWithConfig(t, "")

	var out, errb bytes.Buffer
	code := cmdAttach(nil, &out, &errb)
	if code != 1 {
		t.Fatalf("code = %d, want 1; out=%q err=%q", code, out.String(), errb.String())
	}
	if !strings.Contains(errb.String(), "no live run to attach to") {
		t.Errorf("stderr should name the state, got %q", errb.String())
	}
	if !strings.Contains(errb.String(), "warden run pre-push") {
		t.Errorf("stderr should say how to start one, got %q", errb.String())
	}
}

// TestCmdAttach_OutsideRepo pins that attach fails as a repo error, not as
// "no live run": there is no gate to attach to because there is no repo, and
// conflating the two would send the reader looking for a run.
func TestCmdAttach_OutsideRepo(t *testing.T) {
	chdir(t, t.TempDir())

	var out, errb bytes.Buffer
	if code := cmdAttach(nil, &out, &errb); code == 0 {
		t.Fatalf("attach outside a repo must fail; out=%q", out.String())
	}
	if strings.Contains(errb.String(), "no live run") {
		t.Errorf("a missing repo must not be reported as a missing run: %q", errb.String())
	}
}
