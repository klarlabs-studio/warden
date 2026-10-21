package domain

import (
	"crypto/ed25519"
	"encoding/base64"
	"reflect"
	"testing"
)

// anchorRecord is a signed full-scope note on commit "anc" whose tree is
// "headtree", as the writer records it, with the signer's public key.
func anchorRecord(t *testing.T, mutate func(*RunRecord)) (RunRecord, string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	rec := RunRecord{
		RunID: "run_anchor", CommitSHA: "anc", StepsRun: []StepName{"test"},
		EvidenceChainRoot: "root", Evidence: []EvidenceEntry{{Kind: "x", Hash: "root"}},
		Scope: fullScope(),
	}
	if mutate != nil {
		mutate(&rec)
	}
	key := base64.StdEncoding.EncodeToString(pub)
	rec.PublicKey, rec.Algorithm = key, AlgorithmEd25519
	payload, err := rec.SigningPayload()
	if err != nil {
		t.Fatal(err)
	}
	rec.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
	return rec, key
}

func anchorReq(key string) AnchorRequirement {
	return AnchorRequirement{
		ConfigurationDigest: "sha256:config",
		Steps:               map[StepName]string{"test": "sha256:tmpl"},
		TrustedKeys:         []string{key},
	}
}

// Every condition of the anchor rule is load-bearing: dropping any one of
// them lets a record anchor that must not.
func TestQualifiesAsAnchor(t *testing.T) {
	good, key := anchorRecord(t, nil)
	if !good.QualifiesAsAnchor("anc", "headtree", anchorReq(key)) {
		t.Fatal("a signed, trusted, full-scope note on its own tree must anchor")
	}
	// A key given as its fingerprint is the same signer.
	if !good.QualifiesAsAnchor("anc", "headtree", AnchorRequirement{ConfigurationDigest: "sha256:config", TrustedKeys: []string{good.SignerFingerprint()}}) {
		t.Fatal("a fingerprint must name the signer too")
	}

	cases := map[string]struct {
		mutate     func(*RunRecord) // applied before signing
		after      func(*RunRecord) // applied after signing (tampering)
		sha, tree  string
		req        func(AnchorRequirement) AnchorRequirement
		wantAnchor bool
	}{
		"no scope (a note from a repo without select: affected)": {mutate: func(r *RunRecord) { r.Scope = nil }},
		"affected scope":                     {mutate: func(r *RunRecord) { r.Scope = affectedScope() }},
		"full kind under an unknown version": {mutate: func(r *RunRecord) { r.Scope.Version = SelectionVersion + 1 }},
		"sparse full":                        {mutate: func(r *RunRecord) { r.Scope.Sparse = &SparseScope{Cone: []string{"a"}} }},
		"carries an affected original": {mutate: func(r *RunRecord) {
			r.ReattestedFrom = "src"
			r.CarriedOriginal = &RunRecord{CommitSHA: "src", Scope: affectedScope()}
		}},
		"external run":     {mutate: func(r *RunRecord) { r.ExternalRun = &ExternalRunRef{} }},
		"bound elsewhere":  {sha: "other"},
		"broken chain":     {mutate: func(r *RunRecord) { r.EvidenceChainRoot = "nope" }},
		"tree moved since": {tree: "othertree"},
		"no tree":          {tree: "-"},
		"other config":     {req: func(q AnchorRequirement) AnchorRequirement { q.ConfigurationDigest = "sha256:other"; return q }},
		"no config":        {req: func(q AnchorRequirement) AnchorRequirement { q.ConfigurationDigest = ""; return q }},
		"step it never ran": {req: func(q AnchorRequirement) AnchorRequirement {
			q.Steps = map[StepName]string{"test": "sha256:tmpl", "lint": "sha256:l"}
			return q
		}},
		"other template": {req: func(q AnchorRequirement) AnchorRequirement {
			q.Steps = map[StepName]string{"test": "sha256:new"}
			return q
		}},
		"untrusted signer":  {req: func(q AnchorRequirement) AnchorRequirement { q.TrustedKeys = []string{"someone-else"}; return q }},
		"no trusted signer": {req: func(q AnchorRequirement) AnchorRequirement { q.TrustedKeys = nil; return q }},
		"unsigned":          {after: func(r *RunRecord) { r.Signature = "" }},
		"edited after signing": {after: func(r *RunRecord) {
			r.Scope.ConfigurationDigest = "sha256:config2"
		}},
		"scope stripped after signing": {after: func(r *RunRecord) { r.Scope = nil }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rec, key := anchorRecord(t, tc.mutate)
			if tc.after != nil {
				s := *rec.Scope
				rec.Scope = &s
				tc.after(&rec)
			}
			sha, tree := "anc", "headtree"
			if tc.sha != "" {
				sha = tc.sha
			}
			if tc.tree == "-" {
				tree = ""
			} else if tc.tree != "" {
				tree = tc.tree
			}
			req := anchorReq(key)
			if tc.req != nil {
				req = tc.req(req)
			}
			if got := rec.QualifiesAsAnchor(sha, tree, req); got != tc.wantAnchor {
				t.Fatalf("QualifiesAsAnchor = %v, want %v", got, tc.wantAnchor)
			}
		})
	}
}

