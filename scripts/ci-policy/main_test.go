package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestPrepareIndependentObjects(t *testing.T) {
	dir := t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", append([]string{"-C", dir}, args...)...)
		c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		out, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git: %v %s", e, out)
		}
		return strings.TrimSpace(string(out))
	}
	write := func(name, data string) {
		t.Helper()
		if e := os.WriteFile(filepath.Join(dir, name), []byte(data), 0600); e != nil {
			t.Fatal(e)
		}
	}
	run("init")
	run("config", "user.name", "warden-ci")
	run("config", "user.email", "warden-ci")
	write("go.mod", "module before\n")
	run("add", "go.mod")
	run("commit", "-m", "base")
	base := run("rev-parse", "HEAD")
	write("go.mod", "module after\n")
	write("a\tb\nc", "hello\n")
	run("add", "go.mod", "a\tb\nc")
	run("commit", "-m", "candidate")
	commit := run("rev-parse", "HEAD")
	write("go.mod", "dirty must not be trusted\n")
	policy := filepath.Join(t.TempDir(), "policy.json")
	digest := "sha256:" + strings.Repeat("a", 64)
	template := `{"version":1,"jobs":{"test":{"version":1,"steps":{"test":{"command":"/usr/local/bin/warden-linux-test","configuration_digest":"` + digest + `","image_id":"` + digest + `","image_id_kind":"manifest","platform":"linux/arm64","network":"none"}}},"lint":null}}`
	if e := os.WriteFile(policy, []byte(template), 0600); e != nil {
		t.Fatal(e)
	}
	opts := options{repo: dir, template: policy, base: base, commit: commit, branch: "feature"}
	var output bytes.Buffer
	if e := runCLI([]string{"--repo", dir, "--template", policy, "--base", base, "--commit", commit, "--branch", "feature"}, &output); e != nil {
		t.Fatal(e)
	}
	out, err := prepare(opts)
	if err == nil && !bytes.Equal(out, output.Bytes()) {
		t.Fatal("CLI output differs")
	}
	if err != nil {
		t.Fatal(err)
	}
	var p domain.CIJobPolicy
	if err = json.Unmarshal(out, &p); err != nil {
		t.Fatal(err)
	}
	s := p.Jobs["test"].Steps["test"]
	if len(s.Inputs) != 1 || s.Inputs["go.mod"] != domain.DigestBytes([]byte("module after\n")) {
		t.Fatalf("inputs: %v", s.Inputs)
	}
	expected, _ := domain.DigestJSON([]string{"WARDEN_HOOK=pre_push", "WARDEN_BRANCH=feature", "WARDEN_CHANGED_FILES=a\tb\nc\ngo.mod", "WARDEN_FILES_TOUCHED=2", "WARDEN_LINES_CHANGED=3"})
	if s.ContextDigest != expected {
		t.Fatalf("context %s != %s", s.ContextDigest, expected)
	}
	// A replacement ref cannot alter immutable candidate inputs or context.
	run("replace", commit, base)
	replaced, e := prepare(opts)
	if e != nil || !bytes.Equal(out, replaced) {
		t.Fatalf("replacement ref influenced policy: %v", e)
	}
	run("replace", "-d", commit)
	other := opts
	other.branch = "different"
	changed, e := prepare(other)
	if e != nil || bytes.Equal(out, changed) {
		t.Fatalf("branch context did not change: %v", e)
	}
	if p.Jobs["lint"] != nil {
		t.Fatal("always-run job changed")
	}
	for _, tc := range []struct {
		name   string
		change func(*options)
	}{
		{"mutable commit", func(o *options) { o.commit = "HEAD" }},
		{"missing branch", func(o *options) { o.branch = "" }},
		{"candidate policy", func(o *options) { o.template = filepath.Join(dir, "go.mod") }},
		{"not a commit", func(o *options) { o.commit = run("rev-parse", "HEAD:go.mod") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := opts
			tc.change(&o)
			if _, e := prepare(o); e == nil {
				t.Fatal("accepted unsafe input")
			}
		})
	}
	for _, bad := range []string{
		strings.Replace(template, `"version":1`, `"version":1,"version":1`, 1),
		strings.Replace(template, `"command":`, `"unknown":true,"command":`, 1),
		strings.Replace(template, `"command":`, `"inputs":{},"command":`, 1),
		template + `{}`,
	} {
		if err = os.WriteFile(policy, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, e := prepare(opts); e == nil {
			t.Fatal("accepted unsafe template")
		}
	}
	if err = os.WriteFile(policy, []byte(template), 0600); err != nil {
		t.Fatal(err)
	}
	run("update-index", "--add", "--cacheinfo", "120000,"+run("rev-parse", commit+":go.mod")+",package-lock.json")
	run("commit", "-m", "symlink")
	opts.commit = run("rev-parse", "HEAD")
	if _, e := prepare(opts); e == nil {
		t.Fatal("accepted symlink manifest")
	}
	run("update-index", "--add", "--cacheinfo", "160000,"+commit+",submodule")
	run("commit", "-m", "submodule")
	opts.commit = run("rev-parse", "HEAD")
	if _, e := prepare(opts); e == nil {
		t.Fatal("accepted submodule")
	}
}

func TestUniqueJSON(t *testing.T) {
	for _, s := range []string{`{"a":{"b":1,"b":2}}`, `{"a":[{"x":1,"x":2}]}`, `{"a":`} {
		if uniqueJSON([]byte(s)) == nil {
			t.Fatalf("accepted %s", s)
		}
	}
	if e := uniqueJSON([]byte(`{"a":[1,{"b":2}],"c":true}`)); e != nil {
		t.Fatal(e)
	}
}

func TestCLIRejectsBadInvocation(t *testing.T) {
	for _, args := range [][]string{nil, {"--unknown"}, {"extra"}, {"--commit", "HEAD"}} {
		var out bytes.Buffer
		if e := runCLI(args, &out); e == nil || out.Len() != 0 {
			t.Fatalf("unsafe output for %v: %q %v", args, out.String(), e)
		}
	}
	if _, e := git(t.TempDir(), "cat-file", "-t", "missing"); e == nil {
		t.Fatal("missing object accepted")
	}
}
