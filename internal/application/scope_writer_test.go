package application

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

// The writer side of affected-scope provenance (design 0006 §4, PR 5b): a
// pre-push run under select: affected measures its change from the nearest
// trusted full-scope ancestor, and records which workspaces it checked inside
// the signed note — or checks every workspace, and says why.

// scopeFixture is a pre-push run of one per-workspace step, `test`, over the
// pnpm-shaped wsGraph, with a change in packages/ui since the anchor.
type scopeFixture struct {
	cfg    domain.Config
	git    *fakeGit
	src    *fakeWorkspaces
	signer Signer
	kernel *fakeKernel
	forge  *fakeForge
	attest bool
}

func newScopeFixture(t *testing.T) *scopeFixture {
	t.Helper()
	cfg := wsConfig(map[string][]domain.StepName{"pre_push": {"lint", "test"}}, domain.WorkspaceSelectAffected)
	cfg.Status = domain.StatusConfig{Enabled: boolPtr(true)}
	cfg.PR = domain.PRConfig{Enabled: true}
	return &scopeFixture{
		cfg: cfg,
		git: &fakeGit{root: t.TempDir(), branch: "main", head: "head1", wt: &fakeWorktree{dir: "/wt", headSHA: "head1"},
			trees: map[string]string{"head1": "tree1"}},
		src: &fakeWorkspaces{
			tree:    "tree1",
			graphs:  map[string]domain.WorkspaceGraph{"tree1": wsGraph(), "anc1": wsGraph()},
			changed: []string{"packages/ui/button.ts"},
		},
		signer: newFakeSigner(t),
		kernel: &fakeKernel{outcomes: map[domain.StepName]domain.StepStatus{}},
		forge:  &fakeForge{available: true},
	}
}

