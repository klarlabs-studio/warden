# Monorepo mode (preview)

> **Preview.** Monorepo mode is arriving in steps. This release discovers
> workspaces (`warden workspaces`), computes affected selections, and runs
> steps defined under `per_workspace:` once per workspace. At pre-commit
> they run for the workspaces the staged change affects (with
> `select: affected`); at pre-push they run for every workspace. Notes record
> each workspace's result as evidence, but no workspace scope yet, so every
> note still verifies as a full run (see
> [how verifiers treat a partial record](#how-verifiers-treat-a-partial-record)).
> Configuration keys and output formats may still change between releases.

Monorepo mode teaches warden what a workspace is, so that it can run
per-workspace checks for only the workspaces a change affects, and, in a later
release, record in the signed note that it did. You can inspect discovery and
selection with `warden workspaces`, and see what a hook runs per workspace
with `warden policy explain`.

## Listing workspaces

```console
$ warden workspaces
4 workspace(s) at 3f1c2a9b0d4e (graph sha256:…)
  PATH            NAME         ECOSYSTEM  DEPENDS ON
  apps/api        api          js         -
  apps/web        web          js         packages/ui
  packages/ui     @acme/ui     js         packages/utils
  packages/utils  @acme/utils  js         -
```

`--json` prints the same graph with its digest.

## Selecting affected workspaces

```console
$ warden workspaces --affected --base origin/main
measured from 8df03dd176a1 (merge-base of HEAD and origin/main) to 9df3ceae9dc5; 1 changed path(s)
affected: 3 of 4 workspace(s)
  apps/web        depends on: packages/ui
  packages/ui     depends on: packages/utils
  packages/utils  changed: packages/utils/index.ts
selection sha256:…
```

The change is measured from the merge-base of `HEAD` and `--base`, so commits
that landed on the base branch after yours forked are not counted. Without
`--base`, warden uses the remote's default branch (`origin/HEAD`). `--json`
prints the full selection: every changed path, the rules applied, both graph
digests and both configuration digests.

A workspace is selected when a changed path lies in it, or when it depends,
directly or through other workspaces, on one that is. A path belongs to the
workspace with the longest directory containing it, so a nested workspace owns
its own files.

Selection is conservative. All workspaces are selected (`full`), and the output
names the reason, when any of these holds:

| Rule | When |
|---|---|
| `configuration` | `.warden.yaml` changed, or the effective configuration (including any `extends:` base) differs from the one at the merge-base, or that one could not be read |
| `invalid-path` | a changed path cannot be attributed to a directory |
| `root-manifest` | `package.json`, `pnpm-workspace.yaml`, `go.work` or `go.mod` at the repository root changed |
| `lockfile` | a lockfile at the repository root changed (`pnpm-lock.yaml`, `package-lock.json`, `yarn.lock`, `go.work.sum`, `go.sum`, …) |
| `global` | a changed path matches a shared build setting (`.npmrc`, `turbo.json`, `nx.json`, `tsconfig.base.json`, …) |
| `unowned` | a changed path belongs to no workspace, for example a CI workflow or a doc at the root |

A workspace the change deletes is listed under `removed`, and the workspaces
that depended on it are still selected.

The `workspaces:` block in `.warden.yaml` adjusts these rules, and
`--affected` reads it from the configuration committed at HEAD:

```yaml
workspaces:
  discover: [js, go]
  global: ["tools/build/**"]          # also select every workspace
  ignore: ["docs/**", ".github/**"]   # unowned paths that select nothing
  unowned: full                       # default; `ignore` lets every unowned path select nothing
```

`global` only adds to the built-in paths, and `ignore` never exempts a
workspace's own files, `.warden.yaml`, a root manifest, a root lockfile or a
global path. A configuration that differs from the merge-base's forces a full
selection, so editing these rules never narrows the selection of the change
that edits them. Older wardens reject the `workspaces:` and `per_workspace:`
keys, so add them only once a release that knows them runs on every machine
and CI runner that reads the configuration.

## Running checks per workspace

A step defined under `per_workspace:` is one step, listed in `steps:` like any
other, that runs once per workspace:

```yaml
steps:
  pre_commit: [lint, test]
  pre_push: [rebase, test, typecheck]
workspaces:
  discover: [js, go]
  select: affected        # pre-commit runs only affected workspaces
  ignore: ["docs/**"]
  concurrency: 4          # optional
per_workspace:
  test:
    each: "pnpm test"     # once per selected workspace, in its directory
    ecosystems: [js]      # optional: only these ecosystems' workspaces
  typecheck:
    batch: "turbo run typecheck {workspaces.filter}"   # one call for all of them
```

A step is defined under `commands:` or under `per_workspace:`, never both. A
`per_workspace:` definition takes precedence over the built-in `lint` and
`test`, and a custom step needs no `commands:` entry.

### Which workspaces run

| Hook | `select: all` (default) | `select: affected` |
|---|---|---|
| pre-commit | every workspace | the workspaces the staged change affects, and their dependents |
| pre-push | every workspace | every workspace |

At pre-commit, the change is measured from `HEAD` to the tree the step runs
against: the staged change, plus any fix that an earlier step whose writes are
kept (`rebase`, `auto_fix`, `writes:`) made. Both graphs are read from git
objects, and the [selection rules](#selecting-affected-workspaces) apply. In
particular, a change to `.warden.yaml`, or a configuration in force that
differs from the one committed at `HEAD` (an unstaged edit, or an `extends:`
base), runs every workspace.

Every workspace also runs when a step whose writes are kept runs at or after
the first per-workspace step, or when a per-workspace step keeps its own
writes. Its writes would land after the selection was made, so the selection
would describe a different tree from the one committed. Put writers before
per-workspace steps to keep affected selection.

Pre-push always runs every workspace. Running only affected workspaces there
needs a trusted full run to measure from and a scope recorded in the signed
note, so that verifiers can tell a partial check from a full one. A later
release adds both.

Only the ecosystems named in `workspaces.discover` take part. A change in a
directory that belongs to an ecosystem not listed is a path no workspace owns,
and with the default `unowned: full` it runs every workspace.

When nothing is selected (for example, a change only to ignored docs, or no
selected workspace in the step's `ecosystems`), the step passes as **skipped**
and says why. It does not count as a check that ran.

### Commands and placeholders

An `each:` command runs in the workspace's directory, and may use
`{workspace.path}` and `{workspace.name}`. A `batch:` command runs once at the
repository root, and may use `{workspaces.paths}`, `{workspaces.names}` and
`{workspaces.filter}`. `{workspaces.filter}` becomes one `--filter=<name>`
argument per workspace, which turbo and pnpm both accept.

warden replaces each placeholder with a single-quoted value, so a name or a
path always reaches the command as one literal word, whatever it contains.
Write placeholders outside quotes of your own: inside `'...'` the value's
quotes would end yours, and inside `"..."` they would be printed literally.

```yaml
each: 'go vet ./... && echo {workspace.name} >> "$LOG"'   # right
each: "sh -c 'echo {workspace.path}'"                     # wrong: nested quotes
```

The same values are in the environment: `WARDEN_WORKSPACE_PATH` and
`WARDEN_WORKSPACE_NAME` for `each:`, and `WARDEN_SELECTED_WORKSPACES` (the
selected paths, one per line) for both modes.

### Results

The step fails when any workspace's command fails. The failure names the
workspace (`step test failed in workspace apps/web`) and carries its output.
Every selected workspace still runs, so one failure does not hide another. A
`batch:` command is one command, so its failure names the batch rather than a
workspace.

Each workspace's result is a `<step>.workspace` record in the run's evidence
chain, so a pre-push note says which workspaces a passing step ran for.

### Concurrency

`workspaces.concurrency` bounds how many per-workspace commands run at once
across all per-workspace steps of a run. The default is half the CPUs (at
least one). Steps that run in parallel share that one bound rather than each
getting their own, so a parallel batch never multiplies it. A per-workspace
step whose writes are kept runs one workspace at a time.

The step's `timeouts:` entry covers all of its workspaces together.

### What a per-workspace step does not do

- It is never cached (`cache:`) and never predicted (`predictive_cache:`). Its
  result depends on the selection as well as on the files, and neither cache
  is keyed by the selection. Either entry for a per-workspace step is a load
  error, and so is a `diagnostics:` file, which it does not read.
- It does not run in `warden watch` live mode, which runs only `commands:`
  checks.
- It records no execution contract, so `verify --requirements` cannot reuse
  it.

## Where workspaces come from

Discovery reads manifests from the commit's git objects. It never reads the
working tree and never runs npm, pnpm, yarn or the go command. A
`package.json` that is not committed is not a workspace, and anyone with the
same commits computes the same graph.

| Ecosystem | Workspaces | Dependencies |
|---|---|---|
| pnpm | directories matched by `packages` in `pnpm-workspace.yaml` that hold a `package.json`; `!` patterns exclude | any of `dependencies`, `devDependencies`, `peerDependencies`, `optionalDependencies` naming another workspace's package, with any version spec, including `workspace:` and `workspace:` aliases |
| npm, yarn | the same, from `workspaces` in the root `package.json` (an array, or yarn's `{"packages": [...]}`); used only when there is no `pnpm-workspace.yaml` | as for pnpm |
| Go | every `use` directory of a root `go.work`, including `use .` | a `require` of another workspace module's path, or a `replace` (in `go.work` or the module's `go.mod`) pointing at another workspace module's directory |

Details:

- Patterns support `*` (within one directory level) and `**` (any number of
  levels). Brace and bracket expressions are refused with an error rather than
  read differently from your package manager.
- `node_modules` directories are never workspaces. The root package is a
  workspace only when a pattern names `.` itself.
- A repository with one `go.mod` and no `go.work` has no Go workspaces: relating
  its packages would need `go list`, which discovery does not run.

Discovery fails, with a message naming the file, instead of leaving a
workspace out. That happens for a manifest that does not parse, a workspace
package without a `name`, a `go.work` `use` outside the repository or without
a `go.mod`, a workspace name with characters outside letters, digits, `.`,
`_`, `~`, `-` and `/` (plus a leading `@scope/`), two workspaces with one name
or directory (including a directory that is both a JS workspace and a Go
module).

Dependency cycles between workspaces are fine. Two packages that list each
other in `devDependencies`, or Go modules that require each other, form a
cycle, and a change to any workspace on it selects every workspace on it,
plus everything that depends on them.

## What a selection does not cover

A selection is only as complete as the manifests. If one workspace reads
files of another without declaring a dependency on it, a change to the second
does not select the first. That is why a later release will record in the
signed note which workspaces were checked, and why verifiers refuse such a
partial record by default. Until a warden writes one, affected selection
applies only at pre-commit, where nothing is signed.

## How verifiers treat a partial record

No warden writes a partial record yet. This release teaches every verifier
to read one, so that the verifiers in your CI are ready before any warden
that writes them ships. **Upgrade every verifier, then the developers'
machines**: a pinned verifier older than this release refuses every note that
records a scope, full ones included (see below).

A repository that configures `workspaces:` will record a signed `scope` in
each note: `full` when every workspace was checked, `affected` when the
per-workspace steps ran only for the workspaces a change affected. The scope
names the selection algorithm version, the trusted full run an affected
selection was measured from, the tree, the configuration and graph digests,
the selection digest, every workspace, the workspaces each per-workspace step
ran for, and why each was selected. A note without a scope is a full run, as
every note written today is.

An affected record claims less than a full one: "these workspaces were
checked on this tree; the others were checked by an earlier trusted full run,
and nothing they declare a dependency on has changed since". So, by default:

| Verifier | An affected note |
|---|---|
| `warden verify`, with or without `--key` | not validated; exits 1 with `affected-scope` and the scope, such as `affected: test[3/12 workspaces]` |
| `warden verify --range` | the commit fails with reason `affected-scope`; commits inside its push span fail with the same reason and name it as their covering note |
| `warden verify --requirements` | refused before any contract is compared |
| `--min-profile`, or a version 2 requirements file | conforms to no profile; the failing clause is "the note attests a full run, not an affected-workspace selection" |
| `warden attest --predicate vsa` | `FAILED`, with `WARDEN_SOURCE_AFFECTED_*` levels and never `WARDEN_SOURCE_GATED` |
| `warden reattest` | carries the scope unchanged, so the relocated note stays affected and refused |
| `warden why`, `warden axi verify`, the MCP `verify` tool | show the scope, and that verifiers refuse it |

No flag accepts an affected note in this release. Accepting one safely needs
the verifier to recompute the selection itself, from rules read at a trusted
base rather than from the commit under test; that is planned as a separate
option.

A scope the verifier cannot read as full is treated as affected: an unknown
kind, a selection algorithm version newer than the verifier's, or a recorded
sparse checkout.

The scope is inside the signature. Deleting it from a signed note, or changing
`affected` to `full`, breaks the signature, and a pinned verify refuses the
note as tampered. A warden older than this release that pins a key drops the
`scope` field it does not know, computes a different payload and refuses the
note the same way: it fails closed, for full scopes too, because it cannot
tell them apart. A warden older than this release that verifies **without**
a key would read an affected note as validated. Pin a key wherever a verdict
skips work, which `warden-verify` already requires.
