// Package steps holds Warden's built-in step implementations. Each satisfies
// application.Step and confines its side effects to the run's worktree.
package steps

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"go.klarlabs.de/warden/internal/application"
	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/git"
	"go.klarlabs.de/warden/internal/infrastructure/proc"
)

// ShellStep runs a configured shell command (lint, test) in the worktree. A
// zero exit is a pass; any non-zero exit fails the step with the command's
// combined output captured as a finding, so the developer sees exactly why.
type ShellStep struct {
	name domain.StepName
	// cmdKey is the key into StepContext.Commands (e.g. "lint", "test").
	cmdKey string
}

// NewShellStep binds a step name to the command key it runs.
func NewShellStep(name domain.StepName, cmdKey string) ShellStep {
	return ShellStep{name: name, cmdKey: cmdKey}
}

func (s ShellStep) Name() domain.StepName { return s.name }

func (s ShellStep) Run(ctx context.Context, sc application.StepContext) (domain.StepResult, error) {
	command := sc.Commands[s.cmdKey]
	if strings.TrimSpace(command) == "" {
		// No command configured: the step is a no-op pass rather than a hard
		// failure, so a repo can adopt Warden before wiring every command.
		return domain.StepResult{
			Step:    s.name,
			Status:  domain.StepPass,
			Summary: string(s.name) + ": no command configured, skipped",
		}, nil
	}

	out, err, contended := s.runIn(ctx, sc, sc.WorktreeDir, command)
	return s.resultFor(ctx, sc, out, err, contended), nil
}

// resultFor turns a finished command into the step's normalized result. It is
// separate from Run so a step that wraps a shell command with extra
// interpretation (see SecurityScanStep) can still fall back to exactly this
// behavior when its own interpretation is unavailable — one definition of what
// a failing command means, not two that can drift.
func (s ShellStep) resultFor(ctx context.Context, sc application.StepContext, out []byte, err error, contended bool) domain.StepResult {
	if err != nil {
		msg := strings.TrimSpace(string(out))
		summary := string(s.name) + " failed"
		blocker := domain.BlockerNone
		// rule/why/fix carry the machine-readable half of what the prose below
		// already says. The step knows exactly which of the four failure modes it
		// hit and, in the environmental cases, the command that resolves it —
		// leaving that only in the message forces every reader to parse it back
		// out of English.
		var rule, why string
		var fix *domain.Fix
		envFail, missingToolchain := detectEnvFailure(string(out), sc.WorktreeDir)
		switch {
		// A cancelled context means the per-step timeout fired: say so plainly,
		// since the command's own output rarely explains a kill.
		case ctx.Err() == context.DeadlineExceeded:
			msg = string(s.name) + " timed out after " + sc.Timeout.String()
			if trimmed := strings.TrimSpace(string(out)); trimmed != "" {
				msg += "\n" + trimmed
			}
			summary = string(s.name) + " timed out"
			rule = "step/timeout"
			why = "The step exceeded its configured timeout and was killed, so it never reached a " +
				"verdict about your change. Either the command genuinely hangs, or the budget is " +
				"too tight for this repo."
			fix = &domain.Fix{Command: "# raise the budget in .warden.yaml, e.g. timeouts: { " +
				string(s.name) + ": \"10m\" }"}
		// The command never ran: another process still held its lock when the
		// wait budget ran out. The gate still fails — "I could not check" is not
		// "the tree is clean" — but it must not claim the tree is dirty.
		case contended:
			blocker = domain.BlockerContention
			summary = string(s.name) + " could not run (lock contention)"
			msg = string(s.name) + " could not run: another process held its lock for " +
				contentionBudget.String() + ". Nothing is wrong with your tree — wait for the other " +
				"run to finish and retry.\n" + parallelRunnerHint(sc.Commands[s.cmdKey]) + msg
			rule = "step/lock-contention"
			why = "The tool refused to start because another copy of itself holds a machine-global " +
				"lock. That is not a verdict on your tree — the check never ran — and it clears on " +
				"its own once the other run finishes."
			// golangci-lint's lock guards a SHARED cache, and warden already gives
			// each run its own, so the thing the lock protects is protected
			// twice. Naming the flag turns a recurring annoyance into a one-line
			// config change.
			if hint := parallelRunnerHint(sc.Commands[s.cmdKey]); hint != "" {
				fix = &domain.Fix{Command: "# add --allow-parallel-runners to the " +
					string(s.name) + " command in .warden.yaml"}
			}
		// The command never ran either, for the other environmental reason: the
		// executable it needs is not installed. Same rule — fail, but say what is
		// actually wrong, and name the command that fixes it.
		case missingToolchain:
			blocker = domain.BlockerEnvironment
			summary = string(s.name) + " could not run (missing toolchain)"
			msg = envFail.message(string(s.name)) + "\n" + msg
			rule = "step/missing-toolchain"
			why = "The step's executable or dependencies are not present in the validation " +
				"worktree, so the command never ran. Retrying changes nothing until the install " +
				"below has happened."
			// detectEnvFailure already derived the install command from the
			// lockfile actually present, so hand it over as a fix rather than
			// only mentioning it mid-paragraph.
			if envFail.Remediation != "" {
				fix = &domain.Fix{Command: envFail.Remediation}
			}
		}
		return domain.StepResult{
			Step:   s.name,
			Status: domain.StepFail,
			Findings: []domain.Finding{{
				Severity: domain.SeverityHigh,
				Message:  msg,
				Rule:     rule,
				Why:      why,
				Fix:      fix,
			}},
			Summary: summary,
			Blocker: blocker,
		}
	}
	return domain.StepResult{
		Step:    s.name,
		Status:  domain.StepPass,
		Summary: string(s.name) + " passed",
	}
}

