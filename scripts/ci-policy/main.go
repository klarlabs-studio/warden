// ci-policy prepares protected CI expectations from immutable candidate objects.
// Run this tool from reviewed code, never from the candidate checkout.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
	gitrepo "go.klarlabs.de/warden/internal/infrastructure/git"
	"go.klarlabs.de/warden/internal/infrastructure/sbom"
)

type options struct{ repo, template, base, commit, branch string }

func main() {
	if err := runCLI(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runCLI(args []string, output io.Writer) error {
	var o options
	fs := flag.NewFlagSet("ci-policy", flag.ContinueOnError)
	fs.StringVar(&o.repo, "repo", "", "candidate repository (data only)")
	fs.StringVar(&o.template, "template", "", "protected policy template outside candidate checkout")
	fs.StringVar(&o.base, "base", "", "full pre-push comparison base identity")
	fs.StringVar(&o.commit, "commit", "", "full candidate commit identity")
	fs.StringVar(&o.branch, "branch", "", "expected local branch name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected arguments")
	}
	result, err := prepare(o)
	if err != nil {
		return err
	}
	_, err = output.Write(result)
	return err
}

func git(dir string, args ...string) ([]byte, error) {
	cmd := exec.Command("git", append([]string{"--no-replace-objects", "-C", dir}, args...)...)
	cmd.Env = gitrepo.ScrubHookEnv(os.Environ())
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git %s: %w", args[0], err)
	}
	return out, nil
}

func prepare(o options) ([]byte, error) {
	if !domain.ValidTreeSHA(o.base) || !domain.ValidTreeSHA(o.commit) || o.branch == "" {
		return nil, errors.New("explicit base, commit and branch are required")
	}
	root, err := filepath.EvalSymlinks(o.repo)
	if err != nil {
		return nil, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	top, err := git(root, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	root, err = filepath.EvalSymlinks(strings.TrimSpace(string(top)))
	if err != nil {
		return nil, err
	}
	template, err := filepath.EvalSymlinks(o.template)
	if err != nil {
		return nil, err
	}
	template, err = filepath.Abs(template)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, template)
	if err != nil {
		return nil, err
	}
	if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, errors.New("template must be outside candidate checkout")
	}
	for _, id := range []string{o.base, o.commit} {
		typ, e := git(root, "cat-file", "-t", id)
		if e != nil {
			return nil, e
		}
		if strings.TrimSpace(string(typ)) != "commit" {
			return nil, errors.New("base and candidate must be commits")
		}
	}
	data, err := os.ReadFile(template)
	if err != nil {
		return nil, err
	}
	var p domain.CIJobPolicy
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, err
	}
	if err = dec.Decode(new(any)); err != io.EOF {
		return nil, errors.New("trailing policy data")
	}
	// Reject duplicate keys before decoding can silently replace a reviewed value.
	if err := uniqueJSON(data); err != nil {
		return nil, err
	}
	entries, err := git(root, "ls-tree", "-r", "-z", "--full-tree", o.commit)
	if err != nil {
		return nil, err
	}
	inputs := map[string]string{}
	for _, entry := range bytes.Split(entries, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		meta, name, ok := strings.Cut(string(entry), "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 {
			return nil, errors.New("invalid tree listing")
		}
		if fields[0] == "160000" {
			return nil, errors.New("submodules are unsupported")
		}
		if !sbom.IsDependencyManifest(path.Base(name)) {
			continue
		}
		if fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
			return nil, errors.New("manifest must be a regular tracked file")
		}
		content, e := git(root, "cat-file", "blob", fields[2])
		if e != nil {
			return nil, e
		}
		inputs[name] = domain.DigestBytes(content)
	}
	stats, err := (&gitrepo.Repo{Dir: root}).DiffStatsBetween(o.base, o.commit)
	if err != nil {
		return nil, err
	}
	context, err := domain.DigestJSON([]string{
		"WARDEN_HOOK=pre_push", "WARDEN_BRANCH=" + o.branch,
		"WARDEN_CHANGED_FILES=" + strings.Join(stats.Paths, "\n"),
		"WARDEN_FILES_TOUCHED=" + strconv.Itoa(stats.FilesTouched),
		"WARDEN_LINES_CHANGED=" + strconv.Itoa(stats.LinesChanged),
	})
	if err != nil {
		return nil, err
	}
	for _, r := range p.Jobs {
		if r == nil {
			continue
		}
		for name := range r.Steps {
			s := r.Steps[name]
			if s.ContextDigest != "" || s.Inputs != nil {
				return nil, errors.New("template must omit derived context_digest and inputs")
			}
			s.ContextDigest = context
			s.Inputs = inputs
			r.Steps[name] = s
		}
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return json.MarshalIndent(p, "", "  ")
}

// uniqueJSON checks nested objects, including fields not used by the template.
func uniqueJSON(data []byte) error {
	d := json.NewDecoder(bytes.NewReader(data))
	var value func() error
	value = func() error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		seen := map[string]bool{}
		for d.More() {
			if delim == '{' {
				key, e := d.Token()
				if e != nil {
					return e
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errors.New("duplicate policy key")
				}
				seen[name] = true
			}
			if err := value(); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	}
	return value()
}
