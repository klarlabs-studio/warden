# Background feedback

`warden watch --staged` (see [predictive staged checks](predictive-cache.md))
writes what it observes to a document that editors and other tools can read:

```sh
warden feedback status --format json
```

The command prints the watcher's last document for the current worktree, with
`freshness` filled in by the reader. Use it from anything that can run a
command: a VS Code task, a Vim quickfix command, Emacs compile-mode, or a
script.

Starting the watcher stays a user action (a terminal, a task), because it runs
the repository's configured commands. An editor integration should read the
document, not start the watcher, and should honour workspace trust.

## What it is, and what it is not

The document is advisory and unsigned. It is not evidence. The gate,
`verify`, `attest`, notes and execution requirements never open it, and a test
in warden's own suite fails the build if any of them does. A forged document
that claims a pass changes nothing at `warden run pre-commit`.

`cached` in the document means a staged prediction existed for the snapshot
when the watcher ran the step. It does not mean the gate will skip it: the
gate checks the prediction itself, against the tree it is about to commit.

## Where it lives

`<absolute git dir>/warden/feedback/status.json`, one per worktree: each
linked worktree has its own git dir, and so its own document, lock and
predictions. Editors do not need the path; `warden feedback status` resolves
it from the current directory.

Only the watcher writes the document. Gate runs do not. It is replaced
atomically (written to a temporary file, then renamed), so a reader never sees
a partial document. On Windows the rename is retried briefly when a reader
holds the file open; if it still cannot be replaced, the next heartbeat writes
it again.

One watcher per worktree writes it. The watcher holds
`<absolute git dir>/warden/feedback/watch.lock`, which records its pid and
start time. A second `warden watch --staged` in the same worktree exits 75 and
names the pid of the watcher that holds the lock. A lock left by a watcher
that crashed (its process is gone, or it stopped refreshing the lock for 15
seconds) is taken over by the next watcher. A watcher that finds its lock was
taken over (for example after the machine was suspended) stops with exit
code 75 and writes nothing more.

## Freshness

A producer can only say what it observed and when. Whether that still
describes the repository is a question about now, so the reader decides:

| `freshness` | meaning |
|---|---|
| `stale_snapshot` | the staged tree, HEAD, branch or effective configuration changed since the watcher observed them |
| `producer_gone` | the watcher did not stop cleanly, and its process is gone or its heartbeat is older than 15 seconds |
| `stale_environment` | the watcher saw the container environment change (image rebuilt or retagged, runtime switched, engine stopped) after the snapshot was evaluated, and has not started evaluating it yet |
| `expired` | a passing step's staged prediction is past its 10-minute expiry, so the gate would run the check again |
| `current` | none of the above: the document describes the worktree as it is now |

The first row that applies wins. `stale_snapshot` comes first because results
for another snapshot say nothing about this one, whoever produced them. The
reader recomputes the snapshot from git and the configuration, using a
private copy of the index so it never takes `index.lock`. It never probes a
container engine, which is why only the watcher can report
`stale_environment`. The watcher refreshes `producer.heartbeat_at` every 5
seconds.

A watcher that stops cleanly (Ctrl+C, SIGTERM, or the end of `--once`) sets
`state` to `stopped`. Its results still describe the snapshot, so the reader
can report them as `current` until the worktree changes.

`warden feedback status` exits 1 when no watcher has written a document for
the worktree, when the document is not a valid `warden.feedback/v1`, or when
the current snapshot cannot be computed. It exits 2 for a bad invocation.

## Schema: `warden.feedback/v1`

```json
{
  "schema": "warden.feedback/v1",
  "producer": { "warden_version": "0.38.0", "pid": 4242, "started_at": "…", "heartbeat_at": "…" },
  "mode": "staged",
  "worktree": "/abs/checkout",
  "state": "complete",
  "message": "",
  "snapshot": {
    "id": "sha256:…",
    "tree": "<tree the index would write>",
    "base": "<HEAD commit>",
    "branch": "feat/x",
    "configuration_digest": "sha256:…",
    "environment_digest": "sha256:…"
  },
  "evaluated_at": "…",
  "steps": [{
    "step": "lint",
    "status": "fail",
    "summary": "lint failed",
    "blocker": "",
    "started_at": "…",
    "finished_at": "…",
    "environment": { "runtime": "docker", "image": "team-checks:local", "image_id": "sha256:…", "platform": "linux/arm64", "network": "none" },
    "prediction": { "saved": false, "expires_at": null },
    "findings": [{ "severity": "high", "message": "<verbatim output, capped>", "source": "output" }],
    "output_truncated": false
  }],
  "freshness": "current"
}
```

- `state`: `idle` (nothing staged), `pending` (known, not running, e.g. paused
  while a gate run is in progress, with the reason in `message`), `running`,
  `complete`, `blocked` (the machine prevented the evaluation, such as a
  stopped engine; it is retried and is not a verdict), or `stopped`.
- `snapshot.id` covers `tree`, `base`, `branch` and `configuration_digest`:
  what a reader can recompute. `environment_digest` identifies the sampled
  container environment the evaluation started under; it is recorded beside
  the id, not in it.
- `steps` describe only `snapshot.id`. When the snapshot changes, the watcher
  replaces them with `pending` entries; no result is carried over to a new
  snapshot. A step that never started is dropped when the evaluation ends.
- `step.status`: `pending`, `running`, `pass`, `fail`, `cached` or `skipped`.
- `environment` is present only for observed container execution.
- `prediction.saved` is true when this run saved the pass as a staged
  prediction; `expires_at` is when the prediction behind a `pass` or `cached`
  step expires.
- `findings` are warden's findings, each with a `source`: `step` for a
  structured finding a step reported, `output` for a failing command's
  verbatim output. Warden does not guess a file or line from a tool's output,
  so an `output` finding has none. Each message is capped at 64 KiB;
  `output_truncated` says one was cut.
- `freshness` is the reader's. A document on disk carries it only as
  `stale_environment`; the reader rejects a document that asserts any other
  value.

Fields may be added within v1. A change to an existing field's name or meaning
is a new schema version.
