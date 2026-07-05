package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/proc"
	"go.klarlabs.de/warden/stepsdk"
)

// SubprocessStep adapts an external process that speaks the stepsdk wire
// protocol (§6) into an application.Step. This is the single trust boundary for
// repo-authored custom steps: they run as separate processes, never loaded
// into the daemon (§3 non-goal).
type SubprocessStep struct {
	name domain.StepName
	// bin is the executable to run. By convention Warden resolves a custom
	// step named "foo" to the binary "warden-step-foo" on PATH.
	bin string
}

// NewSubprocessStep binds a custom step name to its resolved binary.
func NewSubprocessStep(name domain.StepName, bin string) SubprocessStep {
	return SubprocessStep{name: name, bin: bin}
}

// customStepBinary is the conventional executable name for a custom step.
func customStepBinary(name domain.StepName) string {
	return "warden-step-" + string(name)
}

func (s SubprocessStep) Name() domain.StepName { return s.name }

// Run marshals the step input to JSON on the child's stdin, runs it, and
// decodes the JSON result from stdout, translating it back into a StepResult.
// A non-zero exit or unparseable output is an operational error, distinct from
// a clean StatusFail the step itself reports.
func (s SubprocessStep) Run(ctx context.Context, sc application.StepContext) (domain.StepResult, error) {
	in := stepsdk.Input{
		SchemaVersion: stepsdk.SchemaVersion,
		StepID:        string(s.name),
		Hook:          sc.Hook.ConfigKey(),
		RepoPath:      sc.WorktreeDir,
		Branch:        sc.Branch,
		DiffSummary: stepsdk.DiffSummary{
			FilesTouched: sc.Diff.FilesTouched,
			LinesChanged: sc.Diff.LinesChanged,
		},
		ResolvedAgent: sc.Agent,
		PriorFindings: toWireFindings(sc.PriorFindings),
	}
	payload, err := json.Marshal(in)
	if err != nil {
		return domain.StepResult{}, fmt.Errorf("marshal step input: %w", err)
	}

	cmd := exec.CommandContext(ctx, s.bin)
	cmd.Dir = sc.WorktreeDir
	// Own process group so a cancelled/timed-out custom step is killed with any
	// children it spawned, not left orphaned past the run.
	proc.Isolate(cmd)
	cmd.Stdin = bytes.NewReader(payload)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return domain.StepResult{}, fmt.Errorf("run %s: %w: %s", s.bin, err, stderr.String())
	}

	var out stepsdk.Output
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return domain.StepResult{}, fmt.Errorf("decode %s output: %w", s.bin, err)
	}
	return fromWireOutput(s.name, out), nil
}

func toWireFindings(fs []domain.Finding) []stepsdk.Finding {
	if len(fs) == 0 {
		return nil
	}
	out := make([]stepsdk.Finding, len(fs))
	for i, f := range fs {
		out[i] = stepsdk.Finding{
			Severity: string(f.Severity),
			Message:  f.Message,
			File:     f.File,
			Line:     f.Line,
		}
	}
	return out
}

func fromWireOutput(name domain.StepName, out stepsdk.Output) domain.StepResult {
	res := domain.StepResult{
		Step:   name,
		Status: domain.StepStatus(out.Status),
		Fixed:  out.Fixed,
	}
	for _, f := range out.Findings {
		res.Findings = append(res.Findings, domain.Finding{
			Severity: domain.Severity(f.Severity),
			Message:  f.Message,
			File:     f.File,
			Line:     f.Line,
		})
	}
	return res
}
