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
