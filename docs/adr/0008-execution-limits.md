# ADR 0008 — Execution limits and strict@2: observed, not requested

- Status: **Accepted** (2026-10-22; open questions resolved with the recommended defaults)
- Date: 2026-10-22
- Builds on: ADR 0007 (compliance profiles), whose table lists "container
  resource limits" as a strict@2 candidate and whose sequencing (step 5)
  requires `execution.limits` to ship readers-first in its own release.

## Context

strict@1 bounds *where* a required shell check runs (a container, `network: none`) but not *how much* it may consume. A runaway `test` (a fork bomb, a leak, an unbounded fuzz loop) can still exhaust the developer's machine or the engine VM, and the signed note says nothing about it.

Adding limits is easy. Adding them **honestly** is the actual design problem, because every provenance bug this project has shipped had one shape: warden stated something it did not observe. Passing `--memory=4g` to an engine is a *request*. What reaches the workload is a separate fact:

- Measured on this machine (Docker Desktop 29.8.2, cgroup v2): with `--memory=512m --cpus=1.5 --pids-limit=128`, the container's own `/sys/fs/cgroup/memory.max`, `cpu.max` and `pids.max` read `536870912`, `150000 100000`, `128`. Without flags they read `max`, `max 100000`, `max`. So the workload can observe its bounds exactly.
- In the same container `nproc` printed `4` and `/proc/meminfo` `MemTotal` the VM's 8 GB: **those do not reflect the limits** and must never be used as evidence on docker-compatible engines.
- Rootless podman on cgroup v1 is documented to *warn and ignore* resource limits. A design that records the flags would record bounds that did not exist. (Not measured here: no podman locally. CI has a podman job — PR 3 verifies it.)
- Apple `container` 1.5.0 exposes `--cpus` and `--memory` (VM-level) and `--ulimit`, but no pids limit. Its service was not running here, so the in-VM view is **unverified** (see Open questions).

A related finding in today's code: `ExecutionEnvironment.Network` is filled from configuration (`container_compat.go:67`, `container_apple.go:85`), i.e. the request, not an observation. Engines honour `--network=none` reliably, so this has not produced a wrong note, but it is the same shape, and the probe below can close it for free (by routes, not interfaces).

## Decision

### 1. Config: `execution.limits`

```yaml
execution:
  image: ghcr.io/acme/checks@sha256:…
  network: none
  limits:
    memory: 4GiB      # binary units only; also caps swap (memory-swap = memory)
    cpus: "2"         # decimal, > 0
    pids: 1024        # integer, > 0
```

