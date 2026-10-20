package service

import (
	"fmt"
	"slices"

	"go.klarlabs.de/warden/internal/domain"
)

// RangeVerifyOptions tunes how strict a range gate is. The zero value is the
// lenient default that matches today's single-commit verify: a note must attest
// each commit, but an attested-yet-unsigned note is allowed, so upgrading does
// not suddenly fail a repo that never signed. RequireSigned adds "the signature
// must verify"; TrustedKeys additionally pins "…and be one of these keys".
type RangeVerifyOptions struct {
	RequireSigned  bool
	ExternalPolicy ExternalPolicy
	// TrustedKeys pins the trusted signer set explicitly (an out-of-band anchor).
	// When set it wins outright. When empty and UseRoster is true, VerifyRange
	// reads the roster from the BASE ref itself (see below).
	TrustedKeys []string
	// UseRoster asks VerifyRange to resolve the trusted-signer roster from the
	// range's BASE ref — the trusted side — when no explicit TrustedKeys are
	// pinned. This invariant ("a range gate never reads trust from the head it is
	// checking") lives in the application core so every delivery surface inherits
	// it, rather than each adapter re-deriving it.
	UseRoster  bool
	SkipMerges bool
	// ForgePolicy decides what happens to a commit the forge authored — one with
	// no note and a verified signature from ForgeKeys. Zero value rejects.
	ForgePolicy domain.ForgePolicy
	// ForgeKeys pins the fingerprints that identify the forge. Empty means the
	// classification cannot run at all, so a nil-config caller keeps exactly the
	// behavior it had before this existed.
	ForgeKeys []string
	// MinProfiles are explicit minimum compliance profiles (--min-profile). They
	// are checked IN ADDITION to the base ref's `profile:`, which VerifyRange
	// always reads: each one applies, and a commit must conform to all of them.
	MinProfiles []domain.ComplianceProfile
	// ScopePolicy decides whether a note attesting an affected-scope run
	// passes. The zero value, the only one there is, refuses it with
	// ReasonAffectedScope.
	ScopePolicy ScopePolicy
}

// RangeProfile is one minimum compliance profile a range gate enforced, and
// where it came from. A profile both the base config and a flag named is listed
// once, with both sources set.
type RangeProfile struct {
	Profile domain.ComplianceProfile
	// FromBase: the base ref's resolved `profile:` (through its extends chain).
	FromBase bool
	// FromFlag: an explicit minimum the caller passed (--min-profile).
	FromFlag bool
}

// RangeVerifyResult is the per-commit outcome of a `base..head` gate.
type RangeVerifyResult struct {
	Base    string
	Head    string
	Commits []domain.CommitVerdict
	// Effective is the options actually enforced, with TrustedKeys resolved from
	// the base ref when UseRoster requested it — so a caller can report the true
	// gate depth without re-deriving it. RosterFromBase records that a base-ref
	// roster supplied those keys.
	Effective      RangeVerifyOptions
	RosterFromBase bool
	// Profiles are the minimum compliance profiles every noted commit was
	// checked against. Empty means no profile was enforced: the base names none
	// and no flag asked for one, exactly the gate as it was before profiles.
	Profiles []RangeProfile
}

// OK reports whether every commit in the range passed the gate. An empty range
// (nothing between base and head) is trivially OK.
func (r RangeVerifyResult) OK() bool {
	for _, c := range r.Commits {
		if !c.OK() {
			return false
		}
	}
	return true
}

// Failures returns the commits that did not pass, in range order.
func (r RangeVerifyResult) Failures() []domain.CommitVerdict {
	var out []domain.CommitVerdict
	for _, c := range r.Commits {
		if !c.OK() {
			out = append(out, c)
		}
	}
	return out
}

