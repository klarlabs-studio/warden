package service

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// Design 0006 PR 5a: the verifier side of affected-scope provenance. No
// writer records a scope yet, so these tests sign scoped records through the
// service's real signer and write them as notes, then read them back through
// every verifier.

// scopeOf returns a scope of the given kind as a writer records it: two of
// three workspaces checked by one per-workspace step when affected, all three
// when full.
func scopeOf(kind domain.SelectionKind) *domain.RunScope {
	all := []string{"apps/web", "packages/api", "packages/ui"}
	sc := &domain.RunScope{
		Version:             domain.SelectionVersion,
		Kind:                kind,
		HeadTree:            "headtree",
		ConfigurationDigest: "sha256:config",
		GraphDigest:         "sha256:graph",
		SelectionDigest:     "sha256:selection",
		Workspaces:          all,
		Steps:               map[domain.StepName]domain.StepScope{"test": {Selected: all, TemplateDigest: "sha256:tmpl"}},
	}
	if kind == domain.SelectionAffected {
		sc.Anchor, sc.AnchorTree = "anchorsha", "anchortree"
		sc.Steps = map[domain.StepName]domain.StepScope{"test": {Selected: []string{"apps/web", "packages/ui"}, TemplateDigest: "sha256:tmpl"}}
	}
	return sc
}

// scopedRecord is attestRecord with a scope.
func scopedRecord(sha, runID string, kind domain.SelectionKind) domain.RunRecord {
	rec := attestRecord(sha, runID)
	rec.Scope = scopeOf(kind)
	return rec
}

// ownKey is the fingerprint of the service's signer, for pinning.
func ownKey(t *testing.T, svc *Service) string {
	t.Helper()
	_, fp := svc.SigningKey()
	if fp == "" {
		t.Fatal("no signing key")
	}
	return fp
}

// An affected note is refused by default, pinned key or not, and is reported
// as refused for its scope alone: trusted, signed, chain intact. A full-scope
// note and a note without a scope both verify.
func TestVerify_AffectedScopeIsRefusedByDefault(t *testing.T) {
	for _, tc := range []struct {
		name      string
		kind      domain.SelectionKind
		validated bool
	}{
		{"affected", domain.SelectionAffected, false},
		{"full", domain.SelectionFull, true},
		{"no scope", "", true},
		{"unknown kind", "partial", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, svc := newRepoSvc(t)
			c := commit(t, dir, svc, "c")
			rec := attestRecord(c, "r1")
			if tc.kind != "" {
				rec.Scope = scopeOf(tc.kind)
			}
			if err := svc.Repo().WriteNote(c, signAs(t, svc, rec)); err != nil {
				t.Fatal(err)
			}
			for _, keys := range [][]string{nil, {ownKey(t, svc)}} {
				res, err := svc.VerifyWithPolicy(c, ExternalReject, keys...)
				if err != nil {
					t.Fatal(err)
				}
				if res.Validated != tc.validated {
					t.Fatalf("keys=%v: Validated = %v, want %v", keys, res.Validated, tc.validated)
				}
				if res.ScopeRefused == tc.validated || res.Affected == tc.validated {
					t.Errorf("keys=%v: ScopeRefused=%v Affected=%v for validated=%v", keys, res.ScopeRefused, res.Affected, tc.validated)
				}
				if len(keys) > 0 && !res.Trusted {
					t.Errorf("keys=%v: the note's trust must survive the scope refusal", keys)
				}
				// The external-run policy does not widen it either.
				if res2, _ := svc.VerifyWithPolicy(c, ExternalAllow, keys...); res2.Validated != tc.validated {
					t.Errorf("ExternalAllow changed the scope verdict")
				}
			}
		})
	}
}

