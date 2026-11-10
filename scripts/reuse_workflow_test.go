package scripts_test

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func reuseWorkflow(t *testing.T) map[string]any {
	t.Helper()
	b, err := os.ReadFile(repoFile(t, ".github/workflows/ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var w map[string]any
	if err := yaml.Unmarshal(b, &w); err != nil {
		t.Fatal(err)
	}
	return w["jobs"].(map[string]any)
}

func TestProtectedReuseWorkflowBoundary(t *testing.T) {
	requirePlannerTools(t)
	jobs := reuseWorkflow(t)
	job := jobs["test-reuse-plan"].(map[string]any)
	if job["if"] != "${{ github.event_name == 'pull_request' && github.event.pull_request.head.repo.full_name == github.repository }}" {
		t.Fatal("fork/push fallback changed")
	}
	if job["permissions"].(map[string]any)["contents"] != "read" {
		t.Fatal("verifier needs only read access")
	}
	if job["outputs"].(map[string]any)["reuse-sha"] != "${{ steps.decision.outcome == 'success' && steps.decision.outputs.reuse-sha || '' }}" {
		t.Fatal("failed step can publish output")
	}
	env := job["env"].(map[string]any)
	contract := strings.TrimSpace(env["EXPECTED_CONTRACT"].(string))
	caller := jobs["ci"].(map[string]any)
	if strings.TrimSpace(caller["with"].(map[string]any)["test-container-contract"].(string)) != contract {
		t.Fatal("protected and caller contracts differ")
	}
	if caller["if"] != "${{ !cancelled() }}" {
		t.Fatal("failed verifier must not suppress CI")
	}
	var pinCheck, evidence, script string
	reported := false
	gated := map[string]bool{}
	for _, raw := range job["steps"].([]any) {
		step := raw.(map[string]any)
		if step["name"] == "Check out protected verifier and policy" {
			with := step["with"].(map[string]any)
			if ref, _ := with["ref"].(string); !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(ref) || with["persist-credentials"] != false {
				t.Fatal("protected source must remain immutable and credential-free")
			}
		}
		if step["id"] == "pins" {
			// Drift is a misconfiguration and must stay visible, never tolerated.
			if step["continue-on-error"] != nil || evidence != "" || script != "" {
				t.Fatal("pin drift must fail visibly, before evidence and the tolerated decision step")
			}
			pinCheck = step["run"].(string)
		}
		if step["id"] == "evidence" {
			if step["if"] != "steps.pins.outputs.consistent == 'true'" || step["continue-on-error"] != nil || script != "" {
				t.Fatal("evidence must follow consistent pins and precede the decision")
			}
			evidence = step["run"].(string)
		}
		uses, _ := step["uses"].(string)
		if step["name"] == "Build protected verifier" || strings.HasPrefix(uses, "actions/setup-go@") || step["id"] == "decision" {
			gated[fmt.Sprint(step["name"], uses)] = step["if"] == "steps.evidence.outputs.present == 'true'"
		}
		if step["id"] == "decision" {
			if step["continue-on-error"] != true {
				t.Fatal("a verifier failure must fall back to tests")
			}
			script = step["run"].(string)
		}
		if step["name"] == "Report verifier failure" {
			reported = step["if"] == "steps.decision.outcome == 'failure'" && strings.Contains(step["run"].(string), "::warning::")
		}
	}
	if pinCheck == "" || evidence == "" || script == "" || len(gated) != 3 {
		t.Fatal("missing verifier boundary")
	}
	for step, ok := range gated {
		if !ok {
			t.Fatalf("%s must run only with container evidence", step)
		}
	}
	if !reported {
		t.Fatal("a failed verifier must be reported, not hidden by continue-on-error")
	}
	for _, tc := range []struct {
		name         string
		mutate       func(map[string]any)
		fail         string
		base, branch string
		// note replaces the candidate's container test environment; "-" omits it.
		note     string
		baseCI   bool // the comparison base already carries the (drifted) contract
		pinsFail bool
		want     bool
	}{
		{name: "matching", want: true},
		{name: "ordinary run", mutate: func(p map[string]any) {
			p["jobs"].([]any)[0].(map[string]any)["action"] = "run"
			p["run_jobs"] = []string{"test"}
			p["reusable_jobs"] = []string{}
		}},
		{name: "wrong commit", mutate: func(p map[string]any) { p["sha"] = strings.Repeat("f", 40) }},
		{name: "extra job", mutate: func(p map[string]any) {
			p["jobs"] = append(p["jobs"].([]any), map[string]any{"name": "lint", "action": "skip", "reasons": []string{"wrong inventory"}, "verification_ms": 0})
		}},
		{name: "contradictory list", mutate: func(p map[string]any) { p["run_jobs"] = []string{"test"} }},
		{name: "unknown version", mutate: func(p map[string]any) { p["version"] = 2 }},
		{name: "missing notes", fail: "fetch"},
		{name: "host-gated note", note: "-"},
		{name: "other image", note: `{"image_id":"sha256:` + strings.Repeat("0", 64) + `","platform":"linux/amd64"}`},
		{name: "other platform", note: `{"image_id":"CONFIG","platform":"linux/arm64"}`},
		{name: "unparseable note", note: `not json`},
		{name: "stale protected ref", fail: "contract:config_id", baseCI: true, pinsFail: true},
		{name: "contract drift", fail: "contract:config_id"},
		{name: "image drift", fail: "contract:image"},
		{name: "command drift", fail: "contract:command"},
		{name: "platform drift", fail: "contract:platform"},
		{name: "publisher drift", fail: "contract:signing_identity"},
		{name: "preparer failure", fail: "policy"},
		{name: "planner failure", fail: "planner"},
		{name: "symbolic base", base: "HEAD"},
		{name: "invalid branch", branch: "bad\nreuse-sha=forged"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newVerifyFixture(t, false)
			workspace := t.TempDir()
			runtime := t.TempDir()
			bin := filepath.Join(runtime, "warden-reuse-bin")
			if err := os.MkdirAll(bin, 0700); err != nil {
				t.Fatal(err)
			}
			trusted := filepath.Join(workspace, "trusted-reuse")
			actions := filepath.Join(trusted, ".github", "actions")
			if err := os.MkdirAll(actions, 0700); err != nil {
				t.Fatal(err)
			}
			profile := filepath.Join(trusted, ".github", "warden", "linux-test", "profiles", "amd64")
			if err := os.MkdirAll(profile, 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"publication.json", "docker-policy-template.json"} {
				data, err := os.ReadFile(repoFile(t, ".github/warden/linux-test/profiles/amd64/"+name))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(profile, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			expected := contract
			if strings.HasPrefix(tc.fail, "contract:") {
				var c map[string]string
				if err := json.Unmarshal([]byte(expected), &c); err != nil {
					t.Fatal(err)
				}
				c[strings.TrimPrefix(tc.fail, "contract:")] = "different"
				b, err := json.Marshal(c)
				if err != nil {
					t.Fatal(err)
				}
				expected = string(b)
			}
			actual, err := os.ReadFile(repoFile(t, ".github/actions/ci-plan.sh"))
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(actions, "ci-plan.sh"), actual, 0600); err != nil {
				t.Fatal(err)
			}
			p := map[string]any{"version": 1, "sha": f.sha, "verification_ms": 1, "jobs": []any{map[string]any{"name": "test", "action": "skip", "reasons": []string{"matching"}, "verification_ms": 1}}, "run_jobs": []string{}, "reusable_jobs": []string{"test"}}
			if tc.mutate != nil {
				tc.mutate(p)
			}
			data, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			report := filepath.Join(runtime, "report.json")
			if err = os.WriteFile(report, data, 0600); err != nil {
				t.Fatal(err)
			}
			realGit, err := exec.LookPath("git")
			if err != nil {
				t.Fatal(err)
			}
			mocks := map[string]string{
				"git":       "#!/bin/sh\nif [ \"$1\" = fetch ]; then [ \"$FAIL\" != fetch ]; exit; fi\nexec \"$REAL_GIT\" \"$@\"\n",
				"ci-policy": "#!/bin/sh\n[ \"$FAIL\" != policy ] || exit 1\nprintf '%s\\n' \"$@\" > \"$RUNNER_TEMP/policy-args\"\nprintf '%s\\n' '{\"version\":1,\"jobs\":{\"test\":{}}}'\n",
				"warden":    "#!/bin/sh\n[ \"$FAIL\" != planner ] || exit 1\ncat \"$REPORT\"\n",
			}
			for name, body := range mocks {
				if err = os.WriteFile(filepath.Join(bin, name), []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
			}
			base := f.sha
			if tc.base != "" {
				base = tc.base
			}
			branch := "feature"
			if tc.branch != "" {
				branch = tc.branch
			}
			event := filepath.Join(runtime, "event.json")
			eventData, err := json.Marshal(map[string]any{"pull_request": map[string]any{"head": map[string]string{"ref": branch}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(event, eventData, 0600); err != nil {
				t.Fatal(err)
			}
			var c map[string]string
			if err := json.Unmarshal([]byte(contract), &c); err != nil {
				t.Fatal(err)
			}
			env := `{"image_id":"CONFIG","platform":"linux/amd64"}`
			if tc.note != "" {
				env = tc.note
			}
			note := `{"environments":{"test":` + strings.ReplaceAll(env, "CONFIG", c["config_id"]) + `}}`
			switch tc.note {
			case "-":
				note = `{"commit_sha":"` + f.sha + `"}`
			case "not json":
				note = env
			}
			gitRun(t, f.checkout, "notes", "--ref=warden", "add", "-f", "-m", note, f.sha)
			if tc.baseCI {
				// A main commit that already carries the drifted contract.
				mk := exec.Command("bash", "-c", `set -e
blob=$(printf '%s\n' "$CONTRACT" | git hash-object -w --stdin)
export GIT_INDEX_FILE="$RUNNER_TEMP/base-index"
git update-index --add --cacheinfo "100644,$blob,.github/workflows/ci.yml"
git commit-tree "$(git write-tree)" -p HEAD -m base`)
				mk.Dir = f.checkout
				mk.Env = append(os.Environ(), "CONTRACT="+expected, "RUNNER_TEMP="+runtime, "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t")
				out, err := mk.Output()
				if err != nil {
					t.Fatal(err)
				}
				base = strings.TrimSpace(string(out))
			}
			if err := os.Symlink(f.checkout, filepath.Join(workspace, "reuse-candidate")); err != nil {
				t.Fatal(err)
			}
			// Run the steps in order, honoring their if: gates.
			var log []byte
			run := func(name, body, dir string) (map[string]string, error) {
				output := filepath.Join(runtime, name+"-output")
				cmd := exec.Command("bash", "-c", body)
				cmd.Dir = dir
				cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_NO_REPLACE_OBJECTS=1", "CANDIDATE_SHA="+f.sha, "COMPARISON_BASE="+base, "EXPECTED_CONTRACT="+expected, "GITHUB_EVENT_PATH="+event, "GITHUB_WORKSPACE="+workspace, "RUNNER_TEMP="+runtime, "GITHUB_OUTPUT="+output, "WARDEN_COMMIT="+f.sha, "WARDEN_TRUSTED_KEYS="+actionTestPin, "WARDEN_PLAN_MODE=enforce", "FAIL="+tc.fail, "REAL_GIT="+realGit, "REPORT="+report)
				out, err := cmd.CombinedOutput()
				log = append(log, out...)
				raw, _ := os.ReadFile(output)
				values := map[string]string{}
				for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
					if k, v, ok := strings.Cut(line, "="); ok {
						values[k] = v
					}
				}
				return values, err
			}
			pins, pinsErr := run("pins", pinCheck, workspace)
			if (pinsErr != nil) != tc.pinsFail {
				t.Fatalf("pin check error = %v, want failure %v: %s", pinsErr, tc.pinsFail, log)
			}
			var result []byte
			var runErr error
			if pins["consistent"] == "true" {
				found, err := run("evidence", evidence, f.checkout)
				if err != nil {
					t.Fatalf("evidence must never fail the job: %v %s", err, log)
				}
				if found["present"] == "true" {
					var decided map[string]string
					decided, runErr = run("decision", script, f.checkout)
					if sha, ok := decided["reuse-sha"]; ok {
						result = []byte("reuse-sha=" + sha + "\n")
					}
				}
			}
			if tc.want {
				if runErr != nil || string(result) != "reuse-sha="+f.sha+"\n" {
					t.Fatalf("matching result: %v %s output=%s", runErr, log, result)
				}
				args, err := os.ReadFile(filepath.Join(runtime, "policy-args"))
				if err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(string(args), "--base\n"+base+"\n--commit\n"+f.sha+"\n--branch\nfeature\n") {
					t.Fatalf("independent context lost: %s", args)
				}
			} else if len(result) != 0 {
				t.Fatalf("unverified reuse escaped: %s (%s)", result, log)
			}
		})
	}
}