func (f *scopeFixture) digest(t *testing.T) string {
	t.Helper()
	d, err := domain.DigestJSON(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// anchor returns a candidate for commit sha holding a full-scope note signed
// by signer, as the writer records one, after mutate.
func (f *scopeFixture) anchor(t *testing.T, sha string, signer Signer, mutate func(*domain.RunRecord)) AnchorCandidate {
	t.Helper()
	tmpl, err := domain.PerWorkspaceStep{Each: "pnpm test"}.TemplateDigest()
	if err != nil {
		t.Fatal(err)
	}
	rec := domain.RunRecord{
		RunID: "run_" + sha, CommitSHA: sha, StepsRun: []domain.StepName{"lint", "test"},
		EvidenceChainRoot: "root", Evidence: []domain.EvidenceEntry{{Kind: "x", Hash: "root"}},
		Scope: &domain.RunScope{
			Version: domain.SelectionVersion, Kind: domain.SelectionFull, HeadTree: "tree-" + sha,
			ConfigurationDigest: f.digest(t), Workspaces: []string{"apps/api", "apps/web", "packages/ui"},
			Full:  &domain.ForcedFull{Rule: domain.FullRuleNoAnchor},
			Steps: map[domain.StepName]domain.StepScope{"test": {Selected: []string{"apps/api", "apps/web", "packages/ui"}, TemplateDigest: tmpl}},
		},
	}
	if mutate != nil {
		mutate(&rec)
	}
	if signer != nil {
		r := &Runner{Signer: signer}
		if reason := r.sign(&rec); reason != "" {
			t.Fatal(reason)
		}
	}
	return AnchorCandidate{SHA: sha, Tree: "tree-" + sha, Record: rec}
}

// run runs pre-push and returns the result, the workspaces `test` ran for
// and the run error.
func (f *scopeFixture) run(t *testing.T) (RunResult, []string, error) {
	t.Helper()
	r := newRunner(t, f.git, f.kernel, fakeApprover{approve: true}, f.cfg)
	r.Workspaces = f.src
	r.Signer = f.signer
	r.Forge = f.forge
	r.Settings.AttestOnly = f.attest
	factory := &selectionFactory{fakeFactory: fakeFactory{kernel: f.kernel}}
	r.Kernels = factory
	var ran []string
	f.kernel.during = func(step domain.StepName) {
		if sel, err := factory.sc.Selection(step); err == nil && step == "test" {
			ran = sel.Selection.Paths()
		}
	}
	res, err := r.Run(context.Background(), domain.PrePush)
	return res, ran, err
}

// An anchor that qualifies: the run checks the changed workspace and its
// dependent, and the signed note records exactly that, the anchor it rests on
// and the tree it ran on.
func TestScopeWriter_AffectedRunRecordsItsScope(t *testing.T) {
	f := newScopeFixture(t)
	f.git.ancestors = []AnchorCandidate{f.anchor(t, "anc1", f.signer, nil)}
	res, ran, err := f.run(t)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"apps/web", "packages/ui"}; !reflect.DeepEqual(ran, want) {
		t.Fatalf("test ran for %v, want %v", ran, want)
	}
	if !reflect.DeepEqual(f.git.anchorSearches, []string{"head1"}) {
		t.Fatalf("the anchor search must start once, below the checked commit: %v", f.git.anchorSearches)
	}
	if !f.git.wroteNote || !f.git.pushed {
		t.Fatalf("an attested affected run writes its note and pushes (note %v, pushed %v)", f.git.wroteNote, f.git.pushed)
	}
	note := f.git.note
	s := note.Scope
	if s == nil || s.Kind != domain.SelectionAffected || s.Anchor != "anc1" || s.AnchorTree != "tree-anc1" || s.HeadTree != "tree1" ||
		s.ConfigurationDigest != f.digest(t) || s.Version != domain.SelectionVersion {
		t.Fatalf("scope %+v", s)
	}
	if want := []string{"apps/web", "packages/ui"}; !reflect.DeepEqual(s.Steps["test"].Selected, want) {
		t.Fatalf("scope.steps.test.selected = %v", s.Steps["test"].Selected)
	}
	if want := []string{"apps/api", "apps/web", "packages/ui"}; !reflect.DeepEqual(s.Workspaces, want) {
		t.Fatalf("scope.workspaces = %v", s.Workspaces)
	}
	if _, ok := s.Steps["lint"]; ok {
		t.Fatal("a repo-wide step has no workspace scope")
	}
	// Inside the signature: the note verifies, and reads as partial.
	if !note.VerifySignature() || note.FullScope() {
		t.Fatalf("signed %v, full %v", note.VerifySignature(), note.FullScope())
	}
	// Every surface says it plainly, the qualifier ahead of anything a forge
	// would clip.
	if !strings.Contains(res.Message, "scope affected: test[2/3 workspaces]") {
		t.Errorf("message %q", res.Message)
	}
	if len(f.forge.statuses) != 1 || !strings.HasPrefix(f.forge.statuses[0].description, "warden gate passed, affected scope: 2 of 3 workspaces (partial check)") {
		t.Errorf("status %+v", f.forge.statuses)
	}
	for _, want := range []string{"**Scope:** affected", "**2 of 3**", "`apps/web`", "`packages/ui`", "`anc1`", "refuses an affected-scope note by default"} {
		if !strings.Contains(f.forge.commentBody, want) {
			t.Errorf("PR comment missing %q:\n%s", want, f.forge.commentBody)
		}
	}
}

// Each check that forces full does so BEFORE any step runs, and the note
// records a full scope naming the rule: a signed full-scope note is what a
// later affected run anchors on.
func TestScopeWriter_ForcedFull(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(*testing.T, *scopeFixture)
		rule   domain.FullRule
		search bool // whether an anchor search is expected
		signed bool
	}{
		{"no signer", func(t *testing.T, f *scopeFixture) {
			f.git.ancestors = []AnchorCandidate{f.anchor(t, "anc1", f.signer, nil)}
			f.signer = nil
		}, domain.FullRuleNoSigner, false, false},
		{"attest-only", func(t *testing.T, f *scopeFixture) {
			f.git.ancestors = []AnchorCandidate{f.anchor(t, "anc1", f.signer, nil)}
			f.attest = true
		}, domain.FullRuleAttestOnly, false, true},
		{"no anchor", func(t *testing.T, f *scopeFixture) {}, domain.FullRuleNoAnchor, true, true},
		{"only an unsigned anchor", func(t *testing.T, f *scopeFixture) {
			f.git.ancestors = []AnchorCandidate{f.anchor(t, "anc1", nil, nil)}
		}, domain.FullRuleNoAnchor, true, true},
		{"an anchor signed by a key off the roster", func(t *testing.T, f *scopeFixture) {
			f.git.ancestors = []AnchorCandidate{f.anchor(t, "anc1", f.signer, nil)}
			f.cfg.TrustedKeys = []string{newFakeSigner(t).PublicKey()}
		}, domain.FullRuleNoAnchor, true, true},
		{"an anchor signed by a stranger", func(t *testing.T, f *scopeFixture) {
			f.git.ancestors = []AnchorCandidate{f.anchor(t, "anc1", newFakeSigner(t), nil)}
		}, domain.FullRuleNoAnchor, true, true},
		{"only an affected ancestor", func(t *testing.T, f *scopeFixture) {
			f.git.ancestors = []AnchorCandidate{f.anchor(t, "anc1", f.signer, func(r *domain.RunRecord) {
				r.Scope.Kind, r.Scope.Full, r.Scope.Anchor, r.Scope.AnchorTree = domain.SelectionAffected, nil, "anc0", "tree-anc0"
			})}
		}, domain.FullRuleNoAnchor, true, true},
		{"only an unscoped ancestor", func(t *testing.T, f *scopeFixture) {
			f.git.ancestors = []AnchorCandidate{f.anchor(t, "anc1", f.signer, func(r *domain.RunRecord) { r.Scope = nil })}
		}, domain.FullRuleNoAnchor, true, true},
		{"an anchor whose checks ran on another tree", func(t *testing.T, f *scopeFixture) {
			f.git.ancestors = []AnchorCandidate{f.anchor(t, "anc1", f.signer, func(r *domain.RunRecord) { r.Scope.HeadTree = "pre-fix" })}
		}, domain.FullRuleNoAnchor, true, true},
		{"an anchor under another configuration", func(t *testing.T, f *scopeFixture) {
			f.git.ancestors = []AnchorCandidate{f.anchor(t, "anc1", f.signer, func(r *domain.RunRecord) { r.Scope.ConfigurationDigest = "sha256:old" })}
		}, domain.FullRuleNoAnchor, true, true},
		{"an anchor that never ran the step", func(t *testing.T, f *scopeFixture) {
			f.git.ancestors = []AnchorCandidate{f.anchor(t, "anc1", f.signer, func(r *domain.RunRecord) { r.Scope.Steps = nil })}
		}, domain.FullRuleNoAnchor, true, true},
		{"a lockfile change since the anchor", func(t *testing.T, f *scopeFixture) {
			f.git.ancestors = []AnchorCandidate{f.anchor(t, "anc1", f.signer, nil)}
			f.src.changed = []string{"pnpm-lock.yaml"}
		}, domain.FullRuleLockfile, true, true},
		{"a writer after the per-workspace step", func(t *testing.T, f *scopeFixture) {
			f.git.ancestors = []AnchorCandidate{f.anchor(t, "anc1", f.signer, nil)}
			f.cfg.Steps["pre_push"] = []domain.StepName{"test", "fmt"}
			f.cfg.Writes = []string{"fmt"}
		}, domain.FullRuleWriterOrder, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newScopeFixture(t)
			tc.setup(t, f)
			res, ran, err := f.run(t)
			if err != nil {
				t.Fatal(err)
			}
			if len(ran) != 3 {
				t.Fatalf("a full run checks every workspace, ran %v", ran)
			}
			if searched := len(f.git.anchorSearches) > 0; searched != tc.search {
				t.Fatalf("anchor searched = %v, want %v", searched, tc.search)
			}
			s := f.git.note.Scope
			if s == nil || !s.IsFull() || s.Full == nil || s.Full.Rule != tc.rule || s.Anchor != "" {
				t.Fatalf("want a full scope forced by %s, got %+v", tc.rule, s)
			}
			if f.git.note.Signed() != tc.signed {
				t.Fatalf("signed = %v, want %v", f.git.note.Signed(), tc.signed)
			}
			if !strings.Contains(res.Message, "scope full: test[3/3 workspaces]") {
				t.Errorf("message %q", res.Message)
			}
		})
	}
}

