<p align="center">
  <img src="assets/logo.svg" alt="warden" width="116" height="116">
</p>

<h1 align="center">warden</h1>

<p align="center">
  <a href="https://github.com/klarlabs-studio/warden/actions/workflows/ci.yml"><img src="https://github.com/klarlabs-studio/warden/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
  <a href="https://github.com/klarlabs-studio/warden/releases/latest"><img src="https://img.shields.io/github/v/release/klarlabs-studio/warden?sort=semver" alt="Release"></a>
  <a href="https://www.npmjs.com/package/@klarlabs-studio/warden"><img src="https://img.shields.io/npm/v/@klarlabs-studio/warden?logo=npm" alt="npm"></a>
  <a href="https://pkg.go.dev/go.klarlabs.de/warden"><img src="https://pkg.go.dev/badge/go.klarlabs.de/warden.svg" alt="Go Reference"></a>
  <a href="LICENSE"><img src="https://img.shields.io/github/license/klarlabs-studio/warden" alt="License: MIT"></a>
</p>

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

Every self-fetched binary — the installer scripts and the version-pinned git
hook that bootstraps warden on a fresh clone — is **SHA-256-verified against the
release's `checksums.txt` before it is made executable, and fails closed on any
mismatch**; the cached binary is re-verified on every run. See
[SECURITY.md](SECURITY.md#supply-chain-integrity-of-the-self-fetched-binary) for
the integrity model and the signature-verification follow-up.

## Adopt an existing repo in one command

```bash
cd your-repo
warden import --write   # reads your Makefile / package.json / lefthook / CI into .warden.yaml
warden init             # installs hooks + records the adoption point
```

`warden init` alone also works — it auto-detects the language (Go, Rust, JS/TS,
Python) and pre-fills sensible lint/test commands.

Adopting a strict linter on a repo with existing debt, or running warden
alongside Copilot review and automation PRs? See the
[adoption guide](docs/adoption-guide.md) for gating the change (not the history)
and the CI/bot settings that keep automated PRs from stalling.

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
the push, so git's own (now-stale) push must be stopped from racing it. As a
result git prints `error: failed to push some refs` on **every successful
push** — this is expected, not a failure: your gated commit is already on the
remote. Warden pre-empts it with a `warden: git will now print '…' — that's
expected` line so you know to ignore git's error.

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

Rather than pass fingerprints on every call, commit a **trusted-signer roster**
to `.warden.yaml` — a bare `warden verify` / `--range` then requires a trusted
signer automatically, and it inherits through `extends:` so an org names its
signers once:

```yaml
# .warden.yaml
trusted_keys:
  - 3a76a2b850d0e957   # add yours with `warden key show`; inspect with `warden key list`
```

### Enforcing provenance across a range

`warden verify` checks one commit (the provenance-*skip* primitive). To **gate**
a whole branch or PR — fail if *any* commit lacks trustworthy provenance — use
`--range`:

```bash
# fail unless every commit origin/main..HEAD is warden-validated
warden verify --range origin/main..HEAD

# escalate: each must be signed, and by a key in the trusted set
warden verify --range origin/main..HEAD --require-signed --key <fp1>,<fp2>

warden verify --range origin/main..HEAD --json   # per-commit verdicts for CI
```

It exits non-zero with a per-commit reason — `missing` (no note),
`broken-chain` (a note that doesn't attest the commit — tampered or
transplanted), `unsigned`, or `untrusted`. Unlike `warden doctor`, which flags
only *missing* notes since adoption, `--range` also fails a tampered or
untrusted note, over an arbitrary `BASE..HEAD`. Merge commits are skipped by
default (`--skip-merges`); their parents are gated individually.

To turn this into a **required check** that blocks un-gated PRs from merging,
use the bundled `warden-gate` action — it runs `verify --range` on the PR head
(gating the merge before a squash rewrites history). See
[CI provenance gate](docs/ci-provenance-gate.md) for the workflow and a
self-hosted pre-receive recipe, and [ADR-0002](docs/adr/0002-provenance-enforcement.md)
for the design.

Each note also carries a small **SBOM**: a SHA-256 digest of every dependency
lockfile present at validation (`go.sum`, `package-lock.json`, `Cargo.lock`, …).
Because it's part of the signed, hash-chained record, a validated commit ships a
tamper-evident, signed fingerprint of exactly which dependency sets it had —
shown by `warden why`.

## Configuration (`.warden.yaml`)

```yaml
extends: ../.warden.base.yaml   # optional — inherit an org base config; this file overrides it
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
writes: [codegen]   # steps whose tree writes must be KEPT — run as sequential barriers (not isolated/discarded)
symlink_deps: false   # default false = hardlink-copy node_modules into the worktree (works with Turbopack); true = fast symlink
timeouts: { test: "5m", review: "2m" }   # kill + fail a step that hangs longer than this ("0" = no limit; a malformed value is rejected at load, never silently unlimited)
notify: true     # default — desktop notification after a slow interactive pre-push (a failed/blocked push always notifies)
notify_after: 10s   # default — a *passing* run only notifies once it ran at least this long (fast green gates stay silent); must be a valid Go duration or the config is rejected at load
cache: { test: ["**/*.go", "go.mod", "go.sum"] }   # skip a step when its declared inputs are unchanged
risk: { diff_lines_high: 400, files_touched_high: 15 }
pr: { enabled: true, comment: true }   # open/update a PR on a passing push, post a gate-result comment
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

By default Warden runs independent steps concurrently, so the gate is as slow as
the slowest step, not the sum of all of them:

```
schedule:  intent → rebase → [review ∥ document ∥ test ∥ lint]
```

Every concurrent step runs in its **own ephemeral worktree** cloned from the
run's worktree, so steps can't race each other — even a coding-agent step
(`review`/`document`/`intent`) that edits files runs isolated, and its writes are
discarded when the batch finishes (only its findings are kept).

A step is instead a **sequential barrier** — it runs alone, in order, in the
shared worktree with its writes preserved — when its changes must be *kept*:
`rebase` (rewrites history), any step given an `auto_fix` budget (its fixes are
folded back into the tree), or a step you list under `writes:`. So to have a step
persist tracked-file changes — a codegen command, or a `document` agent that must
keep its docs — give it an `auto_fix` budget or add it to `writes:`. Set
`parallel: false` to force the classic one-step-at-a-time pipeline.

On an interactive terminal the pre-push run shows a live TUI: a spinner and a
counting-up timer per step, a tail of each running step's output as it streams,
and the approval gate answered inline.

### Step cache

Declare a step's input globs under `cache:` and warden skips it when every
matched file is byte-identical to the step's last passing run — so an unchanged
`test` doesn't re-run on a docs-only push. The cache lives in `.git` (per-clone,
never committed); the key also covers the step's command, so changing what the
step runs busts it. Only non-mutating steps are cacheable, and correctness rests
on declaring *all* of a step's inputs (same contract as bazel/turbo). A step's
first cache line appears as `test (cached — inputs unchanged)`.

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
| `warden verify --range base..head [--require-signed] [--key fp] [--json]` | gate a whole range — exit non-zero if any commit lacks trusted provenance |
| `warden key show` | print this machine's provenance signing key + fingerprint |
| `warden why [commit]` | explain what the gate did for a commit — matched rules, steps, signer — from its note |
| `warden recipes [name]` | list / print paste-able check recipes (gitleaks, semgrep, trivy, coverage-delta, …) |
| `warden watch` | re-run the fast checks on save — a continuous dev feedback loop |
| `warden attach` | watch a running gate live from another terminal (Unix socket) |
| `warden ci [--branch b] [--wait]` | report (or poll) CI status for the branch's PR |
| `warden axi <verb>` | flags-only agent surface, TOON output |
| `warden mcp serve` | MCP server over stdio |

### Agent surfaces and `run_trigger` trust

The `axi` and `mcp` surfaces are non-interactive: they auto-approve gate
findings because there is no human at a prompt. That is fine for the read-only
operations (`policy_explain` / `policy-explain`, `steps_list` / `steps`), but
`run_trigger` (and `warden axi run-trigger`) **executes the repository's
`.warden.yaml` `commands` as shell**. Pointing an MCP-enabled agent at an
untrusted cloned repo and letting it call `run_trigger` would therefore be
arbitrary code execution from that repo's config, with the human-approval step a
normal interactive `warden run` keeps.

So `run_trigger` **refuses by default** on these surfaces and runs only when the
operator has explicitly trusted the repo:

- **MCP** (`warden mcp serve`): set `WARDEN_MCP_ALLOW_RUN=1` in the server's
  environment. An MCP client cannot pass flags, so the env var is the only knob.
- **axi** (`warden axi run-trigger`): pass `--trust`, or set
  `WARDEN_MCP_ALLOW_RUN=1`.

Grant trust only for repositories whose `.warden.yaml` you have reviewed. The
normal interactive `warden` flow is unaffected — it still prompts a human.

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

## Contributing

Contributions welcome — see [CONTRIBUTING.md](CONTRIBUTING.md) for the dev setup
and the `make ci` pipeline every change must pass. By participating you agree to
the [Code of Conduct](CODE_OF_CONDUCT.md). Found a security issue? See
[SECURITY.md](SECURITY.md) — please don't open a public issue. Release history
is in the [CHANGELOG](CHANGELOG.md).

## License

MIT © Felix Geelhaar