func scopeGraph() WorkspaceGraph {
	return WorkspaceGraph{Workspaces: []Workspace{
		{Path: "packages/ui", Name: "ui", Ecosystem: "js"},
		{Path: "apps/web", Name: "web", Ecosystem: "js", Deps: []string{"packages/ui"}},
		{Path: "svc", Name: "svc", Ecosystem: "go"},
	}}
}

func TestNewRunScope_Affected(t *testing.T) {
	g := scopeGraph()
	sel, err := Select(SelectionInput{Base: g, Head: g, Changed: []string{"packages/ui/a.ts"}, AnchorConfigurationDigest: "c", ConfigurationDigest: "c"})
	if err != nil {
		t.Fatal(err)
	}
	steps := map[StepName]PerWorkspaceStep{
		"test":      {Each: "pnpm test"},
		"gotest":    {Each: "go test ./...", Ecosystems: []string{"go"}},
		"typecheck": {Batch: "tsc {workspaces.paths}", Ecosystems: []string{"js"}},
	}
	scope, err := NewRunScope(ScopeInput{
		Selection: WorkspaceSelection{Selection: sel, Graph: g}, HeadTree: "tree1",
		Anchor: "anc", AnchorTree: "anctree", ConfigurationDigest: "c", Steps: steps,
	})
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := sel.Digest()
	tmpl, _ := steps["test"].TemplateDigest()
	if scope.Kind != SelectionAffected || scope.Version != SelectionVersion || scope.Anchor != "anc" || scope.AnchorTree != "anctree" ||
		scope.HeadTree != "tree1" || scope.ConfigurationDigest != "c" || scope.SelectionDigest != digest || scope.GraphDigest != sel.HeadGraphDigest {
		t.Fatalf("scope header %+v", scope)
	}
	if want := []string{"apps/web", "packages/ui", "svc"}; !reflect.DeepEqual(scope.Workspaces, want) {
		t.Fatalf("workspaces %v", scope.Workspaces)
	}
	if want := []string{"apps/web", "packages/ui"}; !reflect.DeepEqual(scope.Steps["test"].Selected, want) || scope.Steps["test"].TemplateDigest != tmpl {
		t.Fatalf("test step %+v", scope.Steps["test"])
	}
	// A step limited to an ecosystem nothing affected records an empty
	// selection, not a missing one.
	if got := scope.Steps["gotest"].Selected; got == nil || len(got) != 0 {
		t.Fatalf("gotest selected %#v", got)
	}
	if !reflect.DeepEqual(scope.Selected, sel.Workspaces) || scope.IsFull() {
		t.Fatalf("selected %+v", scope.Selected)
	}
	if got := scope.Label(); got != "affected 2/3" {
		t.Fatalf("label %q", got)
	}
}

func TestNewRunScope_RefusesWhatItCannotStandBehind(t *testing.T) {
	g := scopeGraph()
	affected, _ := Select(SelectionInput{Base: g, Head: g, Changed: []string{"packages/ui/a.ts"}, AnchorConfigurationDigest: "c", ConfigurationDigest: "c"})
	full, _ := FullSelection(g, ForcedFull{Rule: FullRuleNoAnchor})
	for name, in := range map[string]ScopeInput{
		"affected without an anchor": {Selection: WorkspaceSelection{Selection: affected, Graph: g}, HeadTree: "t"},
		"no head tree":               {Selection: WorkspaceSelection{Selection: full, Graph: g}},
		"full without a rule": {Selection: WorkspaceSelection{Selection: func() Selection {
			s := full
			s.Full = nil
			return s
		}(), Graph: g}, HeadTree: "t"},
		"unknown kind": {Selection: WorkspaceSelection{Selection: func() Selection {
			s := full
			s.Kind = "partial"
			return s
		}(), Graph: g}, HeadTree: "t"},
	} {
		if _, err := NewRunScope(in); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	// A full selection drops any anchor it was handed: it rests on nothing.
	scope, err := NewRunScope(ScopeInput{Selection: WorkspaceSelection{Selection: full, Graph: g}, HeadTree: "t", Anchor: "a", AnchorTree: "b"})
	if err != nil || scope.Anchor != "" || scope.AnchorTree != "" || !scope.IsFull() || scope.Full.Rule != FullRuleNoAnchor {
		t.Fatalf("full scope %+v %v", scope, err)
	}
	if scope.Label() != "full" || (*RunScope)(nil).Label() != "" {
		t.Fatal("labels")
	}
}
