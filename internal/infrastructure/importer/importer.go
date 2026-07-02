// Package importer bootstraps a Warden config from the automation a repo
// already carries. Adopting Warden should be one command, not a hand-written
// .warden.yaml, so Detect inspects the conventional places a project keeps its
// lint/test/security commands — Makefile, package.json, lefthook, GitHub
// workflows — and maps what it finds onto Warden's built-in steps. It is pure
// parsing infrastructure: it reads files and returns a domain.Config plus
// human-readable notes; it never writes or executes anything.
package importer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"go.klarlabs.de/warden/internal/domain"
)

// stepSecurityScan is the custom step Warden inserts when a repo already runs a
// security/audit command. It is not a built-in — the shell command backs it.
const stepSecurityScan domain.StepName = "security-scan"

// Detect inspects the repo root and returns a starter Config together with
// notes describing what was imported. It is best-effort: every source is
// optional and a missing or malformed file is skipped rather than failing the
// whole detection. When several sources define the same command the higher
// priority one wins — Makefile > package.json > lefthook > workflows — because a
// Makefile target is the most deliberate, project-blessed entrypoint and a
// workflow `run:` line is the most heuristic. When nothing is found the returned
// Config is the zero value and the notes say so, so callers can tell the
// difference between "imported nothing" and "imported an empty config".
func Detect(root string) (domain.Config, []string, error) {
	c := &collector{commands: map[string]string{}}

	// Ordered by descending priority: the first source to define a command
	// keeps it, so later sources only fill gaps.
	c.fromMakefile(root)
	c.fromPackageJSON(root)
	c.fromLefthook(root)
	c.fromWorkflows(root)

	if len(c.commands) == 0 {
		return domain.Config{}, []string{
			"no lint/test/security commands detected in Makefile, package.json, lefthook, or GitHub workflows — nothing imported",
		}, nil
	}

	// pre-push mirrors the default sequence but trimmed to the steps we can
	// actually back with a command, with security-scan inserted only when found.
	prePush := []domain.StepName{domain.StepRebase, domain.StepLint}
	if c.securityFound {
		prePush = append(prePush, stepSecurityScan)
	}
	prePush = append(prePush, domain.StepTest)

	cfg := domain.Config{
		Agent:    "auto",
		Hooks:    domain.HookConfig{PreCommit: true, PrePush: false},
		Commands: c.commands,
		Steps: map[string][]domain.StepName{
			domain.PreCommit.ConfigKey(): {domain.StepLint},
			domain.PrePush.ConfigKey():   prePush,
		},
		Risk: domain.RiskConfig(domain.DefaultRiskThresholds()),
	}
	return cfg, c.notes, nil
}

// collector accumulates commands and notes across sources, enforcing the
// first-writer-wins priority via setCommand.
type collector struct {
	commands      map[string]string
	notes         []string
	securityFound bool
}

// setCommand records cmd=val the first time cmd is seen, keeping the earlier
// (higher-priority) source and noting where it came from.
func (c *collector) setCommand(cmd, val, note string) {
	if val == "" {
		return
	}
	if _, ok := c.commands[cmd]; ok {
		return
	}
	c.commands[cmd] = val
	c.notes = append(c.notes, note)
	if cmd == string(stepSecurityScan) {
		c.securityFound = true
	}
}

// --- Makefile -------------------------------------------------------------

