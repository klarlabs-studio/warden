// Package config is the infrastructure adapter that persists and loads Warden's
// .warden.yaml. It is the anti-corruption boundary between the on-disk YAML
// representation and the domain Config value object: parsing/serialization
// concerns live here, never in the domain or the policy resolution service.
package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"go.klarlabs.de/warden/internal/domain"
)

// FileName is the repo-root config file Warden reads.
const FileName = ".warden.yaml"

// maxConfigBytes caps a single config file (the root .warden.yaml or any
// extends target). A real Warden policy is a few KiB; the cap defends the
// loader against an accidentally or maliciously huge file exhausting memory.
const maxConfigBytes = 1 << 20 // 1 MiB

// Repository loads and saves the domain Config for a single repository root. It
// satisfies the application's ConfigRepository port.
type Repository struct {
	root string
}

// NewRepository binds a config Repository to a repository root directory.
func NewRepository(root string) *Repository { return &Repository{root: root} }

// Load reads and parses .warden.yaml, resolving any `extends:` chain. A missing
// file is not an error: it yields a zero Config so callers fall back to
// documented defaults.
func (r *Repository) Load() (domain.Config, error) {
	return loadFrom(r.root, filepath.Join(r.root, FileName), nil)
}

// Exists reports whether the repo has a .warden.yaml at all.
//
// Callers must not infer this from a loaded Config: a missing file and a file
// that happens to set only fields the caller does not inspect both parse to the
// same values, and treating the second as absent overwrites a real policy. That
// is exactly how `warden init` used to reset a config that configured `steps:`
// and `trusted_keys:` but no `commands:`.
func (r *Repository) Exists() bool {
	_, err := os.Stat(filepath.Join(r.root, FileName))
	return err == nil
}

// maxExtends bounds the extends chain so a misconfiguration can't loop forever.
const maxExtends = 10

// loadFrom parses the config at path and, if it extends a base, loads that base
// and overlays this config on top. root is the repository root every config in
// the chain must stay within; seen carries the chain so far to detect cycles
// and cap depth.
func loadFrom(root, path string, seen []string) (domain.Config, error) {
	data, err := readConfigFile(path)
	if os.IsNotExist(err) {
		return domain.Config{}, nil
	}
	if err != nil {
		return domain.Config{}, err
	}
	cfg, err := Parse(data)
	if err != nil {
		return domain.Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return domain.Config{}, fmt.Errorf("%s: %w", path, err)
	}
	if cfg.Extends == "" {
		return cfg, nil
	}

	if len(seen) >= maxExtends {
		return domain.Config{}, fmt.Errorf("extends chain too deep (>%d) at %s", maxExtends, path)
	}
	basePath := cfg.Extends
	if !filepath.IsAbs(basePath) {
		basePath = filepath.Join(filepath.Dir(path), basePath)
	}
	basePath = filepath.Clean(basePath)
	// Containment: an extends target — relative or absolute — must resolve
	// inside the repo root. Otherwise a repo .warden.yaml could inherit
	// commands: (which later run via `sh -c`) from an un-versioned file such as
	// `extends: /etc/x.yaml` or `../../shared.yaml`, or read an arbitrary file.
	// Inherited policy must be committed to the repo, not pulled from outside.
	if !within(root, basePath) {
		return domain.Config{}, fmt.Errorf("extends target %s escapes repo root %s", basePath, root)
	}
	chain := make([]string, 0, len(seen)+1)
	chain = append(chain, seen...)
	chain = append(chain, filepath.Clean(path))
	if slices.Contains(chain, basePath) {
		return domain.Config{}, fmt.Errorf("extends cycle: %s already in the chain", basePath)
	}
	base, err := loadFrom(root, basePath, chain)
	if err != nil {
		return domain.Config{}, fmt.Errorf("load extends base %s: %w", basePath, err)
	}
	return cfg.OverlayOnto(base), nil
}

// FileReaderAtRef reads a repo-relative path as committed at some fixed git ref,
// returning found=false when the ref has no such file. It is the seam that lets
// ResolveAtRef walk the same extends chain Load walks, but against committed
// bytes at a ref rather than the working tree.
type FileReaderAtRef func(repoRelPath string) (data []byte, found bool, err error)

// ResolveAtRef resolves the config — including its extends chain — as committed
// at a git ref, using read to fetch each file. It mirrors Load's semantics
// (extends overlay, cycle and depth caps, repo-root containment, the byte cap)
// but in ref-relative POSIX path space, so a range gate can read its
// trusted-signer roster from the trusted base rather than the head it is
// checking. A ref with no root config yields a zero Config.
func ResolveAtRef(read FileReaderAtRef) (domain.Config, error) {
	return resolveAtRef(read, FileName, nil)
}

