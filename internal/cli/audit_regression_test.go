package cli

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestVerifyRequiresSignatureWithoutRoster(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	repoWithConfig(t, "")
	svc, err := newService(autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	sha, _ := svc.Repo().HeadSHA()
	rec := domain.RunRecord{CommitSHA: sha, EvidenceChainRoot: "a", Evidence: []domain.EvidenceEntry{{Hash: "a"}}}
	if err := svc.Repo().WriteNote(sha, rec); err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	if code := cmdVerify([]string{"--commit", sha, "--require-signed"}, &out, &errs); code != 1 {
		t.Fatalf("code=%d output=%s %s", code, out.String(), errs.String())
	}
}
func TestPushTargetsMustMatchCheckedOutBranch(t *testing.T) {
	sha := strings.Repeat("a", 40)
	other := strings.Repeat("b", 40)
	zero := strings.Repeat("0", 40)
	valid := "refs/heads/main " + sha + " refs/heads/main " + zero
	for _, tc := range []struct {
		name, payload string
		ok            bool
	}{{"current", valid, true}, {"other", "refs/heads/other " + other + " refs/heads/other " + zero, false}, {"stale", "refs/heads/main " + other + " refs/heads/main " + zero, false}, {"multiple", valid + "\nrefs/heads/other " + other + " refs/heads/other " + zero, false}, {"mapping", "refs/heads/main " + sha + " refs/heads/other " + zero, false}, {"malformed", "missing fields", false}} {
		t.Run(tc.name, func(t *testing.T) {
			if err := checkPushTargets(tc.payload, "main", sha); (err == nil) != tc.ok {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestRangeExternalFlagsAreEnforced(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	dir := repoWithConfig(t, "")
	svc, err := newService(autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	base, _ := svc.Repo().HeadSHA()
	cmd := exec.Command("git", "commit", "--allow-empty", "--no-verify", "-m", "external")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	sha, _ := svc.Repo().HeadSHA()
	ref := domain.ExternalRunRef{Provider: "fixture", RunID: "1", Repository: "fixture/repo", Checks: []string{"lint"}}
	if _, err := svc.AttestExternal(sha, ref, false); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		flags []string
		pass  bool
	}{{nil, false}, {[]string{"--allow-external"}, true}, {[]string{"--require-external"}, true}, {[]string{"--allow-external", "--require-external"}, false}} {
		args := append([]string{"--range", base + ".." + sha}, tc.flags...)
		var out, errs bytes.Buffer
		if code := cmdVerify(args, &out, &errs); (code == 0) != tc.pass {
			t.Fatalf("flags=%v code=%d output=%s %s", tc.flags, code, out.String(), errs.String())
		}
	}
}
