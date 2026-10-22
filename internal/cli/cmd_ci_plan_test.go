package cli

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.klarlabs.de/warden/internal/domain"

	"go.klarlabs.de/warden/internal/service"
)

func TestCIPlanInvocationAndPolicy(t *testing.T) {
	for _, args := range [][]string{
		{"ci", "plan"}, {"ci", "plan", "--policy", "missing", "--key", ","},
		{"ci", "plan", "--policy", "missing", "--key", "pinned", "--commit", ""},
		{"ci", "plan", "--unknown"}, {"ci", "plan", "extra"},
		{"ci", "plan", "--policy", "missing", "--key", "pinned"},
	} {
		if code, _, _ := run(args...); code != 2 {
			t.Fatalf("%v: code=%d", args, code)
		}
	}
	for _, data := range []string{
		`{}`, `null`, `{"version":1,"jobs":{}}`,
		`{"version":1,"jobs":{"test":null},"extra":true}`,
		`{"version":1,"jobs":{"test":null,"test":null}}`,
		`{"version":1,"jobs":{"test":{"version":2,"profile":{"min":"strict"}}}}`,
		`{"version":1,"jobs":{"test":null}} {}`,
		strings.Repeat(" ", (1<<20)+1),
	} {
		file := filepath.Join(t.TempDir(), "jobs.json")
		if err := os.WriteFile(file, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if code, _, _ := run("ci", "plan", "--policy", file, "--key", "pinned"); code != 2 {
			t.Fatalf("accepted policy %q: %d", data[:min(len(data), 100)], code)
		}
	}
}

func TestCIPlanNoNoteRunsJobs(t *testing.T) {
	repoWithConfig(t, "")
	file := filepath.Join(t.TempDir(), "jobs.json")
	if err := os.WriteFile(file, []byte(`{"version":1,"jobs":{"package":null}}`), 0600); err != nil {
		t.Fatal(err)
	}
	code, out, errb := run("ci", "plan", "--policy", file, "--key", "pinned", "--json")
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, errb)
	}
	var plan service.CIPlan
	if err := json.Unmarshal([]byte(out), &plan); err != nil {
		t.Fatal(err)
	}
	if plan.SHA == "" || len(plan.RunJobs) != 1 || len(plan.ReusableJobs) != 0 || len(plan.Jobs) != 1 || plan.Jobs[0].Action != "run" {
		t.Fatalf("unsafe plan: %s", out)
	}
	if code, out, errb = run("ci", "plan", "--policy", file, "--key", "pinned"); code != 0 || !strings.Contains(out, "package: run") {
		t.Fatalf("text: %d %s %s", code, out, errb)
	}
	if code, _, _ = run("ci", "plan", "--policy", file, "--key", "pinned", "--commit", "missing-revision"); code == 0 {
		t.Fatal("accepted missing commit")
	}
}

func TestCIPlanMatchingSignedNote(t *testing.T) {
	repoWithConfig(t, "")
	svc, err := newService(autoApprover{})
	if err != nil {
		t.Fatal(err)
	}
	head, err := svc.Repo().HeadSHA()
	if err != nil {
		t.Fatal(err)
	}
	tree, err := svc.Repo().TreeSHA(head)
	if err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(nil)
	digest := domain.DigestBytes([]byte("fixture"))
	env := domain.ExecutionEnvironment{Runtime: "docker", ImageID: digest, ImageIDKind: "config", Platform: "linux/amd64", Network: "none"}
	req := domain.StepRequirements{Command: "true", ConfigurationDigest: digest, ContextDigest: digest, Inputs: map[string]string{}, ImageID: digest, ImageIDKind: "config", Platform: "linux/amd64", Network: "none"}
	record := domain.RunRecord{CommitSHA: head, StepsRun: []domain.StepName{domain.StepTest}, EvidenceChainRoot: "h0", Evidence: []domain.EvidenceEntry{{Hash: "h0"}}, PublicKey: base64.StdEncoding.EncodeToString(pub), Environments: map[domain.StepName]domain.ExecutionEnvironment{domain.StepTest: env}, Contracts: map[domain.StepName]domain.ExecutionContract{domain.StepTest: {Version: 1, CommandDigest: domain.DigestBytes([]byte("true")), ConfigurationDigest: digest, ContextDigest: digest, Inputs: map[string]string{}, TreeSHA: tree, Environment: env}}}
	payload, err := record.SigningPayload()
	if err != nil {
		t.Fatal(err)
	}
	record.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
	if err := svc.Repo().WriteNote(head, record); err != nil {
		t.Fatal(err)
	}
	policy := domain.CIJobPolicy{Version: 1, Jobs: map[string]*domain.ExecutionRequirements{"unit": {Version: 1, Steps: map[domain.StepName]domain.StepRequirements{domain.StepTest: req}}, "package": nil}}
	data, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "jobs.json")
	if err := os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{record.SignerFingerprint(), "untrusted"} {
		code, out, errb := run("ci", "plan", "--policy", file, "--key", key, "--commit", head, "--json")
		if code != 0 {
			t.Fatalf("code=%d stderr=%s", code, errb)
		}
		var plan service.CIPlan
		if err := json.Unmarshal([]byte(out), &plan); err != nil {
			t.Fatal(err)
		}
		want := 0
		if key == record.SignerFingerprint() {
			want = 1
		}
		if plan.SHA != head || len(plan.ReusableJobs) != want || len(plan.RunJobs) != 2-want {
			t.Fatalf("wrong JSON decisions: %s", out)
		}
	}
}
