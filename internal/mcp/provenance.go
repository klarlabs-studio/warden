package mcpserver

import (
	"fmt"

	"go.klarlabs.de/warden/internal/domain"
)

// The provenance surface: the read-only half of Warden's operation set, exposed
// to agents.
//
// Warden's differentiating claim is that a validated commit carries signed,
// hash-chained proof that the checks ran — but until now an agent could only
// EXECUTE the gate (run_trigger), never INTERROGATE it. The single most useful
// question Warden can answer, "is this commit validated?", had no agent-facing
// answer at all, so an agent reasoning about whether a branch was gated had to
// shell out to the CLI and scrape prose.
//
// Every operation here is a pure read. None of them executes repo-authored
// shell, so none consults the RunGate that guards run_trigger — the trust
// checkpoint exists to stop arbitrary code execution from an untrusted
// .warden.yaml, and reading a note cannot do that.

// VerifyOutput is one commit's provenance verdict — the provenance-skip
// primitive behind `warden verify`. Validated answers the question on its own;
// the signature fields say how much weight it carries.
type VerifyOutput struct {
	SHA string `json:"sha"`
	// Validated is true when a note exists AND its evidence chain is intact AND
	// it attests THIS commit. It is fail-closed: an intact but unbound or
	// transplanted note is not validated.
	Validated bool `json:"validated"`
	// Signed reports whether the note carries a signature; SignatureValid
	// whether that signature verifies against its embedded key; Signer is the
	// signer's fingerprint. Trusted is true only when the caller pinned a
	// trusted key set and the signature was made by one of those keys.
	Signed         bool   `json:"signed"`
	SignatureValid bool   `json:"signature_valid"`
	Signer         string `json:"signer,omitempty"`
	Trusted        bool   `json:"trusted"`
	// RunID, Steps and MatchedRules come from the note itself, so they describe
	// what the gate actually did rather than what policy would do today. Empty
	// when no note exists.
	RunID        string            `json:"run_id,omitempty"`
	Steps        []domain.StepName `json:"steps,omitempty"`
	MatchedRules []string          `json:"matched_rules,omitempty"`
	// WardenVersion is the warden that produced the note.
	WardenVersion string `json:"warden_version,omitempty"`
	// ReattestedFrom, when set, means this record was carried over from a
	// tree-identical validated commit (the squash-merge case) rather than being
	// a fresh validation. An auditor must be able to tell those apart.
	ReattestedFrom string `json:"reattested_from,omitempty"`
	// CoversFrom names the commit the gated push started from: everything in
	// (CoversFrom, SHA] was published by the same gated push.
	CoversFrom string `json:"covers_from,omitempty"`
	// Profile, StepOutcomes and Approval are the signed observations a note
	// written under a compliance profile carries: the profile and its
	// definition digest, what happened to each step (executed, cached,
	// predicted, skipped) and how the approval gate was answered. Absent for a
	// note written without a profile, which records none of them.
	Profile      *domain.ProfileRef                         `json:"profile,omitempty"`
	StepOutcomes map[domain.StepName]domain.StepObservation `json:"step_outcomes,omitempty"`
	Approval     *domain.ApprovalRef                        `json:"approval,omitempty"`
	// Authorship lists the agent declarations a profiled gate observed:
	// trailers on the attested commits and harness markers in its
	// environment. Declarations, not authorship; absent when none matched.
	Authorship *domain.AuthorshipSignals `json:"authorship,omitempty"`
	// FullScope is false when the note attests an affected-scope monorepo run
	// (per-workspace checks for the affected workspaces only), which is never
	// validated by default. ScopeRefused says the note was sound otherwise and
	// refused for that alone, and Scope is the signed scope it records.
	// FullScope is always present and false when there is no note, so an
	// agent never reads "full" into an absent field.
	FullScope    bool             `json:"full_scope"`
	ScopeRefused bool             `json:"scope_refused,omitempty"`
	Scope        *domain.RunScope `json:"scope,omitempty"`
}

// RangeVerifyOutput gates a whole base..head span — the shape a PR check needs.
// OK is the verdict; Commits carries the per-commit reasons so an agent can
// report exactly which commits are missing provenance and why.
type RangeVerifyOutput struct {
	Base string `json:"base"`
	Head string `json:"head"`
	// OK is true when every commit in the range passed. An empty range is
	// trivially OK.
	OK bool `json:"ok"`
	// Commits carries a verdict per commit with a reason: ok, missing,
	// broken-chain, unsigned, untrusted, affected-scope, and the others
	// domain.VerifyReason defines.
	Commits []domain.CommitVerdict `json:"commits"`
	// RequireSigned and TrustedKeys report the depth actually enforced, which is
	// not always what the caller asked for: when UseRoster resolves a roster from
	// the base ref, the effective key set comes from there. A caller reporting
	// "this range is gated" must be able to say how deeply.
	RequireSigned  bool     `json:"require_signed"`
	TrustedKeys    []string `json:"trusted_keys,omitempty"`
	RosterFromBase bool     `json:"roster_from_base"`
	// Profiles lists the minimum compliance profiles enforced and where each
	// came from. Absent when none applied: the base names no profile and none
	// was asked for.
	Profiles []RangeProfileOutput `json:"profiles,omitempty"`
	// Failed counts commits that did not pass, so a caller can branch without
	// walking the list.
	Failed int `json:"failed"`
}

