package cli

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/service"
)

// cliScope is a scope as a writer records it. Affected: one per-workspace
// step ran for two of three workspaces.
func cliScope(kind domain.SelectionKind) *domain.RunScope {
	all := []string{"apps/web", "packages/api", "packages/ui"}
	selected := all
	if kind == domain.SelectionAffected {
		selected = []string{"apps/web", "packages/ui"}
	}
	return &domain.RunScope{
		Version: domain.SelectionVersion, Kind: kind, HeadTree: "tree",
		ConfigurationDigest: "sha256:c", GraphDigest: "sha256:g", SelectionDigest: "sha256:s",
		Workspaces: all,
		Steps:      map[domain.StepName]domain.StepScope{domain.StepTest: {Selected: selected, TemplateDigest: "sha256:t"}},
	}
}

// scopedNoteOnHead signs a profiled, push-span record carrying scope and writes
// it as the note on a new commit above the repository's first one. It returns
// the base, the commit between and the tip, plus the signer's fingerprint.
func scopedNoteOnHead(t *testing.T, scope *domain.RunScope) (base, mid, tip, fp string) {
	t.Helper()
	dir := repoWithConfig(t, "")
	git := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = dir
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	base = git("rev-parse", "HEAD")
	git("commit", "--allow-empty", "--no-verify", "-m", "mid")
	mid = git("rev-parse", "HEAD")
	git("commit", "--allow-empty", "--no-verify", "-m", "tip")
	tip = git("rev-parse", "HEAD")

	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	permissive, err := domain.ParseComplianceProfile("permissive")
	if err != nil {
		t.Fatal(err)
	}
	ref := domain.NewProfileRef(permissive)
	executed := domain.StepObservation{Outcome: domain.StepExecuted}
	rec := domain.RunRecord{
		RunID: "scoped-run", CommitSHA: tip, CoversFrom: base,
		StepsRun:          []domain.StepName{domain.StepCredentials, domain.StepLint, domain.StepTest},
		EvidenceChainRoot: "root", Evidence: []domain.EvidenceEntry{{Hash: "root"}},
		PublicKey:    base64.StdEncoding.EncodeToString(pub),
		Profile:      &ref,
		StepOutcomes: map[domain.StepName]domain.StepObservation{domain.StepCredentials: executed, domain.StepLint: executed, domain.StepTest: executed},
		Approval:     &domain.ApprovalRef{Channel: domain.ApprovalNone},
		Scope:        scope,
	}
	payload, err := rec.SigningPayload()
	if err != nil {
		t.Fatal(err)
	}
	rec.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
	svc, err := newService(autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Repo().WriteNote(tip, rec); err != nil {
		t.Fatal(err)
	}
	return base, mid, tip, rec.SignerFingerprint()
}

// Every verify surface refuses an affected note by default and says why with
// the reason affected-scope; the same note with a full scope passes each one.
func TestCmdVerify_AffectedScopeIsRefusedEverywhere(t *testing.T) {
	for _, kind := range []domain.SelectionKind{domain.SelectionAffected, domain.SelectionFull} {
		t.Run(string(kind), func(t *testing.T) {
			base, mid, _, fp := scopedNoteOnHead(t, cliScope(kind))
			req := filepath.Join(t.TempDir(), "requirements.json")
			if err := os.WriteFile(req, []byte(`{"version":2,"profile":{"min":"permissive"}}`), 0o600); err != nil {
				t.Fatal(err)
			}
			affected := kind == domain.SelectionAffected
			for _, tc := range []struct {
				name    string
				args    []string
				refusal string
			}{
				{"key", []string{"--key", fp}, "affected-scope: the note attests an affected-scope run (affected: test[2/3 workspaces])"},
				{"no key", nil, "affected-scope"},
				{"require-signed", []string{"--require-signed"}, "affected-scope"},
				{"allow-external", []string{"--key", fp, "--allow-external"}, "affected-scope"},
				{"requirements", []string{"--key", fp, "--requirements", req}, "affected-scope: the note attests per-workspace checks"},
				{"min-profile", []string{"--key", fp, "--min-profile", "permissive"}, "the note attests a full run, not an affected-workspace selection"},
				{"range", []string{"--range", base + "..HEAD", "--key", fp}, "affected-scope run"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var out, errb bytes.Buffer
					code := cmdVerify(tc.args, &out, &errb)
					all := out.String() + errb.String()
					if !affected {
						if code != 0 {
							t.Fatalf("a full-scope note must pass: exit %d\n%s", code, all)
						}
						return
					}
					if code != 1 {
						t.Fatalf("exit %d, want 1:\n%s", code, all)
					}
					if !strings.Contains(all, tc.refusal) {
						t.Fatalf("output lacks %q:\n%s", tc.refusal, all)
					}
				})
			}

			// --range --json carries the reason on the tip AND on the span
			// commit under it, which names the tip as its covering note.
			var out, errb bytes.Buffer
			code := cmdVerify([]string{"--range", base + "..HEAD", "--key", fp, "--json"}, &out, &errb)
			var export rangeExport
			if err := json.Unmarshal(out.Bytes(), &export); err != nil {
				t.Fatalf("json: %v\n%s", err, out.String())
			}
			if !affected {
				if code != 0 || !export.OK {
					t.Fatalf("full scope range: exit %d %+v", code, export)
				}
				return
			}
			if code != 1 || export.OK || export.Failed != 2 {
				t.Fatalf("range json: exit %d %+v", code, export)
			}
			for _, c := range export.Commits {
				if c.Reason != domain.ReasonAffectedScope {
					t.Errorf("commit %s reason %q, want %q", c.SHA, c.Reason, domain.ReasonAffectedScope)
				}
				if c.SHA == mid && c.CoveredBy == "" {
					t.Errorf("the span commit must name its refused covering note: %+v", c)
				}
			}
		})
	}
}

