package scripts_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

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
		{"trusted attestation", "0123456789abcdef", "0", true, true},
		{"multiple keys", "0123456789abcdef,fedcba9876543210", "0", true, true},
		{"unverified", "0123456789abcdef", "1", true, false},
		{"verifier error", "0123456789abcdef", "2", true, false},
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
			cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "GITHUB_ACTION_PATH="+filepath.Dir(repoFile(t, ".github/actions/warden-verify/action.yml")), "GITHUB_OUTPUT="+output, "WARDEN_COMMIT="+commit, "WARDEN_TRUSTED_KEYS="+tt.keys, "VERIFY_ARGS="+args, "VERIFY_STATUS="+tt.status)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("check: %v: %s", err, out)
			}
			got, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			want := "validated=false\n"
			if tt.validated {
				want = "validated=true\n"
			}
			if string(got) != want {
				t.Fatalf("output = %q, want %q", got, want)
			}
			argv, err := os.ReadFile(args)
			if tt.called {
				if err != nil {
					t.Fatal(err)
				}
				wantArgs := []string{"verify", "--commit", commit, "--key", tt.keys, "--quiet"}
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
	cmd := exec.Command("bash", repoFile(t, ".github/actions/warden-verify/verify.sh"))
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"), "GITHUB_OUTPUT="+filepath.Join(dir, "output"), "WARDEN_COMMIT=tip", "WARDEN_TRUSTED_KEYS=0123456789abcdef", "WARDEN_REQUIREMENTS=/protected/policy with spaces.json", "WARDEN_CONTAINER_STEPS=lint,test", "WARDEN_CONTAINER_IMAGE_ID="+image, "VERIFY_ARGS="+argsFile)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script: %v %s", err, out)
	}
	data, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"verify", "--commit", "tip", "--key", "0123456789abcdef", "--requirements", "/protected/policy with spaces.json", "--container-steps", "lint,test", "--container-image-id", image, "--quiet"}
	if got := strings.Split(strings.TrimSuffix(string(data), "\x00"), "\x00"); !reflect.DeepEqual(got, want) {
		t.Fatalf("arguments: %q want %q", got, want)
	}
}
