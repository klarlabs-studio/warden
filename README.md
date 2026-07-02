# warden

A configurable git commit/push gate installed as **native git hooks** — `git commit` and `git push` themselves are the gated commands, no second remote and no changed muscle memory.

Warden runs a policy-driven pipeline (lint, test, review, …) in a **disposable worktree** so a run never touches your live checkout, then fast-forwards your real branch and performs the push itself once everything passes. Policy is a set of stacking **rules** (match on branch, path glob, and a risk heuristic → overrides), and the pipeline is extensible with a typed subprocess SDK.

Built on [`axi-go`](https://go.klarlabs.de/axi) (execution kernel — typed actions, effect-gated approval, tamper-evident evidence chain), [`fortify`](https://go.klarlabs.de/fortify) (resilience), [`statekit`](https://go.klarlabs.de/statekit) (policy visualization), and [`mcp-go`](https://go.klarlabs.de/mcp) (MCP surface).

## Install

```bash
go install go.klarlabs.de/warden@latest
```

## Quick start

```bash
cd your-repo
warden init                      # installs pre-commit + pre-push hooks, writes .warden.yaml,
                                 # records an adoption point at HEAD
warden policy explain            # print the resolved effective policy for a hypothetical push
```

From then on `git commit` / `git push` are gated. Warden's own push runs with
`--no-verify` so it never re-triggers the hook and recurses.

## How it works

- **pre-commit** (fast, local): seeds a worktree from `HEAD` + staged changes,
  runs the fast step subset (default: `lint`), and re-applies any auto-fixes to
  your working tree. Passes → the commit proceeds.
- **pre-push** (full pipeline): seeds a worktree from the branch tip, runs the
  resolved pipeline (`intent → rebase → review → test → document → lint`),
  pauses at an approval gate when a rule requires it, then **fast-forwards your
  local branch and pushes itself**, writing a hash-chained provenance note under
  `refs/notes/warden` for each validated commit. If the branch moved mid-run the
  fast-forward is aborted, never forced.

The pre-push hook always exits non-zero on success — Warden already performed
the push, so git's own (now-stale) push must be stopped from racing it.

## Configuration (`.warden.yaml`)

```yaml
agent: auto
hooks: { pre_commit: true, pre_push: true }
commands:
  lint: "golangci-lint run ./..."
  test: "go test -race ./..."
# Agent steps (intent/review/document) run the command configured for the
# resolved agent, expanding {prompt}/{step}/{repo}. No command → advisory skip;
# Warden never guesses an agent's CLI.
agent_commands:
  claude: "claude -p {prompt}"
  codex: "codex exec {prompt}"
steps:
  pre_commit: [lint]
  pre_push: [intent, rebase, review, test, document, lint]
risk: { diff_lines_high: 400, files_touched_high: 15 }
rules:
  - match: { branch: main }
    then: { require_approval: true, auto_fix: { test: 1 } }
  - match: { paths: ["security/**", "auth/**"] }
    then:
      agent: { review: codex }
      steps: { pre_push: { insert_after: lint, add: [security-scan] } }
  - match: { risk: high }
    then: { require_approval: true, agent: { review: claude } }
```

All matching rules stack: per field the most specific wins (ties broken by
declaration order); step `add`/`skip` are unioned. `warden policy explain`
prints the result — the intended mitigation for a rule that misconfigures the
gate.

## Commands

| Command | Description |
|---|---|
| `warden init [--hooks=pre-commit,pre-push]` | initialize, install hooks, record adoption point |
| `warden hooks enable\|disable <hook>` | change hook selection |
| `warden run <pre-commit\|pre-push>` | run the gate (invoked by the hook shims) |
| `warden policy explain [--hook h] [--branch b] [--paths glob,...] [--chart]` | print resolved policy (or an XState statechart) |
| `warden steps list` | list built-in + custom steps by hook |
| `warden doctor [--branch b]` | audit which commits since adoption carry a validation note |
| `warden axi <verb>` | flags-only agent surface, TOON output |
| `warden mcp serve` | MCP server over stdio |

## Custom steps

Two ways, easy first.

### 1. A command (no code)

Give a step a name and a command. Any step name with a `commands.<name>` entry
runs that command in the worktree; a non-zero exit fails the gate. This is the
common case — a custom check is just a command you already run.

```yaml
commands:
  security-scan: "nox scan . -severity-threshold high"
steps:
  pre_push: [rebase, lint, security-scan, test]
```

### 2. A subprocess step (structured findings)

When a step needs to return per-file findings, request approval, or react to
earlier steps' findings, write a small program that speaks the JSON wire
protocol over stdin/stdout using the `stepsdk` package:

```go
package main

import "go.klarlabs.de/warden/stepsdk"

func main() {
	stepsdk.Run(func(in stepsdk.Input) stepsdk.Output {
		// inspect in.RepoPath (the worktree), in.DiffSummary, in.PriorFindings...
		return stepsdk.Pass()
	})
}
```

Build it as `warden-step-<name>` on `PATH` and reference `<name>` in the step
list. Either way, custom steps run as isolated subprocesses — no repo-authored
code is loaded into the daemon.

## Bypass provenance (`warden doctor`)

`git ... --no-verify` bypasses any hook by design; Warden does not fight that,
but makes it visible after the fact. Each validated commit gets a git note
carrying its `axi-go` evidence chain. `warden doctor` walks commits since the
adoption point and flags any without a matching note — so on a shared branch
every contributor can see which commits were actually validated, with no
central server. Note-push is best-effort: a failed note never blocks the push.

## Development

```bash
go build ./...
go test ./...
```

Architecture (hexagonal): `internal/domain` (policy model), `internal/policy`
(rule resolution), `internal/application` (the pipeline Runner + ports),
`internal/infrastructure/{git,kernel,steps,hooks,explain}` (adapters),
`internal/service` (composition root), `internal/cli` + `internal/mcp`
(delivery), `stepsdk` (public custom-step SDK).

## License

MIT © Felix Geelhaar
