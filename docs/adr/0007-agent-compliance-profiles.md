# ADR 0007 — Agent compliance profiles

- Status: **Accepted**
- Date: 2026-10-18
- Roadmap item: "Agent compliance profiles"

## Context

A repository that lets coding agents commit and push has to answer three
questions. Today each answer is spread over half a dozen settings, and any of
them can be weakened without anyone noticing:

1. **What must have run.** `steps:` merges by union, but a rule's
   `then.steps.skip` can drop a mandated step. An agent step with no command is
   skipped, and until recently looked like a pass.
2. **Where it ran.** `execution:` merges field by field, so a child config can
   set `mode: host` or `network: bridge` over an org base that asked for
   containers.
3. **What the agent could do.** An agent step can write to the canonical tree,
   it sees the whole host environment, and through MCP it can approve its own
   run.

The pieces for a real answer already exist: container execution recorded as
signed `Environments`, signed `Contracts`, `verify --requirements` loaded from
protected policy, `signing.required`, Agent Trace notarization, and the
per-machine trust grant for `run_trigger`. What is missing is a **name** for a
known-good combination, a **floor** that `extends:` and `rules:` cannot lower, a
**record** of which combination was in force, and a **check** that CI can run
without trusting the candidate's own config.

The hard part is the one ADR 0003 dealt with. A profile name in a note is a
*claim*. If `verify` accepted `profile: strict` on the strength of the name
alone, warden would be asserting containerised, executed, isolated checks that
it never looked at in the record.

## Decision

### 1. What a profile is

A profile is a **built-in, versioned, immutable** bundle: config defaults plus
*floors* (constraints checked against the merged effective config), selected
with one key:

```yaml
profile: strict        # resolves to strict@1; a bare name never moves to a later version
```

There are three levels in a total order, `permissive < standard < strict`, so CI
can ask for "at least standard". Use cases ("autonomous agent commits",
"regulated repo") are documented as which level to pick, not as extra profiles.

How it is applied:

- The profile is an **implicit lowest base layer**: the extends chain is
  overlaid onto the profile's defaults.
- Floors are then checked against the effective config, both when loading the
  working tree and when resolving a config at a git ref.
- A violation is a config load error that names the field and, where one file
  is responsible, the file that set it.
- The Go type is `ComplianceProfile`, because "profile" already means axi effect
  and idempotency profiles in the kernel.

| Setting | permissive@1 | standard@1 | strict@1 |
|---|---|---|---|
| Required pre-push steps | none | ⊇ `credentials, lint, test` | standard + `security-scan, review` |
| Required pre-commit steps | none | ⊇ `credentials` | ⊇ `credentials, lint` |
| Required steps cannot be skipped by a rule | n/a | load error | load error |
| Commands present for required shell steps | n/a | load error if missing | load error if missing |
| Agent step must actually run | no | yes, for any listed agent step | yes |
| Shell checks run in a container | any | any | `mode` is not host and an image is set |
| Network for shell checks | any | any | `none` only |
| Agent auto-fix and kept writes | allowed | allowed, recorded | forbidden for agent steps |
| Agent tree isolation | off | always a clone; a failed clone fails the step; tracked tree and HEAD guarded | same |
| Agent environment | full | full | a base allowlist plus the bundled preset's own variables; bundled presets only |
| Agent timeout | as configured | 10m default when unset | default as standard; `"0"` refused |
| Container resource limits | n/a | n/a | strict@2 candidate |
| Signing | as configured | `required: true` | `required: true` (ssh signer recommended, not forced) |
| `require_approval` via MCP or axi | auto-approved | the agent surface cannot satisfy it | same |
| `run_trigger` permission | unchanged | unchanged | unchanged |
| Commits that declare an agent | recorded | recorded | a declared agent requires a notarized Agent Trace |
| Default agent for built-in agent steps (`agent:`) | applies when it names a bundled preset | same | same |

A profile **can never grant MCP run permission.** Trust is a per-machine
operator decision, and a cloned repository granting itself trust is the attack
the grant exists to stop. A profile only restricts.