// RangeProfileOutput is one minimum compliance profile a range gate enforced.
type RangeProfileOutput struct {
	Profile string `json:"profile"`
	// FromBase: the base ref's .warden.yaml named it. FromFlag: the caller did.
	FromBase bool `json:"from_base"`
	FromFlag bool `json:"from_flag"`
}

// AuditOutput is a branch's provenance report since adoption — the shape behind
// both `warden doctor` (is anything ungated?) and `warden audit` (the
// compliance export).
type AuditOutput struct {
	// Adoption is the commit warden started gating at. Commits before it are
	// out of scope, not violations.
	Adoption string `json:"adoption"`
	Branch   string `json:"branch"`
	// Verified counts commits whose note ATTESTS them; Defective, those carrying
	// a note that does not; Unverified, the real gaps (Defective included).
	Verified   int `json:"verified"`
	Defective  int `json:"defective"`
	Unverified int `json:"unverified"`
	// Covered counts commits a gated push span published — warden validates one
	// tree per run and vouches for the span, so these need no note of their own.
	// Unknown counts commits warden cannot speak to at all: a branch that was
	// never pushed, or provenance older than push spans. Both used to be summed
	// into Unverified, which reported an ordinary gated multi-commit push as a
	// pile of unchecked commits.
	Covered int `json:"covered"`
	Unknown int `json:"unknown"`
	// Affected counts commits gated by an affected-scope run: a partial
	// check, neither verified nor a gap.
	Affected int `json:"affected"`
	// Reattestable counts the unverified commits a tree-identical validated
	// commit can vouch for — the recoverable share of the gap (the squash-merge
	// signature), as opposed to commits that were genuinely never gated.
	Reattestable int `json:"reattestable"`
	// Commits is the per-commit detail. Present on audit; doctor reports the
	// same shape so one schema serves both.
	Commits []AuditCommit `json:"commits"`
}

// AuditCommit is one commit's line in an audit report.
type AuditCommit struct {
	SHA     string `json:"sha"`
	Author  string `json:"author,omitempty"`
	Date    string `json:"date,omitempty"`
	Subject string `json:"subject,omitempty"`
	HasNote bool   `json:"has_note"`
	// ChainIntact requires the note to attest THIS commit, so an unbound or
	// transplanted note reads as false rather than as verified.
	ChainIntact bool              `json:"chain_intact"`
	RunID       string            `json:"run_id,omitempty"`
	Steps       []domain.StepName `json:"steps,omitempty"`
	// ReattestableFrom names a validated commit reproducing this commit's tree
	// exactly. It turns a bare "unverified" into an actionable one: the content
	// WAS gated under a different commit id.
	ReattestableFrom string `json:"reattestable_from,omitempty"`
	// Scope labels a commit gated by an affected-scope run ("affected 3/12"):
	// a partial check, which verify refuses by default.
	Scope string `json:"scope,omitempty"`
}

// StatusOutput describes the gate's installed state — what an agent needs to
// know before it reasons about a repo at all: whether the hooks are actually
// armed, where gating started, and what would run.
type StatusOutput struct {
	Version string `json:"version"`
	RepoDir string `json:"repo_dir"`
	// InstalledHooks maps hook name to whether warden's shim is installed. A
	// repo with a .warden.yaml but no installed hook is configured, not gated —
	// the distinction a "why didn't this get checked?" question turns on.
	InstalledHooks map[string]bool `json:"installed_hooks"`
	// Adoption is the commit gating started at, empty when never adopted.
	Adoption  string            `json:"adoption,omitempty"`
	PreCommit []domain.StepName `json:"pre_commit"`
	PrePush   []domain.StepName `json:"pre_push"`
	// SigningKey is this machine's provenance signing fingerprint — the value a
	// verifier pins with --key.
	SigningKey string `json:"signing_key,omitempty"`
}

// defaultCommit is the commit-ish the provenance reads fall back to. "Is HEAD
// gated?" is the overwhelmingly common question, so it should cost no arguments.
const defaultCommit = "HEAD"

