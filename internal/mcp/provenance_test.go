package mcpserver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"go.klarlabs.de/mcp"

	"go.klarlabs.de/warden/internal/domain"
)

// "Is HEAD gated?" is the question an agent asks most, so it must cost no
// arguments. An empty commit defaults to HEAD rather than erroring or verifying
// whatever the empty string resolves to.
func TestHandleVerify_DefaultsToHEAD(t *testing.T) {
	f := &fakeFacade{verify: ProvenanceRecord{Validated: true}}
	out, err := handleVerify(f, VerifyInput{})
	if err != nil {
		t.Fatalf("handleVerify: %v", err)
	}
	if f.verifyArg.commit != "HEAD" {
		t.Errorf("commit passed through = %q, want HEAD", f.verifyArg.commit)
	}
	if out.SHA != "HEAD" {
		t.Errorf("SHA = %q, want the resolved default in the output too", out.SHA)
	}
	if !out.Validated {
		t.Error("Validated must carry the facade's verdict")
	}
}

// An explicit commit and pinned keys must reach the facade unchanged — pinning a
// key is the difference between "a warden ran here" and "a warden I trust ran
// here", so silently dropping it would weaken the check without saying so.
func TestHandleVerify_PassesCommitAndKeysThrough(t *testing.T) {
	f := &fakeFacade{}
	keys := []string{"3a76a2b850d0e957", "beefcafe"}
	if _, err := handleVerify(f, VerifyInput{Commit: "abc123", TrustedKeys: keys}); err != nil {
		t.Fatalf("handleVerify: %v", err)
	}
	if f.verifyArg.commit != "abc123" {
		t.Errorf("commit = %q, want abc123", f.verifyArg.commit)
	}
	if len(f.verifyArg.keys) != 2 || f.verifyArg.keys[0] != keys[0] {
		t.Errorf("trusted keys = %v, want %v", f.verifyArg.keys, keys)
	}
}

// A commit with no note is a well-formed negative answer, not an error: the
// caller asked a yes/no question and "no" is a legitimate reply.
func TestHandleVerify_UnvalidatedIsNotAnError(t *testing.T) {
	f := &fakeFacade{verify: ProvenanceRecord{Validated: false}}
	out, err := handleVerify(f, VerifyInput{Commit: "deadbeef"})
	if err != nil {
		t.Fatalf("an unvalidated commit must not error: %v", err)
	}
	if out.Validated {
		t.Error("Validated must be false")
	}
	if out.RunID != "" || len(out.Steps) != 0 {
		t.Error("record-derived fields must stay empty when there is no record")
	}
}

// The record's own fields describe what the gate ACTUALLY did, so they must be
// projected from the note rather than re-derived from current policy.
func TestHandleVerify_ProjectsTheRecord(t *testing.T) {
	f := &fakeFacade{verify: ProvenanceRecord{
		Validated: true,
		Signed:    true, SignatureValid: true, Signer: "3a76a2b850d0e957", Trusted: true,
		Record: &domain.RunRecord{
			RunID:          "run-1",
			StepsRun:       []domain.StepName{domain.StepLint, domain.StepTest},
			MatchedRules:   []string{"branch:main"},
			WardenVersion:  "0.20.4",
			ReattestedFrom: "cafe1234",
			CoversFrom:     "base9999",
		},
	}}
	out, err := handleVerify(f, VerifyInput{Commit: "abc"})
	if err != nil {
		t.Fatalf("handleVerify: %v", err)
	}
	if out.RunID != "run-1" || out.WardenVersion != "0.20.4" {
		t.Errorf("record fields not projected: %+v", out)
	}
	if len(out.Steps) != 2 {
		t.Errorf("Steps = %v, want the note's two steps", out.Steps)
	}
	// A re-attestation asserts "the same validated content under a new commit
	// id", never a fresh validation. An auditor must be able to tell them apart.
	if out.ReattestedFrom != "cafe1234" {
		t.Error("ReattestedFrom must survive; it distinguishes a carried-over record from a fresh run")
	}
	if out.CoversFrom != "base9999" {
		t.Error("CoversFrom must survive; it is the signed push span")
	}
	if !out.Trusted || !out.SignatureValid {
		t.Error("signature verdict must carry through")
	}
}