// makefileTargetRE matches a rule's target name at the start of a line, e.g.
// "lint:" or "security-scan: deps". It deliberately ignores indented recipe
// lines and variable assignments (a "=" immediately after the name).
var makefileTargetRE = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9_-]*)\s*:([^=]|$)`)

// fromMakefile maps well-known Makefile targets to commands. A dedicated "lint"
// target is preferred over the finer-grained fmt/vet/format/check ones, since a
// project that has both usually means "lint" to be the umbrella.
func (c *collector) fromMakefile(root string) {
	data, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		return
	}
	targets := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" || line[0] == '\t' || line[0] == ' ' || line[0] == '.' {
			continue
		}
		if m := makefileTargetRE.FindStringSubmatch(line); m != nil {
			targets[m[1]] = true
		}
	}

	if t := firstPresent(targets, "lint", "vet", "fmt", "format", "check"); t != "" {
		c.setCommand("lint", "make "+t, "Makefile: lint <- target '"+t+"'")
	}
	if targets["test"] {
		c.setCommand("test", "make test", "Makefile: test <- target 'test'")
	}
	if t := firstPresent(targets, "security", "audit", "sec", "scan"); t != "" {
		c.setCommand(string(stepSecurityScan), "make "+t, "Makefile: security-scan <- target '"+t+"'")
	}
}

// firstPresent returns the first candidate that exists in set, or "".
func firstPresent(set map[string]bool, candidates ...string) string {
	for _, c := range candidates {
		if set[c] {
			return c
		}
	}
	return ""
}

// --- package.json ---------------------------------------------------------

// fromPackageJSON reads the npm scripts block. We record the canonical npm
// invocation (npm run lint / npm test / npm audit) rather than the script body,
// since that is the stable entrypoint a contributor already runs.
func (c *collector) fromPackageJSON(root string) {
	data, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return
	}
	if _, ok := pkg.Scripts["lint"]; ok {
		c.setCommand("lint", "npm run lint", "package.json: lint <- scripts.lint")
	}
	if _, ok := pkg.Scripts["test"]; ok {
		c.setCommand("test", "npm test", "package.json: test <- scripts.test")
	}
	if _, ok := pkg.Scripts["audit"]; ok {
		c.setCommand(string(stepSecurityScan), "npm audit", "package.json: security-scan <- scripts.audit")
	}
}

// --- lefthook -------------------------------------------------------------

// lefthookFile mirrors the subset of lefthook.yml we care about: the run: line
// of each named command under the pre-commit and pre-push hooks.
type lefthookFile struct {
	PreCommit lefthookHook `yaml:"pre-commit"`
	PrePush   lefthookHook `yaml:"pre-push"`
}

type lefthookHook struct {
	Commands map[string]struct {
		Run string `yaml:"run"`
	} `yaml:"commands"`
}

// fromLefthook maps lefthook commands to Warden commands by the command's own
// name (a command called "lint" backs lint, "test" backs test, etc.), keeping
// the repo's exact run: string so behavior is preserved.
func (c *collector) fromLefthook(root string) {
	var data []byte
	var err error
	for _, name := range []string{"lefthook.yml", "lefthook.yaml"} {
		if data, err = os.ReadFile(filepath.Join(root, name)); err == nil {
			break
		}
	}
	if err != nil {
		return
	}
	var lf lefthookFile
	if err := yaml.Unmarshal(data, &lf); err != nil {
		return
	}
	for _, hook := range []lefthookHook{lf.PreCommit, lf.PrePush} {
		// Iterate command names deterministically so notes/results are stable.
		names := make([]string, 0, len(hook.Commands))
		for name := range hook.Commands {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			run := strings.TrimSpace(hook.Commands[name].Run)
			switch strings.ToLower(name) {
			case "lint":
				c.setCommand("lint", run, "lefthook: lint <- command '"+name+"'")
			case "test":
				c.setCommand("test", run, "lefthook: test <- command '"+name+"'")
			case "security", "audit", "sec", "scan":
				c.setCommand(string(stepSecurityScan), run, "lefthook: security-scan <- command '"+name+"'")
			}
		}
	}
}

// --- GitHub workflows -----------------------------------------------------

// lintHints and testHints are substrings that flag a workflow run: line as a
// lint or test step. This is a heuristic — the notes it produces say so.
var (
	lintHints = []string{"golangci", "eslint", "clippy", "ruff", "lint"}
	testHints = []string{"pytest", "go test", "cargo test", "npm test", "test"}
)

// fromWorkflows scans .github/workflows/*.yml for run: steps and takes the first
// line that looks like a lint or a test command. Workflows are the lowest
// priority source because a run: line is the least explicit signal of intent.
func (c *collector) fromWorkflows(root string) {
	dir := filepath.Join(root, ".github", "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	files := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if ext := strings.ToLower(filepath.Ext(e.Name())); ext == ".yml" || ext == ".yaml" {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files) // deterministic "first match" across files

	var lint, test string
	for _, name := range files {
		if lint != "" && test != "" {
			break
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var node any
		if err := yaml.Unmarshal(data, &node); err != nil {
			continue
		}
		for _, run := range collectRunStrings(node) {
			if lint == "" && matchesHint(run, lintHints) {
				lint = firstLine(run)
			}
			if test == "" && matchesHint(run, testHints) {
				test = firstLine(run)
			}
		}
	}
	c.setCommand("lint", lint, "workflows: lint <- run step (heuristic, best-effort — verify)")
	c.setCommand("test", test, "workflows: test <- run step (heuristic, best-effort — verify)")
}

// collectRunStrings walks a decoded YAML tree and returns every string value
// bound to a "run" key, in document order. This copes with the arbitrary
// nesting of jobs.<id>.steps[].run without modeling the whole workflow schema.
func collectRunStrings(node any) []string {
	var out []string
	switch v := node.(type) {
	case map[string]any:
		for key, val := range v {
			if key == "run" {
				if s, ok := val.(string); ok {
					out = append(out, s)
				}
			}
			out = append(out, collectRunStrings(val)...)
		}
	case []any:
		for _, item := range v {
			out = append(out, collectRunStrings(item)...)
		}
	}
	return out
}

// matchesHint reports whether run contains any hint substring (case-insensitive).
func matchesHint(run string, hints []string) bool {
	low := strings.ToLower(run)
	for _, h := range hints {
		if strings.Contains(low, h) {
			return true
		}
	}
	return false
}

// firstLine returns the first non-empty trimmed line of a (possibly multi-line)
// run: block, which is the actual command in the common single-command case.
func firstLine(run string) string {
	for _, line := range strings.Split(run, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return strings.TrimSpace(run)
}