// handleVerify verifies one commit. An empty commit means HEAD. Split out of the
// tool closure so it can be unit-tested directly against a fake facade, matching
// the existing handlePolicyExplain/handleStepsList pattern.
func handleVerify(f Facade, in VerifyInput) (VerifyOutput, error) {
	commit := in.Commit
	if commit == "" {
		commit = defaultCommit
	}
	rec, err := f.Verify(commit, in.TrustedKeys)
	if err != nil {
		return VerifyOutput{}, err
	}
	return newVerifyOutput(commit, rec), nil
}

// handleVerifyRange gates base..head. SkipMerges defaults to TRUE to match the
// CLI: a merge commit's parents are gated individually, so gating the merge
// itself reports a failure for a commit that was never separately validated. The
// pointer in the input distinguishes "unset" from "explicitly false".
func handleVerifyRange(f Facade, in VerifyRangeInput) (RangeVerifyOutput, error) {
	head := in.Head
	if head == "" {
		head = defaultCommit
	}
	skipMerges := true
	if in.SkipMerges != nil {
		skipMerges = *in.SkipMerges
	}
	var mins []domain.ComplianceProfile
	if in.MinProfile != "" {
		p, err := domain.ParseComplianceProfile(in.MinProfile)
		if err != nil {
			return RangeVerifyOutput{}, fmt.Errorf("min_profile: %w", err)
		}
		mins = append(mins, p)
	}
	return f.VerifyRange(in.Base, head, RangeVerifyRequest{
		MinProfiles:   mins,
		RequireSigned: in.RequireSigned,
		TrustedKeys:   in.TrustedKeys,
		// Resolve the roster from the BASE ref when no keys are pinned: the base
		// is the trusted side, and a range gate must never read its trust anchor
		// from the head it is checking.
		UseRoster:  len(in.TrustedKeys) == 0,
		SkipMerges: skipMerges,
	})
}

// handleDoctor reports gate coverage since adoption.
func handleDoctor(f Facade, in BranchInput) (AuditOutput, error) {
	rep, err := f.Doctor(in.Branch)
	if err != nil {
		return AuditOutput{}, err
	}
	return newAuditOutput(rep), nil
}

// handleAudit exports the full per-commit provenance report.
func handleAudit(f Facade, in BranchInput) (AuditOutput, error) {
	rep, err := f.Audit(in.Branch)
	if err != nil {
		return AuditOutput{}, err
	}
	return newAuditOutput(rep), nil
}

// newVerifyOutput projects a service verify result onto the wire shape. The
// record's fields are read only when a record exists, so an unvalidated commit
// returns a well-formed negative rather than an error.
func newVerifyOutput(sha string, r ProvenanceRecord) VerifyOutput {
	out := VerifyOutput{
		SHA:            sha,
		Validated:      r.Validated,
		Signed:         r.Signed,
		SignatureValid: r.SignatureValid,
		Signer:         r.Signer,
		Trusted:        r.Trusted,
		ScopeRefused:   r.ScopeRefused,
	}
	if r.Record != nil {
		out.FullScope = r.Record.FullScope()
		out.Scope = r.Record.Scope
		out.RunID = r.Record.RunID
		out.Steps = r.Record.StepsRun
		out.MatchedRules = r.Record.MatchedRules
		out.WardenVersion = r.Record.WardenVersion
		out.ReattestedFrom = r.Record.ReattestedFrom
		out.CoversFrom = r.Record.CoversFrom
		out.Profile = r.Record.Profile
		out.StepOutcomes = r.Record.StepOutcomes
		out.Approval = r.Record.Approval
		out.Authorship = r.Record.Authorship
	}
	return out
}

// newAuditOutput projects a domain audit report onto the wire shape, computing
// the tallies here so every caller reports the same numbers rather than each
// re-deriving them.
func newAuditOutput(rep domain.AuditReport) AuditOutput {
	t := rep.Counts()
	out := AuditOutput{
		Adoption:     rep.Adoption,
		Branch:       rep.Branch,
		Verified:     t.Verified,
		Defective:    t.Defective,
		Unverified:   t.Unverified,
		Covered:      t.Covered,
		Unknown:      t.Unknown,
		Affected:     t.Affected,
		Reattestable: len(rep.Reattestable()),
		Commits:      make([]AuditCommit, 0, len(rep.Commits)),
	}
	// Index rather than range-copy: CommitStatus is 128 bytes and an audit walks
	// every commit since adoption.
	for i := range rep.Commits {
		c := &rep.Commits[i]
		out.Commits = append(out.Commits, AuditCommit{
			SHA:              c.SHA,
			Author:           c.Author,
			Date:             c.Date,
			Subject:          c.Subject,
			HasNote:          c.HasNote,
			ChainIntact:      c.ChainIntact,
			RunID:            c.RunID,
			Steps:            c.Steps,
			ReattestableFrom: c.ReattestableFrom,
			Scope:            c.Scope,
		})
	}
	return out
}
