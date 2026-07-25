package scanner

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Pin is a scanner version CI pins, and the file that pins it.
type Pin struct {
	Version string
	// Source is the repo-relative path of the file the pin was read from, so
	// the failure message can point at the one line to change.
	Source string
	// Key is the workflow key the pin was read from (e.g. "NOX_VERSION").
	Key string
}

// pinKeys are the workflow keys that pin a scanner version. Reading the pin out
// of the workflow — rather than having .warden.yaml restate it — is the whole
// point: two copies of a version number are two things to forget, and the
// forgotten one is what silently invalidates the baseline.
func pinKeys(binary string) []string {
	up := strings.ToUpper(binary)
	low := strings.ToLower(binary)
	return []string{up + "_VERSION", low + "-version", low + "_version"}
}

// workflowDir is where GitHub Actions workflows live.
const workflowDir = ".github/workflows"

// maxWorkflowBytes caps a workflow file read.
const maxWorkflowBytes = 1 << 20

// DiscoverPin finds the scanner version CI pins, by reading the repo's
// workflows. pinFile, when set, is a repo-relative path to the single workflow
// to read; otherwise every workflow is searched.
//
// Three outcomes matter to the caller and they are all different: a pin (gate
// on it), no pin (nothing to compare — stay quiet, most repos do not pin), and
// conflicting pins (two workflows disagreeing is already the drift this check
// exists to catch, reported as an error rather than silently picking one).
func DiscoverPin(ctx context.Context, root, binary, pinFile string) (Pin, bool, error) {
	// A pin_file may point across a repository boundary, for a fleet that pins
	// its scanner once in a shared reusable workflow (#112). The repo still
	// names only WHERE the pin lives; the version itself is never restated.
	if spec, ok := ParseRemoteSpec(pinFile); ok {
		return discoverRemotePin(ctx, spec, binary)
	}

	files, err := workflowFiles(root, pinFile)
	if err != nil {
		return Pin{}, false, err
	}

	var found []Pin
	for _, rel := range files {
		data, err := readCapped(filepath.Join(root, rel), maxWorkflowBytes)
		if err != nil {
			// A workflow warden cannot read is not a reason to fail a push.
			continue
		}
		if pin, ok := pinFromWorkflow(data, binary, rel); ok {
			found = append(found, pin)
		}
	}
	if len(found) == 0 {
		return Pin{}, false, nil
	}

	first := found[0]
	for _, p := range found[1:] {
		if !SameVersion(p.Version, first.Version) {
			return Pin{}, false, fmt.Errorf(
				"the repo pins two different %s versions: %s=%s in %s but %s=%s in %s — "+
					"they cannot both be what CI runs, and the one that is wrong invalidates every baseline entry it scans against",
				binary, first.Key, first.Version, first.Source, p.Key, p.Version, p.Source)
		}
	}
	return first, true, nil
}

// pinFromWorkflow extracts a scanner pin from one workflow's bytes, labeled
// with source for the message that names the line to change. Shared by the
// local and remote paths so "what a pin looks like" has exactly one definition
// — a second parser would drift from the first and disagree silently.
func pinFromWorkflow(data []byte, binary, source string) (Pin, bool) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return Pin{}, false
	}
	for _, v := range findKeys(&doc, pinKeys(binary)) {
		return Pin{Version: v.value, Source: source, Key: v.key}, true
	}
	return Pin{}, false
}

// workflowFiles lists the workflow files to search, newest-sorted for stable
// output. An explicit pinFile is used as given so a missing one is an error the
// operator sees, rather than a check that quietly stops running.
func workflowFiles(root, pinFile string) ([]string, error) {
	if pinFile != "" {
		if _, err := os.Stat(filepath.Join(root, pinFile)); err != nil {
			return nil, fmt.Errorf("security_scan.pin_file %q: %w", pinFile, err)
		}
		return []string{pinFile}, nil
	}
	entries, err := os.ReadDir(filepath.Join(root, workflowDir))
	if err != nil {
		return nil, nil // no workflows: nothing pins anything
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if ext := filepath.Ext(e.Name()); ext != ".yml" && ext != ".yaml" {
			continue
		}
		out = append(out, filepath.ToSlash(filepath.Join(workflowDir, e.Name())))
	}
	sort.Strings(out)
	return out, nil
}

