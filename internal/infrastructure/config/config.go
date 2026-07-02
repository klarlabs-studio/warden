// Package config is the infrastructure adapter that persists and loads Warden's
// .warden.yaml. It is the anti-corruption boundary between the on-disk YAML
// representation and the domain Config value object: parsing/serialization
// concerns live here, never in the domain or the policy resolution service.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"

	"go.klarlabs.de/warden/internal/domain"
)

// FileName is the repo-root config file Warden reads.
const FileName = ".warden.yaml"

// Repository loads and saves the domain Config for a single repository root. It
// satisfies the application's ConfigRepository port.
type Repository struct {
	root string
}

// NewRepository binds a config Repository to a repository root directory.
func NewRepository(root string) *Repository { return &Repository{root: root} }

// Load reads and parses .warden.yaml. A missing file is not an error: it yields
// a zero Config so callers fall back to documented defaults.
func (r *Repository) Load() (domain.Config, error) {
	path := filepath.Join(r.root, FileName)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return domain.Config{}, nil
	}
	if err != nil {
		return domain.Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	return Parse(data)
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
// preserving the rest of the file byte-for-byte where possible — comments,
// ordering, and formatting — by editing the YAML node tree in place rather than
// re-serializing the whole domain Config. `warden hooks enable/disable` and
// `init` route through here so toggling a hook never strips a user's inline
// documentation. When no file exists yet it falls back to a minimal Save.
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
