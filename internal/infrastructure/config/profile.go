package config

import (
	"fmt"
	"path/filepath"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
)

// layer is one file of an extends chain as parsed on its own, before merging.
// Keeping it lets a profile error name the file that set the offending value
// instead of only the merged result, which no one wrote.
type layer struct {
	name string
	cfg  domain.Config
}

// displayName shows a config path relative to the repository root when it can,
// since that is how a reader finds the file.
func displayName(root, path string) string {
	if rel, err := filepath.Rel(root, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return path
}

// checkProfileRatchet refuses a child that names a lower profile than the one
// its base resolves to. The merge itself would keep the higher one, which is
// safe, but silent: the author of the child wrote `standard` and would read the
// file as standard while strict applied, or wrote it hoping to relax an org
// floor and would never learn it did nothing. A load error says both.
func checkProfileRatchet(child layer, base domain.Config, baseLayers []layer) error {
	if child.cfg.Profile == "" || base.Profile == "" {
		return nil
	}
	c, err := domain.ParseComplianceProfile(child.cfg.Profile)
	if err != nil {
		return fmt.Errorf("%s: %w", child.name, err)
	}
	b, err := domain.ParseComplianceProfile(base.Profile)
	if err != nil {
		return err
	}
	if !c.Lowers(b) {
		return nil
	}
	return fmt.Errorf("profile %s in %s cannot lower %s inherited from %s",
		child.cfg.Profile, child.name, base.Profile, profileSource(b, baseLayers))
}

// profileSource names the file in a chain that set the profile the chain
// resolved to: the nearest file naming a profile of that rank.
func profileSource(want domain.ComplianceProfile, layers []layer) string {
	for i := range layers {
		l := &layers[i]
		if l.cfg.Profile == "" {
			continue
		}
		p, err := domain.ParseComplianceProfile(l.cfg.Profile)
		if err == nil && p.String() == want.String() {
			return l.name
		}
	}
	return "the extends chain"
}

// applyProfile puts the profile the chain selected into force: its Base() is
// the lowest layer, and its floors are checked against the result. A config
// without a profile is returned untouched, so nothing about it changes.
func applyProfile(cfg domain.Config, layers []layer) (domain.Config, error) {
	if cfg.Profile == "" {
		return cfg, nil
	}
	p, err := domain.ParseComplianceProfile(cfg.Profile)
	if err != nil {
		return domain.Config{}, err
	}
	eff := p.Apply(cfg)
	violations := p.Check(eff)
	if len(violations) == 0 {
		return eff, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "config does not meet profile %s (set in %s):", p, profileSource(p, layers))
	for _, v := range violations {
		fmt.Fprintf(&b, "\n  - %s: %s", v.Field, v.Message)
		if src := setBy(v, layers); src != "" {
			fmt.Fprintf(&b, " (set in %s)", src)
		}
	}
	return domain.Config{}, fmt.Errorf("%s", b.String())
}

// setBy names the nearest file whose own config set a violation's value, or ""
// when the violation is an absence no single file is responsible for.
func setBy(v domain.FloorViolation, layers []layer) string {
	if v.SetBy == nil {
		return ""
	}
	for i := range layers {
		if v.SetBy(layers[i].cfg) {
			return layers[i].name
		}
	}
	return ""
}