// editNote rewrites the note on sha through git itself, as a hand-edit would:
// it decodes the stored JSON into a generic map, applies edit, and writes the
// result back verbatim, never through RunRecord.
func editNote(t *testing.T, dir, sha string, edit func(map[string]any)) {
	t.Helper()
	show := exec.Command("git", "notes", "--ref=warden", "show", sha)
	show.Dir = dir
	raw, err := show.Output()
	if err != nil {
		t.Fatalf("git notes show: %v", err)
	}
	var note map[string]any
	if err := json.Unmarshal(raw, &note); err != nil {
		t.Fatal(err)
	}
	edit(note)
	out, err := json.Marshal(note)
	if err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(t.TempDir(), "note.json")
	if err := os.WriteFile(f, out, 0o600); err != nil {
		t.Fatal(err)
	}
	add := exec.Command("git", "notes", "--ref=warden", "add", "-f", "-F", f, sha)
	add.Dir = dir
	if b, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git notes add: %v: %s", err, b)
	}
}

// ScopeCannotBeStripped: an affected note whose scope was deleted by hand
// reads, field-wise, as an unscoped full run. Its signature no longer
// verifies, so a pinned verify refuses it, and so does the range gate — as
// tampering, never as a validated full run.
func TestVerify_ScopeCannotBeStripped(t *testing.T) {
	for name, edit := range map[string]func(map[string]any){
		"scope removed":       func(n map[string]any) { delete(n, "scope") },
		"kind rewritten":      func(n map[string]any) { n["scope"].(map[string]any)["kind"] = "full" },
		"scope nulled":        func(n map[string]any) { n["scope"] = nil },
		"untouched (control)": func(map[string]any) {}, // an unedited affected note stays refused for its scope
	} {
		t.Run(name, func(t *testing.T) {
			dir, svc := newRepoSvc(t)
			base := commit(t, dir, svc, "base")
			c := commit(t, dir, svc, "c")
			if err := svc.Repo().WriteNote(c, signAs(t, svc, scopedRecord(c, "r1", domain.SelectionAffected))); err != nil {
				t.Fatal(err)
			}
			editNote(t, dir, c, edit)
			rec, err := svc.Repo().ReadNote(c)
			if err != nil || rec == nil {
				t.Fatalf("read edited note: %v", err)
			}
			key := ownKey(t, svc)
			res, err := svc.Verify(c, key)
			if err != nil {
				t.Fatal(err)
			}
			if res.Validated {
				t.Fatalf("a pinned verify accepted the edited note (scope now %v)", rec.Scope)
			}
			rng, err := svc.VerifyRange(base, c, RangeVerifyOptions{TrustedKeys: []string{key}})
			if err != nil {
				t.Fatal(err)
			}
			got := verdict(t, rng, c).Reason
			if name == "untouched (control)" {
				if got != domain.ReasonAffectedScope {
					t.Fatalf("untouched affected note: reason %q, want %q", got, domain.ReasonAffectedScope)
				}
				return
			}
			if !rec.FullScope() {
				t.Fatalf("precondition: the edit must make the record read as full, scope=%v", rec.Scope)
			}
			if res.ScopeRefused || !res.Signed || res.SignatureValid {
				t.Errorf("an edited note must fail on its signature: %+v", res)
			}
			if got != domain.ReasonBrokenChain {
				t.Errorf("range reason %q, want %q (a broken signature is tampering)", got, domain.ReasonBrokenChain)
			}
		})
	}
}

