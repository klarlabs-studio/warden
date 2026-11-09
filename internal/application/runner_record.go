package application

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.klarlabs.de/warden/internal/domain"
)

// statusDescription is the one line GitHub shows beside the check. It names
// the steps, because "passed" without them invites the reader to assume a gate
// ran checks it was never configured to run.
//
// A recorded scope comes first, ahead of the step list: the forge clips a long
// description from the end, and "affected" is the one qualifier that must not
// be what gets clipped.
func statusDescription(run *domain.Run, rec *domain.RunRecord) string {
	head := "warden gate passed"
	if rec != nil && rec.Scope != nil {
		if rec.FullScope() {
			head += ", full scope"
		} else {
			n, m := rec.Scope.Proportion()
			head += fmt.Sprintf(", affected scope: %d of %d workspaces (partial check)", n, m)
		}
	}
	steps := run.Policy().Steps
	if len(steps) == 0 {
		return head
	}
	return head + " (" + domain.DescribeSteps(steps, run.StepNotes()) + ")"
}

// attachScope records the run's workspace scope on record, when it has one to
// record: a pre-push run under `workspaces.select: affected` whose
// per-workspace steps ran. Every other run records none.
//
// An affected scope is attached only when the tree the selection was computed
// on, which the per-workspace checks ran on, is the attested commit's tree. The
// ordering rule makes them equal by construction (no step that keeps its
// writes runs at or after a per-workspace step); this checks it against git
// rather than trusting it, and refuses when it does not hold.
func (r *Runner) attachScope(record *domain.RunRecord, sels *workspaceSelections, resolved domain.ResolvedPolicy, sha string) error {
	scope, err := buildScope(sels, resolved)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrScopeNotAttestable, err)
	}
	if scope == nil {
		return nil
	}
	if !scope.IsFull() {
		tree, err := r.Git.CommitTree(sha)
		if err != nil {
			return fmt.Errorf("%w: the tree of %s could not be read: %v", ErrScopeNotAttestable, short12(sha), err)
		}
		if tree != scope.HeadTree {
			return fmt.Errorf("%w: the per-workspace checks ran on tree %s, but %s holds tree %s",
				ErrScopeNotAttestable, short12(scope.HeadTree), short12(sha), short12(tree))
		}
	}
	record.Scope = scope
	return nil
}

// notarizeAgentTrace binds a digest of the repo's Agent Trace record into the
// provenance note.
//
// A missing record is NOT an error by default: a human commit legitimately has
// no agent trace, so absence is the normal case. agent_trace.required flips that
// for a repo where every change is expected to carry one.
//
// A record that is present but unreadable or malformed is different — something
// meant to be notarized could not be, and silently gating without it would
// produce a note that looks complete and is not.
func (r *Runner) notarizeAgentTrace(record *domain.RunRecord, cfg domain.Config, worktreeDir string) error {
	path := strings.TrimSpace(cfg.AgentTrace.Path)
	if path == "" {
		return nil // not configured
	}
	// The path is repo-relative and must stay inside the worktree.
	full := filepath.Join(worktreeDir, filepath.Clean(path))
	if !strings.HasPrefix(full, filepath.Clean(worktreeDir)+string(os.PathSeparator)) {
		return fmt.Errorf("agent_trace.path %q escapes the repository", path)
	}
	raw, err := os.ReadFile(full)
	if err != nil {
		if os.IsNotExist(err) && !cfg.AgentTrace.Required {
			return nil
		}
		return fmt.Errorf("agent trace %s could not be read: %w", path, err)
	}
	ref, err := domain.NewAgentTraceRef(path, raw)
	if err != nil {
		return fmt.Errorf("agent trace %s: %w", path, err)
	}
	record.AgentTrace = &ref
	return nil
}

