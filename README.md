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
scripts, pre-commit config, lefthook, or CI).

## Install

warden is one static binary; pick whatever your machine already has — no Go
toolchain required.

```bash
# npx — no install (works anywhere Node is present)
npx @klarlabs-studio/warden init

# curl (Linux/macOS)
curl -fsSL https://raw.githubusercontent.com/klarlabs-studio/warden/main/scripts/install.sh | sh

# Homebrew
brew trust klarlabs-studio/tap        # first time only
brew install --cask klarlabs-studio/tap/warden

# Go devs
go install go.klarlabs.de/warden@latest   # or: go run go.klarlabs.de/warden@latest init
```

Homebrew refuses to load a cask from a third-party tap it has not been told
to trust, so the first install of anything from this tap needs
`brew trust klarlabs-studio/tap` once — per machine, not per tool.

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
warden import --write   # reads your Makefile / package.json / .pre-commit-config.yaml / lefthook / CI
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
  pauses at an approval gate when a rule requires it, then fast-forwards your
  local branch to whatever the pipeline produced and writes a hash-chained
  provenance note under `refs/notes/warden` for each validated commit. If the
  branch moved mid-run the fast-forward is aborted, never forced.

**A passing push exits 0 and prints no error.** When the pipeline changed
nothing, Warden stands aside and lets git perform and report the push itself, so
`git push` means exactly what it always did and its exit code answers "did it
land?" on its own.

Warden performs the push itself only when git cannot do it unaided: a step
rewrote the commit (an auto-fix, an amending agent step), the push needs a
policy-decided force (`push.force: lease`), or a PR is to be opened. Git resolves
the refs it will push *before* calling the hook and its push protocol is a
compare-and-swap, so its now-stale attempt would be rejected — Warden pushes,
then fails the hook on purpose to pre-empt it. Only on that path does git print
`error: failed to push some refs`, and Warden tells you first:

```
warden: pushed 9d90f7f3657c to origin/feature; local branch fast-forwarded
warden: git will now print 'error: failed to push some refs' — that's expected, not a failure…
```

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

### When a note can't be signed

Signing is best-effort: an unwritable config dir, a malformed key file or a
failure inside the signer leaves the note **unsigned** rather than failing a push
that already succeeded. A developer shouldn't be blocked from pushing because a
key directory went read-only.

But an unsigned note isn't a smaller version of a signed one — it proves the
checks ran, not *who* ran them, so `verify --require-signed` rejects it. Warden
now says so at the moment it happens:

```
warden: provenance note written UNSIGNED: no signing key is available on this machine.
        It still proves the checks ran, but not WHO ran them — `warden verify
        --require-signed` will reject it. Set signing.required to fail instead.
```

Where the provenance is load-bearing, refuse instead:

```yaml
signing:
  required: true    # default false — fail the run rather than write an unsigned note
```

The read side has been configurable since `trusted_keys`; this is the write-side
counterpart. A repo whose CI gates on `--require-signed` should not be able to
produce notes that gate will later reject — by then the commit is in history and
whoever could have fixed it has moved on. **Enforcement only at verification time
is enforcement too late.**

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

### Measuring adoption across a fleet

A gate that is routinely bypassed protects nothing **and** removes the signal
that it ever ran. `doctor` answers that one repo at a time — which is exactly the
granularity at which fleet-wide drift stays invisible. `warden fleet status`
rolls it up:

```bash
warden fleet status --root ~/dev        # scan one level for repos
warden fleet status ~/dev/a ~/dev/b     # or name them
warden fleet status --root ~/dev --json # for a dashboard
```

```
6 repos gated, 31 skipped (11 configured but never adopted)
257 commits since adoption: 73 verified, 141 bypassed (54.9%), 43 reattestable

  ✗  proctor                      61/61 bypassed (100.0%)
  ✗  mcp-go                       36/55 bypassed (65.5%)
  !  armada                       configured but never adopted — run `warden init`
  ✓  statekit                     18 verified
```

Two distinctions do the work here:

- **Bypassed is not the same as `doctor`'s "unverified".** A squash-merge unbinds
  a note while the *content* was gated under the pre-squash commit. Those are
  reported as **reattestable**, not as bypasses — counting them would inflate the
  number that is supposed to trigger an intervention, and an alarmist metric gets
  dismissed as noisy.
- **"Configured but never adopted" is not "not a warden repo".** The first is a
  stalled adoption with a one-command fix and someone who already intended it;
  the second isn't a problem. Merging them buries the actionable one.

Exits non-zero when any commit was genuinely bypassed, so it composes as a CI or
cron check. A reattestable gap does not fail it — `warden reattest --all` rebinds
those.

### Notarizing an Agent Trace record

[Agent Trace](https://github.com/cursor/agent-trace) standardizes how AI
contributions are recorded alongside human ones. Every implementation of it is a
**self-report**: the agent that wrote the code also writes the record saying what
it wrote. Useful — and unfalsifiable. Nothing stops the record being edited
afterwards, and nothing ties it to a moment the code was actually checked.

Warden isn't the authoring tool and doesn't pretend to be. What it can do is
**notarize**: at the moment it gates a commit, hash whatever trace record is
present and bind that digest into the signed, hash-chained provenance note.

```yaml
agent_trace:
  path: .agent-trace.json   # empty (default) disables notarization
  # required: true          # fail the run when the record is missing
```

`warden why` then reports whether the record still matches what was signed:

```
agent trace:   .agent-trace.json (notarized, spec 0.1.0)
agent trace:   .agent-trace.json (CHANGED SINCE — the record no longer matches what was signed, spec 0.1.0)
```

The trace stays the agent's claim. What warden adds is evidence of **when that
claim existed** and that **it hasn't changed since** — so rewriting a
`contributor: ai` range as `human` after the fact is detectable, which it is not
anywhere else.

**A missing record is not an error by default.** A human commit legitimately has
no agent trace, so absence is the normal case; `required: true` is for a repo
where every change is expected to carry one and its absence is itself the finding.

**Warden does not validate the record against the schema** beyond the few fields
that identify it as a trace. Agent Trace is a draft RFC and will move; a warden
that rejected next quarter's revision would fail gates over a spec change — a far
worse failure than notarizing a record it doesn't fully understand. The digest is
what carries the guarantee, and that holds whatever the schema becomes.

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
  pre_push: [intent, rebase, review, test, document, lint, credentials]   # credentials: refuse a push carrying a secret (see below)
parallel: true   # default — run independent checks concurrently (see below)
writes: [codegen]   # steps whose tree writes must be KEPT — run as sequential barriers (not isolated/discarded)
symlink_deps: false   # default false = hardlink-copy node_modules into the worktree (works with Turbopack); true = fast symlink
timeouts: { test: "5m", review: "2m" }   # kill + fail a step that hangs longer than this ("0" = no limit; a malformed value is rejected at load, never silently unlimited)
notify: true     # default — desktop notification after a slow interactive pre-push (a failed/blocked push always notifies)
notify_after: 10s   # default — a *passing* run only notifies once it ran at least this long (fast green gates stay silent); must be a valid Go duration or the config is rejected at load
cache: { test: ["**/*.go", "go.mod", "go.sum"] }   # skip a step when its declared inputs are unchanged
risk: { diff_lines_high: 400, files_touched_high: 15 }
security_scan: { mode: delta }   # default — the security-scan step fails only on findings THIS change introduced (see below)
pr: { enabled: true, comment: true }   # open/update a PR on a passing push, post a gate-result comment
agent_trace: { path: "" }   # notarize an Agent Trace record; empty disables it
signing: { required: false }   # default — true refuses to write an unsigned note instead of warning
push: { force: lease }   # default — a rebased branch is pushed with --force-with-lease pinned to the remote-tracking ref; `never` refuses instead (see below)
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
| `warden import [--write]` | generate `.warden.yaml` from an existing Makefile / package.json / `.pre-commit-config.yaml` / lefthook / CI |
| `warden status` | show gate state: armed hooks, adoption point, resolved steps |
| `warden doctor [--branch b]` | audit which commits since adoption carry a validation note |
| `warden fleet status [--root d] [PATH...]` | gate coverage and **bypass rate** across many repos |
| `warden audit [--branch b] [--format text\|json\|md]` | export a commit-provenance report (compliance) |
| `warden verify [--commit c] [--key fp] [--quiet]` | exit 0 if a commit is warden-validated — the CI provenance-skip primitive |
| `warden verify --range base..head [--require-signed] [--key fp] [--json]` | gate a whole range — exit non-zero if any commit lacks trusted provenance |
| `warden attest [--commit c]` | export a commit's provenance as an in-toto statement (sigstore/GUAC interop) |
| `warden reattest [--commit c] [--push]` | re-attest a squash-merge commit from the tree-identical validated commit |
| `warden reattest --all [--branch b] [--push]` | sweep a branch: re-attest every recoverable squash-merge gap since adoption |
| `warden key show` | print this machine's provenance signing key + fingerprint |
| `warden trust add\|list\|remove [path]` | allow the agent surfaces to run **this** repo's commands (per-repo `run_trigger` opt-in) |
| `warden why [commit]` | explain what the gate did for a commit — matched rules, steps, signer — from its note |
| `warden recipes [name]` | list / print paste-able check recipes (gitleaks, semgrep, trivy, coverage-delta, …) |
| `warden watch` | re-run the fast checks on save — a continuous dev feedback loop |
| `warden attach` | watch a running gate live from another terminal (Unix socket) |
| `warden ci [--branch b] [--wait]` | report (or poll) CI status for the branch's PR |
| `warden axi <verb>` | flags-only agent surface, TOON output (`verify`, `verify-range`, `doctor`, `audit`, `status`, `policy-explain`, `steps`, `run-trigger`) |
| `warden mcp serve` | MCP server over stdio |

### Agent surfaces

Both agent surfaces — `warden mcp serve` (MCP over stdio) and `warden axi <verb>`
(flags-only, TOON output) — expose the same operation set. The provenance reads
are the point: an agent can ask **"is this commit actually gated?"** and get a
structured answer, rather than having to run the gate to find out.

| MCP tool | axi verb | Answers |
|---|---|---|
| `verify` | `verify` | Does this commit carry signed, chain-intact provenance bound to it? |
| `verify_range` | `verify-range` | Is *every* commit in `base..head` gated? (the PR-check shape) |
| `doctor` | `doctor` | Which commits since adoption have no note — where was the gate bypassed? |
| `audit` | `audit` | Full per-commit provenance export, for compliance |
| `status` | `status` | Are the hooks actually armed? What would run? Which key signs here? |
| `policy_explain` | `policy-explain` | What policy resolves for a hypothetical push? |
| `steps_list` | `steps` | Which steps exist per hook? |
| `run_trigger` | `run-trigger` | Run the pipeline (**gated on trust** — see below) |
| `run_status` | — | Poll a run started by `run_trigger`: finished steps, then the verdict |

Everything except `run_trigger` is a pure read, marked `readOnlyHint` on MCP, and
needs no trust opt-in. The two gate verbs put their verdict in the **exit status**
as well as the payload, so they compose in a shell:

```bash
warden axi verify && echo "already validated — CI can skip the checks"
warden axi verify-range --base origin/main --require-signed
```

A failed run reports `blocker` and `retryable` alongside its findings, so an agent
can tell "your change is wrong" (don't retry) from "another process held the
linter's lock" (retry later) without parsing prose.

#### Runs are asynchronous over MCP

A full pipeline routinely takes minutes, so `run_trigger` **returns immediately**
with a `run_id` and `phase: running`. Poll `run_status(run_id)` for the steps that
have finished and, once it ends, the verdict:

```jsonc
// run_trigger → returns in milliseconds
{ "run_id": "run-1", "phase": "running", "steps": [] }

// run_status → while it works
{ "run_id": "run-1", "phase": "running", "steps": [ { "step": "lint", "status": "pass" } ] }

// run_status → when it ends
{ "run_id": "run-1", "phase": "complete", "summary": { "outcome": "passed", … } }
```

`phase: complete` means the run **finished**, not that the gate **passed** — read
`summary.outcome` for the verdict. A run that could not be carried out at all
reports `errored` instead, which is a different thing from a rejected change and
must not be treated as one.

Steps appear as they finish, so an agent can start fixing a lint failure while the
test step is still running. Run ids are per-process and don't survive a server
restart. `warden axi run-trigger` stays **synchronous** — a one-shot CLI
invocation should block until it has an answer.

### `run_trigger` trust

The `axi` and `mcp` surfaces are non-interactive: they auto-approve gate
findings because there is no human at a prompt. That is fine for the read-only
operations above, but
`run_trigger` (and `warden axi run-trigger`) **executes the repository's
`.warden.yaml` `commands` as shell**. Pointing an MCP-enabled agent at an
untrusted cloned repo and letting it call `run_trigger` would therefore be
arbitrary code execution from that repo's config, with the human-approval step a
normal interactive `warden run` keeps.

So `run_trigger` **refuses by default** on these surfaces and runs only when the
operator has explicitly trusted the repo. Grant it **per repository**:

```bash
cd your-repo
warden trust add        # this repo only
warden trust list       # what have I granted?
warden trust remove     # revoke
```

The allowlist lives beside your signing key under the per-user config dir (never
in the repo — a repository must not be able to declare itself trusted), is
`0600`, and is plain text you can read and audit.

Two narrower grants also work:

- **`--trust`** on `warden axi run-trigger`, scoped to that single invocation.
- **`WARDEN_MCP_ALLOW_RUN=1`**, which trusts **every** repo the process is
  pointed at. Prefer `warden trust add`; reach for the env var only where there
  is no persistent config dir to hold an allowlist — a container or a CI job,
  where the whole workspace is disposable anyway.

> **Changed in v0.21.0.** The env var was previously the only opt-in, and it
> authorized a *process*, not a repository. An MCP server is long-lived while an
> agent moves between checkouts, so one grant silently covered every repo the
> server was later pointed at — including one cloned minutes afterwards. The
> per-repo grant names its subject, the same fix git made with `safe.directory`.

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

### The `security-scan` step

`security-scan` is the one command step Warden interprets rather than just runs.
When its command is a [nox](https://github.com/nox-hq/nox) scan, Warden reads the
scan's `findings.json` and gates on **what your change introduced**, not on the
tree's total state:

```yaml
security_scan:
  mode: delta          # default. total = fail on any unwaived finding, whoever added it
  base: ""             # default: merge-base with the branch's upstream (falls back to origin/HEAD)
  version_check: true  # default: refuse to scan when the local scanner isn't the version CI pins
  pin_file: ""         # default: search .github/workflows/*.yml for the pin
```

**Why delta is the default.** Gating on the tree's absolute state means an
unrelated one-line change inherits the repo's entire historical backlog as a
precondition. Measured across a fleet rollout, five repos had a finished,
unrelated config commit blocked by 7 / 16 / 21 / 44 / 71 pre-existing findings —
and the gate was red in 5 of 7 repos sampled while commits kept landing, i.e. it
was being routed around with `--no-verify`. A gate that is routinely bypassed
protects nothing *and* removes the signal that it ever ran. Delta gating keeps
the property that matters (you cannot add a vulnerability) and drops the wall:
pre-existing findings are reported as a counted warning. Set `mode: total` in a
repo that has genuinely reached zero and wants to stay there.

**Scanner version drift.** Scanners renumber their rule IDs between releases, so
the same hit gets a different fingerprint — and every entry in the committed
baseline stops matching at once. One repo pinned `NOX_VERSION: 1.3.0` in CI while
developers ran 1.15.0: none of its **729** baseline entries matched anything CI
scanned, CI reported 240 phantom criticals, and every release failed for a month
before anyone noticed (the job only ran on tags). Warden now refuses to scan when
the scanner on `PATH` is not the version the repo's workflows pin, naming both
versions and the file that pins them, so the mismatch surfaces at pre-push where
it is cheap. The pin is read from the workflow rather than restated in
`.warden.yaml` — a second copy is a second thing to forget. Warden also reports a
baseline that matches **zero** current findings as drift rather than as hundreds
of new criticals. **Rule of thumb: bump the pin and regenerate the baseline in
the same commit.**

Anything Warden cannot interpret — `make audit`, `npm audit`, a nox command that
directs its own `-output`/`-format` — keeps the plain behavior: run it, fail on a
non-zero exit. The same is true whenever the report cannot be read or the base
commit cannot be scanned: the step degrades toward failing, never toward passing.

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

#### Findings that can be acted on

A finding needs only `Severity` and `Message`. But your step usually knows more
than that — which rule fired, what breaks if it's ignored, and the command that
fixes it — and whoever reads the finding otherwise has to rediscover all three:

```go
return stepsdk.Fail(stepsdk.Finding{
	Severity: "high",
	Message:  "unused import",
	File:     "main.go",
	Line:     7,
	Rule:     "ST1003",                                    // what you search, waive, or baseline by
	Why:      "an unused import fails the build on CI",    // the justification severity alone doesn't carry
	Fix:      &stepsdk.Fix{Command: "goimports -w main.go"},
})
```

That turns a failed gate into **run → read → fix → re-run** instead of
**run → guess → re-run**, which is the difference between an agent that can
close the loop and one that can't. Humans get the same lines under the finding:

```
  [high] main.go:7 unused import
         rule: ST1003
         why: an unused import fails the build on CI
         fix: goimports -w main.go
```

`Fix` also takes a `Patch` (a unified diff), preferred when you know the change
exactly since it can be reviewed before it's applied. **Both are advisory** —
warden never applies a fix on the strength of a finding. Folding changes into the
tree is a policy decision the repo makes with an `auto_fix` budget, so attaching a
patch can't escalate your step into a tree write.

All four fields are optional and omitted from the wire when unset, so steps
written before they existed keep working unchanged.

### Pinning the scanner when the pin lives elsewhere

The `security-scan` step refuses to run when the scanner on your PATH differs
from the version CI pins — a scanner that renumbers rule IDs between releases
invalidates every baseline fingerprint at once, so the whole triaged corpus
reads as net-new.

It finds the pin by reading the workflow that already declares it. If your fleet
pins the scanner **once**, centrally, in a shared reusable workflow, point at it
across the repository boundary:

```yaml
security_scan:
  pin_file: my-org/.github/.github/workflows/go-ci.yml@main
```

This names *where* the pin lives, never what it is — there is still exactly one
copy of the version number, in the workflow that already defines it. Both shapes
are understood: a scalar (`NOX_VERSION: "1.16.1"` in `env:` or `with:`) and a
reusable workflow's own input **default**, which is how the defining repo states
it.

Resolution is cached for an hour and bounded by a short timeout, and **any**
failure — offline, moved, renamed, no pin in the file — degrades to "no pin
found" rather than blocking a push. `warden status` reports when the check is
inert, so a silent check is never mistaken for a passing one.

### What `rebase` rebases onto

The `rebase` step targets the branch's **integration base** — the ref it is
meant to merge into — resolved in order:

1. `pr.base` when the repo configures one (`origin/<base>`),
2. otherwise the remote's default head (`origin/HEAD` → e.g. `origin/main`),
3. otherwise an explicitly-set `@{upstream}`, but only when it points somewhere
   other than this branch's own remote ref.

It deliberately never rebases onto `origin/<this-branch>`. That was the original
behaviour and it is correct only until the branch is rewritten: after a local
rebase onto an updated `main` — the standard way to satisfy *"head branch is not
up to date with the base branch"* — `origin/<branch>` still holds the commit you
just replaced, so `origin/<branch>..HEAD` contains **main's** commits. The step
would then replay main onto the superseded tip and fail on main's own conflicts,
refusing a push that was never wrong.

When no integration base exists (a brand-new branch, or a repo whose only branch
is the one being pushed) the step passes and says so, rather than failing.

### Tools that refuse to run concurrently

Some checkers guard a shared resource with a mutex and refuse to start while
another copy of themselves is running — `golangci-lint` ("parallel
golangci-lint is running"), `cargo`'s build lock, and friends. That refusal is
**not a verdict on your tree**: the check never ran. It is also easy to trigger
by accident — run the linter in one terminal, commit from another.

Warden waits it out rather than reporting a lint error that doesn't exist. On a
recognized contention message the step retries (up to a minute), saying so once:

```
warden: another process holds lint's lock, waiting…
```

If the lock is still held when that budget runs out, the gate still fails —
"I could not check" is not "the tree is clean" — but it says which, and exits
`75` instead of `1` (see [Exit codes](#exit-codes)):

```
warden: step lint could not run: another process holds its lock
```

Only a narrow set of known contention messages is retried, so a genuine failure
fails immediately and keeps the tool's own output.

**To stop it recurring with golangci-lint**, add `--allow-parallel-runners` to
your lint command. That lock exists to keep concurrent runs from corrupting a
*shared* cache, and warden already points every run at its own
`GOLANGCI_LINT_CACHE` — so the thing the lock protects is already protected, and
two repos need never queue behind each other again:

```yaml
commands:
  lint: "golangci-lint run --allow-parallel-runners ./..."
```

Warden says so in the failure message when it sees you haven't.

### Compiled languages: build-cache reuse

A worktree contains **tracked files only** — that's what makes the isolation worth
having. But it also means every gitignored build cache is absent, so a compiled
language would rebuild from scratch on every gated push.

Warden redirects each toolchain's own cache-location variable at a directory
under `.git/` that survives the worktree, so the second run onward is warm:

| Toolchain | Variable | Detected by |
|---|---|---|
| Rust | `CARGO_TARGET_DIR` | `Cargo.toml` |

Measured on a six-crate Rust workspace, the real pre-commit gate: **86s → 4s.**

This is a *redirection*, not a copy — deliberately. Dependency directories are
hardlink-copied into the worktree (see `symlink_deps`), which is safe because
`node_modules` is read-mostly. A compiler **writes** to its cache, and hardlinks
share inodes, so copying one that way could corrupt your live cache. Pointing the
toolchain at its own directory lets its own locking handle concurrency — which is
what those variables exist for.

Warden never overrides a variable you already set: if you or your CI has
deliberately placed a cache somewhere, that wins. **Go is absent on purpose** — its
build cache already lives outside the repo, so a fresh worktree is already warm
and redirecting it would only make things worse.

### Steps whose toolchain isn't installed

A validation worktree is a git worktree, so it starts with tracked files only.
When a step's executable isn't there you get the shell's verdict, not the
tool's:

```
> astro build
sh: astro: command not found
```

That is an environment failure, not a build failure, and warden reports it as
one — with the install command derived from the lockfile actually present, and
exit code `78`:

```
warden: step js-build could not run: its toolchain or dependencies are not installed
  [high]  js-build could not run: astro is not installed in the validation worktree.
          This is an environment problem, not a problem with your change.
          Run: pnpm install --frozen-lockfile (in web)
```

The gate still fails: an unbuilt tree is not a validated tree. (Warden links or
copies gitignored `node_modules` from your live checkout into the worktree — see
`symlink_deps` / `materialize_deps` — so this usually means the deps aren't
installed in your checkout either.)

#### Private registries: use `NODE_AUTH_TOKEN`, never `npm config set`

If installing needs auth for a private registry package, **export the token as
an environment variable**:

```bash
export NODE_AUTH_TOKEN="$(gh auth token)"   # in your shell / .envrc, not in the repo
```

Warden passes your environment through to steps unchanged, and a tracked
`.npmrc` referring to `${NODE_AUTH_TOKEN}` resolves against it:

```
@org:registry=https://npm.pkg.github.com
//npm.pkg.github.com/:_authToken=${NODE_AUTH_TOKEN}
```

**Do not** reach for the command every guide suggests:

```bash
# DON'T — this writes a LIVE TOKEN into .npmrc
npm config set //npm.pkg.github.com/:_authToken "$(gh auth token)"
```

`.npmrc` is a **tracked file in most repos**, holding exactly the placeholder
above. `npm config set` overwrites it in place with the real credential, and the
next `git add` stages a secret. The whole point of a gate is that its happy path
can't end in a leak — so warden's built-in `credentials` step refuses the push if
it happens anyway.

### The `credentials` step

Runs by default at pre-push. It reads the files your change touched and refuses
the push if any of them carries something shaped like a live credential —
GitHub/npm/AWS/Slack/Stripe/OpenAI/Anthropic/Google tokens and PEM private keys:

```
warden: step credentials failed
  [high] .npmrc:2 looks like a live GitHub token (first 7 chars…). A tracked file must not
         carry a credential — move it to an environment variable …
```

Matches are redacted in the output, and lines that defer to a variable
(`${NODE_AUTH_TOKEN}`, `{{ secrets.X }}`, `$(vault read …)`) or that hold an
obvious dummy (AWS's documented `AKIA…EXAMPLE` key) are not findings — a check that cries
wolf gets deleted, and then it catches nothing.

It is deliberately shallow: prefix-tagged token formats only, changed files
only, no entropy heuristics and no history. For real coverage add
`warden recipes gitleaks`. To turn it off, leave `credentials` out of
`steps.pre_push`.

### Exit codes

`warden run` distinguishes "your change is wrong" from "this machine wasn't
ready", so a retry wrapper can tell them apart without parsing prose:

| Code | Meaning | Retry? |
|------|---------|--------|
| `0` | passed (pre-commit, or a pre-push where git completed the push) | — |
| `1` | the gate reached a verdict about your change | no |
| `2` | usage error | no |
| `3` | **passed**, and *warden* performed the push (see below) | no — you're done |
| `75` | a step couldn't run: another process holds its lock (`EX_TEMPFAIL`) | **yes**, later |
| `78` | a step couldn't run: its toolchain/deps aren't installed (`EX_CONFIG`) | no — run the remediation |

A passing pre-push usually exits `0`. It exits `3` in the one case where warden
performs the push itself — after a step rewrote the branch, or when a force is
needed — because git's own now-stale push must then be stopped from racing it.
That case is a **success**: your commits are on the remote.

These codes are what `warden run` returns. **Git does not propagate a hook's exit
status** — it only distinguishes zero from non-zero, then reports its own failure
as `1` and prints `error: failed to push some refs`. So the distinct codes are for
whatever calls warden directly (retry wrappers, CI steps, the `axi`/MCP agent
surfaces); at an interactive `git push` you still see git's `1`, which is why
warden prints its own explanation first.

> **Changed in v0.21.0.** The warden-performed push used to exit `1`, sharing a
> code with "the gate rejected your change" — so the most common successful
> outcome was indistinguishable from a rejection. If you have a wrapper script or
> CI step keying on `1`, treat `3` as success.

## Rebasing a gated branch (`push.force`)

Warden **performs the push itself** — it validates, then pushes, then fails the
hook so git's own (now redundant) push can't race it. A consequence: git hands
the pre-push hook no signal that you typed `--force`, so warden has to decide
for itself how to push a branch whose history you rewrote.

It detects the rewrite (the remote tip is no longer an ancestor of yours) and,
by default, pushes with `--force-with-lease` **pinned to your remote-tracking
ref** — the value you last fetched. A commit someone else pushed since then
invalidates the lease and the push is refused, so a rewrite can only ever
discard history you have actually seen.

```yaml
push:
  force: lease   # default — rewrite with a lease when the branch was rebased
  # force: never # refuse instead; the push fails as git would
```

An ordinary fast-forward never forces, whatever the setting — the flag is
reserved for the case that needs it.

**Why `lease` is the default.** Because warden owns the push, refusing to
rewrite doesn't leave you with git's usual "use `--force`" nudge; it leaves you
with `git push --no-verify`, which skips the gate entirely and writes **no
provenance**. A default that pushes people toward bypassing the gate is worse
for the thing warden protects than one that rewrites a branch you already
rewrote locally. Set `force: never` if your repo would rather the push fail.

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