### 2. Enforcement in three layers, each honest about its limits

**L1, local and at load (guards against accidents, not against an adversary):**

- **Ratchet across `extends:`.** `profile` merges as the higher of base and
  child. A child that names a *lower* level is a load error naming both files,
  for example `profile standard in .warden.yaml cannot lower strict inherited
  from org/.warden.base.yaml`. This is louder than `security_scan.mode`'s silent
  keep-the-base on purpose: the author of the child should learn that the line
  they wrote does nothing.
- **Floors on the merged result.** They catch `execution.mode: host`,
  `network: bridge`, a skip of a required step, and `auto_fix` on an agent step,
  however each was introduced.
- L1 cannot stop a developer from editing `.warden.yaml` or pushing with
  `--no-verify`. It fails early; it is not the control.

**L2, the signed record.** New `RunRecord` fields inside the signing payload,
all omitted when empty, and **emitted only when a profile is active**: the
profile reference (name, version, digest), per-step observed outcomes
(executed, cached, predicted, skipped), the approval channel, and observed
authorship signals.

- The profile digest is the digest of the built-in definition. A verifier
  compares it with *its own* table for `name@version`; an unknown version or a
  different digest (a fork that redefines strict, say) fails closed.
- Downgrades behave as in ADR 0003: an older warden drops the unknown fields,
  recomputes the payload and fails the signature, so verifiers that pin a key
  reject what they cannot read. Notes written without a profile stay byte for
  byte what they were.

**L3, CI (this is the control):**

- `warden verify --min-profile <level>` passes only if the note is validated,
  trusted and not external; its profile ranks at or above the minimum with a
  matching digest; and **conformance is recomputed from the observations**, not
  from the name.
- Strict conformance requires `executed` for every required step; container
  evidence with `network: none` for each required shell step; every agent step
  isolated with the tree unchanged; an approval channel other than auto
  wherever approval was required; and an Agent Trace wherever an agent was
  declared.
- A requirements file version 2 may name a minimum profile. An older warden
  rejects version 2, so the action reports `validated=false` and CI runs its
  checks (fail-safe).
- The range gate reads the minimum profile from the **base ref's** config, the
  way it already takes `trusted_keys` from the base. A PR that lowers
  `profile:` is judged by the profile of the branch it targets. As with the
  roster, there is nothing to enforce until a base carries a profile.

**What strict@1 does not prove:**

- That an agent's reasoning was correct. Agent steps produce advisory findings.
- That agent steps had no network. They run on the host; see §3.
- That an approval came from a human. `tty` means "a terminal answered", and an
  agent can drive a pty.
- That a commit without signals was written by a human, or that a commit with
  one was written by an agent. A signal is a declaration.

### 3. Execution boundaries for agent steps

| Boundary | Before profiles | strict@1 | Later |
|---|---|---|---|
| Container | Agent steps run on the host | Still the host, recorded honestly as host | An agent image behind an egress allowlist, recorded with `network: egress`, never `none` (its own ADR) |
| Network | Unrestricted | Unrestricted: an agent CLI must reach its model endpoint | As above |
| Writes | The canonical tree is reachable | Always an ephemeral clone, with the canonical tree and HEAD compared before and after; untracked output allowed; a failed clone fails the step | |
| Execution | A missing command is a skip | A missing command fails the step as a configuration problem (exit 78) | |
| Time | Optional `timeouts:` | Profile default; `"0"` refused | |
| Resources | None | None for agents (they run on the host) | `execution.limits` for shell containers, strict@2 |
| Environment | Everything but a few `GIT_*` variables | A base allowlist (`PATH HOME USER LOGNAME LANG LC_* TERM TMPDIR SHELL`) plus a per-preset table, each name checked against its CLI | `agent_env:` for custom agents |

The enforceable boundary for agent steps in @1 is **authority** (no writes, no
skipping, no self-approval, no ambient environment), not network isolation.
Container plus no network applies to the *deterministic* checks, and those are
what make the gate's verdict trustworthy.

