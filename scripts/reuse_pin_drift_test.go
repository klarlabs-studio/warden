package scripts_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// protectedRef returns the pinned ref of the named checkout step in a workflow.
func protectedRef(t *testing.T, workflow, job, step string) string {
	t.Helper()
	b, err := os.ReadFile(repoFile(t, ".github/workflows/"+workflow))
	if err != nil {
		t.Fatal(err)
	}
	var w struct {
		Jobs map[string]struct {
			Steps []struct {
				Name string            `yaml:"name"`
				With map[string]string `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(b, &w); err != nil {
		t.Fatal(err)
	}
	for _, s := range w.Jobs[job].Steps {
		if s.Name == step {
			return s.With["ref"]
		}
	}
	t.Fatalf("%s: no %q step in job %s", workflow, step, job)
	return ""
}

// TestProtectedReusePinsMatchContract reads the image receipts at the commit the
// protected jobs actually check out, not the working tree. Republishing the test
// image updates the working-tree receipts and ci.yml's contract; if the protected
// ref is not advanced with them, the verifier compares the new contract with old
// receipts, errors inside a continue-on-error step, and CI stays green (#377).
func TestProtectedReusePinsMatchContract(t *testing.T) {
	ref := protectedRef(t, "ci.yml", "test-reuse-plan", "Check out protected verifier and policy")
	native := protectedRef(t, "native-container-reuse.yml", "reuse", "Check out protected tools and profile")
	if ref != native {
		t.Fatalf("protected refs differ: ci.yml %s, native-container-reuse.yml %s", ref, native)
	}
	root := repoFile(t, ".")
	git := func(args ...string) (string, error) {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.Output()
		return string(out), err
	}
	// Only a full-history repository can answer "is the pinned ref's receipt
	// current?". Two environments legitimately cannot, and the workflow's own
	// "Check protected pins match the caller contract" step covers both in CI:
	//   - a container gate step mounts only the worktree at /workspace, so the
	//     worktree's .git file points at a gitdir that is not visible;
	//   - CI's test job (and clones derived from it) check out one commit.
	// A full repository that lacks the commit is a real failure.
	if _, err := git("rev-parse", "--git-dir"); err != nil {
		t.Skipf("no git repository visible here (container step): %v", err)
	}
	if _, err := git("cat-file", "-e", ref+"^{commit}"); err != nil {
		out, shallowErr := git("rev-parse", "--is-shallow-repository")
		if shallowErr == nil && strings.TrimSpace(out) == "true" {
			t.Skipf("shallow clone lacks protected ref %s", ref)
		}
		t.Fatalf("protected ref %s is not in this full repository: %v", ref, err)
	}
	show := func(path string) map[string]any {
		out, err := git("show", ref+":"+path)
		if err != nil {
			t.Fatalf("%s:%s: %v", ref, path, err)
		}
		var v map[string]any
		if err := json.Unmarshal([]byte(out), &v); err != nil {
			t.Fatalf("%s:%s: %v", ref, path, err)
		}
		return v
	}
	jobs := reuseWorkflow(t)
	var contract map[string]string
	env := jobs["test-reuse-plan"].(map[string]any)["env"].(map[string]any)
	if err := json.Unmarshal([]byte(strings.TrimSpace(env["EXPECTED_CONTRACT"].(string))), &contract); err != nil {
		t.Fatal(err)
	}
	const profile = ".github/warden/linux-test/profiles/amd64/"
	receipt := show(profile + "publication.json")
	for field, key := range map[string]string{"image": "registry_reference", "config_id": "docker_config_id", "platform": "platform", "signing_identity": "signing_identity"} {
		if receipt[key] != contract[field] {
			t.Errorf("protected ref %s publication %s = %v, ci.yml contract %s = %s: advance the protected ref with the image pins", ref, key, receipt[key], field, contract[field])
		}
	}
	step := show(profile + "docker-policy-template.json")["jobs"].(map[string]any)["test"].(map[string]any)["steps"].(map[string]any)["test"].(map[string]any)
	if step["image_id"] != contract["config_id"] || step["command"] != contract["command"] || step["platform"] != contract["platform"] {
		t.Errorf("protected ref %s docker policy template does not match the ci.yml contract", ref)
	}
}
