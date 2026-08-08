package cli

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/service"
)

// headSHA is the SHA of HEAD in the repo the test chdir'd into.
func headSHA(t *testing.T) string {
	t.Helper()
	svc, err := newService(autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	head, err := svc.Repo().HeadSHA()
	if err != nil {
		t.Fatal(err)
	}
	return head
}

// noteOnHead writes rec as HEAD's warden note, binding it to HEAD when the
// caller has not already done so, and returns the SHA. A caller that signs must
// bind first — CommitSHA is inside SigningPayload — so an already-set binding is
// left alone. Every `why` case needs a note; only the record's shape differs.
func noteOnHead(t *testing.T, rec domain.RunRecord) string {
	t.Helper()
	svc, err := newService(autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	head, err := svc.Repo().HeadSHA()
	if err != nil {
		t.Fatal(err)
	}
	if rec.CommitSHA == "" {
		rec.CommitSHA = head
	}
	if err := svc.Repo().WriteNote(head, rec); err != nil {
		t.Fatal(err)
	}
	return head
}

// TestCmdWhy_NoNote pins the answer for a commit the gate never saw: `why` is a
// question about the record, and saying "no record" is the honest answer — with
// a non-zero exit, because the caller asked whether provenance exists.
func TestCmdWhy_NoNote(t *testing.T) {
	repoWithConfig(t, "")
	var out, errb bytes.Buffer
	if code := cmdWhy(nil, &out, &errb); code != 1 {
		t.Fatalf("code = %d, want 1; out=%q err=%q", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "no warden note") {
		t.Errorf("output should say there is no note, got %q", out.String())
	}
}

// TestCmdWhy_RendersRecord drives the whole render over a fully populated note:
// every optional field is present, so every conditional branch is taken.
func TestCmdWhy_RendersRecord(t *testing.T) {
	dir := repoWithConfig(t, "")

	trace := []byte(`{"version":"0.1","id":"tr-1"}`)
	if err := os.WriteFile(filepath.Join(dir, "trace.json"), trace, 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(trace)

	pub, priv, _ := ed25519.GenerateKey(nil)
	rec := domain.RunRecord{
		CommitSHA:         headSHA(t),
		RunID:             "run_1",
		Timestamp:         "2026-08-08T00:00:00Z",
		WardenVersion:     "0.24.2",
		StepsRun:          []domain.StepName{"lint", "review"},
		Agent:             map[domain.StepName]string{"review": "claude"},
		MatchedRules:      []string{"main", "docs"},
		EvidenceChainRoot: "h0",
		Evidence:          []domain.EvidenceEntry{{Hash: "h0"}},
		Dependencies:      []domain.DependencyManifest{{Ecosystem: "go", Path: "go.sum", Digest: "sha256:abc"}},
		AgentTrace: &domain.AgentTraceRef{
			Digest: hex.EncodeToString(sum[:]), Path: "trace.json", SpecVersion: "0.1", TraceID: "tr-1",
		},
		PublicKey: base64.StdEncoding.EncodeToString(pub),
	}
	p, _ := rec.SigningPayload()
	rec.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, p))
	head := noteOnHead(t, rec)

	var out, errb bytes.Buffer
	// An explicit commit argument is taken as the commit, not as a flag.
	if code := cmdWhy([]string{head}, &out, &errb); code != 0 {
		t.Fatalf("code = %d, want 0; err=%q", code, errb.String())
	}
	got := out.String()
	for _, want := range []string{
		"run:           run_1",
		"when:          2026-08-08T00:00:00Z",
		"warden:        0.24.2",
		"lint",
		"review(agent=claude)",
		"matched rules: main, docs",
		"evidence:      1 records",
		"valid, signed by",
		"agent trace:   trace.json (notarized, spec 0.1)",
		"sbom:          1 lockfile(s)",
		"go        go.sum  sha256:abc",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

// TestCmdWhy_AgentTraceStates covers the reason the trace line exists: warden
// notarized a record, and the question a reader has later is whether that record
// is still the one that was signed.
func TestCmdWhy_AgentTraceStates(t *testing.T) {
	cases := []struct {
		name    string
		write   []byte // nil = do not write the file at all
		digest  []byte // the bytes the note claims to have notarized
		wantSub string
	}{
		{"unchanged", []byte("trace-a"), []byte("trace-a"), "notarized, spec"},
		{"rewritten", []byte("trace-b"), []byte("trace-a"), "CHANGED SINCE"},
		{"deleted", nil, []byte("trace-a"), "no longer present"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := repoWithConfig(t, "")
			if tc.write != nil {
				if err := os.WriteFile(filepath.Join(dir, "trace.json"), tc.write, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			sum := sha256.Sum256(tc.digest)
			noteOnHead(t, domain.RunRecord{
				RunID: "r", EvidenceChainRoot: "h0", Evidence: []domain.EvidenceEntry{{Hash: "h0"}},
				AgentTrace: &domain.AgentTraceRef{Digest: hex.EncodeToString(sum[:]), Path: "trace.json", SpecVersion: "0.1"},
			})

			var out, errb bytes.Buffer
			if code := cmdWhy(nil, &out, &errb); code != 0 {
				t.Fatalf("code = %d, want 0; err=%q", code, errb.String())
			}
			if !strings.Contains(out.String(), tc.wantSub) {
				t.Errorf("trace line should report %q, got:\n%s", tc.wantSub, out.String())
			}
		})
	}
}

// TestCmdWhy_NoRulesIsNotNoOutput pins the empty-rule wording: a run that
// matched no rule ran under the base policy, which is a different statement
// from "warden could not tell".
func TestCmdWhy_NoRulesIsNotNoOutput(t *testing.T) {
	repoWithConfig(t, "")
	noteOnHead(t, domain.RunRecord{RunID: "r", EvidenceChainRoot: "h0", Evidence: []domain.EvidenceEntry{{Hash: "h0"}}})

	var out, errb bytes.Buffer
	if code := cmdWhy(nil, &out, &errb); code != 0 {
		t.Fatalf("code = %d, want 0; err=%q", code, errb.String())
	}
	if !strings.Contains(out.String(), "matched rules: (none — base policy)") {
		t.Errorf("expected the base-policy wording, got:\n%s", out.String())
	}
	// An unsigned note must say so rather than stay silent about signing.
	if !strings.Contains(out.String(), "signature:     unsigned") {
		t.Errorf("expected an explicit unsigned line, got:\n%s", out.String())
	}
}

func TestChainState(t *testing.T) {
	if got := chainState(true); got != "intact" {
		t.Errorf("chainState(true) = %q, want intact", got)
	}
	if got := chainState(false); got != "BROKEN" {
		t.Errorf("chainState(false) = %q, want BROKEN", got)
	}
}

func TestWhySignature(t *testing.T) {
	cases := []struct {
		name string
		res  service.VerifyResult
		want string
	}{
		{"valid", service.VerifyResult{Signed: true, SignatureValid: true, Signer: "fp1"}, "valid, signed by fp1"},
		{"present but bad", service.VerifyResult{Signed: true}, "present but INVALID"},
		{"absent", service.VerifyResult{}, "unsigned"},
	}
	for _, tc := range cases {
		if got := whySignature(tc.res); got != tc.want {
			t.Errorf("%s: whySignature = %q, want %q", tc.name, got, tc.want)
		}
	}
}
