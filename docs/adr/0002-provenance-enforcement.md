# ADR 0002 — Closing the provenance enforcement loop

- Status: **Proposed** (Phase 1 in progress)
- Date: 2026-07-06

## Context

warden's *production* half is complete: a pre-push run validates a commit in a
disposable worktree, then fast-forwards + pushes itself and writes a signed,
commit-bound, evidence-chained `RunRecord` to `refs/notes/warden` (`runner.go`,
`domain/runrecord.go`). The *consumption* half — something that **rejects** a
commit lacking trustworthy provenance — is where the value leaks out.

What exists today, and why none of it is a sound gate:

| Command | Range | Exit semantics | Gap as an enforcement gate |
|---|---|---|---|
| `verify` | **one** commit (`--commit`) | 0 if `Attests`, else 1; `--key` pins trust | single-commit only; framed as CI *skip* (speed), not *range gate* |
| `audit` | `adoption..branch` | **always 0** (report) | informational by design |
| `doctor` | `adoption..branch` | 1 if `unverified > 0` | **leaky** (below) |

`warden doctor` is the closest thing to a gate, and it has three holes:

1. **Tampered notes pass.** `AuditReport.Counts()` classifies a commit with a
   note as `verified` regardless of chain integrity; doctor exits non-zero only
   when `unverified > 0` (note *absent*). A commit whose note exists but whose
   evidence chain is **broken** (`HasNote && !ChainIntact`) does not trip the
   gate. Only *absence* is caught, not *tampering*.
2. **Any self-signed note passes.** doctor never checks signatures or a trusted
   key. An attacker generates their own ed25519 key, runs warden on their own
   machine, and produces a note that `Attests` — doctor is satisfied. doctor
   proves *"a warden ran here,"* never *"a warden I trust ran here."*
3. **Fixed `adoption..branch` range.** There is no way to gate an arbitrary
   `base..head` (e.g. `origin/main..PR-head`), which is exactly what a PR
   required-check needs.

And above all of it: **nothing server-side runs any of these.** The whole model
is opt-in local hooks plus a CI step the repo chooses to add. `git push
--no-verify`, an uninstalled hook, or simply not wiring the check bypasses
everything. Per the fleet's own history, the "Warden provenance" check is a
non-failing reporter — requiring it enforces nothing.

### The squash-merge break

Even with a perfect local gate, the dominant GitHub workflow defeats it.
warden signs commit `X` on a feature branch; GitHub's **Squash and merge**
creates a *new* commit `Y` (new SHA, new tree) on `main` with **no note**. So
`main` accrues un-provenanced commits even when every developer ran warden
faithfully, and a naive `doctor` on `main` flags `Y` forever. warden gates
*direct pushes* but is blind to *platform merges*.

## Decision

Build the **enforcement half** in three phases. Each phase is independently
shippable and useful; later phases depend only on the primitive from Phase 1.

### Phase 1 — `warden verify --range <base>..<head>` (the primitive)

A true range gate, distinct from `doctor`'s leaky one:

- Verifies **every** commit in `base..head`. A commit passes only when its note
  `Attests` (chain intact **and** commit-bound) — closing hole #1: a broken or
  transplanted note **fails**, it is not counted as "verified."
- `--require-signed` requires a *valid signature*; `--key <roster>` requires the
  signature to be from a **trusted** key — closing hole #2. Without them the
  gate degrades gracefully to "attested but unsigned is allowed," matching
  today's default so nobody's CI breaks on upgrade.
- **Exit non-zero if any commit fails**, with a per-commit reason
  (`missing` / `broken-chain` / `unsigned` / `untrusted`). `--json` emits the
  full per-commit verdict for CI.
- **Merge commits**: skipped by default (`--skip-merges`, on) — a true merge's
  parents are each gated, and the merge itself introduces no tree change warden
  authored. `--no-skip-merges` to require them.
- `--base` defaults to the merge-base with the configured default branch when
  only a head is given, so `warden verify --range` "just works" on a PR branch.

This hardens the existing gate *and* is the building block for Phases 2–3. It
touches only read/verify paths — **no change to the push/sign path**, so the
security-critical production side is untouched.

### Phase 2 — server-side enforcement wrappers

- A shipped **GitHub Action** running
  `warden verify --range origin/<base>..<pr-head> --require-signed --key <roster>`
  as a **required status check** on PRs. It runs on the PR *head* — which still
  carries the note — so it gates the **merge** (the check must pass before the
  squash happens). This is the pragmatic answer to the squash break: enforce
  *before* the platform rewrites history, not after.
- A **pre-receive hook** recipe for self-hosted Git (Gitea/GitLab/bare) that
  runs the same range verify and **rejects the push** — the genuine server-side
  gate where we control the server.

### Phase 3 — managed trust + post-merge closure

- A committed, itself-signed `.warden/trusted-keys` roster so `--key` is a repo
  fact with a rotation story, not a hand-passed CLI argument.
- Optional **merge-time re-attestation**: a bot that, after the Phase-2 check
  passes, re-signs the squash commit `Y` so `main` is provenanced end-to-end and
  `doctor` on `main` stays green.
- Emit **in-toto / SLSA source attestations** as an interop format, so warden
  provenance feeds sigstore / GUAC / policy engines instead of being a
  warden-only note shape.

## Consequences

- **Positive:** the notes warden already signs finally have a consumer that can
  *fail*; the gate stops accepting tampered or untrusted notes; PR-based teams
  get a merge gate that survives squash; self-hosted gets a real pre-receive
  gate.
- **Cost:** Phase 2/3 introduce an org trust-roster concept and a hosted
  component (the Action / bot). Kept out of the core binary — the binary only
  gains a pure, read-only `verify --range`.
- **Non-goal:** we do **not** change the always-exit-non-zero push behavior or
  the signing path. Enforcement is added strictly on the *verification* side.

## Status / rollout

Phase 1 lands first as `warden verify --range` with `--require-signed`,
`--key`, `--json`, `--skip-merges`. Phases 2–3 follow as separate deliverables
once the primitive is proven.
