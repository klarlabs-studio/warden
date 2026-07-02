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
	"go.klarlabs.de/warden/internal/infrastructure/detect"
	"go.klarlabs.de/warden/internal/infrastructure/forge"
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
	forge   *forge.GH
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
	gh := forge.NewGH(repo.Dir)
	runner := &application.Runner{
		Git:      git.NewAdapter(repo),
		Configs:  configs,
		Kernels:  kernel.NewFactory(steps.Default()),
		Approver: approver,
		Forge:    gh,
		Settings: application.Settings{Version: version, Remote: DefaultRemote},
	}
	return &Service{repo: repo, configs: configs, forge: gh, runner: runner, version: version, remote: DefaultRemote}, nil
}

// CIStatus reports the CI check status for a branch's pull request (branch ""
// = current). Used by `warden ci`.
func (s *Service) CIStatus(ctx context.Context, branch string) (domain.CIStatus, error) {
	if !s.forge.Available() {
		return domain.CIStatus{}, fmt.Errorf("gh CLI not found on PATH; install it to query CI status")
	}
	if branch == "" {
		b, err := s.repo.CurrentBranch()
		if err != nil {
			return domain.CIStatus{}, err
		}
		branch = b
	}
	return s.forge.Checks(ctx, branch)
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

// SetObserver attaches a step-progress observer for the next run (used by the
// interactive TUI). Runs are sequential, so setting it on the shared runner is
// safe.
func (s *Service) SetObserver(o application.Observer) { s.runner.Observer = o }

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
// records the adoption point (§9). It returns the project language detected when
// writing a starter config (LangUnknown when a config already existed or none
// was recognized), so the caller can report what it pre-filled.
func (s *Service) Init(selected []domain.Hook) (domain.Language, error) {
	gitDir, err := s.repo.GitDir()
	if err != nil {
		return domain.LangUnknown, err
	}
	if err := hooks.Install(gitDir, selected, s.version); err != nil {
		return domain.LangUnknown, err
	}
	lang, err := s.writeStarterConfig(selected)
	if err != nil {
		return domain.LangUnknown, err
	}
	head, err := s.repo.HeadSHA()
	if err != nil {
		return domain.LangUnknown, fmt.Errorf("read HEAD for adoption point: %w", err)
	}
	return lang, s.repo.WriteAdoption(head)
}

// SetHook enables or disables a single hook after init, updating both the
// installed shim and the recorded selection in .warden.yaml.
func (s *Service) SetHook(hook domain.Hook, enabled bool) error {
	gitDir, err := s.repo.GitDir()
	if err != nil {
		return err
	}
	if enabled {
		if err := hooks.Install(gitDir, []domain.Hook{hook}, s.version); err != nil {
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
	// Update only the hooks selection in place so the user's config comments
	// and formatting survive the toggle.
	return s.configs.SetHooks(cfg.Hooks)
}

// InstalledHooks reports which hooks currently have a managed shim.
func (s *Service) InstalledHooks() (map[domain.Hook]bool, error) {
	gitDir, err := s.repo.GitDir()
	if err != nil {
		return nil, err
	}
	return hooks.Installed(gitDir), nil
}

// writeStarterConfig writes a minimal .warden.yaml only when none exists. On an
// existing (user-authored) config it never rewrites the file — it updates just
// the hooks selection in place, preserving comments and formatting. When it does
// write a starter, it detects the project language and pre-fills lint/test
// commands, returning the detected language for reporting.
func (s *Service) writeStarterConfig(selected []domain.Hook) (domain.Language, error) {
	existing, err := s.Config()
	if err != nil {
		return domain.LangUnknown, err
	}
	hookCfg := hookConfigFrom(selected)

	// A config with any rules or commands is considered user-authored: leave it
	// untouched except for the hooks selection.
	if len(existing.Rules) > 0 || len(existing.Commands) > 0 {
		return domain.LangUnknown, s.configs.SetHooks(hookCfg)
	}

	lang := detect.Language(s.repo.Dir)
	commands := domain.LanguageCommands(lang)
	if commands == nil {
		// Unknown project: leave placeholders for the author to fill in.
		commands = map[string]string{"lint": "", "test": ""}
	}
	cfg := domain.Config{
		Agent:    "auto",
		Hooks:    hookCfg,
		Commands: commands,
		Steps: map[string][]domain.StepName{
			domain.PreCommit.ConfigKey(): domain.DefaultSteps(domain.PreCommit),
			domain.PrePush.ConfigKey():   domain.DefaultSteps(domain.PrePush),
		},
		Risk: domain.RiskConfig(domain.DefaultRiskThresholds()),
	}
	return lang, s.configs.Save(cfg)
}

// hookConfigFrom turns a hook selection list into a HookConfig.
func hookConfigFrom(selected []domain.Hook) domain.HookConfig {
	var h domain.HookConfig
	for _, hook := range selected {
		switch hook {
		case domain.PreCommit:
			h.PreCommit = true
		case domain.PrePush:
			h.PrePush = true
		}
	}
	return h
}
