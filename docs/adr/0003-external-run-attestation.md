# ADR 0003 — Attesting an external run

- Status: **Proposed**
- Date: 2026-08-02
- Issue: [#177](https://github.com/klarlabs-studio/warden/issues/177)

## Context

`provenance-main.yml` (0.22.0–0.23.1) closes a real gap: warden's gate is
client-side `pre-push`, so a commit the forge creates on merge — a GitHub
squash-merge, a web edit, a merged Dependabot PR — is a **new object warden
never saw**. On the fleet this was measured against, *every one* of the eleven
remaining "bypassed" commits was committed by GitHub, not by a person evading
anything.

It closes that gap by **re-running warden's configured steps** against the
merged tree. But `.warden.yaml` describes those very commands as mirrors of CI:

```yaml
lint: "golangci-lint run ./..."     # Mirror CI stage 1
test: "go test -race ./... && …"    # Mirror CI unit + e2e stages
```

So every merge to `main` runs lint + security + race tests **twice** — once in
the shared CI, once in the attest job — on the same tree, for the same result.
It also forced a second copy of the tool version pins into this repo, which is
the drift problem #112 documents.

The cost is accepted today because the alternatives available *today* are worse.
This ADR is about making a better one available.

## The thing that makes this hard

warden's note asserts: **"these checks ran and passed on this tree."** That
assertion is what `verify` consumes and what a required check enforces.

Two shortcuts suggest themselves, and both are wrong for the same reason:

1. **Attest after a `workflow_run`, executing nothing.** The note claims checks
   this job never ran.
2. **Attest with a reduced step set** the bare runner can manage. A note
   recording only `credentials` is *indistinguishable at `verify` time* from one
   recording the full gate.

Both manufacture provenance rather than record it. That is the defect class
0.21.2 → 0.23.1 were spent removing — warden asserting more than its evidence
supports — and re-introducing it deliberately, in the name of CI minutes, would
undo the point of the tool.

So the requirement is not "attest without running". It is: **record honestly
that someone else ran the checks, in a form a consumer can tell apart from
warden having run them itself, and can independently check.**

## Precedent in the record

`ReattestedFrom` already does exactly this shape of thing:

> The evidence below is carried over from that run and this record is re-signed
> locally, so a re-attestation is transparent: it asserts "the same validated
> content, under a new commit id" — **never a fresh validation.**

An external-run attestation is the same move along a different axis: same
commit, different *executor*. The design should follow it.

## Decision

Add an optional `ExternalRun` reference to `RunRecord`, inside the signed
payload, and make it change what the note asserts — visibly.

```go
// ExternalRunRef names the platform run that executed the checks, when warden
// did not execute them itself.
type ExternalRunRef struct {
    Provider   string `json:"provider"`              // "github-actions"
    RunID      string `json:"run_id"`
    Attempt    int    `json:"attempt,omitempty"`
    URL        string `json:"url,omitempty"`
    Repository string `json:"repository"`            // owner/name, as the provider names it
    // Commit is the SHA the external run executed against. It MUST equal the
    // record's CommitSHA: a run against a different tree proves nothing about
    // this one, and accepting a mismatch is how "CI passed" becomes "some CI
    // passed, somewhere".
    Commit string `json:"commit"`
    // Checks names what the external run reported, so the note is specific
    // about what was covered rather than asserting a bare "CI passed".
    Checks []string `json:"checks"`
}
```

### What the note asserts, and how a consumer tells

With `ExternalRun` set, the note asserts: *"the signer vouches that run X on
platform P reported these checks passing for this commit"* — **not** *"warden
executed these checks"*.

`verify` gains an explicit policy, defaulting to the stricter reading:

| flag | accepts |
|---|---|
| *(default)* | local attestations only — an external one is **not** validated |
| `--allow-external` | either |
| `--require-external` | external only (for a CI-attested branch policy) |

Defaulting to local-only is the fail-closed choice: every existing consumer of
`verify` today means "warden ran the checks", and silently widening that on
upgrade would weaken every gate in place without anyone opting in.

### The downgrade problem, and why the format already solves most of it

An older warden reading a note with `external_run` ignores the unknown JSON
field. The question is whether it then accepts a *weaker* claim as if it were
the strong one.

Checking rather than assuming: `SigningPayload` marshals **the struct the
reading binary knows**.

```go
func (r RunRecord) SigningPayload() ([]byte, error) {
    r.Signature = ""
    return json.Marshal(r)
}
```

An old binary drops `external_run` on unmarshal, recomputes the payload without
it, and gets **different bytes** — so signature verification fails. That splits
consumers cleanly:

| consumer | behaviour on an external note it cannot understand |
|---|---|
| signature-checking (`--key`, `--require-signed`, or a `trusted_keys` roster) | **rejects** — fail-closed, correct |
| `Attests()` only (no roster, no `--key`) | accepts — evidence + chain + binding ignore unknown fields |

So the exposure is narrower than "every old verifier": it is exactly the
consumers who never pinned a signer. Which yields a cleaner rule than a second
notes ref:

> **An external-run attestation MUST be signed. warden refuses to write one
> unsuppressed.**

That makes old verifiers fail closed *by construction* — the same mechanism that
already protects `agent_trace` and every other field added since binding — and
costs nothing at read time. A consumer with no roster was already accepting "any
warden ran here" rather than "a warden I trust ran here"; this design does not
widen that, and the signing requirement means the new claim never reaches them
in a form they would misread as local.

(The earlier draft of this ADR proposed a separate `refs/notes/warden-external`
ref for this. Reading `SigningPayload` showed the payload mechanism already
provides the guarantee, and a second ref would have added a fetch, a push, and a
migration for protection that exists.)

### What makes the external claim trustworthy

Phase 1 records the reference and signs it with the CI key. That is honest —
"the holder of this trusted key says run X passed" — but the claim about run X
is only as good as the key holder, exactly like every other note.

Phase 2 binds the platform's own evidence, so the claim is checkable without
trusting the signer:

- **GitHub OIDC**: the job's ID token carries `repository`, `sha`, `run_id`,
  `workflow_ref`, signed by GitHub and verifiable against its JWKS. Recording
  the token's claims plus its signature digest makes "run X, on repo Y, for
  commit Z" independently confirmable.
- **Artifact attestations**: `actions/attest-build-provenance` produces a
  sigstore-backed statement that can be referenced by digest.

Phase 2 is where this stops being "trust the CI key" and starts being evidence.
It shares its dependency with ADR-0002 Phase 2.5 (keyless OIDC signing) and
should land with or after it.

### Interaction with `--attest-only`

They stay distinct, because they assert different things:

- `--attest-only` — **run** the steps, write the note, push nothing. Unchanged.
- `--attest-external` (new) — do **not** run the steps; record the external run
  reference. Refuses unless `ExternalRun.Commit == HEAD`.

Sharing one flag would make the strong and weak claims a runtime coin-flip, and
the whole point is that they must not be confusable.

## Consequences

**Good.** The duplication in `provenance-main.yml` goes away: one CI run, and
the attestation records what actually ran. The pin duplication (#112) goes with
it, since the attest job no longer needs the toolchain. The note distinguishes
"warden ran the checks" from "CI ran the checks" — which was never expressible
before, and which is a distinction consumers should be able to make.

**Costs.** A `verify` policy surface that did not exist, and one more way for a
gate to be misconfigured. And an honest reduction in what a CI-attested commit
proves: warden vouches for a report, not for an execution it performed. That is
a real reduction, and it is why the default stays local-only and why the note
says which kind it is.

**Not addressed.** Nothing here lets a *developer's* machine attest an external
run — the reference is only trustworthy when the signer is the platform's own
identity. A laptop asserting "CI run 123 passed" is a claim with no backing, and
this design deliberately does not make it expressible.

## Status of the interim

`provenance-main.yml` keeps re-running the checks until Phase 1 lands. That is
the honest option of the ones available now: the checks genuinely re-ran against
the merged tree, and the note says exactly that.