// The range gate refuses an affected tip with its own reason, and the commits
// its push span covers are refused with it — named as covered by that note,
// not accused of a --no-verify push. A full-scope span covers them as usual.
func TestVerifyRange_AffectedScopeAndItsSpan(t *testing.T) {
	for _, tc := range []struct {
		kind   domain.SelectionKind
		passes bool
	}{{domain.SelectionAffected, false}, {domain.SelectionFull, true}} {
		t.Run(string(tc.kind), func(t *testing.T) {
			dir, svc := newRepoSvc(t)
			base := commit(t, dir, svc, "base")
			a := commit(t, dir, svc, "A")
			tip := commit(t, dir, svc, "B (tip)")
			rec := scopedRecord(tip, "r1", tc.kind)
			rec.CoversFrom = base
			if err := svc.Repo().WriteNote(tip, signAs(t, svc, rec)); err != nil {
				t.Fatal(err)
			}
			for name, opts := range map[string]RangeVerifyOptions{
				"attested":       {},
				"require-signed": {RequireSigned: true},
				"pinned":         {TrustedKeys: []string{ownKey(t, svc)}},
				"allow-external": {ExternalPolicy: ExternalAllow},
			} {
				res, err := svc.VerifyRange(base, tip, opts)
				if err != nil {
					t.Fatal(err)
				}
				if res.OK() != tc.passes {
					t.Fatalf("%s: OK = %v, want %v: %+v", name, res.OK(), tc.passes, res.Commits)
				}
				if tc.passes {
					if v := verdict(t, res, a); v.CoveredBy != tip {
						t.Errorf("%s: a full span must cover A: %+v", name, v)
					}
					continue
				}
				if v := verdict(t, res, tip); v.Reason != domain.ReasonAffectedScope {
					t.Errorf("%s: tip reason %q, want %q", name, v.Reason, domain.ReasonAffectedScope)
				}
				if v := verdict(t, res, a); v.Reason != domain.ReasonAffectedScope || v.CoveredBy != tip {
					t.Errorf("%s: span commit under an affected tip = %+v, want refused as affected-scope, covered by the tip", name, v)
				}
			}
		})
	}
}

// A span commit refused under an affected tip stays a gap: a full span that
// also covers it still lets it pass, whichever of the two notes the gate reads
// first.
func TestVerifyRange_FullSpanStillCoversWhatAnAffectedSpanRefused(t *testing.T) {
	for _, newerIsAffected := range []bool{false, true} {
		name := map[bool]string{false: "affected note older", true: "affected note newer"}[newerIsAffected]
		t.Run(name, func(t *testing.T) {
			dir, svc := newRepoSvc(t)
			base := commit(t, dir, svc, "base")
			a := commit(t, dir, svc, "A")
			mid := commit(t, dir, svc, "B")
			tip := commit(t, dir, svc, "C")
			affectedAt, fullAt := mid, tip
			if newerIsAffected {
				affectedAt, fullAt = tip, mid
			}
			affected := scopedRecord(affectedAt, "r1", domain.SelectionAffected)
			affected.CoversFrom = base
			full := scopedRecord(fullAt, "r2", domain.SelectionFull)
			full.CoversFrom = base
			for sha, rec := range map[string]domain.RunRecord{affectedAt: affected, fullAt: full} {
				if err := svc.Repo().WriteNote(sha, signAs(t, svc, rec)); err != nil {
					t.Fatal(err)
				}
			}
			res, err := svc.VerifyRange(base, tip, RangeVerifyOptions{TrustedKeys: []string{ownKey(t, svc)}})
			if err != nil {
				t.Fatal(err)
			}
			if v := verdict(t, res, a); !v.OK() || v.CoveredBy != fullAt {
				t.Errorf("A is inside a trusted full span and must pass: %+v", v)
			}
			if v := verdict(t, res, affectedAt); v.Reason != domain.ReasonAffectedScope {
				t.Errorf("the affected note itself is still refused: %+v", v)
			}
		})
	}
}

// Execution requirements describe a full run; an affected note is refused
// with its own reason before any contract is compared.
func TestVerifyRequirements_RefusesAffectedScope(t *testing.T) {
	dir, svc := newRepoSvc(t)
	c := commit(t, dir, svc, "c")
	if err := svc.Repo().WriteNote(c, signAs(t, svc, scopedRecord(c, "r1", domain.SelectionAffected))); err != nil {
		t.Fatal(err)
	}
	req := domain.ExecutionRequirements{Version: 2, Profile: &domain.ProfileRequirement{Min: "permissive"}}
	res, err := svc.VerifyRequirements(c, ExternalReject, req, ownKey(t, svc))
	if err != nil {
		t.Fatal(err)
	}
	if res.Validated || len(res.RequirementFailures) != 1 || res.RequirementFailures[0] != scopeRequirementFailure {
		t.Fatalf("requirements must refuse an affected note for its scope: %+v", res)
	}
}