// sign attaches an ed25519 signature to the record when a Signer is configured,
// and returns why it could not when it could not.
//
// Signing stays best-effort: a failure leaves the note unsigned rather than
// failing a push (§9) — a developer should not be blocked because a key
// directory went read-only. But it used to fail SILENTLY, and an unsigned note
// is not a smaller version of a signed one: it proves the checks ran, not who
// ran them, so `verify --require-signed` rejects it. A repo could therefore
// produce months of notes its own CI would refuse and never be told.
//
// The empty return means signed. The caller decides what to do with a reason —
// warn, or (with signing.required) refuse.
func (r *Runner) sign(record *domain.RunRecord) (reason string) {
	if r.Signer == nil {
		return "no signing key is available on this machine (the key directory could not be created or read)"
	}
	// Set the key AND the algorithm before computing the payload, so both are
	// bound into the signature. A record whose algorithm could be changed after
	// signing could be re-labeled into a scheme with weaker verification.
	record.PublicKey = r.Signer.PublicKey()
	record.Algorithm = r.Signer.Algorithm()
	payload, err := record.SigningPayload()
	if err != nil {
		record.PublicKey, record.Algorithm = "", ""
		return "the record could not be serialized for signing: " + err.Error()
	}
	sig, err := r.Signer.Sign(payload)
	if err != nil {
		record.PublicKey, record.Algorithm = "", ""
		return "the signer rejected the payload: " + err.Error()
	}
	record.Signature = sig
	return ""
}

// buildRecord finalizes the evidence chain into a provenance RunRecord (§9).
//
// Under a compliance profile the record also carries the profile, the observed
// step outcomes and the approval gate's answer, all inside the signed payload
// (ADR 0007). Without a profile none of them is set, so the note is
// byte-identical to one written before they existed and older verifiers keep
// verifying it.
func (r *Runner) buildRecord(kernel Kernel, run *domain.Run, approval domain.ApprovalRef) (*domain.RunRecord, error) {
	root, entries, err := kernel.Finalize()
	if err != nil {
		return nil, fmt.Errorf("verify evidence chain: %w", err)
	}
	rec := &domain.RunRecord{
		RunID:             string(run.ID()),
		Timestamp:         r.now().UTC().Format(time.RFC3339),
		WardenVersion:     r.Settings.Version,
		Agent:             run.Policy().Agents,
		StepsRun:          run.Policy().Steps,
		Environments:      run.ExecutionEnvironments(),
		Contracts:         run.ExecutionContracts(),
		MatchedRules:      run.Policy().MatchedRules,
		EvidenceChainRoot: root,
		Evidence:          entries,
	}
	if enabled := run.Policy().Execution.AttestObservations; enabled != nil && *enabled {
		rec.Observations = run.ExecutionObservations()
	}
	if err := attachProfileObservations(rec, run, approval); err != nil {
		return nil, err
	}
	return rec, nil
}

// attachProfileObservations adds the profile-gated fields to rec when the run's
// policy was resolved under a profile, and leaves rec untouched otherwise.
//
// The profile is re-parsed rather than copied from the resolved policy so the
// recorded digest is always this binary's built-in definition: a verifier
// comparing it with its own table then compares like with like.
func attachProfileObservations(rec *domain.RunRecord, run *domain.Run, approval domain.ApprovalRef) error {
	resolved := run.Policy().Profile
	if resolved == nil {
		return nil
	}
	p, err := domain.ParseComplianceProfile(resolved.Name)
	if err != nil {
		// The loader already accepted this profile; failing here means the
		// policy names a profile this binary cannot describe. Writing a record
		// without it would silently drop the claim the repo asked for.
		return fmt.Errorf("record compliance profile: %w", err)
	}
	ref := domain.NewProfileRef(p)
	rec.Profile = &ref
	rec.StepOutcomes = run.StepOutcomes()
	// Only a profile that demands them signs deadlines: any other note keeps
	// the exact payload older verifiers sign over.
	if p.RequiresShellTimeout() {
		rec.Deadlines = run.StepDeadlines()
	}
	if approval.Channel == "" {
		approval.Channel = domain.ApprovalNone
	}
	rec.Approval = &approval
	return nil
}