// The nearest QUALIFYING ancestor anchors: a nearer note that does not
// qualify is passed over, not mistaken for the end of the search.
func TestScopeWriter_SkipsToTheNearestQualifyingAnchor(t *testing.T) {
	f := newScopeFixture(t)
	f.src.graphs["anc2"] = wsGraph()
	f.git.ancestors = []AnchorCandidate{
		f.anchor(t, "anc1", nil, nil), // unsigned
		f.anchor(t, "anc2", f.signer, nil),
	}
	if _, _, err := f.run(t); err != nil {
		t.Fatal(err)
	}
	if s := f.git.note.Scope; s == nil || s.Kind != domain.SelectionAffected || s.Anchor != "anc2" {
		t.Fatalf("scope %+v", s)
	}
	if !strings.Contains(strings.Join(f.src.asked, " "), "changed:anc2..tree1") {
		t.Fatalf("the change must be measured from the anchor to the checked tree: %v", f.src.asked)
	}
}

// The committed roster decides who may anchor, when there is one.
func TestScopeWriter_RosterSignerAnchors(t *testing.T) {
	f := newScopeFixture(t)
	ci := newFakeSigner(t)
	f.cfg.TrustedKeys = []string{ci.PublicKey()}
	f.git.ancestors = []AnchorCandidate{f.anchor(t, "anc1", ci, nil)}
	if _, _, err := f.run(t); err != nil {
		t.Fatal(err)
	}
	if s := f.git.note.Scope; s == nil || s.Kind != domain.SelectionAffected {
		t.Fatalf("a roster-signed anchor must anchor: %+v", s)
	}
}

