package scripts_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// These are deliberately repetitive public fingerprints for the mock verifier.
const actionTestPin = "aaaaaaaaaaaaaaaa"

func TestVerifyActionPinnedTrust(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX action")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	// Execute the action's actual check step with a controlled verifier. The
	// output file, rather than the shell's successful exit, authorizes CI reuse.
	data, err := os.ReadFile(repoFile(t, ".github/actions/warden-verify/action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var action struct {
		Runs struct {
			Steps []struct {
				ID  string            `yaml:"id"`
				Run string            `yaml:"run"`
				Env map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(data, &action); err != nil {
		t.Fatal(err)
	}
	var check string
	for _, step := range action.Runs.Steps {
		if step.ID == "check" {
			check = step.Run
			if step.Env["WARDEN_COMMIT"] != "${{ inputs.commit }}" || step.Env["WARDEN_TRUSTED_KEYS"] != "${{ inputs.key }}" {
				t.Fatal("check inputs must be passed through environment")
			}
		}
	}
	if check == "" || strings.Contains(check, "${{") {
		t.Fatal("missing check or inline expression in shell")
	}
	for _, tt := range []struct {
		name, keys, status string
		called, validated  bool
	}{
		{"missing pin", "", "0", false, false},
		{"blank pin", " \t\n", "0", false, false},
		{"empty list", " , ,\t", "0", false, false},
		{"trusted attestation", actionTestPin, "0", true, true},
		{"multiple keys", actionTestPin + ",bbbbbbbbbbbbbbbb", "0", true, true},
		{"unverified", actionTestPin, "1", true, false},
		{"verifier error", actionTestPin, "2", true, false},
		{"literal shell input", "$(touch injected);`touch injected`", "1", true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			mock := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$VERIFY_ARGS\"\nexit \"$VERIFY_STATUS\"\n"
			if err := os.WriteFile(filepath.Join(dir, "warden"), []byte(mock), 0o700); err != nil {
				t.Fatal(err)
			}
			output, args := filepath.Join(dir, "output"), filepath.Join(dir, "args")
			commit := "abc$(touch injected)`touch injected`"
			cmd := exec.Command("bash", "-e", "-c", check)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(),
				"PATH="+dir+":"+os.Getenv("PATH"),
				"GITHUB_ACTION_PATH="+filepath.Dir(repoFile(t, ".github/actions/warden-verify/action.yml")),
				"GITHUB_OUTPUT="+output,
				"WARDEN_COMMIT="+commit,
				"WARDEN_TRUSTED_KEYS="+tt.keys,
				"VERIFY_ARGS="+args,
				"VERIFY_STATUS="+tt.status,
			)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("check: %v: %s", err, out)
			}
			got, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			want := "validated=false\nscope=none\nvalidated-workspaces=[]\n"
			if tt.validated {
				// The mock prints no JSON, as a release older than
				// single-commit --json prints text: the verdict stands on the
				// exit code, and the scope is none.
				want = "validated=true\nscope=none\nvalidated-workspaces=[]\n"
			}
			if string(got) != want {
				t.Fatalf("output = %q, want %q", got, want)
			}
			argv, err := os.ReadFile(args)
			if tt.called {
				if err != nil {
					t.Fatal(err)
				}
				wantArgs := []string{"verify", "--commit", commit, "--key", tt.keys, "--json"}
				if gotArgs := strings.Split(strings.TrimSuffix(string(argv), "\n"), "\n"); !reflect.DeepEqual(gotArgs, wantArgs) {
					t.Fatalf("arguments = %q, want %q", gotArgs, wantArgs)
				}
			} else if !os.IsNotExist(err) {
				t.Fatalf("verifier called without pin: %q, %v", argv, err)
			}
			if _, err := os.Stat(filepath.Join(dir, "injected")); !os.IsNotExist(err) {
				t.Fatal("input executed as shell code")
			}
		})
	}
}

func TestVerifyActionContainerRequirements(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX action")
	}
	dir := t.TempDir()
	mock := "#!/bin/sh\nprintf '%s\\000' \"$@\" > \"$VERIFY_ARGS\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(dir, "warden"), []byte(mock), 0o700); err != nil {
		t.Fatal(err)
	}
	argsFile := filepath.Join(dir, "args")
	image := "sha256:" + strings.Repeat("a", 64)
	requirements := filepath.Join(dir, "policy with spaces.json")
	cmd := exec.Command("bash", repoFile(t, ".github/actions/warden-verify/verify.sh"))
	cmd.Env = append(os.Environ(),
		"PATH="+dir+":"+os.Getenv("PATH"),
		"GITHUB_OUTPUT="+filepath.Join(dir, "output"),
		"WARDEN_COMMIT=tip",
		"WARDEN_TRUSTED_KEYS="+actionTestPin,
		"WARDEN_REQUIREMENTS="+requirements,
		"WARDEN_CONTAINER_STEPS=lint,test",
		"WARDEN_CONTAINER_IMAGE_ID="+image,
		"WARDEN_MIN_PROFILE=strict",
		"VERIFY_ARGS="+argsFile,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script: %v %s", err, out)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"verify", "--commit", "tip", "--key", actionTestPin, "--requirements", requirements, "--container-steps", "lint,test", "--container-image-id", image, "--min-profile", "strict", "--json"}
	if got := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00"); !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments: %q want %q", got, want)
	}
}