// The full-scope pass names the scope it passed with, so a reader can see a
// monorepo note was full rather than assume it.
func TestCmdVerify_FullScopeIsNamed(t *testing.T) {
	_, _, _, fp := scopedNoteOnHead(t, cliScope(domain.SelectionFull))
	var out, errb bytes.Buffer
	if code := cmdVerify([]string{"--key", fp}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s %s", code, out.String(), errb.String())
	}
	if !strings.Contains(out.String(), "scope: full: test[3/3 workspaces]") {
		t.Errorf("a scoped full pass must name its scope:\n%s", out.String())
	}
}

// An affected note never gets WARDEN_SOURCE_GATED, nor anything that starts
// with it, and its verification result is FAILED, as verify reports. Its own
// levels still accumulate.
func TestBuildVSA_AffectedScope(t *testing.T) {
	res := gatedResult()
	res.Validated, res.ScopeRefused = false, true
	res.Record.Evidence = []domain.EvidenceEntry{{Hash: "h"}}
	res.Record.EvidenceChainRoot = "h"
	res.Record.Scope = cliScope(domain.SelectionAffected)
	res.SignatureValid, res.Trusted = true, true

	vsa := buildVSA(res, "", nil)
	if vsa.Predicate.VerificationResult != "FAILED" {
		t.Errorf("an affected note's VSA must be FAILED, got %s", vsa.Predicate.VerificationResult)
	}
	want := []string{levelAffectedGated, levelAffectedSigned, levelAffectedTrusted}
	if got := vsa.Predicate.VerifiedLevels; !slices.Equal(got, want) {
		t.Errorf("levels = %v, want %v", got, want)
	}
	for _, lvl := range vsa.Predicate.VerifiedLevels {
		for _, full := range []string{levelGated, levelSigned, levelTrusted} {
			if strings.HasPrefix(lvl, full) {
				t.Errorf("affected level %q starts with full level %q", lvl, full)
			}
		}
	}

	res.Record.Scope = cliScope(domain.SelectionFull)
	res.Validated, res.ScopeRefused = true, false
	full := buildVSA(res, "", nil)
	if full.Predicate.VerificationResult != "PASSED" || !slices.Equal(full.Predicate.VerifiedLevels, []string{levelGated, levelSigned, levelTrusted}) {
		t.Errorf("a full-scope note keeps the full levels: %+v", full.Predicate)
	}
}

// The native predicate carries the signed scope and says whether it is full.
func TestBuildStatement_CarriesScope(t *testing.T) {
	res := gatedResult()
	res.Record.Scope = cliScope(domain.SelectionAffected)
	st := buildStatement(res)
	if st.Predicate.Scope == nil || st.Predicate.Verification.FullScope {
		t.Errorf("an affected statement must carry its scope and fullScope=false: %+v", st.Predicate)
	}
	plain := gatedResult()
	if st := buildStatement(plain); st.Predicate.Scope != nil || !st.Predicate.Verification.FullScope {
		t.Errorf("an unscoped statement is full and carries no scope: %+v", st.Predicate)
	}
}

// `why` names the scope, says the note is refused by default, and does not
// call the intact chain of a note refused for its scope BROKEN.
func TestCmdWhy_RendersScope(t *testing.T) {
	_, _, tip, _ := scopedNoteOnHead(t, cliScope(domain.SelectionAffected))
	var out, errb bytes.Buffer
	if code := cmdWhy([]string{tip}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	for _, want := range []string{
		"scope:         affected: test[2/3 workspaces] — a partial check; `warden verify` refuses it by default",
		"chain intact",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("why output lacks %q:\n%s", want, out.String())
		}
	}
}

// axi verify and the MCP projection carry the scope verdict.
func TestAxiAndMCPVerify_AffectedScope(t *testing.T) {
	_, _, tip, fp := scopedNoteOnHead(t, cliScope(domain.SelectionAffected))
	svc, err := newService(autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	f := facade{svc: svc}
	var out, errb bytes.Buffer
	if code := axiVerify(f, []string{"--commit", tip, "--key", fp}, &out, &errb); code != 1 {
		t.Fatalf("axi verify of an affected note: exit %d\n%s%s", code, out.String(), errb.String())
	}
	for _, want := range []string{"scope_refused: true", "full_scope: false", "affected: test[2/3 workspaces]"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("axi output lacks %q:\n%s", want, out.String())
		}
	}
	rec, err := f.Verify(tip, []string{fp})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Validated || !rec.ScopeRefused || !rec.Trusted {
		t.Errorf("facade verify: %+v", rec)
	}
}

// The scope summary in a refused line comes from the record, so a refusal
// for an unknown kind says so rather than claiming "affected".
func TestPrintVerify_ScopeRefusedUnknownKind(t *testing.T) {
	var out bytes.Buffer
	printVerify(&out, service.VerifyResult{SHA: "abc123", ScopeRefused: true, Record: &domain.RunRecord{Scope: &domain.RunScope{Version: 1, Kind: "partial"}}}, true)
	if !strings.Contains(out.String(), `affected-scope: the note attests an affected-scope run (unknown kind "partial" (read as partial)`) {
		t.Errorf("unexpected refusal line:\n%s", out.String())
	}
}