// VerifyRange gates every commit in base..head: each must carry a note that
// attests it, and — under RequireSigned / TrustedKeys — a valid, trusted
// signature. Unlike doctor (which flags only *missing* notes), a broken,
// transplanted, or untrusted note fails here. It is a pure read over
// refs/notes/warden: no push or signing path is touched. Notes are fetched
// best-effort first so a fresh CI checkout sees them.
func (s *Service) VerifyRange(base, head string, opts RangeVerifyOptions) (RangeVerifyResult, error) {
	_ = s.repo.FetchNotes(s.remote) // best-effort; provenance is a side-channel

	baseSHA, err := s.repo.ResolveSHA(base)
	if err != nil {
		return RangeVerifyResult{}, fmt.Errorf("resolve base %q: %w", base, err)
	}
	headSHA, err := s.repo.ResolveSHA(head)
	if err != nil {
		return RangeVerifyResult{}, fmt.Errorf("resolve head %q: %w", head, err)
	}

	// Establish the trusted-signer roster from the BASE ref (the trusted side)
	// unless the caller pinned explicit keys. Resolving it here keeps the
	// invariant in the application core: a range gate can never be tricked into
	// trusting a roster the head under gate supplied. A malformed base roster is
	// fail-closed — we refuse rather than silently drop to a weaker depth.
	rosterFromBase := false
	if len(opts.TrustedKeys) == 0 && opts.UseRoster {
		roster, rerr := s.TrustedKeysAt(baseSHA)
		if rerr != nil {
			return RangeVerifyResult{}, fmt.Errorf("read trusted roster at base %s: %w", base, rerr)
		}
		if len(roster) > 0 {
			opts.TrustedKeys = roster
			rosterFromBase = true
		}
	}

	// Same trusted side, same reason: a head that could enable forge acceptance
	// for itself would be choosing whether it has to be gated.
	if opts.UseRoster && len(opts.ForgeKeys) == 0 {
		if fc, ferr := s.ForgeConfigAt(baseSHA); ferr == nil {
			opts.ForgePolicy = fc.Policy()
			opts.ForgeKeys = fc.ForgeKeys()
		}
		// A base config that will not parse leaves the zero policy in place,
		// which rejects. Failing open here would mean a broken .warden.yaml
		// silently widened the gate.
	}

	// The profile is policy of the branch being merged into, so it too comes
	// from the base, in keyed and roster mode alike: a head that lowers
	// `profile:` is judged by the profile it would be merged under. Unlike the
	// forge config above, a base config that will not load fails the gate — a
	// broken file must not switch enforcement off.
	profiles, err := s.rangeProfiles(baseSHA, opts.MinProfiles)
	if err != nil {
		return RangeVerifyResult{}, fmt.Errorf("read compliance profile at base %s: %w", base, err)
	}

	shas, err := s.repo.CommitsInRange(baseSHA, headSHA, opts.SkipMerges)
	if err != nil {
		return RangeVerifyResult{}, fmt.Errorf("walk %s..%s: %w", base, head, err)
	}

	res := RangeVerifyResult{Base: baseSHA, Head: headSHA, Effective: opts, RosterFromBase: rosterFromBase, Profiles: profiles}
	for _, sha := range shas {
		res.Commits = append(res.Commits, s.gateVerdict(sha, opts, profiles))
	}
	s.applySpanCoverage(&res, shas, opts, profiles)
	return res, nil
}

// rangeProfiles combines the base ref's profile with the explicit minimums,
// base first, each distinct profile once.
func (s *Service) rangeProfiles(baseSHA string, flags []domain.ComplianceProfile) ([]RangeProfile, error) {
	var out []RangeProfile
	if p, ok, err := s.ProfileAt(baseSHA); err != nil {
		return nil, err
	} else if ok {
		out = append(out, RangeProfile{Profile: p, FromBase: true})
	}
	for _, f := range flags {
		i := slices.IndexFunc(out, func(r RangeProfile) bool { return r.Profile.String() == f.String() })
		if i >= 0 {
			out[i].FromFlag = true
			continue
		}
		out = append(out, RangeProfile{Profile: f, FromFlag: true})
	}
	return out, nil
}

// noRangeKeys is why the trust clause fails for every note when a profile
// applies but the gate has no key to check signers against. Conformance is a
// claim about what a trusted warden observed, so without a trust anchor no
// note can meet it.
const noRangeKeys = "no trusted key to check the signer against; a profile needs a pinned --key or a trusted_keys roster at the base ref"

// gateVerdict is a commit's full verdict: the provenance checks, then — for a
// commit that passed them on a note of its own — conformance to every profile
// in force. Conformance only ever turns a pass into a failure.
func (s *Service) gateVerdict(sha string, opts RangeVerifyOptions, profiles []RangeProfile) domain.CommitVerdict {
	v, rec := s.verdictFor(sha, opts)
	if !v.OK() || len(profiles) == 0 {
		return v
	}
	if rec == nil {
		// Forge-accepted: no warden record exists whose observations could be
		// recomputed. It stays accepted — forge.accept_authored is an explicit
		// decision of the base config — but is marked, so the pass is never read
		// as profile-checked.
		v.ProfileUnchecked = true
		return v
	}
	if fails := rangeConformance(rec, opts.TrustedKeys, profiles); len(fails) > 0 {
		return domain.CommitVerdict{SHA: sha, Reason: domain.ReasonProfile, ProfileFailures: fails}
	}
	return v
}

