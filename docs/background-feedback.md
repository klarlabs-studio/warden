# Background feedback

`warden watch --staged` (see [predictive staged checks](predictive-cache.md)),
and `warden watch` under a container configuration ([live mode](#live-mode)),
write what they observe to a document that editors and other tools can read:

```sh
warden feedback status --format json       # the document, with freshness
warden feedback status --format problems   # file:line lines for editors
warden feedback status --format sarif      # a SARIF 2.1.0 log
```

The command reads the watcher's last document for the current worktree and
fills in `freshness` itself. Use it from anything that can run a command: a
VS Code task, Vim's quickfix list, or a script (see
[editor recipes](#editor-recipes)). Agents read the same thing through the MCP
tool `feedback_status` or `warden axi feedback` (see [agents](#agents)).

Starting the watcher stays a user action (a terminal, a task), because it runs
the repository's configured commands. An editor integration should read the
document, not start the watcher, and should honour workspace trust.

## Live mode

Under a container configuration (`execution.image`), `warden watch` without
`--staged` checks the working tree as `git add -A` would stage it: tracked
files as they are on disk, deleted files gone, untracked files included,
ignored files left out. `--once` evaluates once and exits.

Each evaluation:

- computes the snapshot's tree from a private copy of the index
  (`GIT_INDEX_FILE`), so it never writes `.git/index` and never takes
  `index.lock`. A commit made while a check runs is unaffected. Every git
  command the watcher starts runs with `GIT_OPTIONAL_LOCKS=0`;
- checks that tree out in a disposable worktree seeded at HEAD, without host
  dependencies, and runs the read-only shell checks there: configured
  commands for `lint`, `test` and custom steps in `steps.pre_commit`. Agent
  steps, `security-scan`, native steps and steps that write the tree
  (`auto_fix`, `writes:`) do not run;
- saves no prediction, applies no fix, signs nothing and writes no note.

The watcher's polls only walk the files and stat git's metadata; they write
nothing. The snapshot is computed when that fingerprint changes. Computing it
stores the contents of changed and untracked files as loose objects in the
repository, which `git gc` collects once nothing refers to them. Content the
store already holds is not written again, so a change that leaves the snapshot
as it was (an ignored file, `git add`) writes nothing, and does not run the
checks again.

The document's `mode` is `live`, and its `snapshot.tree` is the working
tree's. The reader computes the same snapshot to judge freshness, in a
temporary object store, so asking never grows the repository.

Host-mode live watch (no container configuration) still re-runs the commands
in the live tree and writes no document.

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
start time. A second watcher in the same worktree, staged or live, exits 75 and
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
| `stale_snapshot` | the checked tree (the staged one, or in live mode the working tree), HEAD, branch or effective configuration changed since the watcher observed them |
| `producer_gone` | the watcher did not stop cleanly, and its process is gone or its heartbeat is older than 15 seconds |
| `stale_environment` | the watcher saw the container environment change (image rebuilt or retagged, runtime switched, engine stopped) after the snapshot was evaluated, and has not started evaluating it yet |
| `expired` | a passing step's staged prediction is past its 10-minute expiry, so the gate would run the check again |
| `current` | none of the above: the document describes the worktree as it is now |

The first row that applies wins. `stale_snapshot` comes first because results
for another snapshot say nothing about this one, whoever produced them. The
reader recomputes the snapshot from git and the configuration, using a
private copy of the index so it never takes `index.lock`, and a temporary
object store so it never writes to the repository. It never probes a
container engine, which is why only the watcher can report
`stale_environment`. The watcher refreshes `producer.heartbeat_at` every 5
seconds.

A watcher that stops cleanly (Ctrl+C, SIGTERM, or the end of `--once`) sets
`state` to `stopped`. Its results still describe the snapshot, so the reader
can report them as `current` until the worktree changes.

`warden feedback status` exits 1 when no watcher has written a document for
the worktree, when the document is not a valid `warden.feedback/v1`, or when
the current snapshot cannot be computed, whatever the format. It exits 2 for a
bad invocation.

## Diagnostics and freshness

The `problems` and `sarif` formats show results as diagnostics only while
they describe the worktree as it is now:

| `freshness` | diagnostics |
|---|---|
| `current` | shown |
| `expired` | shown, with a note: only a passing step's prediction expired, and the failures still describe this snapshot |
| `stale_snapshot`, `stale_environment`, `producer_gone` | withheld, with the reason |

An editor that re-reads the output therefore clears old diagnostics when the
worktree moves, instead of pointing at lines that changed.

## The `problems` format

One line per item, in the form most editors' problem matchers already read:

```text
warden: feedback complete, current (staged, branch feat/x): 3 failed, 1 passed
cmd/main.go:12: error: [lint/errcheck] Error return value is not checked
warden: test failed: test failed (exit 1)
.warden.yaml:4: error: [test/config-anchor] test failed: test failed (exit 1)
warden: vet failed: vet failed
```

- The first line is always printed. It names the state, the freshness, the
  mode, the branch and how many steps are in each status. When diagnostics are
  withheld, or a prediction expired, a `warden: <freshness>: …` line says why.
  The watcher's own message, if any, follows as `warden: watcher: …`.
- `<file>:<line>: <severity>: [<step>/<rule>] <message>` is printed only for a
  finding that names a file and a line. Severity is `error` (high), `warning`
  (medium) or `info` (low, info). Paths are relative to the repository root.
  The message is cut to its first line, so a tool's multi-line output cannot
  forge more diagnostics; the full text is in the JSON.
- A failing step with no located finding prints
  `warden: <step> failed: <summary>`. That covers every failing shell check
  that declares no SARIF report, because warden passes a command's output
  through verbatim rather than guessing a location from it.
- When the failing step's command is declared under `commands:`, a second line
  anchors it to that line of the configuration, marked with the rule
  `config-anchor`. The anchor follows `extends:`, so it names the file whose
  command is in force (a child's command overrides its base's). It is where
  the step is configured, not where warden saw a problem, and it appears only
  in this format: the JSON's findings never gain a `file` or `line` from it.
  A step whose command no file declares (a built-in step, or a command a
  compliance profile supplies) has no anchor.
- A step the machine stopped (a stopped engine, a missing toolchain, a tool's
  lock) prints `warden: <step> could not run (<blocker>): <summary>`, never
  a failure of the change.
- Steps that passed, were cached, skipped, pending or still running print
  nothing beyond the count in the first line.

## The `sarif` format

A SARIF 2.1.0 log with one run, for tools that import SARIF:

- Each finding of a failing step is a `result` with `kind: fail`, its rule as
  `ruleId` when it has one, and `level` `error`, `warning` or `note`.
- A result has a location only when warden observed one: a relative path is
  resolved against `originalUriBaseIds.SRCROOT` (the worktree), with
  `region.startLine` when the finding names a line. A finding without a file,
  such as a failing command's verbatim output, has no `locations`, and
  `properties.located` is false. A failing step with no findings is one
  location-less result carrying its summary. No result carries the
  configuration anchor.
- A step the machine stopped is not a result about the code. It is a
  `toolExecutionNotification` (`warden/blocked`) on the invocation, which is
  then marked `executionSuccessful: false`; so is a document in state
  `blocked`.
- `run.properties.warden` carries what SARIF has no field for: `state`,
  `freshness`, `diagnostics` (whether results are shown), the `snapshot`,
  `evaluated_at` and each step's status. When diagnostics are withheld,
  `results` is empty and a `warden/freshness` notification says why.

Some SARIF consumers, such as code-scanning upload services, require every
result to have a location. warden does not invent one to satisfy them.

## Located findings from a SARIF report

warden passes a failing command's output through verbatim and never guesses a
file or line from it. A check that can write SARIF 2.1.0 can say where its
problems are instead. Declare the file its command writes, relative to the
repository root:

```yaml
commands:
  lint: "golangci-lint run --output.sarif.path=out/lint.sarif ./..."
diagnostics:
  lint: out/lint.sarif
```

When `lint` fails, warden reads `out/lint.sarif` from the worktree the check
ran in and adds each result as a finding with its file, line, rule and
severity (`error` is high, `warning` medium, `note` low). The command's own
output stays as the first finding. The results appear wherever findings do:
the gate's output, the document (with `source: sarif`), and the `problems` and
`sarif` formats.

The rules:

- **Only a failing check's report is read.** The exit status is the only
  verdict. A check that exits 0 passes even if its report lists errors, and
  stays eligible for a staged prediction. A check that never ran (another
  process held its lock, its toolchain is missing) has no report to read.
- **Only shell checks**: `lint`, `test`, or a step defined under `commands:`.
  `security-scan` already reads its scanner's report, and the configuration is
  refused if `diagnostics:` names it, an agent step, or a built-in step that
  runs no command.
- **The path must stay inside the repository.** It must be relative, written
  with forward slashes, and contain no `..`. Anything else is refused when the
  configuration loads.
- **No symlinks.** Every component of the path, not just the file, must be a
  real directory or file. Under container execution the check can write
  anything into the mounted worktree, including a link to a host path that the
  container cannot see but warden, reading on the host, could. A symlink
  anywhere in the path means the report is ignored.
- **The report must be from this run.** A file that existed before the
  command ran and was not rewritten by it, such as a report committed to the
  repository, is ignored.
- **At most 4 MiB**, and at most 1000 results become findings.
- **Locations are mapped to the repository.** A URI under the container's
  `/workspace` mount or the worktree's host path is made relative to the
  repository root; a relative URI already is (`uriBaseId`s are resolved
  through `originalUriBaseIds`). A result located anywhere else, on another
  host, or under another scheme is dropped and counted. A result with no
  location keeps none.
- Results whose `kind` is not `fail`, and suppressed results (unless the
  suppression was rejected), are skipped.

A report that is missing, a symlink, too large, unchanged or not valid SARIF
2.1.0 never fails the step on its own. The step's summary says what happened,
for example `lint failed; diagnostics: 2 findings from out/lint.sarif; 1
result outside the repository dropped` or `lint failed; diagnostics: ignored
out/lint.sarif: out is a symlink`, and the same line is printed while the
gate runs.

The findings are ordinary findings of a failing step. They are hashed into
the run's evidence chain like any other step's findings. A failing run
writes no commit note, so they never appear in signed provenance.

`diagnostics:` needs warden 0.38.0 or later on every machine and CI runner
that reads the configuration: older releases reject unknown keys and would
refuse to run the gate at all.

## Agents

The MCP tool `feedback_status` (read-only, so it needs no trust opt-in) and
`warden axi feedback` go through the same reader as the command. Both report
`present` (false when no watcher has written a document, with a message saying
how to produce one), `diagnostics` and a `freshness_note`, and the document or
its steps. `warden axi feedback` exits 1 when there is no document. Neither
starts a watcher or runs a check, and neither is provenance: an agent that
needs to know whether a commit was gated asks `verify`.

## Editor recipes

These read the `problems` format. Run them from the repository root, and start
the watcher yourself in a terminal or a separate task. Re-run the reader
whenever you want fresh diagnostics.

### VS Code

A task with a problem matcher, in `.vscode/tasks.json`:

```json
{
  "version": "2.0.0",
  "tasks": [
    {
      "label": "warden: feedback",
      "type": "shell",
      "command": "warden feedback status --format problems",
      "presentation": { "reveal": "silent" },
      "problemMatcher": {
        "owner": "warden",
        "source": "warden",
        "fileLocation": ["relative", "${workspaceFolder}"],
        "pattern": {
          "regexp": "^([^:]+):(\\d+): (error|warning|info): \\[([^\\]]+)\\] (.*)$",
          "file": 1,
          "line": 2,
          "severity": 3,
          "code": 4,
          "message": 5
        }
      }
    }
  ]
}
```

Run it with **Tasks: Run Task**. The matcher picks up the located findings and
the `config-anchor` lines, with `<step>/<rule>` as the problem's code. The
`warden:` lines, freshness included, stay in the task's terminal. warden's own
test suite runs this regular expression against the format's golden output.

### Vim and Neovim

```vim
set makeprg=warden\ feedback\ status\ --format\ problems
set errorformat=%f:%l:\ %trror:\ %m,%f:%l:\ %tarning:\ %m,%f:%l:\ %tnfo:\ %m,%+Gwarden:\ %.%#,%-G%.%#
```

`:make` fills the quickfix list: located findings and `config-anchor` lines
as entries with a file, line and type, and the `warden:` lines, freshness
included, as text entries. Use `:cgetexpr system(&makeprg)` to read it without
`:make`'s side effects.

### Anything else

Any tool that reads `file:line: message` lines can consume the format: match
`^([^:]+):(\d+): (error|warning|info): (.*)$`, and treat lines starting with
`warden: ` as status text.

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
    "tree": "<tree the index would write; in live mode, the working tree's>",
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

- `mode`: `staged` or `live`. It also tells the reader which tree to
  recompute.
- `state`: `idle` (nothing staged; staged mode only), `pending` (known, not running, e.g. paused
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
  structured finding a step reported, `sarif` for a result read from the
  check's declared SARIF report (see
  [located findings](#located-findings-from-a-sarif-report)), `output` for a
  failing command's verbatim output. Warden does not guess a file or line from
  a tool's output, so an `output` finding has none. Each message is capped at 64 KiB;
  `output_truncated` says one was cut.
- `freshness` is the reader's. A document on disk carries it only as
  `stale_environment`; the reader rejects a document that asserts any other
  value.

Fields may be added within v1. A change to an existing field's name or meaning
is a new schema version.