// The selection was computed on one tree; the note attests another. The run
// refuses to write the affected claim, and nothing is pushed.
func TestScopeWriter_RefusesWhenTheAttestedTreeIsNotTheCheckedOne(t *testing.T) {
	f := newScopeFixture(t)
	f.git.ancestors = []AnchorCandidate{f.anchor(t, "anc1", f.signer, nil)}
	f.git.trees["head1"] = "tree-after-a-rewrite"
	_, _, err := f.run(t)
	if !errors.Is(err, ErrScopeNotAttestable) || !strings.Contains(err.Error(), "tree-after-a") {
		t.Fatalf("want a refusal naming the trees, got %v", err)
	}
	if f.git.wroteNote || f.git.pushed {
		t.Fatalf("nothing may be written or pushed (note %v, pushed %v)", f.git.wroteNote, f.git.pushed)
	}
}

// A full scope makes no claim about later trees: a full run whose attested
// tree differs from the checked one is the ordinary auto-fix shape, and is
// still written.
func TestScopeWriter_FullScopeToleratesALaterTree(t *testing.T) {
	f := newScopeFixture(t)
	f.git.trees["head1"] = "tree-after-a-fix"
	if _, _, err := f.run(t); err != nil {
		t.Fatal(err)
	}
	if s := f.git.note.Scope; s == nil || !s.IsFull() || s.HeadTree != "tree1" {
		t.Fatalf("scope %+v", s)
	}
}

// A signer that fails after the selection was made: an unsigned affected note
// proves nothing, so the run refuses it even without signing.required.
func TestScopeWriter_RefusesAnUnsignedAffectedNote(t *testing.T) {
	f := newScopeFixture(t)
	// The anchor is trusted through the roster; this machine's signer then
	// fails when the note is signed.
	f.cfg.TrustedKeys = []string{f.signer.PublicKey()}
	f.git.ancestors = []AnchorCandidate{f.anchor(t, "anc1", f.signer, nil)}
	f.signer = failingSigner{}
	_, _, err := f.run(t)
	if !errors.Is(err, ErrScopeNotAttestable) || !strings.Contains(err.Error(), "could not be signed") {
		t.Fatalf("want a refusal, got %v", err)
	}
	if f.git.wroteNote || f.git.pushed {
		t.Fatalf("nothing may be written or pushed (note %v, pushed %v)", f.git.wroteNote, f.git.pushed)
	}
}

