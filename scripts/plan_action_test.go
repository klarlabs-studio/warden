package scripts_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func requirePlannerTools(t *testing.T) {
	t.Helper()
	requirePOSIX(t)
	for _, tool := range []string{"jq", "realpath"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " unavailable")
		}
	}
}

func planReport(sha string) map[string]any {
	return map[string]any{"version": 1, "sha": sha, "verification_ms": 3, "jobs": []any{
		map[string]any{"name": "unit", "action": "skip", "reasons": []string{"matching evidence"}, "verification_ms": 1},
		map[string]any{"name": "integration", "action": "run", "reasons": []string{"always run"}, "verification_ms": 0},
	}, "run_jobs": []string{"integration"}, "reusable_jobs": []string{"unit"}}
}

func writePlanMock(t *testing.T, f *verifyFixture, content string) {
	t.Helper()
	payload := filepath.Join(t.TempDir(), "report")
	if err := os.WriteFile(payload, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	mock := "#!/bin/sh\ncat '" + payload + "'\n"
	if err := os.WriteFile(filepath.Join(f.bin, "warden"), []byte(mock), 0700); err != nil {
		t.Fatal(err)
	}
}

func TestPlanActionBoundary(t *testing.T) {
	requirePlannerTools(t)
	cases := []struct {
		name    string
		mutate  func(map[string]any)
		suffix  string
		failure bool
		symlink bool
		inside  bool
		keys    string
		valid   bool
	}{
		{name: "complete", keys: actionTestPin, valid: true},
		{name: "planner error", keys: actionTestPin, failure: true},
		{name: "symlink to candidate policy", keys: actionTestPin, inside: true, symlink: true},
		{name: "wrong commit", keys: actionTestPin, mutate: func(p map[string]any) { p["sha"] = strings.Repeat("0", 40) }},
		{name: "missing job", keys: actionTestPin, mutate: func(p map[string]any) { p["jobs"] = p["jobs"].([]any)[:1] }},
		{name: "duplicate job", keys: actionTestPin, mutate: func(p map[string]any) { p["jobs"] = append(p["jobs"].([]any), p["jobs"].([]any)[0]) }},
		{name: "contradictory list", keys: actionTestPin, mutate: func(p map[string]any) { p["reusable_jobs"] = []string{"integration"} }},
		{name: "always run cannot skip", keys: actionTestPin, mutate: func(p map[string]any) {
			p["jobs"].([]any)[1].(map[string]any)["action"] = "skip"
			p["run_jobs"] = []string{}
			p["reusable_jobs"] = []string{"integration", "unit"}
		}},
		{name: "negative duration", keys: actionTestPin, mutate: func(p map[string]any) { p["verification_ms"] = -1 }},
		{name: "unknown version", keys: actionTestPin, mutate: func(p map[string]any) { p["version"] = 2 }},
		{name: "second document", keys: actionTestPin, suffix: "\n{}"},
		{name: "malformed", keys: actionTestPin, suffix: "garbage"},
		{name: "candidate policy", keys: actionTestPin, inside: true},
		{name: "empty trust", keys: " , \t"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newVerifyFixture(t, false)
			p := planReport(f.sha)
			if tc.mutate != nil {
				tc.mutate(p)
			}
			b, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			writePlanMock(t, f, string(b)+tc.suffix)
			policy := filepath.Join(t.TempDir(), "policy.json")
			if tc.inside {
				policy = filepath.Join(f.checkout, "policy.json")
			}
			if err := os.WriteFile(policy, []byte(`{"version":1,"jobs":{"unit":{"version":1,"steps":[{"name":"test"}]},"integration":null}}`), 0600); err != nil {
				t.Fatal(err)
			}
			if tc.failure {
				file := filepath.Join(f.bin, "warden")
				b, err := os.ReadFile(file)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(file, append(b, []byte("exit 2\n")...), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if tc.symlink {
				target := policy
				policy = filepath.Join(t.TempDir(), "linked-policy")
				if err := os.Symlink(target, policy); err != nil {
					t.Fatal(err)
				}
			}
			output, summary := filepath.Join(t.TempDir(), "out"), filepath.Join(t.TempDir(), "summary")
			cmd := exec.Command("bash", repoFile(t, ".github/actions/warden-plan/plan.sh"))
			cmd.Dir = f.checkout
			cmd.Env = append(os.Environ(), "PATH="+f.bin+":"+os.Getenv("PATH"), "WARDEN_COMMIT="+f.sha, "WARDEN_JOB_POLICY="+policy, "WARDEN_TRUSTED_KEYS="+tc.keys, "GITHUB_OUTPUT="+output, "GITHUB_STEP_SUMMARY="+summary, "GITHUB_ACTION_PATH="+filepath.Dir(repoFile(t, ".github/actions/warden-plan/action.yml")))
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("wrapper: %v %s", err, out)
			}
			got, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			want := "plan-valid=false\nreusable-jobs=[]\nrun-jobs=[]\ncommit=\nverification-ms=\n"
			if tc.valid {
				want = "plan-valid=true\nreusable-jobs=[\"unit\"]\nrun-jobs=[\"integration\"]\ncommit=" + f.sha + "\nverification-ms=3\n"
			}
			if string(got) != want {
				t.Fatalf("outputs:\n%s\nwant:\n%s", got, want)
			}
			s, err := os.ReadFile(summary)
			if err != nil {
				t.Fatal(err)
			}
			if tc.valid && !strings.Contains(string(s), "not an observed skip or measured CI savings") {
				t.Fatalf("summary overclaims: %s", s)
			}
		})
	}
}

func TestGitLabPlanHelperMatchesSharedBoundary(t *testing.T) {
	requirePlannerTools(t)
	f := newVerifyFixture(t, true)
	job := interpolateGitLab(t, nil)["warden-verify"]
	item := scriptItem(t, job.Script, "plan-helper")
	if out, code := runItem(t, f.checkout, item, "WARDEN_BIN_DIR="+f.bin); code != 0 {
		t.Fatalf("helper %d: %s", code, out)
	}
	got, err := os.ReadFile(filepath.Join(f.bin, "plan.sh"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(repoFile(t, ".github/actions/ci-plan.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatal("embedded planner differs from shared boundary")
	}
	report, _ := json.Marshal(planReport(f.sha))
	writePlanMock(t, f, string(report))
	policy := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(policy, []byte(`{"version":1,"jobs":{"unit":{},"integration":null}}`), 0600); err != nil {
		t.Fatal(err)
	}
	out, code := f.run(map[string]string{"job-policy": policy, "key": actionTestPin, "on-unverified": "run-checks"})
	if code != 0 {
		t.Fatalf("plan %d: %s", code, out)
	}
	if got := f.verdict(); got["WARDEN_VALIDATED"] != "false" || got["WARDEN_VERDICT"] != "planned" {
		t.Fatalf("planning must not globally validate: %v", got)
	}
	artifact, err := os.ReadFile(filepath.Join(f.checkout, "warden-plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(artifact, &decoded); err != nil || decoded["sha"] != f.sha {
		t.Fatalf("artifact %s: %v", artifact, err)
	}
	helper := interpolateGitLab(t, nil)[".warden-planned-job"].BeforeScript[0]
	for _, name := range []string{"unit", "integration", "unknown"} {
		_, code := runItem(t, f.checkout, helper+"\nwarden_job_reusable "+name)
		if (code == 0) != (name == "unit") {
			t.Fatalf("job %s: exit %d", name, code)
		}
	}
	if err := os.WriteFile(filepath.Join(f.checkout, "warden-plan.json"), append(artifact, artifact...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, code := runItem(t, f.checkout, helper+"\nwarden_job_reusable unit"); code == 0 {
		t.Fatal("multiple documents authorized reuse")
	}
	if err := os.WriteFile(filepath.Join(f.bin, "warden"), []byte("#!/bin/sh\nexit 2\n"), 0700); err != nil {
		t.Fatal(err)
	}
	out, code = f.run(map[string]string{"job-policy": policy, "key": actionTestPin, "on-unverified": "run-checks"})
	if code != 0 {
		t.Fatalf("fallback %d: %s", code, out)
	}
	if got := f.verdict(); got["WARDEN_VALIDATED"] != "false" || got["WARDEN_VERDICT"] != "plan-unavailable" {
		t.Fatalf("failure verdict: %v", got)
	}
	if _, err := os.Stat(filepath.Join(f.checkout, "warden-plan.json")); !os.IsNotExist(err) {
		t.Fatalf("stale plan survived failure: %v", err)
	}
	for _, clauses := range []map[string]string{
		{"job-policy": policy, "key": actionTestPin},
		{"job-policy": policy, "key": actionTestPin, "on-unverified": "run-checks", "min-profile": "strict"},
	} {
		if out, code := f.run(clauses); code != 2 {
			t.Fatalf("incompatible clauses accepted: %d %s", code, out)
		}
	}
}

func TestPlanActionWiring(t *testing.T) {
	data, err := os.ReadFile(repoFile(t, ".github/actions/warden-plan/action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var action struct {
		Runs struct {
			Steps []struct {
				ID, Run string
				Env     map[string]string
			}
		}
	}
	if err := yaml.Unmarshal(data, &action); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, step := range action.Runs.Steps {
		if strings.Contains(step.Run, "${{") {
			t.Fatal("input interpolation in shell")
		}
		if step.ID == "plan" {
			found = true
			for key, input := range map[string]string{"WARDEN_COMMIT": "commit", "WARDEN_JOB_POLICY": "policy", "WARDEN_TRUSTED_KEYS": "key"} {
				if step.Env[key] != "${{ inputs."+input+" }}" {
					t.Fatalf("input %s not isolated in environment", key)
				}
			}
		}
	}
	if !found || !strings.Contains(string(data), "../install-warden.sh") {
		t.Fatal("missing planner or shared installer")
	}
}

func TestGitLabPlannedJobRefusesUntrustedArtifacts(t *testing.T) {
	requirePlannerTools(t)
	for _, mode := range []string{"missing", "stale", "duplicate", "tracked", "symlink", "unknown version"} {
		t.Run(mode, func(t *testing.T) {
			f := newVerifyFixture(t, false)
			p := planReport(f.sha)
			if mode == "stale" {
				p["sha"] = strings.Repeat("0", 40)
			}
			if mode == "unknown version" {
				p["version"] = 2
			}
			if mode == "duplicate" {
				p["jobs"] = append(p["jobs"].([]any), p["jobs"].([]any)[0])
			}
			b, _ := json.Marshal(p)
			path := filepath.Join(f.checkout, "warden-plan.json")
			if mode != "missing" {
				if err := os.WriteFile(path, b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "tracked" {
				gitRun(t, f.checkout, "add", "--", "warden-plan.json")
			}
			if mode == "symlink" {
				target := filepath.Join(t.TempDir(), "plan")
				if err := os.Rename(path, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			}
			helper := interpolateGitLab(t, nil)[".warden-planned-job"].BeforeScript[0]
			if _, code := runItem(t, f.checkout, helper+"\nwarden_job_reusable unit"); code == 0 {
				t.Fatal("untrusted artifact authorized reuse")
			}
		})
	}
}
