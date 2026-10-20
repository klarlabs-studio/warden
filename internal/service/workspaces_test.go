package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commitFiles(t *testing.T, dir, msg string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		gitT(t, dir, "add", "--", rel)
	}
	gitT(t, dir, "commit", "-q", "-m", msg)
}

// goWorkRepo is a go.work monorepo whose root is itself a module ("use .")
// with nested modules: svc requires lib, the root requires svc.
func goWorkRepo(t *testing.T) (dir, base string) {
	t.Helper()
	dir = initRepo(t)
	commitFiles(t, dir, "go.work", map[string]string{
		"go.work":        "go 1.26\n\nuse (\n\t.\n\t./svc\n\t./lib\n)\n",
		"go.mod":         "module example.com/root\n\ngo 1.26\n\nrequire example.com/svc v0.0.0\n",
		"main.go":        "package main\n",
		"svc/go.mod":     "module example.com/svc\n\ngo 1.26\n\nrequire example.com/lib v0.0.0\n",
		"svc/svc.go":     "package svc\n",
		"lib/go.mod":     "module example.com/lib\n\ngo 1.26\n",
		"lib/lib.go":     "package lib\n",
		"tools/tools.go": "package tools\n",
	})
	return dir, gitT(t, dir, "rev-parse", "HEAD")
}

func TestService_WorkspacesAtHead(t *testing.T) {
	dir, base := goWorkRepo(t)
	svc, err := New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	list, err := svc.Workspaces("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if list.Commit != base || len(list.Workspaces) != 3 || !strings.HasPrefix(list.GraphDigest, "sha256:") {
		t.Fatalf("list = %+v", list)
	}
	if _, err := svc.Workspaces("-p"); err == nil {
		t.Fatal("a revision that reads as an option must be refused")
	}
	if _, err := svc.Workspaces("no-such-rev"); err == nil {
		t.Fatal("an unknown revision must be an error")
	}
}

// With a root module ("use ."), a file outside every nested module belongs to
// the root; a change in lib reaches svc and, through svc, the root.
func TestService_AffectedWorkspacesGoWork(t *testing.T) {
	dir, base := goWorkRepo(t)
	svc, err := New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}

	commitFiles(t, dir, "tools", map[string]string{"tools/tools.go": "package tools // edited\n"})
	res, err := svc.AffectedWorkspaces(base)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(res.Selection.Paths(), ","); res.Selection.Kind != domain.SelectionAffected || got != "." {
		t.Fatalf("tools change selected %s (%s), want only the root module", got, res.Selection.Kind)
	}

	commitFiles(t, dir, "lib", map[string]string{"lib/lib.go": "package lib // edited\n"})
	res, err = svc.AffectedWorkspaces(base)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(res.Selection.Paths(), ","); got != ".,lib,svc" || res.HeadWorkspaces != 3 || res.Anchor != base {
		t.Fatalf("lib change selected %s of %d from %s", got, res.HeadWorkspaces, res.Anchor)
	}
}

// An anchor whose configuration does not load is an unknown configuration:
// the selection is full, not an error and never affected.
func TestService_AffectedWorkspacesUnreadableAnchorConfigIsFull(t *testing.T) {
	dir, _ := goWorkRepo(t)
	commitFiles(t, dir, "broken config", map[string]string{".warden.yaml": "no_such_key: true\n"})
	base := gitT(t, dir, "rev-parse", "HEAD")
	commitFiles(t, dir, "fixed config", map[string]string{".warden.yaml": "rules: []\n"})
	commitFiles(t, dir, "lib", map[string]string{"lib/lib.go": "package lib // edited\n"})
	svc, err := New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.AffectedWorkspaces(base)
	if err != nil {
		t.Fatal(err)
	}
	if res.Selection.Kind != domain.SelectionFull || res.Selection.Full.Rule != domain.FullRuleConfiguration || res.Selection.AnchorConfigurationDigest != "" {
		t.Fatalf("selection = %s %+v, want full by configuration", res.Selection.Kind, res.Selection.Full)
	}
}

func TestService_AffectedWorkspacesErrors(t *testing.T) {
	dir, base := goWorkRepo(t)
	svc, err := New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range []string{"", "--all", "no-such-branch"} {
		if _, err := svc.AffectedWorkspaces(b); err == nil {
			t.Errorf("AffectedWorkspaces(%q) succeeded", b)
		}
	}
	if _, err := svc.DefaultAffectedBase(); err == nil {
		t.Error("a repository without a remote has no default base")
	}

	// A broken manifest at either end is an error, not a smaller graph.
	commitFiles(t, dir, "break", map[string]string{"go.work": "go 1.26\nuse ./missing\n"})
	if _, err := svc.AffectedWorkspaces(base); err == nil || !strings.Contains(err.Error(), "workspaces at HEAD") {
		t.Errorf("broken head manifest: %v", err)
	}
	broken := gitT(t, dir, "rev-parse", "HEAD")
	commitFiles(t, dir, "repair", map[string]string{"go.work": "go 1.26\nuse ./lib\n"})
	if _, err := svc.AffectedWorkspaces(broken); err == nil || !strings.Contains(err.Error(), "workspaces at the anchor") {
		t.Errorf("broken anchor manifest: %v", err)
	}
	commitFiles(t, dir, "bad head config", map[string]string{".warden.yaml": "no_such_key: true\n"})
	if _, err := svc.AffectedWorkspaces(base); err == nil || !strings.Contains(err.Error(), "configuration at HEAD") {
		t.Errorf("broken head configuration: %v", err)
	}
}
