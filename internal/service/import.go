package service

import (
	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/importer"
)

// ImportConfig detects the lint/test/security automation the repo already has
// and returns a starter Config plus notes describing what was found. It is the
// engine behind `warden import`: a dry run (write=false) only reports, while
// write=true persists the detected config to .warden.yaml — but only when
// something was actually detected, so an empty repo never clobbers or creates a
// hollow config file. Persisting reuses the same config Repository the rest of
// the service writes through, keeping serialization in one place.
func (s *Service) ImportConfig(write bool) (domain.Config, []string, error) {
	cfg, notes, err := importer.Detect(s.repo.Dir)
	if err != nil {
		return domain.Config{}, nil, err
	}
	if write && len(cfg.Commands) > 0 {
		if err := s.configs.Save(cfg); err != nil {
			return domain.Config{}, notes, err
		}
	}
	return cfg, notes, nil
}
