package importer

import (
	"strings"
	"testing"
)

// A pre-commit config imports to `pre-commit run`, NOT to the individual hooks'
// commands. Those hooks execute inside environments pre-commit provisions — a
// pinned rev of black in its own virtualenv — which warden cannot reproduce from
// the config alone. Reconstructing "black --check ." would name a binary that is
// not on PATH: an import that looks successful and fails at the gate.
func TestDetect_PreCommit_DelegatesToPreCommitRun(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".pre-commit-config.yaml", `
repos:
  - repo: https://github.com/psf/black
    rev: 23.1.0
    hooks:
      - id: black
  - repo: https://github.com/pycqa/flake8
    rev: 6.0.0
    hooks:
      - id: flake8
        args: [--max-line-length=100]
`)
	cfg, notes, err := Detect(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Commands["lint"] != "pre-commit run --all-files" {
		t.Errorf("lint = %q, want the pre-commit runner", cfg.Commands["lint"])
	}
	// The note must say what happened, and how many hooks it covers, so the user
	// can see their config was understood rather than guessed at.
	var found bool
	for _, n := range notes {
		if strings.Contains(n, "pre-commit") && strings.Contains(n, "2") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected a note naming the hook count, got %v", notes)
	}
}

// A security-flavored hook backs the security-scan step with the check the repo
// already trusts, run through pre-commit so it keeps its own environment.
func TestDetect_PreCommit_MapsSecurityHook(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".pre-commit-config.yaml", `
repos:
  - repo: https://github.com/Yelp/detect-secrets
    rev: v1.4.0
    hooks:
      - id: detect-secrets
`)
	cfg, _, err := Detect(root)
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Commands["security-scan"]; got != "pre-commit run --all-files detect-secrets" {
		t.Errorf("security-scan = %q", got)
	}
	// security-scan found means the step joins the pre-push sequence.
	var hasScan bool
	for _, s := range cfg.Steps["pre_push"] {
		if s == stepSecurityScan {
			hasScan = true
		}
	}
	if !hasScan {
		t.Errorf("security-scan must be inserted into pre_push, got %v", cfg.Steps["pre_push"])
	}
}

// check-added-large-files is repo hygiene, not a security scan. Mapping it would
// point the security step at a check that finds no vulnerabilities, which is
// worse than having no security step: it reads as coverage that isn't there.
func TestDetect_PreCommit_HygieneHooksAreNotSecurity(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".pre-commit-config.yaml", `
repos:
  - repo: https://github.com/pre-commit/pre-commit-hooks
    rev: v4.4.0
    hooks:
      - id: check-added-large-files
      - id: trailing-whitespace
`)
	cfg, _, err := Detect(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Commands["security-scan"]; ok {
		t.Errorf("hygiene hooks must not back security-scan, got %q", cfg.Commands["security-scan"])
	}
	// Lint still imports — the hooks are real, just not security ones.
	if cfg.Commands["lint"] == "" {
		t.Error("lint should still import from the hooks present")
	}
}

// A local `language: system` hook runs a plain command from PATH, so warden can
// run it directly. Tests are the case worth lifting into their own step.
func TestDetect_PreCommit_LocalSystemTestHookBecomesTheTestCommand(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".pre-commit-config.yaml", `
repos:
  - repo: local
    hooks:
      - id: unit-tests
        name: unit tests
        entry: pytest
        language: system
        args: [-q]
        pass_filenames: false
`)
	cfg, _, err := Detect(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Commands["test"] != "pytest -q" {
		t.Errorf("test = %q, want the local hook's entry and args", cfg.Commands["test"])
	}
}

// A local hook in a NON-system language runs inside an environment pre-commit
// builds, so its entry is not a command warden can execute. Taking it would
// produce the exact broken import this design avoids.
func TestDetect_PreCommit_NonSystemLocalHookIsNotLiftedOut(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".pre-commit-config.yaml", `
repos:
  - repo: local
    hooks:
      - id: unit-tests
        name: unit tests
        entry: pytest
        language: python
        additional_dependencies: [pytest==7.0.0]
`)
	cfg, _, err := Detect(root)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := cfg.Commands["test"]; ok {
		t.Errorf("a python-language hook's entry is not runnable by warden; got test = %q", got)
	}
}

// A Makefile is the more deliberate, project-blessed entrypoint, so it wins over
// a pre-commit config that also defines linting.
func TestDetect_PreCommit_YieldsToHigherPrioritySources(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "Makefile", "lint:\n\tgolangci-lint run\n")
	writeFile(t, root, ".pre-commit-config.yaml", `
repos:
  - repo: https://github.com/psf/black
    rev: 23.1.0
    hooks:
      - id: black
`)
	cfg, _, err := Detect(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Commands["lint"] != "make lint" {
		t.Errorf("lint = %q, want the Makefile target to win", cfg.Commands["lint"])
	}
}

// Every source is best-effort: a malformed or empty config is skipped, never
// fatal, so one broken file cannot deny the user the rest of the import.
func TestDetect_PreCommit_MalformedOrEmptyIsSkipped(t *testing.T) {
	for name, content := range map[string]string{
		"malformed": "repos: [[[not yaml",
		"no hooks":  "repos: []\n",
		"empty":     "",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, ".pre-commit-config.yaml", content)
			cfg, _, err := Detect(root)
			if err != nil {
				t.Fatalf("a bad config must be skipped, not fatal: %v", err)
			}
			if _, ok := cfg.Commands["lint"]; ok {
				t.Errorf("nothing should be imported from %s content", name)
			}
		})
	}
}

// The .yml spelling is as valid as .yaml and must import identically.
func TestDetect_PreCommit_AcceptsYmlSpelling(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, ".pre-commit-config.yml", `
repos:
  - repo: https://github.com/psf/black
    rev: 23.1.0
    hooks:
      - id: black
`)
	cfg, _, err := Detect(root)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Commands["lint"] != "pre-commit run --all-files" {
		t.Errorf("lint = %q for the .yml spelling", cfg.Commands["lint"])
	}
}
