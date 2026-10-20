# Predictive staged-file checks

Opt specific deterministic, read-only shell checks into staged prediction:

```yaml
execution:
  image: team-checks:local
  runtime: auto
  network: none
predictive_cache: [lint, test]
steps:
  pre_commit: [lint, test]
commands:
  lint: npm run lint
  test: npm test
```

Build or preload the image with the required tools and dependencies. Start the
foreground watcher in a separate terminal:

```sh
warden watch --staged
```

For a single evaluation, use `warden watch --staged --once`. The watcher polls
the worktree's Git index every 400 ms and waits for two matching samples before
starting. `git add`, unstage, branch/HEAD changes, and effective Warden config
changes trigger evaluation. Unstaged source edits do not become staged inputs.
Checks run in disposable, dependency-free container worktrees, not the live
checkout. Obsolete background work is cancelled, and its teardown finishes
before the replacement starts. Ctrl+C or SIGTERM stops the watcher.

The container environment is part of what the watcher watches. At most every
15 seconds it runs `image inspect` on `execution.image` (never engine `info`,
a pull, or a container) and folds the runtime, image ID and platform into the
snapshot. Rebuilding or retagging the image, switching runtime, or starting a
stopped engine therefore starts a new evaluation instead of leaving a pass for
the previous image on screen. The watcher prints the sampled environment when
it starts and when it changes, and each finished step prints the runtime,
image ID and platform it ran under. The sample only decides when to evaluate;
the evaluation resolves the runtime itself, and a prediction records the
environment the check actually ran in.

While a gate run in the same worktree is in progress (it publishes on the
`warden attach` socket), the watcher cancels its own work and waits, then
evaluates again once the run ends. The hook never waits for the watcher.

The watcher also writes what it observes to a per-worktree document that
editors read with `warden feedback status`. It is advisory and the gate never
reads it; see [background feedback](background-feedback.md).

A passing check with complete input capture saves a signed local prediction.
The next pre-commit gate can skip that check when all these still match:

| Input | Required match |
|---|---|
| Source | Entire staged Git tree and base HEAD |
| Command/configuration | Exact command and effective Warden configuration |
| Context | Hook, branch, changed paths, files touched and lines changed |
| Runtime | Inspected image identity, platform, network and runtime metadata |
| Trust | Current local signing key, signature, record purpose, Warden version and executable digest |
| Age | At most 10 minutes; future timestamps are refused |

The gate still creates and inspects its disposable worktree. A hit is reported
as `cached — matched signed staged prediction`, and carries reuse evidence
rather than a fresh execution contract. Predictions live under the worktree's
Git directory at `warden/predictions/`, with one atomically replaced file per
step and Warden binary. Missing, expired, unreadable or altered entries cause
ordinary execution. Cache write failures do not turn passing checks into
failures; staged-watch output reports when a prediction could not be saved.

## Boundaries

This first version supports explicitly selected lint, test and custom shell
commands with `network: none`. Native steps, coding agents, the special
`security-scan` wrapper, configured writers, auto-fix steps, host execution and
network-enabled execution are excluded. Checks must be deterministic for their
recorded inputs; a command depending on wall time or randomness should not opt
in. Initial untracked/ignored inputs, hidden index entries and submodules prevent
complete capture; checks still execute, but cannot produce reusable predictions.
Tracked inputs must remain unchanged during the check.

Pre-push checks execute fresh. Predictions neither publish git notes nor attest
a commit, and cannot satisfy remote execution-contract requirements. The cache
contains successes only; failed, cancelled, fixed or finding-bearing runs do
not replace a successful entry. A valid prior success for exactly the same
inputs can remain usable until its original expiry.

Only opted-in checks run in the predictive loop. The normal hook retains its
full policy and runs the remaining checks. A hook racing unfinished background
work executes normally instead of waiting for that work. Without a watcher,
normal opted-in pre-commit execution can still populate the same cache.
Remove the `predictive_cache` list (or set `predictive_cache: []` to override an
inherited list) to disable reuse. Stop the foreground watcher as well.

`warden watch` without `--staged` is live mode. On the host it re-runs the
commands in the live tree. Under a container configuration it checks a
working-tree snapshot in a disposable container worktree, and it never looks
up or saves a prediction: the snapshot is derived from the working tree, not
the index a commit is built from, and reusing its results at the gate would
rest on an equivalence nobody has proven (see
[background feedback](background-feedback.md#live-mode)).
This feature does not install an operating-system daemon. It precomputes
configured `commands:` checks against the staged snapshot; commands may scope
themselves using `WARDEN_CHANGED_FILES`. A step defined under `per_workspace:`
is never predicted and never reuses a prediction: its result depends on which
workspaces were selected as well as on the tree, and a prediction is not keyed
by the selection. Listing one in `predictive_cache` is a load error (see
[monorepo mode](monorepo.md)).
