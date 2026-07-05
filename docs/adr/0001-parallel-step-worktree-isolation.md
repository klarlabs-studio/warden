# ADR 0001 — Worktree isolation for parallel steps

- Status: **Accepted** (Phase 1 implemented)
- Date: 2026-07-05

## Context

warden validates a commit in **one disposable worktree per run** and hands that
single directory to every step (`runner.go` → `StepContext.WorktreeDir`). The
batch scheduler (`domain.ResolvedPolicy.Batches`) then runs any consecutive
steps for which `Concurrent(s)` is true **in parallel, in that shared
directory**.

The original `Concurrent(s)` was:

```go
return s != StepRebase && s != StepPush && p.AutoFixBudget(s) == 0
```

i.e. it used *"has no auto-fix budget"* as a proxy for *"read-only."* That proxy
is wrong for **coding-agent steps** (`review`, `document`, `intent`, or any
step a rule assigns an agent to): they run arbitrary `agent_commands` via
`sh -c`, and agents routinely **edit files**, yet they carry budget 0 and were
scheduled `Concurrent`.

Notably, the kernel already classifies step *effect* (`kernel.stepEffect`):
only `intent` and `test` are `EffectReadLocal`; everything else — including
`review`, `document`, `lint` — is `EffectWriteLocal`. So the scheduler's
"read-only" notion was **inconsistent with warden's own effect model**.

### Failure

The default pre-push schedule batched `[review, test, document, lint]` to run
together. A `document` agent writing files raced `test`/`lint` reading them (and
raced `review`) in the same directory — non-deterministic corruption, and the
"never disturbs a clean tree" isolation guarantee silently broken. Severity:
**medium** (requires a config with writing agent steps parallel to checks), but
it undermines a core promise.

## Decision

**A step may run in a parallel batch only when warden can be confident it does
not mutate the tracked tree.** Concretely, the following are treated as
tree-writers and run as **sequential barriers** (alone in the worktree), exactly
as `rebase` and auto-fix steps already did:

1. **Built-in coding-agent steps** — `intent`, `review`, `document`
   (`StepName.IsAgentStep`).
2. **Custom steps a rule assigned an agent to** (`ResolvedPolicy.AgentFor(s) != ""`).
3. **Steps declared under the new `writes:` config key** — the escape hatch for
   a custom shell step (codegen, a formatter) that edits tracked files:

   ```yaml
   writes: [codegen]   # runs as a barrier; a concurrent lint never sees a half-written tree
   ```

Everything else — `test`, `lint`, and custom commands the author owns (assumed
checks, same contract as before) — still parallelizes.

### Default step order

Making agents barriers would fragment `test`‖`lint` in the previous default
order (`…review, test, document, lint`), because the `document` writer sat
*between* the two checks. The default pre-push order is therefore regrouped so
the writing agents come first and the read-only checks stay consecutive:

```
intent, rebase, review, document, test, lint
```

so the checks share one parallel batch and validate the tree after the agents
have shaped it.

## Alternatives considered

- **Per-step ephemeral worktrees (full isolation).** Give every parallel-batch
  step its own worktree cloned from a canonical worktree's current state; discard
  their writes. *Rejected for now* — real cost (N worktrees/batch) and subtle
  seeding of post-barrier dirty state, to isolate steps that by contract should
  not write at all. It only pays off to run *write-capable* steps in parallel,
  which warden has no reason to do. Kept as a documented future (Phase 3) if that
  need ever arises.
- **Tie scheduling strictly to `kernel.stepEffect`.** More accurate in principle
  but that model is conservative (`lint` is `EffectWriteLocal`), so it would
  over-serialize real read-only checks, and it lives in the infrastructure layer.

## Consequences

- **Correct by construction:** a writing step can no longer overlap a reader or
  another writer in the shared worktree.
- **Perf:** agent-heavy pipelines lose agent parallelism (agents are few and
  slow — an acceptable trade for correctness). The common `test`‖`lint` batch is
  preserved by the default reorder.
- **Behavior change:** the default pre-push step *order* changed; a repo with an
  explicit `steps:` list is unaffected. Users who interleave a writer between two
  checks in a custom list should group their checks for parallelism.
- **New config surface:** `writes: [step…]` (unions across `extends`, so a base
  cannot silently drop a writer marking).

## Follow-ups

- **Unify the tree-writing signal — DONE.** `ResolvedPolicy.WritesTree` is now the
  single source of truth; both `Concurrent` (scheduling) and the kernel's
  `stepEffect` (axi `EffectLevel`) derive from it, so they can never drift again.
  A step writes the tree iff it is a rebase, a coding-agent step, a rule-assigned
  agent, declared under `writes:`, or carries a positive auto-fix budget. (Safe:
  axi's `IsWriteEffect` has no callers — only the external axis triggers a pause,
  which is unchanged for local steps.)

- **Phase 3 — per-step worktrees — DONE.** Each step in a parallel batch runs in
  its own ephemeral worktree cloned from the canonical one (`Worktree.Clone`,
  `StepContext.WorktreeFor`, `worktreeRegistry`); clones are torn down after the
  batch, so a step's writes are discarded. The scheduler no longer serializes
  agents: a step is a barrier only when its **writes must be kept**
  (`ResolvedPolicy.KeepsWrites` = rebase, an auto-fix budget, or `writes:`);
  everything else — including finding-producing agents (`review`/`intent`/
  `document`) — is *isolatable* and runs concurrently, each in its own worktree.

  Two consequences worth noting:
  1. **To keep a step's tree writes, declare it** — give it an auto-fix budget or
     list it under `writes:` (either makes it a barrier in the canonical
     worktree). An un-declared agent's writes are discarded, so e.g. a `document`
     agent that must persist docs needs a budget. This also *scopes the pre-commit
     auto-fix capture* correctly: only barrier steps touch the canonical worktree,
     so `DiffSince` no longer sweeps up an isolatable step's incidental writes.
  2. **Cost:** N ephemeral worktrees per batch × materialize-by-default = N
     dependency copies. Cheap on the same filesystem (hardlinks); set
     `symlink_deps: true` to make the per-clone dependency exposure O(1).

  `WritesTree` (kept as the kernel's effect signal = `KeepsWrites` + agents) and
  `KeepsWrites` (scheduling) are both derived from one place, so they can't drift.
