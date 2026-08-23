// Package service is Warden's composition root: it wires the git adapter, the
// axi-backed kernel factory, the built-in step registry, and the pipeline
// Runner into one facade that every delivery surface (CLI, axi, MCP) drives.
package service

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/cache"
	"go.klarlabs.de/warden/internal/infrastructure/config"
	"go.klarlabs.de/warden/internal/infrastructure/detect"
	"go.klarlabs.de/warden/internal/infrastructure/forge"
	"go.klarlabs.de/warden/internal/infrastructure/git"
	"go.klarlabs.de/warden/internal/infrastructure/hooks"
	"go.klarlabs.de/warden/internal/infrastructure/kernel"
	"go.klarlabs.de/warden/internal/infrastructure/sbom"
	"go.klarlabs.de/warden/internal/infrastructure/signing"
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
	signer  provenanceSigner
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
	factory := kernel.NewFactory(steps.Default())
	// Step cache lives under the git dir (per-clone, never committed). A failure
	// to locate it just disables caching.
	if gitDir, err := repo.GitDir(); err == nil {
		factory = factory.WithCache(cache.Open(gitDir))
	}
	gitAdapter := git.NewAdapter(repo)
	runner := &application.Runner{
		Git:      gitAdapter,
		DepDrift: gitAdapter,
		Configs:  configs,
		Kernels:  factory,
		Approver: approver,
		Forge:    gh,
		SBOM:     sbom.New(),
		Settings: application.Settings{Version: version, Remote: DefaultRemote},
	}
	// Provenance signing is best-effort: if the key can't be loaded (e.g. a
	// locked-down home in CI), runs still validate and write unsigned notes —
	// loudly, and refusably via signing.required.
	signer := resolveSigner(repo, configs)
	if signer != nil {
		runner.Signer = signer
	}
	return &Service{repo: repo, configs: configs, forge: gh, runner: runner, signer: signer, version: version, remote: DefaultRemote}, nil
}

// provenanceSigner is a Signer that can also identify itself for display and
// for pinning — what `warden key show` needs on top of the signing operations.
type provenanceSigner interface {
	application.Signer
	Fingerprint() string
}

// resolveSigner picks the signing scheme the repo asked for.
//
// A config that cannot be read, or an SSH signer that cannot be prepared, falls
// back to warden's own key rather than to nothing: an unsigned note is a strictly
// worse outcome than a differently-signed one, and the fallback is announced by
// the run's unsigned/degraded reporting if it ends up producing nothing either.
func resolveSigner(repo *git.Repo, configs application.ConfigRepository) provenanceSigner {
	cfg, err := configs.Load()
	if err != nil || !strings.EqualFold(cfg.Signing.Signer, signerSSH) {
		if s := loadSigner(); s != nil {
			return s
		}
		return nil
	}
	// An empty ssh_key falls back to git's own user.signingkey, which is already
	// set in any repo that signs commits — so the common case needs no
	// warden-specific configuration at all.
	keyPath := cfg.Signing.SSHKey
	if keyPath == "" && repo != nil {
		keyPath = repo.ConfigValue("user.signingkey")
	}
	if s, err := signing.LoadSSH(keyPath); err == nil {
		return s
	}
	if s := loadSigner(); s != nil {
		return s
	}
	return nil
}

// signerSSH is the config value selecting SSH signing.
const signerSSH = "ssh"

// loadSigner loads (or first-time generates) the provenance signing key, or
// returns nil if the key dir is unavailable — signing is optional (§9).
func loadSigner() *signing.Signer {
	dir, err := signing.DefaultDir()
	if err != nil {
		return nil
	}
	s, err := signing.Load(dir)
	if err != nil {
		return nil
	}
	return s
}

// SigningKey returns the machine's provenance public key (base64) and its short
// fingerprint for `warden key show`. Both are empty when signing is unavailable.
func (s *Service) SigningKey() (publicKey, fingerprint string) {
	if s.signer == nil {
		return "", ""
	}
	return s.signer.PublicKey(), s.signer.Fingerprint()
}