// An affected note conforms to no compliance profile: profiles describe what
// a gate observed of a full run. The note is otherwise a perfect standard
// note, so the scope clause is the only thing that fails, and the @1 profile
// definitions are untouched.
func TestConformance_AffectedScopeConformsToNoProfile(t *testing.T) {
	for _, level := range []string{"permissive", "standard", "strict"} {
		t.Run(level, func(t *testing.T) {
			dir, svc := newRepoSvc(t)
			c := commit(t, dir, svc, "c")
			rec := profiledRecord(t, c, "r1")
			rec.Scope = scopeOf(domain.SelectionAffected)
			if err := svc.Repo().WriteNote(c, signAs(t, svc, rec)); err != nil {
				t.Fatal(err)
			}
			minimum, err := domain.ParseComplianceProfile(level)
			if err != nil {
				t.Fatal(err)
			}
			res, err := svc.VerifyMinProfile(c, ExternalReject, []domain.ComplianceProfile{minimum}, ownKey(t, svc))
			if err != nil {
				t.Fatal(err)
			}
			if res.Validated || len(res.Conformance) != 1 {
				t.Fatalf("an affected note must not conform to %s: %+v", level, res)
			}
			rep := res.Conformance[0]
			if len(rep.Failures) != 1 || !strings.HasPrefix(rep.Failures[0], clauseScope+": ") {
				t.Errorf("the failure must name the scope clause, not trust: %v", rep.Failures)
			}

			// The same report, read straight from Conformance with the note
			// already accepted (as a future acceptance policy would), still
			// fails: the clause does not lean on Validated.
			direct := Conformance(minimum, VerifyResult{SHA: c, Validated: true, Trusted: true, Record: res.Record})
			if direct.OK() {
				t.Errorf("Conformance passed an affected note it was told was validated")
			}
		})
	}
}

// Reattest preserves scope: an affected note relocated onto a tree-identical
// commit stays affected and refused, even though its trust holds through the
// carried original. A re-attester that strips the outer scope breaks the
// payload equality, and the carried original still reads as affected.
func TestReattest_PreservesAffectedScope(t *testing.T) {
	dir, svc := newRepoSvc(t)
	a := commit(t, dir, svc, "A")
	b := commit(t, dir, svc, "B") // same tree as a: the squash shape
	src := signAs(t, svc, scopedRecord(a, "rA", domain.SelectionAffected))
	if err := svc.Repo().WriteNote(a, src); err != nil {
		t.Fatal(err)
	}
	if res, err := svc.Reattest(b, false); err != nil || !res.Wrote {
		t.Fatalf("reattest: %+v %v", res, err)
	}
	rec, err := svc.Repo().ReadNote(b)
	if err != nil || rec == nil {
		t.Fatalf("no note on b: %v", err)
	}
	want, _ := json.Marshal(src.Scope)
	for name, got := range map[string]*domain.RunScope{"outer": rec.Scope, "carried": rec.CarriedOriginal.Scope} {
		if b, _ := json.Marshal(got); string(b) != string(want) {
			t.Errorf("%s scope altered by re-attestation:\n got %s\nwant %s", name, b, want)
		}
	}

	key := ownKey(t, svc)
	res, err := svc.Verify(b, key)
	if err != nil {
		t.Fatal(err)
	}
	if res.Validated || !res.ScopeRefused || !res.Trusted {
		t.Fatalf("a re-attested affected note must stay refused for its scope alone: %+v", res)
	}

	stripped := *rec
	stripped.Scope = nil
	if svc.carriedOriginalHolds(b, &stripped, svc.reattestTrustSet()) {
		t.Error("a re-attestation with its scope stripped must not hold")
	}
	if stripped.FullScope() {
		t.Error("a re-attestation carrying an affected original must not read as full")
	}
}