func resolveAtRef(read FileReaderAtRef, relPath string, seen []string) (domain.Config, error) {
	data, found, err := read(relPath)
	if err != nil {
		return domain.Config{}, err
	}
	if !found {
		return domain.Config{}, nil
	}
	if len(data) > maxConfigBytes {
		return domain.Config{}, fmt.Errorf("read %s at ref: config exceeds %d bytes", relPath, maxConfigBytes)
	}
	cfg, err := Parse(data)
	if err != nil {
		return domain.Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return domain.Config{}, fmt.Errorf("%s: %w", relPath, err)
	}
	if cfg.Extends == "" {
		return cfg, nil
	}
	if len(seen) >= maxExtends {
		return domain.Config{}, fmt.Errorf("extends chain too deep (>%d) at %s", maxExtends, relPath)
	}
	basePath, ok := resolveRelExtends(relPath, cfg.Extends)
	if !ok {
		return domain.Config{}, fmt.Errorf("extends target %q escapes repo root", cfg.Extends)
	}
	chain := make([]string, 0, len(seen)+1)
	chain = append(chain, seen...)
	chain = append(chain, pathpkg.Clean(relPath))
	if slices.Contains(chain, basePath) {
		return domain.Config{}, fmt.Errorf("extends cycle: %s already in the chain", basePath)
	}
	base, err := resolveAtRef(read, basePath, chain)
	if err != nil {
		return domain.Config{}, fmt.Errorf("load extends base %s: %w", basePath, err)
	}
	return cfg.OverlayOnto(base), nil
}

// resolveRelExtends joins a repo-relative extends target against the directory
// of the file that declared it, staying in POSIX repo-relative space. It rejects
// an absolute target or one that escapes the repo root (a leading ".." after
// cleaning) — inherited policy must be committed inside the repo, matching Load's
// containment rule but expressed in ref-path space.
func resolveRelExtends(fromRel, extends string) (string, bool) {
	if pathpkg.IsAbs(extends) {
		return "", false
	}
	joined := pathpkg.Clean(pathpkg.Join(pathpkg.Dir(fromRel), extends))
	if joined == ".." || strings.HasPrefix(joined, "../") {
		return "", false
	}
	return joined, true
}

// within reports whether target is contained within root (root itself or a
// descendant). Both are resolved to absolute, cleaned form first so a lexical
// ".." escape or an absolute path outside the repo is rejected.
func within(root, target string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absTarget)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// readConfigFile reads a config file, capping the read at maxConfigBytes so an
// oversized file can't exhaust memory. A missing file surfaces os.ErrNotExist
// unchanged so the caller can treat it as "no config".
func readConfigFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxConfigBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(data) > maxConfigBytes {
		return nil, fmt.Errorf("read %s: config exceeds %d bytes", path, maxConfigBytes)
	}
	return data, nil
}