func TestHandleVerify_PropagatesError(t *testing.T) {
	f := &fakeFacade{verifyErr: errors.New("boom")}
	if _, err := handleVerify(f, VerifyInput{}); err == nil {
		t.Fatal("expected the facade error to propagate")
	}
}

// A range gate must never take its trust anchor from the head it is checking. So
// with no keys pinned the roster is resolved from the BASE ref — the trusted
// side. This is the security-relevant default in the whole surface.
func TestHandleVerifyRange_ResolvesRosterFromBaseWhenNoKeysPinned(t *testing.T) {
	f := &fakeFacade{}
	if _, err := handleVerifyRange(f, VerifyRangeInput{Base: "origin/main"}); err != nil {
		t.Fatalf("handleVerifyRange: %v", err)
	}
	if !f.rangeVerifyArg.opts.UseRoster {
		t.Error("with no keys pinned the roster must be read from the base ref, not the head under test")
	}
}

// An explicitly pinned key set is an out-of-band anchor and wins outright, so
// the base-ref roster must not also be consulted.
func TestHandleVerifyRange_PinnedKeysSuppressTheRoster(t *testing.T) {
	f := &fakeFacade{}
	_, err := handleVerifyRange(f, VerifyRangeInput{Base: "origin/main", TrustedKeys: []string{"abc"}})
	if err != nil {
		t.Fatalf("handleVerifyRange: %v", err)
	}
	if f.rangeVerifyArg.opts.UseRoster {
		t.Error("explicitly pinned keys must win outright over the base-ref roster")
	}
	if len(f.rangeVerifyArg.opts.TrustedKeys) != 1 {
		t.Errorf("pinned keys = %v, want them passed through", f.rangeVerifyArg.opts.TrustedKeys)
	}
}

// SkipMerges defaults TRUE, matching the CLI: a merge commit's parents are gated
// individually, so gating the merge itself reports a failure for a commit that
// was never separately validated. The pointer distinguishes unset from false.
func TestHandleVerifyRange_SkipMergesDefaultsTrueButIsOverridable(t *testing.T) {
	f := &fakeFacade{}
	if _, err := handleVerifyRange(f, VerifyRangeInput{Base: "origin/main"}); err != nil {
		t.Fatalf("handleVerifyRange: %v", err)
	}
	if !f.rangeVerifyArg.opts.SkipMerges {
		t.Error("skip_merges must default to true")
	}

	off := false
	if _, err := handleVerifyRange(f, VerifyRangeInput{Base: "origin/main", SkipMerges: &off}); err != nil {
		t.Fatalf("handleVerifyRange: %v", err)
	}
	if f.rangeVerifyArg.opts.SkipMerges {
		t.Error("an explicit skip_merges=false must be honored, not read as unset")
	}
}

func TestHandleVerifyRange_DefaultsHeadToHEAD(t *testing.T) {
	f := &fakeFacade{}
	if _, err := handleVerifyRange(f, VerifyRangeInput{Base: "origin/main"}); err != nil {
		t.Fatalf("handleVerifyRange: %v", err)
	}
	if f.rangeVerifyArg.head != "HEAD" {
		t.Errorf("head = %q, want HEAD", f.rangeVerifyArg.head)
	}
}

