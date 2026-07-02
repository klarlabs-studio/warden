package cli

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// auditRepo builds a temp git repo with one commit, adopts warden in it, and
// chdirs into it. It returns after `warden init` so audit has an adoption point
// to walk from. git is required; callers skip when it is absent.
func auditRepo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "t@t.co"},
		{"config", "user.name", "t"},
		{"commit", "--allow-empty", "-m", "first commit"},
		{"commit", "--allow-empty", "-m", "second commit"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	chdir(t, dir)

	var out, errb bytes.Buffer
	if code := cmdInit([]string{"--hooks=pre-push"}, &out, &errb); code != 0 {
		t.Fatalf("init: code=%d err=%q", code, errb.String())
	}

	// A commit made after adoption with no warden note is the unverified case
	// the audit must surface — the drift a --no-verify push would create.
	post := exec.Command("git", "commit", "--allow-empty", "-m", "post-adoption change")
	post.Dir = dir
	if out, err := post.CombinedOutput(); err != nil {
		t.Fatalf("post-adoption commit: %v: %s", err, out)
	}
}

func TestAudit_JSONExport(t *testing.T) {
	auditRepo(t)

	var out, errb bytes.Buffer
	if code := cmdAudit([]string{"--format", "json"}, &out, &errb); code != 0 {
		t.Fatalf("audit json: code=%d err=%q", code, errb.String())
	}

	var export struct {
		Branch   string `json:"branch"`
		Adoption string `json:"adoption"`
		Summary  struct {
			Verified   int `json:"verified"`
			Intact     int `json:"intact"`
			Unverified int `json:"unverified"`
		} `json:"summary"`
		Commits []struct {
			SHA       string `json:"sha"`
			Validated bool   `json:"validated"`
		} `json:"commits"`
	}
	if err := json.Unmarshal(out.Bytes(), &export); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, out.String())
	}
	if export.Branch == "" {
		t.Error("expected a branch in the export")
	}
	if export.Commits == nil {
		t.Error("expected a commits array in the export")
	}
	// The commit after adoption carries no warden note, so the summary should
	// account for it as unverified rather than silently dropping it.
	if got := len(export.Commits); got != export.Summary.Unverified {
		t.Errorf("expected all %d commits unverified, summary says %d", got, export.Summary.Unverified)
	}
}

func TestAudit_MarkdownTable(t *testing.T) {
	auditRepo(t)

	var out, errb bytes.Buffer
	if code := cmdAudit([]string{"--format", "md"}, &out, &errb); code != 0 {
		t.Fatalf("audit md: code=%d err=%q", code, errb.String())
	}
	got := out.String()
	if !strings.Contains(got, "| SHA | Date | Subject | Status | Run |") {
		t.Errorf("expected a markdown table header, got:\n%s", got)
	}
	if !strings.Contains(got, "**Summary:**") {
		t.Errorf("expected a summary line, got:\n%s", got)
	}
}

func TestAudit_TextAndBadFormat(t *testing.T) {
	auditRepo(t)

	var out, errb bytes.Buffer
	if code := cmdAudit(nil, &out, &errb); code != 0 {
		t.Fatalf("audit text: code=%d err=%q", code, errb.String())
	}
	if !strings.Contains(out.String(), "warden audit") {
		t.Errorf("expected audit header, got:\n%s", out.String())
	}

	// A bad --format is a usage error (exit 2), distinct from a service error.
	out.Reset()
	errb.Reset()
	if code := cmdAudit([]string{"--format", "xml"}, &out, &errb); code != 2 {
		t.Errorf("bad format: expected exit 2, got %d", code)
	}
}
