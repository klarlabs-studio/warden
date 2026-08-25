package steps

import (
	"context"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/proc"
)

// The property the whole change turns on: the caveat is CONTEXT, never a
// verdict. #249 asked for a starved step to be reportable as something other
// than a rejection, and the answer is no — a timeout under load is still
// indistinguishable from a deadlock, so warden says what it measured and leaves
// the conclusion to a human.
func TestLoadCaveat_OnlyOnRealContention(t *testing.T) {
	cases := []struct {
		name string
		load proc.Load
		want bool
	}{
		{"idle", proc.Load{Known: true, Value: 0.4, CPUs: 10}, false},
		{"busy but usable", proc.Load{Known: true, Value: 30, CPUs: 10}, false},
		{"the #249 machine", proc.Load{Known: true, Value: 109, CPUs: 10}, true},
		// Never speculate on a platform that measured nothing.
		// A non-zero Value on an unknown reading: without the Known guard this
		// would report contention from a measurement that never happened.
		{"unknown platform", proc.Load{Known: false, Value: 109, CPUs: 10}, false},
		{"zero value", proc.Load{}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := loadCaveat(c.load)
			if (got != "") != c.want {
				t.Errorf("caveat=%q, want present=%v", got, c.want)
			}
			if !c.want {
				return
			}
			// It must report a measurement and explicitly decline to conclude.
			for _, want := range []string{"on 10 core(s)", "10.9x", "cannot tell which", "verdict above stands"} {
				if !strings.Contains(got, want) {
					t.Errorf("caveat missing %q:\n%s", want, got)
				}
			}
		})
	}
}

// A caveat that quietly softened the outcome would be worse than none: the
// author would read "may be the machine" and merge. Status, and the fact that
// the tool's own output survives, must be untouched.
func TestShellStep_CaveatDoesNotChangeTheVerdict(t *testing.T) {
	sc := application.StepContext{
		WorktreeDir: t.TempDir(),
		Commands:    map[string]string{"lint": `echo "main.go:1:1: undefined: foo"; exit 1`},
	}
	res, err := NewShellStep(domain.StepLint, "lint").Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != domain.StepFail {
		t.Fatalf("status = %s, want fail — a caveat must never soften a verdict", res.Status)
	}
	if res.Summary != "lint failed" {
		t.Errorf("Summary = %q, want the ordinary failure wording", res.Summary)
	}
	if len(res.Findings) != 1 || !strings.Contains(res.Findings[0].Message, "undefined: foo") {
		t.Errorf("the tool's own output must survive: %+v", res.Findings)
	}
}

// The invariant the first draft broke. Message is the TOOL's output, verbatim;
// warden's commentary goes in Why. Appending to Message made it vary with
// machine load between two runs of the same failure, and warden's own gate
// caught it by red-lining two existing tests that assert the output survives
// intact — the feature failing on the property it was written to protect.
func TestShellStep_CaveatNeverEntersTheToolsOutput(t *testing.T) {
	sc := application.StepContext{
		WorktreeDir: t.TempDir(),
		Commands:    map[string]string{"lint": `echo "boom"; exit 1`},
	}
	res, err := NewShellStep(domain.StepLint, "lint").Run(context.Background(), sc)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Findings) != 1 {
		t.Fatalf("findings = %+v, want one", res.Findings)
	}
	// Whatever this machine's load, the tool's own words are untouched.
	if got := strings.TrimSpace(res.Findings[0].Message); got != "boom" {
		t.Errorf("Message = %q, want exactly the tool output %q — warden's commentary belongs in Why", got, "boom")
	}
	if strings.Contains(res.Findings[0].Message, "oversubscribed") {
		t.Error("the load caveat leaked into the tool's output")
	}
}
