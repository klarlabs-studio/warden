package application

import (
	"errors"
	"fmt"
	"os"

	"go.klarlabs.de/warden/internal/domain"
)

// ErrAgentTraceRequired reports a profiled run whose change declares a coding
// agent but carries no notarized Agent Trace, where the profile requires one.
var ErrAgentTraceRequired = errors.New("agent trace required")

// environ is the gating process's environment, injectable for tests.
func (r *Runner) environ() []string {
	if r.Environ != nil {
		return r.Environ()
	}
	return os.Environ()
}

// runProfile returns the compliance profile a policy was resolved under, and
// false when there is none.
func runProfile(resolved domain.ResolvedPolicy) (domain.ComplianceProfile, bool, error) {
	if resolved.Profile == nil {
		return domain.ComplianceProfile{}, false, nil
	}
	p, err := domain.ParseComplianceProfile(resolved.Profile.Name)
	if err != nil {
		return domain.ComplianceProfile{}, false, fmt.Errorf("compliance profile: %w", err)
	}
	return p, true, nil
}

// observeAuthorship reads the agent declarations that bear on a record
// attesting head: the trailers of (base, head] (head alone when base is empty)
// and the markers in warden's own environment. Nil means nothing matched.
//
// A span that cannot be read is an error, not an empty observation: under a
// profile the record must not imply it looked and found nothing.
func (r *Runner) observeAuthorship(base, head string) (*domain.AuthorshipSignals, error) {
	commits, err := r.Git.SpanTrailers(base, head)
	if err != nil {
		return nil, fmt.Errorf("read the trailers of the gated commits: %w", err)
	}
	return domain.ObserveAuthorship(commits, r.environ()), nil
}

// requireTraceForDeclaredAgent enforces strict@1's authorship rule: a change
// that declares an agent must carry a notarized Agent Trace. It only ever
// tightens: no declaration, or a profile without the rule, passes.
func requireTraceForDeclaredAgent(p domain.ComplianceProfile, s *domain.AuthorshipSignals, trace *domain.AgentTraceRef, cfg domain.Config) error {
	if !p.RequiresTraceForDeclaredAgents() || !s.Declared() || trace != nil {
		return nil
	}
	hint := "set agent_trace.path in .warden.yaml to the Agent Trace record your agent writes, and commit the record"
	if cfg.AgentTrace.Path != "" {
		hint = "no Agent Trace record was found at " + cfg.AgentTrace.Path + "; commit the record your agent writes there"
	}
	return fmt.Errorf("%w: profile %s requires an Agent Trace when a change declares a coding agent, and none was notarized. Declared: %s. %s",
		ErrAgentTraceRequired, p, s.Summary(), hint)
}

// preflightAuthorship fails a run early, before any step runs, when the
// profile requires a trace for a declared agent, the commits about to be
// pushed (or the environment) declare one, and the seeded tree has no trace to
// notarize. It decides nothing the final check does not re-decide on the
// attested span; it only spares the developer a full pipeline that can only
// end in the same refusal.
func (r *Runner) preflightAuthorship(resolved domain.ResolvedPolicy, branch, tip string, cfg domain.Config, worktreeDir string) error {
	p, ok, err := runProfile(resolved)
	if err != nil || !ok || !p.RequiresTraceForDeclaredAgents() {
		return err
	}
	base := ""
	if !r.Settings.AttestOnly {
		// Best-effort, as for the record's span: an unknown base reads the tip.
		if b, err := r.Git.PushSpanBase(r.Settings.Remote, branch); err == nil && b != tip {
			base = b
		}
	}
	signals, err := r.observeAuthorship(base, tip)
	if err != nil || !signals.Declared() {
		return err
	}
	var probe domain.RunRecord
	if err := r.notarizeAgentTrace(&probe, cfg, worktreeDir); err != nil {
		return err
	}
	return requireTraceForDeclaredAgent(p, signals, probe.AgentTrace, cfg)
}

// bindAuthorship records the observed agent declarations in a profiled record
// and applies the profile's requirement to them. It must run after the record
// is bound to its commit and span and after the trace is notarized, and before
// signing, so the declarations are inside the signed payload. Without a
// profile the record is left untouched, so unprofiled notes stay
// byte-identical.
func (r *Runner) bindAuthorship(record *domain.RunRecord, resolved domain.ResolvedPolicy, cfg domain.Config) error {
	p, ok, err := runProfile(resolved)
	if err != nil || !ok || record.CommitSHA == "" {
		// No commit, no note: there is nothing to bind declarations to.
		return err
	}
	signals, err := r.observeAuthorship(record.CoversFrom, record.CommitSHA)
	if err != nil {
		return err
	}
	record.Authorship = signals
	return requireTraceForDeclaredAgent(p, signals, record.AgentTrace, cfg)
}
