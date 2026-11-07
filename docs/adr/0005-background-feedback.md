# ADR 0005 — Background feedback: container-aware watch and an editor-readable status

- Status: **Accepted, implemented** (v0.35–v0.38; user-facing description in `docs/background-feedback.md`)
- Date: 2026-10-18

## Context

Two watch modes shipped in 0.34.0 that did not share a model. `warden watch`
ran checks on the live checkout on the host and refused a container
configuration; `warden watch --staged` ran in a disposable worktree in the
configured image and persisted only successes, as signed predictions. A
repository that adopted container execution therefore had no live feedback loop,
and the staged loop reported more than it had observed:

1. The image was not part of what the watcher watched. Rebuilding a tag changed
   the image ID, the gate correctly missed its cache, and the watcher still
   printed "staged checks passed".
2. An environmental failure (engine stopped, lock contention) was recorded as a
   finished verdict and was not retried until the index changed.
3. Runtime identity (runtime, image ID, platform) was dropped from the output.
4. Failures, expiry and "running" were recorded nowhere; a pass aged out after
   the prediction TTL while the watcher still showed it.
5. After a commit the watcher ran a full evaluation against an empty staged diff.
6. Live mode absorbed any edit made during a run into its "last seen" state, so
   that edit was never checked.

Editors had nothing to read: the attach socket lives for one gate run and keeps
no state for a late joiner, and the MCP run registry is per process.

## The constraint that shapes the design

Background feedback is advisory; every other warden artefact is evidence. The
risk is the recurring one in a new place — an editor showing green for a
snapshot that is not the one on disk, or a status file mistaken for something
the gate consults. Two rules follow:

- **Freshness is computed by the reader, not asserted by the writer.** A
  producer says what it observed and when. Whether that still describes the
  repository is a question about now.
- **Nothing that decides reads it.** The gate, `verify`, notes and execution
  requirements are indifferent to the feedback document.

## Decision

1. **One coalescing watch loop for both modes**, so an edit during a run
   cancels and re-queues instead of being swallowed. `--staged` keeps its
   contract (signed local predictions, never notes). Live mode under a container
   config runs in a disposable dependency-free worktree holding a working-tree
   snapshot built through a temporary index, so `.git/index` is never touched,
   and writes no predictions.
2. **The environment is part of the snapshot.** A cached sampler re-inspects the
   image at most every 15 s; `ImageID|platform|runtime` joins the fingerprint,
   so a retag re-evaluates.
3. **Environmental failures retry, they do not complete.** Backoff 5 s → 60 s,
   reported as `state: blocked`. Nothing staged reports `idle`. The watcher
   yields while the gate's attach socket is connectable.
4. **A versioned status document, `warden.feedback/v1`**, written atomically to
   `<git-dir>/warden/feedback/status.json`, read through
   `warden feedback status [--format json|sarif|problems]`. The reader
   recomputes the snapshot id and adds `freshness`
   (`current | stale_snapshot | stale_environment | producer_gone | expired`).
   `steps` describes only the current snapshot id; old results are never carried
   forward under a new id.
5. **Parse only what is already structured.** Structured findings and, opt-in
   (`diagnostics:`), a SARIF 2.1.0 file the command declares. Everything else is
   one finding with the tool's verbatim output and no file or line: a guessed
   location is a claim warden did not observe. SARIF ingestion happens only for
   a failing step, rejects symlinks and paths outside the worktree, and is
   capped at 4 MiB.
6. Exposed to agents as the read-only MCP tool `feedback_status` over the same
   reader.

## Alternatives considered

| Option | Verdict |
|---|---|
| SARIF as the source of truth | Export only. No idiomatic home for running/blocked/stale/heartbeat; most findings have no location. |
| LSP server | Deferred. Long-lived process, document sync warden does not need, and auto-launch would run repo-authored commands on folder open. Can be a thin adapter over the reader later. |
| VS Code extension | Deferred, out of tree. Buildable entirely on the reader. |
| Warm-container reuse | Rejected. Per-execution freshness is part of what a prediction asserts. |

## Consequences

- `diagnostics:` SARIF ingestion is a config field, so it needs the shipping
  release on every machine before it is used (see "A warden config field is a
  breaking change").
- Container ownership: containers carry `warden-<pid>-<nonce>` labels and a
  dead-owner sweep removes leftovers. Owner identity is the machine id stored
  next to the signing key, not hostname+pid, so two machines on a shared engine
  cannot sweep each other's live containers.
- Apple's container CLI cleanup failure is reported as a note, not a FAILED run.