// Save serializes cfg to .warden.yaml.
func (r *Repository) Save(cfg domain.Config) error {
	path := filepath.Join(r.root, FileName)
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// SetHooks updates only the hooks selection in an existing .warden.yaml,
// preserving the rest of the file byte-for-byte — comments, ordering, blank
// lines, and comment alignment — so `warden hooks enable/disable` and `init`
// never strip a user's inline documentation. When no file exists yet it falls
// back to a minimal Save.
//
// It tries a byte-level splice of the two hook values first, because editing
// the YAML node tree is NOT enough: yaml.Node round-trips comments but not
// blank lines or intra-line spacing, so re-encoding the document reflows the
// whole file (#134). That churn is cosmetic in effect but not in consequence —
// it dirties the working tree during unrelated work, and the noisiest part of
// the resulting diff lands on trusted_keys, the one block where a reviewer
// should see nothing but real changes. The node encoder remains the fallback
// for the case the splice cannot handle: a key that must be *created*.
func (r *Repository) SetHooks(h domain.HookConfig) error {
	path := filepath.Join(r.root, FileName)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return r.Save(domain.Config{Hooks: h})
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	if len(doc.Content) == 0 {
		return r.Save(domain.Config{Hooks: h})
	}
	root := doc.Content[0]

	if out, ok := spliceHooks(data, root, h); ok {
		if bytes.Equal(out, data) {
			// Already correct. Writing identical bytes would still bump mtime and
			// make the file look touched to every watcher and build cache, so the
			// honest no-op is to not write at all — and re-pinning hooks after an
			// upgrade is exactly this case.
			return nil
		}
		if err := os.WriteFile(path, out, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
		return nil
	}

	hooks := findOrCreateMap(root, "hooks")
	setScalarBool(hooks, "pre_commit", h.PreCommit)
	setScalarBool(hooks, "pre_push", h.PrePush)

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("encode %s: %w", path, err)
	}
	_ = enc.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// spliceHooks rewrites the two hook values directly in the original bytes,
// leaving every other byte exactly as the author wrote it. It reports false
// when the file does not already carry both scalars — creating a key is a
// structural edit, which is the node encoder's job, not a splice's.
func spliceHooks(data []byte, root *yaml.Node, h domain.HookConfig) ([]byte, bool) {
	hooks := mapValue(root, "hooks")
	if hooks == nil || hooks.Kind != yaml.MappingNode {
		return nil, false
	}
	preCommit := mapValue(hooks, "pre_commit")
	prePush := mapValue(hooks, "pre_push")
	if !spliceable(preCommit) || !spliceable(prePush) {
		return nil, false
	}
	// Flow style (`hooks: {pre_commit: true, pre_push: false}`) puts both values
	// on one line, where the first edit invalidates the second's column. Bail
	// rather than reason about the shift; the fallback handles it correctly.
	if preCommit.Line == prePush.Line {
		return nil, false
	}

	lines := splitLinesKeepEnds(data)
	if !replaceScalar(lines, preCommit, strconv.FormatBool(h.PreCommit)) ||
		!replaceScalar(lines, prePush, strconv.FormatBool(h.PrePush)) {
		return nil, false
	}
	return bytes.Join(lines, nil), true
}

// spliceable reports whether n is a plain boolean scalar we can rewrite in
// place. A non-bool tag means the user wrote something we did not expect there,
// and quietly overwriting it would be worse than reflowing the file.
func spliceable(n *yaml.Node) bool {
	return n != nil && n.Kind == yaml.ScalarNode && n.Tag == "!!bool"
}

// replaceScalar swaps n's value token for want within its own line, keeping
// anything before it (indentation, the key) and after it (a trailing comment).
//
// It verifies the computed span actually holds the value the parser reported
// before writing. yaml.Node columns are the parser's view, not a byte offset we
// derived ourselves, so a mismatch means our arithmetic does not apply to this
// file — and a bad splice would corrupt config rather than merely reformat it.
// Reporting false there routes the caller to the safe fallback.
func replaceScalar(lines [][]byte, n *yaml.Node, want string) bool {
	idx := n.Line - 1
	if idx < 0 || idx >= len(lines) {
		return false
	}
	line := lines[idx]
	start, end := n.Column-1, n.Column-1+len(n.Value)
	if start < 0 || end > len(line) || string(line[start:end]) != n.Value {
		return false
	}
	out := make([]byte, 0, len(line)-len(n.Value)+len(want))
	out = append(out, line[:start]...)
	out = append(out, want...)
	out = append(out, line[end:]...)
	lines[idx] = out
	return true
}

// splitLinesKeepEnds splits data into lines that each retain their trailing
// newline, so re-joining them reproduces the input byte-for-byte — including a
// final line with no newline of its own.
func splitLinesKeepEnds(data []byte) [][]byte {
	var out [][]byte
	for len(data) > 0 {
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			return append(out, data)
		}
		out = append(out, data[:i+1])
		data = data[i+1:]
	}
	return out
}

// mapValue returns the value node for key in a mapping node, or nil.
func mapValue(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

// findOrCreateMap returns the mapping value node for key, creating an empty one
// (appended to mapping) when absent.
func findOrCreateMap(mapping *yaml.Node, key string) *yaml.Node {
	if v := mapValue(mapping, key); v != nil {
		return v
	}
	k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	v := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	mapping.Content = append(mapping.Content, k, v)
	return v
}

// setScalarBool sets (or adds) a boolean scalar under key in a mapping node,
// leaving every other node — and its comments — untouched.
func setScalarBool(mapping *yaml.Node, key string, val bool) {
	s := strconv.FormatBool(val)
	if v := mapValue(mapping, key); v != nil {
		v.Kind, v.Tag, v.Value = yaml.ScalarNode, "!!bool", s
		return
	}
	k := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
	v := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: s}
	mapping.Content = append(mapping.Content, k, v)
}

// Parse decodes config bytes, rejecting unknown fields so typos surface early.
// Exported for callers (and tests) that hold raw bytes rather than a file.
func Parse(data []byte) (domain.Config, error) {
	var cfg domain.Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return domain.Config{}, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}