// contentionBudget bounds how long a step waits out another process's lock
// before giving up. A contending run is nearly always a linter the developer
// started moments earlier in another terminal (or an editor/LSP integration),
// and those finish well inside a minute — long enough to absorb the common
// case, short enough that a genuinely wedged holder does not silently stall
// every commit. A var so tests can shrink it rather than wait a real minute.
var contentionBudget = 60 * time.Second

// contentionPoll is how long to wait between retries. A tool that refuses on a
// lock exits immediately, so re-probing is cheap and a short interval keeps the
// step responsive the moment the lock frees.
var contentionPoll = 2 * time.Second

// runIn runs command in dir, retrying while the failure is another process
// holding the tool's lock rather than a real finding. It reports the last
// output and error plus whether the run ended still contended.
//
// dir is a parameter rather than always sc.WorktreeDir because a step may need
// to run the same command against a second tree — the security scan does, to
// see what the base commit already reported — and it must do so through the
// identical execution path (same shell, same env, same contention handling) or
// the two results are not comparable.
//
// The gate's job is to answer "is this tree clean", and a tool that declined to
// start has not answered it. Waiting briefly turns the overwhelmingly common
// case — a linter still running in the next terminal — from a spurious red gate
// into a slightly slower green one, without ever converting a genuine failure
// into a pass: only output matching a narrow contention signature is retried,
// and exhausting the budget still fails the step.
func (s ShellStep) runIn(ctx context.Context, sc application.StepContext, dir, command string) (out []byte, err error, contended bool) {
	deadline := time.Now().Add(contentionBudget)
	for attempt := 0; ; attempt++ {
		// Run through the shell so configured commands may use pipes and globs,
		// matching how a developer would run them.
		cmd := exec.CommandContext(ctx, "sh", "-c", command)
		cmd.Dir = dir
		cmd.Env = stepEnv(sc)
		// Run in its own process group so a timeout/cancel kills the command's
		// children (go test, tsc, …), not just the wrapping shell.
		proc.Isolate(cmd)

		out, err = runCaptured(cmd, sc)
		if err == nil || !isContention(string(out)) {
			return out, err, false
		}
		// A cancelled/timed-out context must surface as itself, not as contention.
		if ctx.Err() != nil || time.Now().After(deadline) {
			return out, err, ctx.Err() == nil
		}
		if attempt == 0 && sc.OnOutput != nil {
			// Say why the step appears to hang — once, not on every retry.
			sc.OnOutput("warden: another process holds " + string(s.name) + "'s lock, waiting…")
		}
		select {
		case <-ctx.Done():
			return out, err, false
		case <-time.After(contentionPoll):
		}
	}
}

// scrubbedEnv is the process environment with git's hook variables
// (GIT_INDEX_FILE, GIT_DIR, …) removed. It is the baseline for EVERY subprocess
// a step starts, because every one of them runs with its working directory
// inside the disposable worktree, where `.git` is a gitfile rather than a
// directory. Git exports those variables to hook processes with paths relative
// to the live checkout, so a subprocess that inherits them resolves `.git/index`
// through the gitfile and dies with ENOTDIR — or, worse, quietly reads the live
// repo's index instead of the worktree it was asked to judge (#205).
func scrubbedEnv() []string { return git.ScrubHookEnv(os.Environ()) }

// stepEnv augments the process environment with WARDEN_* variables so a command
// can scope itself to what changed — the primitive for incremental checks. For
// example: `go test $(echo "$WARDEN_CHANGED_FILES" | ...)`. The full change set
// (not just a per-step subset) is exposed; scoping is the command's choice.
func stepEnv(sc application.StepContext) []string {
	env := scrubbedEnv()
	// Pin golangci-lint's cache to a per-worktree dir. golangci caches analysis
	// results keyed to absolute paths; because each run uses a fresh random
	// worktree, a shared cache returns results referencing a deleted worktree
	// path — so `//nolint` directives aren't honored and it reports phantom
	// failures. A per-worktree cache (a deterministic sibling, cleaned by
	// Worktree.Remove) keeps each run's cache self-consistent. Empty
	// WorktreeDir (e.g. a step run outside a worktree) leaves the default.
	if sc.WorktreeDir != "" {
		env = append(env, "GOLANGCI_LINT_CACHE="+sc.WorktreeDir+"-golangci-cache")
	}
	// Redirect compiled-language build caches to a location that survives the
	// worktree, so a gated push does not recompile the world every time. See
	// buildcache.go for why this is a redirection rather than a copy.
	env = append(env, buildCacheEnv(sc.BuildCacheDir, sc.WorktreeDir, env)...)
	env = append(env,
		"WARDEN_HOOK="+sc.Hook.ConfigKey(),
		"WARDEN_BRANCH="+sc.Branch,
		"WARDEN_CHANGED_FILES="+strings.Join(sc.Diff.Paths, "\n"),
		"WARDEN_FILES_TOUCHED="+strconv.Itoa(sc.Diff.FilesTouched),
		"WARDEN_LINES_CHANGED="+strconv.Itoa(sc.Diff.LinesChanged),
	)
	return env
}
