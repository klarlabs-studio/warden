# ADR 0002 — Closing the provenance enforcement loop

- Status: **Accepted** (Phases 1–3 implemented: `verify --range` gate, the
  `warden-gate` action, the `trusted_keys` roster, `warden reattest`, and
  `warden attest` in-toto output)
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

- **Trusted-signer roster (done).** A committed `trusted_keys` list in
  `.warden.yaml` (not a separate `.warden/trusted-keys` file as first sketched —
  riding on `Config` was chosen so it reuses the load/validate/merge machinery
  and, crucially, **inherits through `extends:`**: an org base policy names its
  signers once and every repo unions them in). A bare `warden verify` /
  `verify --range` with no `--key` now requires a trusted signer from the roster,
  so `warden-gate` needs no hand-passed fingerprints. Merge is a union (a child
  may add a signer, visibly, but cannot silently drop an org's). The roster's
  trust anchor is **PR review plus warden's own gate on `.warden.yaml`** — a
  cryptographic self-signature was considered and deferred: it only moves the
  trust root (who signs the roster?) without adding assurance over a reviewed,
  gated, version-controlled file. `warden key list` shows the effective roster.
- **Post-merge re-attestation (done — `warden reattest`, no hosted infra).** The
  original sketch was a GitHub App with a bot signing key; a cleaner design
  replaced it. A squash commit `Y` reproduces the gated PR head `X`'s tree
  *exactly* (`git rev-parse Y^{tree} == X^{tree}`), so `warden reattest` — run
  **locally** by a maintainer whose key is already trusted — finds the
  tree-identical, intact, validly-signed source note, carries its evidence onto
  `Y`, marks it `ReattestedFrom: X`, and re-signs locally. This needs **no new
  trusted CI key and no hosted component** — it's a local vouch over
  byte-identical content, and it fails safe: with no tree-identical validated
  source, it writes nothing (never asserts validation that didn't happen). Closes
  the squash-merge gap so `doctor`/`audit` on the base branch stay green.
- **in-toto / SLSA source attestations (done — `warden attest`).** Projects a
  commit's `RunRecord` into an in-toto Statement v1 with a warden-specific
  predicate type (`https://warden.klarlabs.de/provenance/v1`) — deliberately not
  `slsa.dev/provenance`, since warden attests *source* review/test provenance,
  not *build* provenance. Read-only; wrap in a DSSE envelope / `cosign attest` to
  sign the statement. Feeds sigstore / GUAC / policy engines.

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

Phase 1 landed as `warden verify --range` with `--require-signed`, `--key`,
`--json`, `--skip-merges`. Phase 2 landed as the `warden-gate` composite action
(`.github/actions/warden-gate`) plus a self-hosted pre-receive recipe — see
[docs/ci-provenance-gate.md](../ci-provenance-gate.md). Phase 3 landed in full: the
trusted-signer roster (`.warden.yaml` `trusted_keys` + `warden key list`),
post-merge re-attestation (`warden reattest`), and in-toto output (`warden
attest`). The enforcement loop is closed end to end.