// The min-profile input reaches the verifier through the environment, never
// inline, and a verifier that rejects the flag (an older release, exit 2)
// leaves validated=false so CI runs its checks.
func TestVerifyActionMinProfile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX action")
	}
	data, err := os.ReadFile(repoFile(t, ".github/actions/warden-verify/action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var action struct {
		Inputs map[string]struct {
			Default string `yaml:"default"`
		} `yaml:"inputs"`
		Runs struct {
			Steps []struct {
				ID  string            `yaml:"id"`
				Env map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(data, &action); err != nil {
		t.Fatal(err)
	}
	if in, ok := action.Inputs["min-profile"]; !ok || in.Default != "" {
		t.Fatal("min-profile must be an optional input defaulting to empty")
	}
	passed := false
	for _, step := range action.Runs.Steps {
		if step.ID == "check" && step.Env["WARDEN_MIN_PROFILE"] == "${{ inputs.min-profile }}" {
			passed = true
		}
	}
	if !passed {
		t.Fatal("min-profile must reach the check step through WARDEN_MIN_PROFILE")
	}
	for _, tc := range []struct {
		profile, status, want string
		wantFlag              bool
	}{
		{"", "0", "validated=true\nscope=none\nvalidated-workspaces=[]\n", false},
		{"strict", "0", "validated=true\nscope=none\nvalidated-workspaces=[]\n", true},
		{"strict", "2", "validated=false\nscope=none\nvalidated-workspaces=[]\n", true},
		{"strict", "1", "validated=false\nscope=none\nvalidated-workspaces=[]\n", true},
	} {
		dir := t.TempDir()
		mock := "#!/bin/sh\nprintf '%s\\000' \"$@\" > \"$VERIFY_ARGS\"\nexit \"$VERIFY_STATUS\"\n"
		if err := os.WriteFile(filepath.Join(dir, "warden"), []byte(mock), 0o700); err != nil {
			t.Fatal(err)
		}
		argsFile, output := filepath.Join(dir, "args"), filepath.Join(dir, "output")
		cmd := exec.Command("bash", repoFile(t, ".github/actions/warden-verify/verify.sh"))
		cmd.Env = append(os.Environ(),
			"PATH="+dir+":"+os.Getenv("PATH"),
			"GITHUB_OUTPUT="+output,
			"WARDEN_COMMIT=tip",
			"WARDEN_TRUSTED_KEYS="+actionTestPin,
			"WARDEN_MIN_PROFILE="+tc.profile,
			"VERIFY_ARGS="+argsFile,
			"VERIFY_STATUS="+tc.status,
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("script: %v %s", err, out)
		}
		if got, _ := os.ReadFile(output); string(got) != tc.want {
			t.Errorf("profile %q status %s: output %q want %q", tc.profile, tc.status, got, tc.want)
		}
		args, err := os.ReadFile(argsFile)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(string(args), "--min-profile\x00"+tc.profile+"\x00"); got != tc.wantFlag {
			t.Errorf("profile %q: --min-profile passed=%v want %v (%q)", tc.profile, got, tc.wantFlag, args)
		}
	}
}

// warden-gate forwards min-profile to the range verifier. A binary that cannot
// check it exits 2, and the gate fails rather than passing an unchecked range.
func TestGateActionMinProfileFailsClosed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX action")
	}
	data, err := os.ReadFile(repoFile(t, ".github/actions/warden-gate/action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var action struct {
		Runs struct {
			Steps []struct {
				Name string            `yaml:"name"`
				Run  string            `yaml:"run"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"runs"`
	}
	if err := yaml.Unmarshal(data, &action); err != nil {
		t.Fatal(err)
	}
	var gate string
	for _, step := range action.Runs.Steps {
		if step.Name == "Gate the range" {
			gate = step.Run
			if step.Env["MIN_PROFILE"] != "${{ inputs.min-profile }}" {
				t.Fatal("min-profile must reach the gate through MIN_PROFILE")
			}
		}
	}
	if gate == "" || strings.Contains(gate, "${{") {
		t.Fatal("missing gate step or inline expression in shell")
	}
	for _, tc := range []struct {
		profile, status string
		wantCode        int
		wantFlag        bool
	}{
		{"", "0", 0, false},
		{"strict", "2", 2, true},
	} {
		dir := t.TempDir()
		mock := "#!/bin/sh\nprintf '%s\\000' \"$@\" > \"$VERIFY_ARGS\"\nexit \"$VERIFY_STATUS\"\n"
		if err := os.WriteFile(filepath.Join(dir, "warden"), []byte(mock), 0o700); err != nil {
			t.Fatal(err)
		}
		argsFile := filepath.Join(dir, "args")
		cmd := exec.Command("bash", "-c", gate)
		cmd.Env = append(os.Environ(),
			"PATH="+dir+":"+os.Getenv("PATH"),
			"GITHUB_STEP_SUMMARY="+filepath.Join(dir, "summary"),
			"IN_BASE=base", "IN_HEAD=head", "REQUIRE_SIGNED=false", "SKIP_MERGES=true",
			"KEY="+actionTestPin, "MIN_PROFILE="+tc.profile,
			"VERIFY_ARGS="+argsFile, "VERIFY_STATUS="+tc.status,
		)
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		if code != tc.wantCode {
			t.Errorf("profile %q: exit %d want %d: %s", tc.profile, code, tc.wantCode, out)
		}
		args, err := os.ReadFile(argsFile)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(string(args), "--min-profile\x00"+tc.profile+"\x00"); got != tc.wantFlag {
			t.Errorf("profile %q: --min-profile passed=%v want %v (%q)", tc.profile, got, tc.wantFlag, args)
		}
	}
}

// The scope outputs come from warden's own JSON, and only when that JSON
// agrees with the exit code; the verdict itself is still the exit code
// alone. Workspace paths are data: they reach the output as JSON, never as
// shell code.
func TestVerifyActionScopeOutputs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX action")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash not available")
	}
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq not available")
	}
	weird := `apps/w $(touch injected) "q" \ ` + "`touch injected`"
	weirdJSON := `"apps/w $(touch injected) \"q\" \\ ` + "`touch injected`" + `"`
	full := `{"commit":"c","validated":true,"scope":"full","validated_workspaces":["apps/web","packages/ui"]}`
	affected := `{"commit":"c","validated":false,"scope":"affected","validated_workspaces":["apps/web",` + weirdJSON + `]}`
	for _, tc := range []struct {
		name, status, report string
		noJQ                 bool
		want                 string
	}{
		{"full", "0", full, false, `validated=true` + "\nscope=full\n" + `validated-workspaces=["apps/web","packages/ui"]`},
		{"affected", "1", affected, false, "validated=false\nscope=affected\n" + `validated-workspaces=["apps/web",` + weirdJSON + `]`},
		{"none", "1", `{"validated":false,"scope":"none","validated_workspaces":[]}`, false, "validated=false\nscope=none\nvalidated-workspaces=[]"},
		// The JSON must agree with the exit code, or it says nothing.
		{"full claim on failure", "1", full, false, "validated=false\nscope=none\nvalidated-workspaces=[]"},
		{"affected claim on pass", "0", affected, false, "validated=true\nscope=none\nvalidated-workspaces=[]"},
		{"affected claim on error", "2", affected, false, "validated=false\nscope=none\nvalidated-workspaces=[]"},
		// Malformed or foreign output is no claim.
		{"text from an older release", "0", "validated abc1234 (run, 3 steps, chain-intact)", false, "validated=true\nscope=none\nvalidated-workspaces=[]"},
		{"non-string workspace", "1", `{"validated":false,"scope":"affected","validated_workspaces":["a",1]}`, false, "validated=false\nscope=none\nvalidated-workspaces=[]"},
		{"missing workspaces", "0", `{"validated":true,"scope":"full"}`, false, "validated=true\nscope=none\nvalidated-workspaces=[]"},
		{"string validated", "0", `{"validated":"true","scope":"full","validated_workspaces":[]}`, false, "validated=true\nscope=none\nvalidated-workspaces=[]"},
		{"no jq", "1", affected, true, "validated=false\nscope=none\nvalidated-workspaces=[]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0o700); err != nil {
				t.Fatal(err)
			}
			mock := "#!" + bash + "\nprintf '%s\\n' \"$VERIFY_REPORT\"\nexit \"$VERIFY_STATUS\"\n"
			if err := os.WriteFile(filepath.Join(bin, "warden"), []byte(mock), 0o700); err != nil {
				t.Fatal(err)
			}
			if !tc.noJQ {
				if err := os.Symlink(jq, filepath.Join(bin, "jq")); err != nil {
					t.Fatal(err)
				}
			}
			output := filepath.Join(dir, "output")
			cmd := exec.Command(bash, repoFile(t, ".github/actions/warden-verify/verify.sh"))
			cmd.Dir = dir
			// PATH holds only the mock and (maybe) jq: the script needs
			// nothing else, so "no jq" is real.
			cmd.Env = []string{
				"PATH=" + bin,
				"GITHUB_OUTPUT=" + output,
				"WARDEN_COMMIT=tip",
				"WARDEN_TRUSTED_KEYS=" + actionTestPin,
				"VERIFY_STATUS=" + tc.status,
				"VERIFY_REPORT=" + tc.report,
			}
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("script: %v %s", err, out)
			}
			got, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want+"\n" {
				t.Fatalf("outputs:\n%s\nwant:\n%s", got, tc.want)
			}
			if _, err := os.Stat(filepath.Join(dir, "injected")); !os.IsNotExist(err) {
				t.Fatal("a workspace path executed as shell code")
			}
			if tc.name == "affected" {
				var paths []string
				_, line, _ := strings.Cut(strings.Split(string(got), "\n")[2], "=")
				if err := json.Unmarshal([]byte(line), &paths); err != nil || !reflect.DeepEqual(paths, []string{"apps/web", weird}) {
					t.Fatalf("validated-workspaces does not round-trip: %q %v", paths, err)
				}
			}
		})
	}
}

// The action declares the scope outputs, taken from the check step.
func TestVerifyActionDeclaresScopeOutputs(t *testing.T) {
	data, err := os.ReadFile(repoFile(t, ".github/actions/warden-verify/action.yml"))
	if err != nil {
		t.Fatal(err)
	}
	var action struct {
		Outputs map[string]struct {
			Value string `yaml:"value"`
		} `yaml:"outputs"`
	}
	if err := yaml.Unmarshal(data, &action); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"validated", "scope", "validated-workspaces"} {
		if got, want := action.Outputs[name].Value, "${{ steps.check.outputs."+name+" }}"; got != want {
			t.Errorf("output %s = %q, want %q", name, got, want)
		}
	}
}
