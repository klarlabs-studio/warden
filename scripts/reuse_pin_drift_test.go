package scripts_test

import (
	"encoding/json"
	"fmt"
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
	env := jobs["test-reuse-plan"].(map[string]any)["env"].(map[string]any)
	rawContract := strings.TrimSpace(env["EXPECTED_CONTRACT"].(string))
	var contract map[string]string
	if err := json.Unmarshal([]byte(rawContract), &contract); err != nil {
		t.Fatal(err)
	}
	const profile = ".github/warden/linux-test/profiles/amd64/"
	receipt := show(profile + "publication.json")
	var drift []string
	for field, key := range map[string]string{"image": "registry_reference", "config_id": "docker_config_id", "platform": "platform", "signing_identity": "signing_identity"} {
		if receipt[key] != contract[field] {
			drift = append(drift, fmt.Sprintf("publication %s = %v, contract %s = %s", key, receipt[key], field, contract[field]))
		}
	}
	step := show(profile + "docker-policy-template.json")["jobs"].(map[string]any)["test"].(map[string]any)["steps"].(map[string]any)["test"].(map[string]any)
	if step["image_id"] != contract["config_id"] || step["command"] != contract["command"] || step["platform"] != contract["platform"] {
		drift = append(drift, "docker policy template differs from the contract")
	}
	if len(drift) == 0 {
		return
	}
	// An unreadable side is read as "carries the contract": that is the answer
	// under which drift fails, so a history this cannot inspect never excuses it.
	carries := func(rev string) bool {
		ci, err := git("show", rev+":.github/workflows/ci.yml")
		return err != nil || strings.Contains(ci, rawContract)
	}
	detail := strings.Join(drift, "; ")
	switch classifyPinDrift(carries("origin/main"), carries("HEAD^"), os.Getenv(requireCurrentPinsEnv) == "1") {
	case pinDriftInFlight:
		t.Skipf("this branch changes the container contract; after it merges, run scripts/advance-protected-ref.sh <merge-sha> (%s)", detail)
	case pinDriftAdoption:
		t.Skipf("this commit adopts the container contract, and the protected ref cannot name it before it is on main; "+
			"run scripts/advance-protected-ref.sh with this commit next (%s)", detail)
	default:
		t.Fatalf("protected ref %s is stale: %s. Run scripts/advance-protected-ref.sh <merge-sha>", ref, detail)
	}
}

// requireCurrentPinsEnv, set to "1", withdraws the one allowance drift has on
// main. Only the `Protected reuse pins current` job sets it.
const requireCurrentPinsEnv = "WARDEN_REQUIRE_CURRENT_PINS"

type pinDrift int

const (
	// pinDriftStale: main carries a contract the protected ref's receipts do
	// not, and this commit is not the one that introduced it.
	pinDriftStale pinDrift = iota
	// pinDriftInFlight: a branch is changing the contract and has not merged.
	pinDriftInFlight
	// pinDriftAdoption: this very commit introduced the contract.
	pinDriftAdoption
)

// classifyPinDrift says what a mismatch between the contract and the protected
// ref's receipts means for the commit under test.
//
// The ref can only advance to a reviewed merge, so two states run ahead of it
// by construction: the branch that changes the contract, and the commit that
// lands it on main. The second matters because this test is a step of the gate
// that attests each commit on main. Failing there left the adoption merge
// permanently unattested — for following the documented procedure, at the one
// commit where no ref it could have pinned existed yet (04d297f).
//
// The allowance covers that commit only. A later commit made while the ref is
// still behind fails, here and at the gate, and with requireCurrent — the job
// that watches main — the adoption commit fails too, so main stays red from the
// moment the contract lands until the ref advances.
func classifyPinDrift(mainCarries, parentCarries, requireCurrent bool) pinDrift {
	switch {
	case !mainCarries:
		return pinDriftInFlight
	case !parentCarries && !requireCurrent:
		return pinDriftAdoption
	default:
		return pinDriftStale
	}
}

func TestClassifyPinDrift(t *testing.T) {
	for _, tc := range []struct {
		name                                       string
		mainCarries, parentCarries, requireCurrent bool
		want                                       pinDrift
	}{
		{"a branch changing the contract", false, false, false, pinDriftInFlight},
		{"the commit that lands the contract on main", true, false, false, pinDriftAdoption},
		{"the job watching main, on that same commit", true, false, true, pinDriftStale},
		{"a later commit while the ref is still behind", true, true, false, pinDriftStale},
		{"a later commit, watched", true, true, true, pinDriftStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyPinDrift(tc.mainCarries, tc.parentCarries, tc.requireCurrent); got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

// The allowance for the adoption commit is only safe while something still
// fails on it. That something is one job, and it is armed by one variable.
func TestTheJobWatchingMainRequiresCurrentPins(t *testing.T) {
	job, ok := reuseWorkflow(t)["protected-reuse-pins"].(map[string]any)
	if !ok {
		t.Fatal("ci.yml has no protected-reuse-pins job")
	}
	if job["if"] != "${{ github.event_name == 'push' }}" {
		t.Errorf("protected-reuse-pins runs on %v, want every push", job["if"])
	}
	for _, s := range job["steps"].([]any) {
		step := s.(map[string]any)
		run, _ := step["run"].(string)
		if !strings.Contains(run, "TestProtectedReusePinsMatchContract") {
			continue
		}
		env, _ := step["env"].(map[string]any)
		if env[requireCurrentPinsEnv] != "1" {
			t.Errorf("the step that runs the pin check sets %s=%v, want \"1\": without it the adoption commit skips and main is green while stale",
				requireCurrentPinsEnv, env[requireCurrentPinsEnv])
		}
		return
	}
	t.Error("no step of protected-reuse-pins runs TestProtectedReusePinsMatchContract")
}
