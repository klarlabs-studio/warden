# Monorepo mode (preview)

> **Preview.** This page describes the first, read-only part of monorepo
> mode: `warden workspaces`. Nothing in the gate uses workspaces yet. Steps
> still run for the whole repository, notes are unchanged, and the output
> format of `warden workspaces` may change between releases.

Monorepo mode teaches warden what a workspace is, so that later releases can
run per-workspace checks for only the workspaces a change affects, and record
that it did. This release adds the two parts everything else builds on:
discovering the workspace graph, and computing an affected selection. You can
inspect both with `warden workspaces`.

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

The selection rules are fixed in this release. Configuring additional global
paths, paths to ignore and what an unowned path does comes with the
`workspaces:` configuration block in a later release.

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
module), and a dependency cycle between workspaces.

## What a selection does not cover

A selection is only as complete as the manifests. If one workspace reads
files of another without declaring a dependency on it, a change to the second
does not select the first. That is why a later release will record in the
signed note which workspaces were checked, and verifiers will refuse such a
partial record by default.
