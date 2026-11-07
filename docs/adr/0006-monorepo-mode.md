# ADR 0006 — Monorepo mode: workspace graph, affected selection, scoped provenance

- Status: **Accepted, implemented except PR 7 and PR 8** (v0.39–v0.41; user-facing description in `docs/monorepo.md`)
- Date: 2026-10-18

## Context

warden composed monorepos as one `cd <path> &&` step per detected ecosystem,
run on every push, and let commands scope themselves through
`WARDEN_CHANGED_FILES`. The second route was already an over-claim: a `test`
that checks only changed packages writes `steps_run: [test]` into a signed note
that looks identical to a whole-repo `test`, and `verify` never inspects
`StepsRun`. ADR 0003 named this exact problem for external runs. Affected
selection would build it into the product unless the record states what was
selected and verifiers refuse to read it as a full check.

Constraints from the code: `RunRecord.SigningPayload` marshals every field, so
an older warden drops an unknown field and fails the signature (pinned-key
verifiers fail closed by construction); `carriedOriginalHolds` requires a
re-attestation payload to equal the original's; span coverage only lets a note
close a span if it passes `verdictFor`; dependency drift and SBOM collection
must read the committed tree, not the filesystem.

## Decision

1. **Discovery reads git objects and never executes anything.** `git ls-tree` +
   `cat-file --batch` on the tree being checked; no package manager, `go list`,
   `nx` or `turbo`. Untracked or ignored files cannot change the graph and a
   verifier rebuilds the same graph from the same objects. Ecosystems: JS
   (`package.json` workspaces, `pnpm-workspace.yaml`), Go (`go.work`, module
   `replace`), Cargo (`rust`), and Nx `implicitDependencies` edges only. Nested
   and `.` workspaces resolve by longest prefix. Dependency cycles are allowed;
   the graph is never assumed to be a DAG. Edges are the canonical sorted JSON
   whose digest is `GraphDigest`.
2. **Selection is a pure, conservative domain function.** Changed paths use
   `--name-status -z --no-renames`, so a rename affects both sides. First rule
   that forces **full** wins: `.warden.yaml` or its effective configuration
   digest changed; a workspace-root manifest or root `go.mod`/`go.sum` changed; a
   path matches `workspaces.global`; a path is owned by no workspace and not in
   `workspaces.ignore` (`unowned: full` is the default). Otherwise
   affected = changed workspaces ∪ reverse-transitive dependents, computed on the
   union of the base and head graphs. Property-tested: adding a changed path
   never shrinks the selection, and any full-forcing rule yields every workspace.
3. **Config.** `workspaces:` (`discover`, `select: all|affected`, `global`,
   `ignore`, `unowned`, `concurrency`) and `per_workspace:` steps (`each` or
   `batch`, placeholders from an allowlist, every value shell-quoted because a
   manifest in a PR is attacker-controlled input). A step is either repo-wide or
   per-workspace, never both. `WARDEN_CHANGED_FILES` stays newline-joined.
4. **Provenance.** A signed `RunScope` (kind `full|affected`, anchor, head tree,
   configuration/graph/selection digests, workspaces, per-step selection) is
   written whenever `workspaces:` is configured; a repo without it writes notes
   byte-identical to before. The anchor is the nearest first-parent ancestor
   with a trusted-signed, `Attests`-valid, `kind: full` note with the same
   configuration digest, so an affected record never rests on another affected
   record. Affected scope requires a signature; `--attest-only` is always full.
5. **Verification fails closed.** `ScopePolicy`'s zero value rejects affected
   records (`ScopeRefused`, so delivery can say "partial check" rather than
   "unverified"); `verify --range` inherits this through `verdictFor`;
   `verify --requirements` refuses non-full scope; `reattest` carries the scope
   byte for byte; the VSA predicate never reports `WARDEN_SOURCE_GATED` for an
   affected record. Status renders `test[3/12 workspaces]`.
6. **Readers before writers.** The verifier side (5a) shipped in 0.39.0 and the
   writer (5b) only after that release, so no verifier could meet a scoped note
   it did not understand. The CI action exposes `scope` and `validated-workspaces`
   from single-commit `verify --json`.

## What an affected record claims

"These workspaces were checked on this tree; the others were checked by trusted
full run `Anchor`, and no file in them, in their *declared* dependencies, or in
any global path has changed since." It assumes manifests declare every
dependency; undeclared cross-workspace reads are not detected. That is weaker
than a full run, which is why verifiers refuse it by default. An older warden
with no pinned key would still accept the note; this is documented, and bounded
by the signing requirement.

## Deviations and deferrals

- **`SelectionVersion` was not bumped for Cargo/Nx.** They only widen the
  selection (more root/global paths), and a bump would make released verifiers
  refuse every scoped note.
- **pnpm importer attribution is deliberately not built.** Hoisting makes
  attributing a lockfile change to one workspace unsound; lockfiles stay global.
- **PR 7 (`--accept-affected`, inductive acceptance) and PR 8 (sparse worktrees)
  are deferred** until real use / a benchmark. Inductive acceptance must
  recompute selection from git objects using rules from the trusted base ref,
  never the candidate's config; otherwise a long-standing `ignore: ["**"]` plus
  one old full anchor would skip checks forever. Sparse mode would be opt-in,
  refused whenever unsafe (rebase, writers, agent steps, containers), and gated
  on a benchmark, because pnpm's hoisted `node_modules` dominates run time and
  sparse checkout does not shrink it.

## Alternatives considered

- Native selection (`turbo --filter`, `nx affected`): a verifier cannot rebuild
  it. warden selects and passes the list through `{workspaces.filter}`.
- A separate notes ref for affected records: older verifiers would report each
  as a bypass, the recurring defect pointed the other way.
- Affected→affected chains: unbounded verification, compounding error.
- One step per workspace (`test@web`): breaks `StepName` validation and
  requirements keyed by step.