### 4. Detecting agent authorship

Signals are read at gate time over the pushed span and recorded as *observed
declarations*:

| Signal | Attestable as | Not attestable |
|---|---|---|
| Commit trailers that match known agent identities | "Commit X carried trailer T when it was gated" | That the agent wrote it, or that a commit without the trailer was written by a person |
| Environment markers set by agent harnesses (built-in table) | "The gating process ran with marker M" | Who launched the process |
| Agent Trace record | Its digest at gate time | That its contents are true |
| Committer or author identity (`*[bot]`) | Free text | Anything; shown, never relied on |
| Signer key | Which key signed | Whether a person or an agent held it |

In @1 the tables hold only entries a tool documents itself (Claude Code's
`Co-Authored-By` address and `Claude-Session` trailer, its
`CLAUDE_CODE_CHILD_SESSION` and `CLAUDE_CODE_REMOTE` markers, and Gemini CLI's
`GEMINI_CLI`); a tool with no documented marker or trailer has no entry, and
`docs/agent-profiles.md` lists the sources. When the note claims no span, only
the tip's trailers are read. Nothing is recorded when nothing matches: no
field ever states that a person wrote a commit.

**Signals only tighten.** A declared agent adds requirements (strict requires an
Agent Trace). The absence of signals never relaxes anything: strict@1's core
guarantees apply to every commit, so evading detection only removes the trace
requirement. It cannot buy a weaker gate.

### 5. Settled questions

- **Levels, not use-case profiles.** An ordered set is what makes a minimum
  possible.
- **Definitions are immutable.** A bare `strict` means `strict@1` forever;
  tightening ships as `@2` and is never adopted automatically. The parts of
  strict@1 land over several releases (see Sequencing); the definition and its
  digest freeze in the release that first records the profile in signed notes,
  and never change after that.
- **A child naming a lower profile** is a load error.
- **New signed fields** are emitted only when a profile is active.
- **Auto-approval on the agent surface** is refused under a profile; making that
  the default everywhere is a separate proposal.
- **The top-level `agent:`** applies only under a profile and only when it names
  a bundled preset. Turning it on everywhere would start model calls on every
  push in existing repositories.
- **Custom agents under strict** are refused in @1, since their environment
  needs are unknown.
- **Agent steps in containers** are not part of @1.
- **The ssh signer** is recommended under strict, not required.
- **Trailer and environment-marker tables** are built in for @1, not
  configurable.
- **Role-tagged signing keys for agents** are deferred.
- **Strict refuses host cache hits and predictions for required steps**:
  conformance requires `executed`.

## Sequencing

A new key breaks older wardens hard (config parsing rejects unknown keys), so
the reader ships first:

1. A release ships the `profile` key, the floors checkable from config, the
   ratchet, and `policy explain` support. Nothing new is written to notes.
2. Repositories pin that release in CI and on developer machines before any
   config names a profile. The key's documentation says which release it needs.
3. Later releases add the signed record and `--min-profile` (together, so
   verifiers understand what writers emit), the runtime agent boundaries, the
   approval change and authorship signals.
4. Only after notes carrying a profile exist on trunk does CI add a minimum
   profile to `warden-verify` or `warden-gate`.
5. `execution.limits` (strict@2) follows the same order in its own release.

## Consequences

**Good:**

- One reviewed line (`profile: strict`) replaces a fragile combination of
  settings.
- `extends:` and `rules:` can no longer silently lower the floor.
- CI checks *observations*, not a name.
- The skipped-agent over-claim and agent self-approval stop being possible in
  profiled repositories.

**Costs:**

- New signed fields, a new config key with the compatibility cost above, a
  clone per agent step, and stricter failures that teams will hit when they
  adopt.

**Not addressed:**

- Network isolation for agent steps.
- Proving human authorship or human approval.
- Org mandates that do not go through a committed base or protected CI policy.
  This is by design: `extends` stays inside the repository.