// A repository that selects all writes no scope: its notes stay
// byte-identical to every earlier release's.
func TestScopeWriter_SelectAllWritesNoScope(t *testing.T) {
	f := newScopeFixture(t)
	f.cfg.Workspaces.Select = domain.WorkspaceSelectAll
	f.git.ancestors = []AnchorCandidate{f.anchor(t, "anc1", f.signer, nil)}
	res, ran, err := f.run(t)
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) != 3 || f.git.note.Scope != nil || len(f.git.anchorSearches) != 0 {
		t.Fatalf("ran %v, scope %+v, searches %v", ran, f.git.note.Scope, f.git.anchorSearches)
	}
	if strings.Contains(res.Message, "scope") || strings.Contains(f.forge.commentBody, "Scope") {
		t.Fatalf("no scope, nothing to say: %q\n%s", res.Message, f.forge.commentBody)
	}
}

// select: affected with no per-workspace step in this run: nothing fanned out,
// so there is no scope to record.
func TestScopeWriter_NoPerWorkspaceStepWritesNoScope(t *testing.T) {
	f := newScopeFixture(t)
	f.cfg.Steps["pre_push"] = []domain.StepName{"lint"}
	if _, _, err := f.run(t); err != nil {
		t.Fatal(err)
	}
	if f.git.note.Scope != nil {
		t.Fatalf("scope %+v", f.git.note.Scope)
	}
}

// A failing anchor search fails the run rather than guess.
func TestScopeWriter_AnchorSearchErrorFails(t *testing.T) {
	f := newScopeFixture(t)
	f.git.anchorErr2 = errors.New("rev-list exploded")
	if _, _, err := f.run(t); err == nil || !strings.Contains(err.Error(), "rev-list exploded") {
		t.Fatalf("want the search error, got %v", err)
	}
	if f.git.pushed {
		t.Fatal("nothing may be pushed")
	}
}

// The comment and the status say a full scope plainly too, and a long
// selection is listed up to a bound.
func TestScopeComment_FullAndLong(t *testing.T) {
	full := &domain.RunRecord{Scope: &domain.RunScope{Version: domain.SelectionVersion, Kind: domain.SelectionFull, Workspaces: []string{"a", "b"}, Full: &domain.ForcedFull{Rule: domain.FullRuleNoAnchor}}}
	if got := scopeComment(full); !strings.Contains(got, "**Scope:** full") || !strings.Contains(got, "every workspace (2)") {
		t.Errorf("full: %q", got)
	}
	if scopeComment(&domain.RunRecord{}) != "" {
		t.Error("no scope, no line")
	}
	long := &domain.RunRecord{Scope: &domain.RunScope{Version: domain.SelectionVersion, Kind: domain.SelectionAffected, Anchor: "0123456789abcdef"}}
	for i := 0; i < 12; i++ {
		p := string(rune('a' + i))
		long.Scope.Workspaces = append(long.Scope.Workspaces, p)
		long.Scope.Selected = append(long.Scope.Selected, domain.SelectedWorkspace{Path: p})
	}
	long.Scope.Workspaces = append(long.Scope.Workspaces, "z")
	got := scopeComment(long)
	if !strings.Contains(got, "**12 of 13**") || !strings.Contains(got, "and 2 more") || strings.Contains(got, "`k`") || !strings.Contains(got, "`0123456789ab`") {
		t.Errorf("long: %q", got)
	}
	run := domain.NewRun("run_x", domain.PrePush, domain.ResolvedPolicy{Steps: []domain.StepName{"test"}}, "main")
	if got := statusDescription(run, full); got != "warden gate passed, full scope (test)" {
		t.Errorf("status %q", got)
	}
}