// rangeConformance runs Conformance for each profile against a note that
// already passed the range's provenance checks, and returns every failure as
// "profile <name@version>: <clause>: <reason>". Trusted means what it means
// for the rest of the range gate: the signature verifies and its signer is in
// the effective key set, pinned or read from the base roster.
func rangeConformance(rec *domain.RunRecord, keys []string, profiles []RangeProfile) []string {
	res := VerifyResult{
		SHA:       rec.CommitSHA,
		Validated: true,
		Record:    rec,
		External:  rec.IsExternal(),
		Trusted:   len(keys) > 0 && rec.VerifySignature() && keyTrusted(rec, keys),
	}
	var out []string
	for _, p := range profiles {
		rep := Conformance(p.Profile, res)
		if !rep.OK() && len(keys) == 0 {
			rep.Failures = []string{clauseTrusted + ": " + noRangeKeys}
		}
		for _, f := range rep.Failures {
			out = append(out, "profile "+rep.Min+": "+f)
		}
	}
	return out
}

// applySpanCoverage rescues commits that carry no note of their own but fall
// inside the signed span of one that does.
//
// A run validates one tree — the tip's — so warden deliberately does NOT
// attest each commit of a multi-commit push individually; that would assert
// checks which never ran against those trees. What it does assert is the span:
// a trusted signer ran the policy and published (CoversFrom, CommitSHA]
// together. Reading that back is what makes the range gate check a claim warden
// actually makes, instead of demanding per-commit notes it never writes (#86).
//
// This is not a weakening. A covering note must itself pass the full gate at
// its own commit — same signature and trust requirements — so only a signer
// already trusted to attest commits can cover a span, and the span is bounded
// by real git history (`rev-list base..tip`) rather than by anything the note
// claims about reachability. A commit outside every trusted span keeps its
// original verdict.
func (s *Service) applySpanCoverage(res *RangeVerifyResult, shas []string, opts RangeVerifyOptions, profiles []RangeProfile) {
	gaps := map[string]int{} // sha → index into res.Commits
	for i, c := range res.Commits {
		// Both no-note reasons are gaps a span can close. ReasonForgeAuthored is
		// a REFINEMENT of ReasonMissing — same absent note, better-identified
		// cause — so leaving it out would have made a commit fail merely for
		// being correctly diagnosed: a web edit inside an otherwise gated push
		// passed as ReasonMissing before this classification existed, and must
		// keep passing. A span is also the stronger claim of the two: a trusted
		// signer ran the policy over that range, which is more than "the forge
		// signed this object".
		if c.Reason == domain.ReasonMissing || c.Reason == domain.ReasonForgeAuthored {
			gaps[c.SHA] = i
		}
	}
	if len(gaps) == 0 {
		return
	}
	for _, sha := range shas {
		if _, isGap := gaps[sha]; isGap {
			continue // a commit with no note cannot vouch for anything
		}
		rec, err := s.repo.ReadNote(sha)
		if err != nil || rec == nil || rec.CoversFrom == "" {
			continue
		}
		// The covering note must clear the very same bar it is being used to
		// satisfy — otherwise a span would be a cheaper path to "verified". That
		// includes every profile in force: a covered commit is judged by its
		// covering note, so a note that does not conform cannot cover.
		//
		// A covering note refused only for its affected scope covers nothing
		// either, but its span commits are named for what they are: published
		// by a gated push whose check was partial. Left as ReasonMissing they
		// would read as --no-verify pushes, accusing a bypass that did not
		// happen. They stay gaps, so a span that does pass can still cover them.
		v := s.gateVerdict(sha, opts, profiles)
		partial := v.Reason == domain.ReasonAffectedScope
		if !v.OK() && !partial {
			continue
		}
		covered, err := s.repo.CommitsInSpan(rec.CoversFrom, sha)
		if err != nil {
			continue
		}
		for _, c := range covered {
			i, isGap := gaps[c]
			switch {
			case !isGap:
			case partial:
				res.Commits[i] = domain.CommitVerdict{SHA: c, Reason: domain.ReasonAffectedScope, CoveredBy: sha}
			default:
				res.Commits[i] = domain.CommitVerdict{SHA: c, CoveredBy: sha}
				delete(gaps, c)
			}
		}
	}
}