// doctor and audit report the same schema, so an agent learns one shape. The
// tallies are computed once here rather than by each caller.
func TestHandleDoctorAndAudit_ShareTheSchemaAndCountCorrectly(t *testing.T) {
	rep := domain.AuditReport{
		Adoption: "adopt00",
		Branch:   "main",
		Commits: []domain.CommitStatus{
			{SHA: "a", HasNote: true, ChainIntact: true},
			{SHA: "b", HasNote: true, ChainIntact: false},
			{SHA: "c"},                              // never gated
			{SHA: "d", ReattestableFrom: "gated99"}, // recoverable gap
		},
	}
	f := &fakeFacade{doctor: rep, audit: rep}

	doc, err := handleDoctor(f, BranchInput{Branch: "main"})
	if err != nil {
		t.Fatalf("handleDoctor: %v", err)
	}
	aud, err := handleAudit(f, BranchInput{Branch: "main"})
	if err != nil {
		t.Fatalf("handleAudit: %v", err)
	}

	if doc.Verified != 2 || doc.Intact != 1 || doc.Unverified != 2 {
		t.Errorf("counts = verified %d intact %d unverified %d, want 2/1/2", doc.Verified, doc.Intact, doc.Unverified)
	}
	// The recoverable share of the gap must be distinguishable from commits that
	// were genuinely never gated — otherwise "2 unverified" reads as two holes
	// when one is a squash-merge whose content WAS validated.
	if doc.Reattestable != 1 {
		t.Errorf("Reattestable = %d, want 1", doc.Reattestable)
	}
	if len(doc.Commits) != 4 {
		t.Errorf("Commits = %d, want per-commit detail", len(doc.Commits))
	}
	if doc.Verified != aud.Verified || len(doc.Commits) != len(aud.Commits) {
		t.Error("doctor and audit must report the same schema")
	}
	if f.doctorBranch != "main" || f.auditBranch != "main" {
		t.Error("branch must pass through to both")
	}
}

func TestHandleDoctor_PropagatesError(t *testing.T) {
	if _, err := handleDoctor(&fakeFacade{doctorErr: errors.New("no repo")}, BranchInput{}); err == nil {
		t.Fatal("expected the facade error to propagate")
	}
}

// A trust refusal must reach the caller as its own message, not as the
// dispatcher's generic "internal error". The refusal names the opt-in that
// resolves it, and an agent that cannot read it has no move but to give up or
// retry identically. Both properties must hold at once: the message travels
// (errors.As finds a ToolInputError, which the dispatcher renders) AND the
// original sentinel still matches (errors.Is), so callers keep their handling.
func TestHandleRunTrigger_RefusalIsLegibleAndStillMatchesTheSentinel(t *testing.T) {
	sentinel := errors.New("run_trigger is refused: this repo is not trusted; run `warden trust add`")
	deny := RunGate(func() error { return sentinel })

	_, err := handleRunTrigger(context.Background(), &fakeFacade{}, deny, RunTriggerInput{Hook: "pre-push"})
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("the original error must stay matchable with errors.Is, got %v", err)
	}
	var inputErr *mcp.ToolInputError
	if !errors.As(err, &inputErr) {
		t.Fatal("the refusal must carry a ToolInputError so the dispatcher shows its message rather than 'internal error'")
	}
	if !strings.Contains(inputErr.Message, "not trusted") {
		t.Errorf("the actionable detail must survive, got %q", inputErr.Message)
	}
}

// errVisible must be safe to apply unconditionally at a call site.
func TestErrVisible_NilStaysNil(t *testing.T) {
	if err := errVisible(nil); err != nil {
		t.Errorf("errVisible(nil) = %v, want nil", err)
	}
}

// The provenance tools are pure reads. They must be reachable without the trust
// opt-in that run_trigger requires, because that checkpoint exists to stop
// arbitrary shell from an untrusted .warden.yaml — and reading a note cannot do
// that. A refusing gate must not make them unavailable.
func TestProvenanceTools_DoNotConsultTheRunGate(t *testing.T) {
	refuse := RunGate(func() error { return errors.New("untrusted repo") })
	srv := NewServer(&fakeFacade{}, "1.2.3", refuse)
	if srv == nil {
		t.Fatal("NewServer returned nil")
	}
	// The handlers take no gate argument at all — that is the structural
	// guarantee. Exercise them to confirm they answer regardless.
	f := &fakeFacade{verify: ProvenanceRecord{Validated: true}}
	if _, err := handleVerify(f, VerifyInput{}); err != nil {
		t.Errorf("verify must answer under a refusing gate: %v", err)
	}
	if _, err := handleDoctor(f, BranchInput{}); err != nil {
		t.Errorf("doctor must answer under a refusing gate: %v", err)
	}
	if _, err := handleAudit(f, BranchInput{}); err != nil {
		t.Errorf("audit must answer under a refusing gate: %v", err)
	}
	if _, err := handleVerifyRange(f, VerifyRangeInput{Base: "origin/main"}); err != nil {
		t.Errorf("verify_range must answer under a refusing gate: %v", err)
	}
}