- Applies to **container shell steps only**. Host steps and agent steps (which run on the host) are not bounded; the docs say so instead of implying otherwise.
- Overlay through `extends:` follows the existing execution overlay; under a profile the floors (below) stop a child removing a limit.
- Available without a profile: a repo can bound its checks for its own sake. It is recorded in the note only when a profile is active (ADR 0007's rule: new signed fields only under a profile).
- New key ⇒ older wardens reject the config. Ships readers-first (§6).

### 2. Applying

| Runtime | Flags |
|---|---|
| docker, podman, nerdctl | `--memory=<b>`, `--memory-swap=<b>` (no swap escape), `--cpus=<n>`, `--pids-limit=<n>` |
| apple | `--memory`, `--cpus`; **no pids** → see Open question 2 |

The flags are a request. Nothing below trusts them.

### 3. Observing (the part that matters)

The docker path already proves its mount from *inside* the container: the checked script reads a nonce file before the user's command. The limits probe joins that prefix, **before** the user's command runs:

1. read `/sys/fs/cgroup/{memory.max,memory.swap.max,cpu.max,pids.max}` (cgroup v2 only) and `/proc/net/route`;
2. write them to `.warden-observed-<nonce>` in the workspace;
3. warden reads and removes that file after the container exits, parses it, and records it as `Environment.Observed`.

Rules:

- **cgroup v1, missing files, or unparseable values record nothing.** No inference from the request, from `docker inspect`, or from `nproc`.
- An observed bound must be finite and **≤ the requested one** to count; a looser observed value (engine ignored or rounded up) is recorded as observed and fails the clause.
- Network is recorded as observed `none` only when `/proc/net/route` has no routes; otherwise it is `present`. Interface lists are **not** evidence: kernels add fallback tunnel devices to every namespace (the existing container integration test relies on routes for this reason).
- `docker inspect` of the created container (`HostConfig.Memory/NanoCpus/PidsLimit`) is kept as a **cross-check**, not evidence: disagreement between the engine's statement and the cgroup is recorded as a warning. This needs `create` + `inspect` + `start --attach` on docker-compatible engines, as the apple path already does.

Residual (documented, not solved): the probe file sits in the writable workspace, so a *malicious* check command could overwrite it after the probe ran. Forging a passing observation requires an engine that silently ignored the limit **and** a command written to lie about it. strict does not claim to defend against a hostile check command (ADR 0007, "What strict does not prove"); this is added to that list explicitly.

### 4. Gate behaviour

A run must not write a note its own profile refuses (the principle #339 applied to the cache). Under a profile that requires limits, a required shell step whose observation is missing, infinite, or looser than configured **fails the step** with a message naming the runtime and the observed values ("podman rootless on cgroup v1 ignores resource limits; use cgroup v2 or rootful podman"). Without such a profile, limits are applied and a missing observation is a warning only.

### 5. Record, signature, verify

- `ExecutionEnvironment` gains `observed: {memory, swap, cpu_quota, cpu_period, pids, network, source: "cgroup2"}` inside the signature. Emitted only under a profile.
- An older verifier drops the unknown field, recomputes the payload without it and fails the signature: pinned verifiers fail closed by construction (same as ADR 0003, and the 0.39.0 scope field).
- `verify --min-profile strict@2` adds two clauses for each required shell step: *observed finite memory, swap, cpu and pids limits from cgroup v2* and *observed network none*.

### 6. strict@2

`strict@2` = `strict@1` + floors:

- `execution.limits.memory`, `.cpus`, `.pids` set (config floor, checked by `policy explain` and at load);
- a finite `timeout` on each required shell step (warden enforces it itself, so it is directly observed);
- conformance requires the observed clauses of §5.

A bare `strict` keeps meaning `strict@1` forever; `strict@2` is opt-in by name. strict@1's definition and golden digest do not change. strict@2's digest freezes in the release that first lets a config select it.

## Alternatives considered

- **Record the flags.** Rejected: that is the recurring over-claim; podman rootless/cgroup v1 would sign bounds that never existed.
- **Trust `docker inspect`.** Rejected as evidence: it is the engine restating the request, not the workload's view. Kept as a cross-check.
- **Host `ulimit`/rlimits.** Rejected: per-process, does not cover the container workload, no memory bound that matters.
- **`nproc` / `MemTotal`.** Rejected for docker-compatible engines (measured: they show the VM, not the limit). Possibly right for apple's per-container VM — Open question 2.

## Sequencing (readers first, as ADR 0007 §5 requires)

| Release | Ships | Why first |
|---|---|---|
| A (0.44) | `execution.limits` accepted and applied; probe runs and its result is shown in `warden why`/run output but **not** written to notes; verifier understands `observed` and strict@2 conformance; `profile: strict@2` **refused** at load ("needs warden ≥ B") | every reader and every machine must parse the key and the field before any note carries it |
| B (0.45) | notes record `observed` under a profile; `profile: strict@2` selectable; gate fails a required step whose observation does not meet strict@2 | writers only after readers |

The `execution.limits` key itself is the breaking part for *configs*: per the "a warden config field is a breaking change" lesson, the docs and changelog of A state that every machine and CI runner needs A before a repo adds the key.

## PR breakdown

1. Domain: `ExecutionLimits` type, parsing (binary memory units, decimal cpus), validation, overlay, `policy explain`; `strict@2` definition behind a load refusal; golden digest test added but marked unfrozen until B.
2. Runtime apply: flags per runtime; docker-compatible path moves to `create`/`inspect`/`start --attach`; label/sweep unchanged.
3. Probe + observation: script prefix, nonce file, parser (cgroup v2 only), network from `/proc/net/route`; warnings on missing/looser observation; inspect cross-check. CI container jobs (docker, podman, nerdctl) assert observed == requested with real engines; podman job also runs a cgroup-v1-style case if the runner allows, otherwise a fake engine.
4. Verifier: `observed` in the record type, strict@2 conformance clauses, `why`/MCP/axi rendering. **Release A.**
5. Writer: record under a profile; gate failure under strict@2; lift the strict@2 load refusal; freeze the digest. Golden-fleet scenario: strict@2 repo with a fake engine that ignores limits → step fails, no note; real docker → note verifies `--min-profile strict@2`. **Release B.**

## Failure modes and test strategy

- *Engine ignores a limit* → observed `max` → step fails under strict@2, warning otherwise. Test: fake engine that drops the flags.
- *cgroup v1 host* → nothing observed → same as above. Test: probe fed v1 layout.
- *Engine rounds a limit* (cpus quota granularity) → observed ≤ requested passes, larger fails. Test table for rounding both ways.
- *Probe file missing* (command deleted it, container killed) → nothing observed. Never fall back to the request.
- *Old verifier* → pinned fails closed, unpinned ignores (documented, as for `scope`).
- Assert the artefact: the golden-fleet scenario reads the note's `observed` block, not the exit code.

## Open questions — resolved 2026-10-22: all recommended defaults accepted

1. **Should strict@2 cap the values** (e.g. memory ≤ 8 GiB)? *Default: no.* Require finite, repo-chosen limits; caps are policy a team can express in its own base config.
2. **Apple `container`** has no pids limit, and its in-VM view (cgroup vs `MemTotal`/`nproc`) is unverified. *Default: strict@2 refuses the apple runtime with a clear message until a pids bound is observable there; verify the VM view before deciding whether apple can satisfy memory/cpu.* Needs `container system start` on a machine where that is acceptable.
3. **Observed network under strict@1 notes**: start recording `observed.network` for strict@1 runs too (a new signed field under an existing profile, so readers-first again)? *Default: no; strict@2 only.* Keeps strict@1 notes byte-stable.
4. **Shell-step timeout in strict@2**: *Default: yes, require a finite `timeout` on required shell steps* — cheap, and warden observes it directly.
5. **Name**: `execution.limits` vs `execution.resources`. *Default: `limits`* (matches ADR 0007 and engine vocabulary).

## What strict@2 still does not prove

Everything strict@1 lists, plus: that a hostile check command did not overwrite its own observation after the probe ran; that agent steps (host) were bounded; that limits held on hosts warden did not run on.

## As built

Recorded after the fact, so this ADR says what shipped as well as what was
decided. Where the two differ, the code and CHANGELOG are authoritative.

- **0.44.0 shipped §1–§6 in one release**, not the two the sequencing table
  plans: `execution.limits`, the in-container probe, signed observations and
  `strict@2`. Signed observations are an explicit opt-in,
  `execution.attest_observations`, rather than "emitted only under a profile";
  `strict@2` turns it on and refuses an opt-out. Observations are signed per
  step in `RunRecord.Observations`, not inside `ExecutionEnvironment`.
- **The probe streams its reading before the command starts**, replacing the
  workspace probe file of §3. That narrows the residual risk §3 describes: the
  check command no longer has a file it could overwrite.
- **Protected requirements version 3** (`observed_limits`) let CI enforce
  specific numeric ceilings; `strict@2` itself only requires finite bounds, as
  open question 1 decided.
- **Decision 4 was left out of `strict@2`.** §6 lists "a finite `timeout` on
  each required shell step", and open question 4 accepted it, but the
  `strict@2` definition published in 0.44.0 does not include it. A published
  version never changes (ADR 0007; the golden digests in
  `internal/domain/profile_test.go`), so it ships as **`strict@3`**:
  `strict@2` plus a load-time floor refusing a required shell step whose
  `timeout` is missing or `"0"`, and a signed `deadlines_ms` map recording the
  deadline warden's executor applied to each step that executed. `verify
  --min-profile strict@3` requires a recorded deadline for every required
  pre-push shell step. Only `strict@3` notes carry the field, so every other
  note keeps the payload released verifiers check.
