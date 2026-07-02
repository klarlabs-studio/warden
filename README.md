# warden

A configurable git commit/push gate installed as **native git hooks** — `git commit` and `git push` themselves are the gated commands, no second remote and no changed muscle memory.

Warden runs a policy-driven pipeline (lint, test, review, …) in a **disposable worktree** so a run never touches your live checkout, then fast-forwards your real branch and performs the push itself once everything passes. Policy is a set of stacking **rules** (match on branch, path glob, and a risk heuristic → overrides), and the pipeline is extensible with a typed subprocess SDK.

Built on [`axi-go`](https://go.klarlabs.de/axi) (execution kernel — typed actions, effect-gated approval, tamper-evident evidence chain), [`fortify`](https://go.klarlabs.de/fortify) (resilience), [`statekit`](https://go.klarlabs.de/statekit) (policy visualization), and [`mcp-go`](https://go.klarlabs.de/mcp) (MCP surface).

## Why not just a Makefile / CI?

`make ci` runs your checks — but in your **dirty working tree** ("passes locally,
fails CI"), and only when you **remember** to run it, leaving **no trace**.
Warden does what a Makefile can't:

- **Runs clean.** Every check runs in a disposable worktree seeded from the
  commit, so passing in warden means passing in CI — reproducibly.
- **Can't be forgotten.** Native `git` hooks fire automatically; no discipline
  required, no changed muscle memory.
- **Leaves proof.** Each gated commit gets a hash-chained validation note that
  travels with the repo — so **CI can trust it and skip re-running the checks**,
  cutting minutes and cost ([provenance-skip](docs/ci-provenance-skip.md)).
- **Scales with risk.** Rules match on branch / path / diff size, so heavy
  checks and human approval apply only where they matter.

It complements your checks rather than replacing them: point warden at the
commands you already run (`warden import` reads them from your Makefile, npm
scripts, lefthook, or CI).

## Install

warden is one static binary; pick whatever your machine already has — no Go
toolchain required.

```bash
# npx — no install (works anywhere Node is present)
npx @klarlabs-studio/warden init

# curl (Linux/macOS)
curl -fsSL https://raw.githubusercontent.com/klarlabs-studio/warden/main/scripts/install.sh | sh

# Homebrew
brew install felixgeelhaar/tap/warden

# Go devs
go install go.klarlabs.de/warden@latest   # or: go run go.klarlabs.de/warden@latest init
```

On Windows: `irm https://raw.githubusercontent.com/klarlabs-studio/warden/main/scripts/install.ps1 | iex`.

The `npx @klarlabs-studio/warden` package is a ~15-line launcher: it ships the prebuilt binary
per platform (the [esbuild pattern](https://github.com/evanw/esbuild/tree/main/npm))
and execs it. All logic lives in the one Go binary; every channel above ships
that same binary.

## Adopt an existing repo in one command

```bash
cd your-repo
warden import --write   # reads your Makefile / package.json / lefthook / CI into .warden.yaml
warden init             # installs hooks + records the adoption point
```

`warden init` alone also works — it auto-detects the language (Go, Rust, JS/TS,
Python) and pre-fills sensible lint/test commands.

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

### Signed provenance

Every validation note is signed with a per-machine ed25519 key (generated on
first run, kept under your user config dir — the private key never leaves the
machine). The signer's public key is bound into its own signature, so the note
proves not just that the evidence chain is intact but that *a specific key*
produced it. `warden verify` reports the signer; pass `--key` to require one:

```bash
warden key show                    # prints the fingerprint to pin
warden verify --key <fingerprint>  # exit 0 only if signed by a trusted key
```

In CI this turns provenance-skip from "a warden ran here" into "a warden **I
trust** ran here" — pass `key:` to the bundled `warden-verify` action. Notes
stay verifiable (chain + signature) without pinning; `--key` just adds the trust
gate.

## Configuration (`.warden.yaml`)

```yaml
agent: auto
hooks: { pre_commit: true, pre_push: true }
commands:
  lint: "golangci-lint run ./..."
  test: "go test -race ./..."
# Agent steps (intent/review/document) run the command configured for the
# resolved agent, expanding {prompt}/{step}/{repo}. claude and codex work out of
# the box via bundled presets — you only need agent_commands to override those
# or add another agent. No command (and no preset) → advisory skip; Warden never
# guesses an agent's CLI.
agent_commands:
  opencode: "opencode run {prompt}"   # example: any other agent
steps:
  pre_commit: [lint]
  pre_push: [intent, rebase, review, test, document, lint]
parallel: true   # default — run independent checks concurrently (see below)
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
gate — including a `schedule:` line that shows exactly which steps run at once.

### Parallel steps

By default Warden runs independent, read-only checks concurrently, so the gate
is as slow as the slowest check, not the sum of all of them:

```
schedule:  intent → rebase → [review ∥ test ∥ document ∥ lint]
```

A step stays a **sequential barrier** (runs alone, in order) when it writes to
the worktree: `rebase` (rewrites history) and any step given an `auto_fix`
budget. Steps around a barrier still parallelize. Like `lefthook`'s parallel
mode, a step that runs concurrently must not modify tracked files — give it an
`auto_fix` budget (which serializes it) if it needs to. Set `parallel: false` to
force the classic one-step-at-a-time pipeline.

On an interactive terminal the pre-push run shows a live TUI: a spinner and a
counting-up timer per step, a tail of each running step's output as it streams,
and the approval gate answered inline.

## Commands

| Command | Description |
|---|---|
| `warden init [--hooks=pre-commit,pre-push]` | initialize, install hooks, record adoption point |
| `warden hooks enable\|disable <hook>` | change hook selection |
| `warden run <pre-commit\|pre-push>` | run the gate (invoked by the hook shims) |
| `warden policy explain [--hook h] [--branch b] [--paths glob,...] [--chart]` | print resolved policy (or an XState statechart) |
| `warden steps list` | list built-in + custom steps by hook |
| `warden import [--write]` | generate `.warden.yaml` from an existing Makefile / package.json / lefthook / CI |
| `warden status` | show gate state: armed hooks, adoption point, resolved steps |
| `warden doctor [--branch b]` | audit which commits since adoption carry a validation note |
| `warden audit [--branch b] [--format text\|json\|md]` | export a commit-provenance report (compliance) |
| `warden verify [--commit c] [--key fp] [--quiet]` | exit 0 if a commit is warden-validated — the CI provenance-skip primitive |
| `warden key show` | print this machine's provenance signing key + fingerprint |
| `warden ci [--branch b] [--wait]` | report (or poll) CI status for the branch's PR |
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