// SignPayload signs arbitrary bytes with the machine's provenance key,
// returning the base64 signature and the key's fingerprint.
//
// It exists so `warden attest --sign` can produce a DSSE envelope without the
// CLI reaching inside the service for the signer. The same key that signs
// notes signs the envelope, which is what keeps one trust decision — the
// trusted-signer roster — governing both.
//
// Empty signer is an error rather than an unsigned result: a caller that asked
// for a signature and silently got none would ship an envelope nobody can
// verify.
func (s *Service) SignPayload(payload []byte) (signature, fingerprint string, err error) {
	if s.signer == nil {
		return "", "", fmt.Errorf("no signing key available; run `warden key show` to create one")
	}
	sig, err := s.signer.Sign(payload)
	if err != nil {
		return "", "", err
	}
	return sig, s.signer.Fingerprint(), nil
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

// GitDir returns the repository's git directory, where per-run state (the attach
// socket, the step cache) lives.
func (s *Service) GitDir() (string, error) { return s.repo.GitDir() }

// Config loads the repo's parsed .warden.yaml.
func (s *Service) Config() (domain.Config, error) { return s.configs.Load() }

// TrustedKeysAt returns the trusted_keys roster as committed at ref, resolving
// any extends chain against that same ref. A range gate calls this with its BASE
// so the trusted-signer roster comes from the trusted side of base..head, never
// the head being checked — otherwise a PR could add its own key to .warden.yaml
// and self-certify. A ref with no committed roster yields nil, leaving the gate
// at attested/signed depth (it never fails closed on a missing roster).
func (s *Service) TrustedKeysAt(ref string) ([]string, error) {
	cfg, err := config.ResolveAtRef(func(rel string) ([]byte, bool, error) {
		return s.repo.FileAtRef(ref, rel)
	})
	if err != nil {
		return nil, err
	}
	return cfg.TrustedKeys, nil
}

// ForgeConfigAt reads the forge policy from ref, for exactly the reason
// TrustedKeysAt does: a range gate must never take its trust decisions from the
// head it is inspecting.
//
// This one matters more than the roster, not less. `forge.accept_authored` is
// the setting that lets an un-noted commit pass, so reading it from the head
// would let a pull request turn the gate off in its own first commit and then
// walk through it. Read from the base — the side already trusted — it can only
// be enabled by a change that itself passed the gate.
func (s *Service) ForgeConfigAt(ref string) (domain.ForgeConfig, error) {
	cfg, err := config.ResolveAtRef(func(rel string) ([]byte, bool, error) {
		return s.repo.FileAtRef(ref, rel)
	})
	if err != nil {
		return domain.ForgeConfig{}, err
	}
	return cfg.Forge, nil
}

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

// SetAttestOnly makes the next run gate and attest WITHOUT moving or pushing the
// branch — the CI post-merge mode. See application.Settings.AttestOnly.
func (s *Service) SetAttestOnly(v bool) { s.runner.Settings.AttestOnly = v }

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

// RepinHook rewrites an installed hook's shim so it records the running
// version. It does not touch .warden.yaml.
//
// SetHook would, because it also maintains the hooks selection -- and a repin
// has no business changing which hooks are armed. That coupling had a sharper
// consequence than the redundant write: SetHook installs the shim FIRST and
// updates the config SECOND, so on a repository whose .warden.yaml cannot be
// parsed the pin was already rewritten by the time the config write failed.
// SetHook returned an error, the caller skipped its "repinned X -> Y" line, and
// the pin had moved with nothing said about it.
func (s *Service) RepinHook(hook domain.Hook) error {
	gitDir, err := s.repo.GitDir()
	if err != nil {
		return err
	}
	return hooks.Install(gitDir, []domain.Hook{hook}, s.version)
}

// InstalledHooks reports which hooks currently have a managed shim.
func (s *Service) InstalledHooks() (map[domain.Hook]bool, error) {
	gitDir, err := s.repo.GitDir()
	if err != nil {
		return nil, err
	}
	return hooks.Installed(gitDir), nil
}

// HookPins reports the warden version each installed shim was written at. The
// shims prefer a warden on PATH, so a pin that differs from the running binary
// is skew a status surface should name rather than leave to be discovered in a
// provenance note after the fact.
func (s *Service) HookPins() (map[domain.Hook]string, error) {
	gitDir, err := s.repo.GitDir()
	if err != nil {
		return nil, err
	}
	return hooks.Pinned(gitDir), nil
}

// writeStarterConfig writes a minimal .warden.yaml only when none exists. On an
// existing (user-authored) config it never rewrites the file — it updates just
// the hooks selection in place, preserving comments and formatting. When it does
// write a starter, it detects the project language and pre-fills lint/test
// commands, returning the detected language for reporting.
func (s *Service) writeStarterConfig(selected []domain.Hook) (domain.Language, error) {
	hookCfg := hookConfigFrom(selected)

	// An existing .warden.yaml is user-authored, full stop: leave it untouched
	// except for the hooks selection.
	//
	// This used to infer authorship from the parsed config — "has rules or
	// commands" — which silently misread any policy built on built-in steps. A
	// config setting `steps:` and `trusted_keys:` but no `commands:` looked
	// absent and was overwritten wholesale, resetting the trusted-signer roster
	// among everything else. Whether a file exists is not something to deduce
	// from its contents.
	if s.configs.Exists() {
		return domain.LangUnknown, s.configs.SetHooks(hookCfg)
	}

	// Detect every ecosystem (a monorepo has several) and compose a lint+test
	// command per unit, path-scoped, plus a nox security scan when nox is on
	// PATH. Falls back to empty placeholders for a repo with no recognized
	// ecosystem.
	ecos := detect.Ecosystems(s.repo.Dir)
	var commands map[string]string
	var stepList map[string][]domain.StepName
	if len(ecos) == 0 {
		// No recognized ecosystem: leave placeholders for the author to fill in
		// (and no lone security step).
		commands = map[string]string{"lint": "", "test": ""}
		stepList = map[string][]domain.StepName{
			domain.PreCommit.ConfigKey(): domain.DefaultSteps(domain.PreCommit),
			domain.PrePush.ConfigKey():   domain.DefaultSteps(domain.PrePush),
		}
	} else {
		commands, stepList = domain.ComposeConfig(ecos, noxAvailable())
	}
	cfg := domain.Config{
		Agent:    "auto",
		Hooks:    hookCfg,
		Commands: commands,
		Steps:    stepList,
		Risk:     domain.RiskConfig(domain.DefaultRiskThresholds()),
	}
	return primaryLanguage(ecos), s.configs.Save(cfg)
}

// primaryLanguage names the repo's headline language for the init summary: the
// root ecosystem's language if present, else the first detected.
func primaryLanguage(ecos []domain.Ecosystem) domain.Language {
	for _, e := range ecos {
		if e.Path == "." {
			return e.Lang
		}
	}
	if len(ecos) > 0 {
		return ecos[0].Lang
	}
	return domain.LangUnknown
}

// noxAvailable reports whether the nox security scanner is on PATH, so init only
// wires a security-scan step a user can actually run.
func noxAvailable() bool {
	_, err := exec.LookPath("nox")
	return err == nil
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
