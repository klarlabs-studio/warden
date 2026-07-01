// Package service is Warden's composition root: it wires the git adapter, the
// axi-backed kernel factory, the built-in step registry, and the pipeline
// Runner into one facade that every delivery surface (CLI, axi, MCP) drives.
package service

import (
	"context"
	"fmt"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/config"
	"go.klarlabs.de/warden/internal/infrastructure/git"
	"go.klarlabs.de/warden/internal/infrastructure/hooks"
	"go.klarlabs.de/warden/internal/infrastructure/kernel"
	"go.klarlabs.de/warden/internal/infrastructure/steps"
	"go.klarlabs.de/warden/internal/policy"
)

// DefaultRemote is the git remote Warden pushes to when config does not say.
const DefaultRemote = "origin"

// Service is the wired facade. Construct it once per command with New.
type Service struct {
	repo    *git.Repo
	configs *config.Repository
	runner  *application.Runner
	version string
	remote  string
}

// New opens the repository containing startDir and wires the full pipeline.
// approver decides the run's approval gate; delivery layers pass an
// implementation suited to their context (terminal prompt, programmatic, …).
func New(startDir, version string, approver application.Approver) (*Service, error) {
	repo, err := git.Open(startDir)
	if err != nil {
		return nil, err
	}
	configs := config.NewRepository(repo.Dir)
	runner := &application.Runner{
		Git:      git.NewAdapter(repo),
		Configs:  configs,
		Kernels:  kernel.NewFactory(steps.Default()),
		Approver: approver,
		Settings: application.Settings{Version: version, Remote: DefaultRemote},
	}
	return &Service{repo: repo, configs: configs, runner: runner, version: version, remote: DefaultRemote}, nil
}

// Repo exposes the underlying repository for git-native surfaces (doctor).
func (s *Service) Repo() *git.Repo { return s.repo }

// Config loads the repo's parsed .warden.yaml.
func (s *Service) Config() (domain.Config, error) { return s.configs.Load() }

// Explain resolves the effective policy for a hypothetical invocation, using
// real diff stats when the invocation matches the current worktree and a
// zero-diff otherwise (so `policy explain --branch other` still works).
func (s *Service) Explain(hook domain.Hook, branch string, paths []string) (domain.ResolvedPolicy, error) {
	cfg, err := s.Config()
	if err != nil {
		return domain.ResolvedPolicy{}, err
	}
	if branch == "" {
		if branch, err = s.repo.CurrentBranch(); err != nil {
			return domain.ResolvedPolicy{}, err
		}
	}
	diff := domain.DiffStats{Paths: paths}
	risk := cfg.Risk.Thresholds().Classify(diff)
	resolved := policy.Resolve(cfg, policy.Input{Hook: hook, Branch: branch, Paths: paths, Risk: risk})
	resolved.Commands = cfg.Commands
	return resolved, nil
}

// Run drives a hook invocation end to end.
func (s *Service) Run(ctx context.Context, hook domain.Hook) (application.RunResult, error) {
	return s.runner.Run(ctx, hook)
}

// StepsList returns the configured (or default) step subset for each hook.
func (s *Service) StepsList() (preCommit, prePush []domain.StepName, err error) {
	cfg, err := s.Config()
	if err != nil {
		return nil, nil, err
	}
	return hookSteps(cfg, domain.PreCommit), hookSteps(cfg, domain.PrePush), nil
}

// hookSteps resolves a hook's configured step list, falling back to the default.
func hookSteps(cfg domain.Config, hook domain.Hook) []domain.StepName {
	if cfg.Steps != nil {
		if s, ok := cfg.Steps[hook.ConfigKey()]; ok {
			return s
		}
	}
	return domain.DefaultSteps(hook)
}

// ApplyFixPatch re-applies a pre-commit auto-fix patch to the live working tree.
func (s *Service) ApplyFixPatch(patch string) error { return s.repo.ApplyPatch(patch) }

// Init installs the selected hooks, writes a starter config if absent, and
// records the adoption point (§9).
func (s *Service) Init(selected []domain.Hook) error {
	gitDir, err := s.repo.GitDir()
	if err != nil {
		return err
	}
	if err := hooks.Install(gitDir, selected); err != nil {
		return err
	}
	if err := s.writeStarterConfig(selected); err != nil {
		return err
	}
	head, err := s.repo.HeadSHA()
	if err != nil {
		return fmt.Errorf("read HEAD for adoption point: %w", err)
	}
	return s.repo.WriteAdoption(head)
}

// SetHook enables or disables a single hook after init, updating both the
// installed shim and the recorded selection in .warden.yaml.
func (s *Service) SetHook(hook domain.Hook, enabled bool) error {
	gitDir, err := s.repo.GitDir()
	if err != nil {
		return err
	}
	if enabled {
		if err := hooks.Install(gitDir, []domain.Hook{hook}); err != nil {
			return err
		}
	} else if err := hooks.Remove(gitDir, hook); err != nil {
		return err
	}

	cfg, err := s.Config()
	if err != nil {
		return err
	}
	switch hook {
	case domain.PreCommit:
		cfg.Hooks.PreCommit = enabled
	case domain.PrePush:
		cfg.Hooks.PrePush = enabled
	}
	return s.configs.Save(cfg)
}

// InstalledHooks reports which hooks currently have a managed shim.
func (s *Service) InstalledHooks() (map[domain.Hook]bool, error) {
	gitDir, err := s.repo.GitDir()
	if err != nil {
		return nil, err
	}
	return hooks.Installed(gitDir), nil
}

// writeStarterConfig writes a minimal .warden.yaml only when none exists, so a
// second init never clobbers a tuned policy.
func (s *Service) writeStarterConfig(selected []domain.Hook) error {
	existing, err := s.Config()
	if err != nil {
		return err
	}
	// A config with any rules or commands is considered user-authored.
	if len(existing.Rules) > 0 || len(existing.Commands) > 0 {
		// Still sync the hook selection so it reflects what was installed.
		return s.syncHookSelection(existing, selected)
	}
	cfg := domain.Config{
		Agent:    "auto",
		Commands: map[string]string{"lint": "", "test": ""},
		Steps: map[string][]domain.StepName{
			domain.PreCommit.ConfigKey(): domain.DefaultSteps(domain.PreCommit),
			domain.PrePush.ConfigKey():   domain.DefaultSteps(domain.PrePush),
		},
		Risk: domain.RiskConfig(domain.DefaultRiskThresholds()),
	}
	return s.syncHookSelection(cfg, selected)
}

func (s *Service) syncHookSelection(cfg domain.Config, selected []domain.Hook) error {
	cfg.Hooks = domain.HookConfig{}
	for _, h := range selected {
		switch h {
		case domain.PreCommit:
			cfg.Hooks.PreCommit = true
		case domain.PrePush:
			cfg.Hooks.PrePush = true
		}
	}
	return s.configs.Save(cfg)
}
