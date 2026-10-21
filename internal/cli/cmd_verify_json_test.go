package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/service"
)

// writerScope is a scope as the pre-push writer records it: the selection
// (Selected) and each per-workspace step's share of it, on tree.
func writerScope(kind domain.SelectionKind, tree string) *domain.RunScope {
	s := cliScope(kind)
	s.HeadTree = tree
	for _, p := range s.Steps[domain.StepTest].Selected {
		s.Selected = append(s.Selected, domain.SelectedWorkspace{Path: p, Name: p})
	}
	if kind == domain.SelectionAffected {
		s.Anchor, s.AnchorTree = "anchor", "anchor-tree"
	}
	return s
}

func verifyJSON(t *testing.T, args ...string) (verifyExport, int, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := cmdVerify(append([]string{"--json"}, args...), &out, &errb)
	var export verifyExport
	if err := json.Unmarshal(out.Bytes(), &export); err != nil {
		t.Fatalf("verify --json printed no JSON (exit %d): %v\n%s%s", code, err, out.String(), errb.String())
	}
	return export, code, out.String()
}

// Single-commit --json says what CI may rely on per workspace, with the exit
// code verify always had: a full note validates every workspace; an affected
// one still fails, and names the workspaces checked on this exact tree only
// when nothing else about the note is in doubt.
func TestCmdVerify_JSONScopeClaim(t *testing.T) {
	affected := []string{"apps/web", "packages/ui"}
	all := []string{"apps/web", "packages/api", "packages/ui"}

	t.Run("full", func(t *testing.T) {
		_, _, tip, fp := scopedNoteOnHeadFor(t, func(tree string) *domain.RunScope {
			return writerScope(domain.SelectionFull, tree)
		})
		got, code, raw := verifyJSON(t, "--key", fp)
		if code != 0 || !got.Validated || got.Scope != service.ScopeClaimFull || !reflect.DeepEqual(got.ValidatedWorkspaces, all) ||
			got.Commit != tip || !got.Trusted || len(got.Failures) != 0 || got.RecordScope == nil {
			t.Fatalf("exit %d: %s", code, raw)
		}
	})

	t.Run("affected", func(t *testing.T) {
		_, _, _, fp := scopedNoteOnHeadFor(t, func(tree string) *domain.RunScope {
			return writerScope(domain.SelectionAffected, tree)
		})
		req := filepath.Join(t.TempDir(), "requirements.json")
		if err := os.WriteFile(req, []byte(`{"version":2,"profile":{"min":"permissive"}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name      string
			args      []string
			scope     service.ScopeClaimKind
			validated []string
		}{
			{"pinned key", []string{"--key", fp}, service.ScopeClaimAffected, affected},
			{"require-signed", []string{"--key", fp, "--require-signed"}, service.ScopeClaimAffected, affected},
			// Trust is the claim's foundation: no pin, no claim.
			{"no key", nil, service.ScopeClaimNone, []string{}},
			{"untrusted key", []string{"--key", "bbbbbbbbbbbbbbbb"}, service.ScopeClaimNone, []string{}},
			// Every other check the caller asks for must hold as well.
			{"requirements", []string{"--key", fp, "--requirements", req}, service.ScopeClaimNone, []string{}},
			{"min-profile", []string{"--key", fp, "--min-profile", "permissive"}, service.ScopeClaimNone, []string{}},
			{"container-steps", []string{"--key", fp, "--container-steps", "test"}, service.ScopeClaimNone, []string{}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got, code, raw := verifyJSON(t, tc.args...)
				if code != 1 || got.Validated {
					t.Fatalf("an affected note must still fail: exit %d\n%s", code, raw)
				}
				if got.Scope != tc.scope || !reflect.DeepEqual(got.ValidatedWorkspaces, tc.validated) {
					t.Fatalf("scope %q %v, want %q %v\n%s", got.Scope, got.ValidatedWorkspaces, tc.scope, tc.validated, raw)
				}
				if len(got.Failures) == 0 {
					t.Fatalf("a refusal must say why:\n%s", raw)
				}
				if (got.RecordScope != nil) != (tc.scope != service.ScopeClaimNone) {
					t.Fatalf("record_scope is shown exactly when the claim rests on it:\n%s", raw)
				}
			})
		}
	})

	// The tree is read from git, not from the note: checks recorded on another
	// tree validate no workspace of this one, and neither does a scope this
	// warden cannot read.
	for name, scopeFor := range map[string]func(string) *domain.RunScope{
		"other tree": func(string) *domain.RunScope { return writerScope(domain.SelectionAffected, "other-tree") },
		"sparse": func(tree string) *domain.RunScope {
			s := writerScope(domain.SelectionAffected, tree)
			s.Sparse = &domain.SparseScope{Cone: []string{"apps/web"}}
			return s
		},
		"newer version": func(tree string) *domain.RunScope {
			s := writerScope(domain.SelectionAffected, tree)
			s.Version = domain.SelectionVersion + 1
			return s
		},
		"sparse full": func(tree string) *domain.RunScope {
			s := writerScope(domain.SelectionFull, tree)
			s.Sparse = &domain.SparseScope{Cone: []string{"apps/web"}}
			return s
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, _, _, fp := scopedNoteOnHeadFor(t, scopeFor)
			got, code, raw := verifyJSON(t, "--key", fp)
			if code != 1 || got.Scope != service.ScopeClaimAffected || len(got.ValidatedWorkspaces) != 0 {
				t.Fatalf("exit %d: %s", code, raw)
			}
		})
	}

	t.Run("no note", func(t *testing.T) {
		repoWithConfig(t, "")
		got, code, raw := verifyJSON(t, "--key", "aaaaaaaaaaaaaaaa")
		if code != 1 || got.Validated || got.Scope != service.ScopeClaimNone || got.ValidatedWorkspaces == nil || len(got.ValidatedWorkspaces) != 0 {
			t.Fatalf("exit %d: %s", code, raw)
		}
	})

	t.Run("quiet wins", func(t *testing.T) {
		_, _, _, fp := scopedNoteOnHeadFor(t, func(tree string) *domain.RunScope {
			return writerScope(domain.SelectionFull, tree)
		})
		var out, errb bytes.Buffer
		if code := cmdVerify([]string{"--json", "--quiet", "--key", fp}, &out, &errb); code != 0 || out.Len() != 0 {
			t.Fatalf("--quiet must print nothing: exit %d %q %q", code, out.String(), errb.String())
		}
	})
}

// A note without a scope (a repository without monorepo mode) is full and
// names no workspace.
func TestCmdVerify_JSONUnscopedNote(t *testing.T) {
	_, _, _, fp := scopedNoteOnHead(t, nil)
	got, code, raw := verifyJSON(t, "--key", fp)
	if code != 0 || got.Scope != service.ScopeClaimFull || got.ValidatedWorkspaces == nil || len(got.ValidatedWorkspaces) != 0 || got.RecordScope != nil {
		t.Fatalf("exit %d: %s", code, raw)
	}
}