// verdictFor classifies one commit against the gate's provenance strictness,
// and returns the note it read (nil when there is none). The order of checks is
// deliberate: absence, then integrity, then signature, then trust — so the
// reported reason is the *first* thing wrong, the most actionable one.
func (s *Service) verdictFor(sha string, opts RangeVerifyOptions) (domain.CommitVerdict, *domain.RunRecord) {
	rec, err := s.repo.ReadNote(sha)
	// A read ERROR and an absent note are different failures. ReadNote already
	// separates them — it returns (nil, nil) for a clean miss and (nil, err)
	// only when a note exists but will not decode — and collapsing them here
	// discarded that, reporting a corrupt note as "no warden note (pushed with
	// --no-verify, or made outside warden)". Both causes named there are wrong
	// for a note that is present but malformed. (#195)
	if err != nil {
		return domain.CommitVerdict{SHA: sha, Reason: domain.ReasonUnreadable}, nil
	}
	if rec == nil {
		// No note. Before calling that a bypass, ask whether a developer was
		// ever in this commit's path: the forge signs the commits IT creates,
		// and warden's hook cannot run on a machine that never held them.
		if v, isForge := s.forgeVerdict(sha, opts); isForge {
			return v, nil
		}
		return domain.CommitVerdict{SHA: sha, Reason: domain.ReasonMissing}, nil
	}
	if !rec.Attests(sha) {
		return domain.CommitVerdict{SHA: sha, Reason: domain.ReasonBrokenChain}, rec
	}
	// A present signature that does not verify means the note was edited after
	// signing: tampered, whatever the key policy (see VerifyWithPolicy).
	if rec.Signed() && !rec.VerifySignature() {
		return domain.CommitVerdict{SHA: sha, Reason: domain.ReasonBrokenChain}, rec
	}
	if rec.IsExternal() {
		if opts.ExternalPolicy == ExternalReject {
			return domain.CommitVerdict{SHA: sha, Reason: domain.ReasonExternal}, rec
		}
		if rec.ValidateExternal() != nil || !rec.VerifySignature() {
			return domain.CommitVerdict{SHA: sha, Reason: domain.ReasonBrokenChain}, rec
		}
	} else if opts.ExternalPolicy == ExternalRequire {
		return domain.CommitVerdict{SHA: sha, Reason: domain.ReasonExternal}, rec
	}
	// A trusted-key requirement implies the signature must first verify.
	if opts.RequireSigned || len(opts.TrustedKeys) > 0 {
		if !rec.VerifySignature() {
			return domain.CommitVerdict{SHA: sha, Reason: domain.ReasonUnsigned}, rec
		}
	}
	if len(opts.TrustedKeys) > 0 && !keyTrusted(rec, opts.TrustedKeys) {
		return domain.CommitVerdict{SHA: sha, Reason: domain.ReasonUntrusted}, rec
	}
	// Last, so the reason names a note that is sound in every other respect
	// and refused only for attesting a partial check. Span coverage inherits
	// this: a covering note has to pass this function.
	if !opts.ScopePolicy.Accepts(rec) {
		return domain.CommitVerdict{SHA: sha, Reason: domain.ReasonAffectedScope}, rec
	}
	return domain.CommitVerdict{SHA: sha}, rec
}

// forgeVerdict classifies an un-noted commit as forge-authored, and applies the
// policy. The second return says whether the classification applied at all, so
// the caller can fall through to ReasonMissing untouched.
//
// The check is deliberately narrow. It requires a signature git could actually
// VERIFY against a key in the local keyring, whose fingerprint is one the
// repository pinned. Any weaker test — the committer being "GitHub
// <noreply@github.com>", or a key id read out of the signature packet — is a
// field the pusher controls, and a gate keyed on it enforces nothing at all.
//
// Fails closed at every step: no pinned keys, no signature, a signature git
// cannot check because the runner lacks the public key, or a fingerprint that
// does not match, all leave the commit exactly as un-noted as it was.
func (s *Service) forgeVerdict(sha string, opts RangeVerifyOptions) (domain.CommitVerdict, bool) {
	if opts.ExternalPolicy == ExternalRequire || len(opts.ForgeKeys) == 0 {
		return domain.CommitVerdict{}, false
	}
	_, fingerprint, good, err := s.repo.CommitSignature(sha)
	if err != nil || !good || !domain.ForgeKeyMatches(fingerprint, opts.ForgeKeys) {
		return domain.CommitVerdict{}, false
	}
	if opts.ForgePolicy == domain.ForgeAccept {
		return domain.CommitVerdict{SHA: sha, ForgeSigner: fingerprint}, true
	}
	// Rejected, but named. The verdict still fails; it no longer accuses a
	// developer of a --no-verify push they did not make.
	return domain.CommitVerdict{SHA: sha, Reason: domain.ReasonForgeAuthored}, true
}
