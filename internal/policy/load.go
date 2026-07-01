package policy

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"go.klarlabs.de/warden/internal/domain"
)

// ConfigFileName is the repo-root config file Warden reads.
const ConfigFileName = ".warden.yaml"

// Load reads and parses the .warden.yaml at repoRoot. A missing file is not an
// error: it returns a zero Config so callers can fall back to defaults.
func Load(repoRoot string) (domain.Config, error) {
	path := filepath.Join(repoRoot, ConfigFileName)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return domain.Config{}, nil
	}
	if err != nil {
		return domain.Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	return Parse(data)
}

// Parse decodes config bytes, rejecting unknown fields so typos surface early.
func Parse(data []byte) (domain.Config, error) {
	var cfg domain.Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return domain.Config{}, fmt.Errorf("parse config: %w", err)
	}
	return cfg, nil
}

// Save writes cfg to the .warden.yaml at repoRoot.
func Save(repoRoot string, cfg domain.Config) error {
	path := filepath.Join(repoRoot, ConfigFileName)
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
