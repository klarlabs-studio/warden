// Package stepsdk is the SDK for authoring Warden pipeline steps as external
// subprocesses.
//
// Warden runs custom steps as separate programs and communicates with them over
// a JSON-over-stdin/stdout wire protocol: the daemon writes a single [Input]
// object to the step's stdin, the step writes a single [Output] object to its
// stdout, and then the step exits. A step's main function is typically a
// one-liner:
//
//	func main() { stepsdk.Run(myHandler) }
//
// This package intentionally depends only on the Go standard library and no
// other Warden package. It is published for third-party step authors, so its
// import graph must stay minimal and stable.
package stepsdk

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// SchemaVersion is the wire-protocol version this SDK speaks. It is stamped onto
// every [Output] so the daemon can detect and reject mismatched step binaries.
const SchemaVersion = 1

// DiffSummary is a coarse description of the change under review. Steps get a
// summary rather than the full diff so the daemon can keep the payload small;
// the full worktree is available at [Input.RepoPath] when a step needs detail.
type DiffSummary struct {
	FilesTouched int `json:"files_touched"`
	LinesChanged int `json:"lines_changed"`
}

// Finding is a single issue a step reports. Findings are advisory data attached
// to the run; the overall gate decision is carried by [Output.Status].
//
// Rule, Why and Fix are the remediation half. Your step usually knows which rule
// fired, what breaks if it is ignored, and the command that resolves it — and
// whoever reads the finding, human or agent, otherwise has to rediscover all
// three. Filling them in is what lets a caller act on a failure rather than
// re-run the gate to work out what it meant.
//
// Everything except Severity and Message is optional; a step that sets only
// those two is exactly as valid as it was before these existed.
type Finding struct {
	Severity string `json:"severity"` // "info" | "low" | "medium" | "high"
	Message  string `json:"message"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	// Rule is the identifier your tool fired, e.g. "SA4006". It is what a reader
	// searches for, waives, or baselines — none of which prose supports.
	Rule string `json:"rule,omitempty"`
	// Why explains what breaks if this is ignored — the justification that
	// severity alone does not carry.
	Why string `json:"why,omitempty"`
	// Fix is the remediation, when you know one.
	Fix *Fix `json:"fix,omitempty"`
}

// Fix is a remediation for a finding: a command to run, a patch to apply, or
// neither.
//
// Both are ADVISORY — warden will not apply them on the strength of your finding
// alone. Folding changes into the tree is a policy decision the repo expresses
// with an `auto_fix` budget, so attaching a patch never escalates your step into
// a tree write. It closes the loop for the reader, nothing more.
type Fix struct {
	// Command resolves the finding, e.g. "gofmt -w internal/foo.go". Make it
	// runnable from the repo root, and have it fix only what the finding says.
	Command string `json:"command,omitempty"`
	// Patch is a unified diff resolving the finding. Prefer it over Command when
	// you know the change exactly: a diff can be reviewed before it is applied,
	// not only after.
	Patch string `json:"patch,omitempty"`
}

// Input is the payload the daemon sends to a step on stdin.
type Input struct {
	SchemaVersion int         `json:"schema_version"`
	StepID        string      `json:"step_id"`
	Hook          string      `json:"hook"` // "pre_commit" | "pre_push"
	RepoPath      string      `json:"repo_path"`
	Branch        string      `json:"branch"`
	DiffSummary   DiffSummary `json:"diff_summary"`
	ResolvedAgent string      `json:"resolved_agent"`
	PriorFindings []Finding   `json:"prior_findings"`
}

// Status is the gate decision a step returns.
type Status string

const (
	// StatusPass allows the git operation to proceed.
	StatusPass Status = "pass"
	// StatusFail blocks the git operation.
	StatusFail Status = "fail"
	// StatusNeedsApproval blocks pending explicit human approval.
	StatusNeedsApproval Status = "needs_approval"
)

// Output is the payload a step writes to stdout.
type Output struct {
	SchemaVersion int       `json:"schema_version"`
	Status        Status    `json:"status"`
	Findings      []Finding `json:"findings,omitempty"`
	// Fixed reports whether the step mutated the worktree to remediate an issue
	// (e.g. auto-formatting), so the daemon knows to re-stage changes.
	Fixed bool `json:"fixed"`
}

// Handler contains a custom step's logic: it receives the decoded [Input] and
// returns an [Output]. Authors implement this and hand it to [Run].
type Handler func(Input) Output

// Pass builds a passing [Output], optionally carrying advisory findings.
func Pass(findings ...Finding) Output {
	return newOutput(StatusPass, findings)
}

// Fail builds a failing [Output] that blocks the git operation.
func Fail(findings ...Finding) Output {
	return newOutput(StatusFail, findings)
}

// NeedsApproval builds an [Output] that blocks pending human approval.
func NeedsApproval(findings ...Finding) Output {
	return newOutput(StatusNeedsApproval, findings)
}

// newOutput centralizes construction so SchemaVersion is always stamped and the
// findings slice is normalized: variadic calls yield a zero-length (not nil)
// slice, but we keep nil as nil so the omitempty tag drops the field.
func newOutput(status Status, findings []Finding) Output {
	if len(findings) == 0 {
		findings = nil
	}
	return Output{SchemaVersion: SchemaVersion, Status: status, Findings: findings}
}

// Run is the one-call entry point for a step's main function. It reads one
// [Input] from os.Stdin, invokes handler, and writes the resulting [Output] to
// os.Stdout. On any I/O or protocol error it writes a best-effort failing
// output and exits with a non-zero status, since a step that cannot report a
// result must not silently let a git operation through.
func Run(handler Handler) {
	if err := RunWith(os.Stdin, os.Stdout, handler); err != nil {
		os.Exit(1)
	}
}

// RunWith is the testable core of [Run]. It reads a single JSON [Input] from r,
// invokes handler, and writes a single JSON [Output] to w. It always stamps the
// current [SchemaVersion] onto the output so handlers cannot forget to.
//
// On a malformed input it does not call handler; instead it writes a failing
// output with a diagnostic finding and returns a non-nil error. This keeps the
// contract fail-closed: a step that cannot understand its input reports "fail"
// rather than crashing or emitting nothing.
func RunWith(r io.Reader, w io.Writer, handler Handler) error {
	var in Input
	// The protocol is exactly one JSON object per invocation, so a streaming
	// Decoder is the natural reader: it consumes one value and stops.
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		decodeErr := fmt.Errorf("stepsdk: decode input: %w", err)
		out := Fail(Finding{
			Severity: "high",
			Message:  fmt.Sprintf("malformed step input: %v", err),
		})
		// Report the malformed input downstream, but surface the original decode
		// error to the caller even if writing that report also fails.
		if writeErr := writeOutput(w, out); writeErr != nil {
			return fmt.Errorf("%w (and writing fail output: %v)", decodeErr, writeErr)
		}
		return decodeErr
	}

	out := handler(in)
	out.SchemaVersion = SchemaVersion // authoritative: never trust the handler here
	if err := writeOutput(w, out); err != nil {
		return fmt.Errorf("stepsdk: encode output: %w", err)
	}
	return nil
}

// writeOutput encodes out as a single JSON object followed by a newline, so
// line-oriented tooling on the daemon side can frame the response.
func writeOutput(w io.Writer, out Output) error {
	enc := json.NewEncoder(w)
	return enc.Encode(out)
}