type pinHit struct{ key, value string }

// scalarOrDefault reads a matched key's version, from either shape a workflow
// writes one in:
//
//	NOX_VERSION: "1.16.1"          # env / with — the value IS the version
//	nox-version:                   # a reusable workflow's own input declaration
//	  description: "…"
//	  default: "1.16.1"            # …where the version is the default
//
// The second shape is the whole point of a centrally-pinned fleet: the repo
// that DEFINES the shared workflow states the version as an input default, and
// every caller inherits it without restating it. Reading only scalars meant
// warden could see a caller that overrode the pin but never the definition that
// set it — so a fleet doing the recommended thing had no discoverable pin at
// all (#112).
func scalarOrDefault(v *yaml.Node) string {
	if v == nil {
		return ""
	}
	if v.Kind == yaml.ScalarNode {
		return v.Value
	}
	if v.Kind != yaml.MappingNode {
		return ""
	}
	for i := 0; i+1 < len(v.Content); i += 2 {
		if v.Content[i].Value == "default" && v.Content[i+1].Kind == yaml.ScalarNode {
			return v.Content[i+1].Value
		}
	}
	return ""
}

// findKeys walks a YAML document for mapping keys matching any of keys and
// returns their scalar values. It walks the whole tree rather than looking at a
// fixed path because a pin is written in a `env:` block at workflow, job or
// step level, or passed as a reusable-workflow `with:` input, and all of them
// are the same fact.
func findKeys(n *yaml.Node, keys []string) []pinHit {
	if n == nil {
		return nil
	}
	var out []pinHit
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if matchesKey(k.Value, keys) {
				if version := normalizeVersion(scalarOrDefault(v)); version != "" {
					out = append(out, pinHit{key: k.Value, value: version})
				}
			}
			out = append(out, findKeys(v, keys)...)
		}
		return out
	}
	for _, c := range n.Content {
		out = append(out, findKeys(c, keys)...)
	}
	return out
}

func matchesKey(got string, keys []string) bool {
	for _, k := range keys {
		if strings.EqualFold(got, k) {
			return true
		}
	}
	return false
}

// versionRe pulls a dotted version out of a string, so `v1.15.0`, `1.15.0` and
// a `go install .../cli@v1.15.0` line all yield the same answer.
var versionRe = regexp.MustCompile(`\d+(?:\.\d+)+(?:[-+][0-9A-Za-z.-]+)?`)

// normalizeVersion reduces a pin or a `--version` line to a bare dotted
// version, or "" when there is none. A pin of "latest" normalizes to "": it
// names no version, so there is nothing to compare and the check stays quiet
// rather than failing every push on a string mismatch.
func normalizeVersion(s string) string {
	return versionRe.FindString(s)
}

// SameVersion compares two version strings after normalization, so "v1.15.0",
// "1.15.0" and "nox 1.15.0 (commit: …)" all agree.
func SameVersion(a, b string) bool {
	na, nb := normalizeVersion(a), normalizeVersion(b)
	return na != "" && na == nb
}

// LocalVersion asks the scanner on PATH what version it is. It returns "" with
// no error when the scanner is absent or does not answer — warden then has
// nothing to compare and skips the check, rather than failing a push because a
// tool it does not require is missing.
func LocalVersion(ctx context.Context, dir, binary string) string {
	if _, err := exec.LookPath(binary); err != nil {
		return ""
	}
	cmd := exec.CommandContext(ctx, binary, "version")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return normalizeVersion(string(out))
}
