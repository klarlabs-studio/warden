package cli

import (
	"bytes"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
)

// Exit 1 already means two different things on pre-push (passed-and-pushed, and
// failed), so it can carry no more information. A wrapper that retries on a lock
// must not also retry a rejected change, and must not retry a checkout that was
// never set up — hence three distinct codes.
func TestExitForBlocker(t *testing.T) {
	cases := []struct {
		blocker domain.Blocker
		want    int
	}{
		{domain.BlockerNone, 1},
		{domain.BlockerContention, exitContention},
		{domain.BlockerEnvironment, exitEnvironment},
	}
	for _, tc := range cases {
		if got := exitForBlocker(tc.blocker); got != tc.want {
			t.Errorf("exitForBlocker(%q) = %d, want %d", tc.blocker, got, tc.want)
		}
	}
	if exitContention == exitEnvironment {
		t.Error("the two blockers must not share a code: one is worth retrying, the other never is")
	}
	// Neither may collide with the codes that already mean something else.
	for _, taken := range []int{0, 1, 2} {
		if exitContention == taken || exitEnvironment == taken {
			t.Errorf("blocker code collides with the existing exit code %d", taken)
		}
	}
}

func TestPrePushExit_BlockerPicksTheCode(t *testing.T) {
	cases := map[domain.Blocker]int{
		domain.BlockerNone:        1,
		domain.BlockerContention:  exitContention,
		domain.BlockerEnvironment: exitEnvironment,
	}
	for blocker, want := range cases {
		var out bytes.Buffer
		res := application.RunResult{
			Outcome: domain.OutcomeFailed,
			Message: "step lint could not run",
			Blocker: blocker,
		}
		if got := runPrePushExit(res, &out); got != want {
			t.Errorf("blocker %q: exit = %d, want %d", blocker, got, want)
		}
	}
}

// The blocker codes must not disturb what #89 established: a pass exits 0 when
// git completed the push, and 1 in the one case where warden pushed itself.
func TestPrePushExit_PassIsUnaffectedByTheBlockerCodes(t *testing.T) {
	var out bytes.Buffer
	gitPushed := application.RunResult{Outcome: domain.OutcomePassed, Message: "pushed", GitCompletesPush: true}
	if got := runPrePushExit(gitPushed, &out); got != 0 {
		t.Errorf("exit = %d, want 0 when git completed the push", got)
	}
	out.Reset()
	wardenPushed := application.RunResult{Outcome: domain.OutcomePassed, Message: "pushed"}
	if got := runPrePushExit(wardenPushed, &out); got != 1 {
		t.Errorf("exit = %d, want 1 when warden pushed and git's stale push must be stopped", got)
	}
}

type stubPreCommitReporter struct{}

func (stubPreCommitReporter) ApplyFixPatch(string) error { return nil }
func (stubPreCommitReporter) StepsList() ([]domain.StepName, []domain.StepName, error) {
	return nil, nil, nil
}

func TestPreCommitExit_BlockerPicksTheCode(t *testing.T) {
	var out, errb bytes.Buffer
	res := application.RunResult{
		Outcome: domain.OutcomeFailed,
		Message: "step lint could not run: another process holds its lock",
		Blocker: domain.BlockerContention,
	}
	if got := runPreCommitExit(stubPreCommitReporter{}, res, &out, &errb); got != exitContention {
		t.Errorf("exit = %d, want %d", got, exitContention)
	}
	if !strings.Contains(errb.String(), "could not run") {
		t.Errorf("stderr should carry the honest reason: %q", errb.String())
	}
}

// The regression from #90: the top-level verdict said "step lint failed" even
// after the step itself had correctly reported contention, so the developer
// still read it as "your code is broken".
func TestRunVerdict_NamesTheObstacleNotTheStep(t *testing.T) {
	cases := map[domain.Blocker]string{
		domain.BlockerNone:        "step lint failed",
		domain.BlockerContention:  "step lint could not run: another process holds its lock",
		domain.BlockerEnvironment: "step lint could not run: its toolchain or dependencies are not installed",
	}
	for blocker, want := range cases {
		run := domain.NewRun("r1", domain.PrePush, domain.ResolvedPolicy{}, "main")
		err := run.RecordStep(domain.StepResult{
			Step:    domain.StepLint,
			Status:  domain.StepFail,
			Blocker: blocker,
		})
		if err != nil {
			t.Fatal(err)
		}
		if run.Message() != want {
			t.Errorf("blocker %q: message = %q, want %q", blocker, run.Message(), want)
		}
		if run.Blocker() != blocker {
			t.Errorf("blocker %q not carried onto the run (got %q)", blocker, run.Blocker())
		}
	}
}
