# ADR 0004 — What warden claims in a VSA's `verifiedLevels`

- Status: **Accepted**
- Date: 2026-08-29
- Supersedes the reasoning in `attest_vsa.go`'s "WHAT IT DELIBERATELY DOES NOT CLAIM"

## Context

`warden attest --predicate vsa` emits a SLSA Verification Summary Attestation
(`https://slsa.dev/verification_summary/v1`). Its `verifiedLevels` carries three
warden-namespaced values and no SLSA level:

```
["WARDEN_SOURCE_GATED", "WARDEN_SOURCE_SIGNED", "WARDEN_SOURCE_TRUSTED"]
```

The code comment justifying this reasons entirely about **build** levels: warden
attests source, not builds, so it must never say `SLSA_BUILD_LEVEL_3`. That
argument is correct and remains so.

It is also no longer the whole question. **SLSA v1.2 introduced a Source
Track**, which is precisely warden's subject matter: it records how a *revision*
was created — whether branch protection was active, whether review requirements
were met, which identities were involved — and has SCSs issue Source VSAs. The
build-level argument does not settle whether warden should claim
`SLSA_SOURCE_LEVEL_n`, because that is a different enum about a different thing.

Two spec requirements bear on this:

- `verifiedLevels` **MUST** include the SLSA source track level the SCS asserts,
  and **MUST** include *only* the highest level met.
- Consumers **MUST ignore** unrecognized values in `verifiedLevels`.

Read together, those have a consequence that was not previously written down:
**a strict SLSA consumer reads warden's VSA as asserting nothing.** All three of
warden's values are unrecognized, so all three are ignored, and no SLSA level
remains. The statement verifies, and conveys no level.

## Decision

**Warden continues to emit only `WARDEN_SOURCE_*` levels, and does not claim a
`SLSA_SOURCE_LEVEL_n`.** The consequence above is accepted deliberately rather
than tolerated accidentally.

The reason is not modesty; it is that warden is **not an SCS**. The source track
levels describe controls enforced by the system that *owns the branch*:

| Source level | Spec summary | Who attests it |
|---|---|---|
| L1 — Version Controlled | "The source is stored and managed through a modern version control system." | "The SCS issues Source VSAs" |
| L2 — Controls | Protected branches and tags; all changes recorded and subject to the org's technical controls | "The SCS generates Source VSAs" |
| L3 — Signed and Auditable Provenance | "The SCS generates credible, tamper-resistant, and **contemporaneous** evidence of how a specific revision was created." | "The SCS issues Source VSAs using provenance attestations as evidence" |
| L4 — Two-Party Review | "Changes in protected branches MUST be agreed to by two or more trusted persons prior to submission." | "The SCS attests through provenance and summary attestations" |

The right-hand column is the decisive one, and it is the spec's own wording at
*every* level: the SCS attests. Every one of these is a property of the forge,
observed at merge time, over a branch. Warden runs as a client-side git hook on a developer's machine. It can
observe that a policy ran against a commit and that the record is signed by a
pinned key. It cannot observe, and must not assert, that a protected branch
required two reviewers — and the local-hook vantage point is exactly the one an
attacker controls if they control the checkout.

A `WARDEN_SOURCE_GATED` value is a claim warden can substantiate from evidence
in its own note. A `SLSA_SOURCE_LEVEL_2` value would be a claim about a system
warden does not run, inferred rather than observed. Emitting it would be this
project's recurring defect — claiming more than the evidence supports — dressed
in a standards-compliant field name, which is the worst version of it: an
over-claim that *looks* like conformance.

## What this costs, stated plainly

Warden's VSA is a **transport for warden's own verdict in a shape SLSA tooling
can parse**, not a conformant Source VSA. A consumer that ingests it gets:

- a subject, digest, and (since this ADR's companion change) `source_refs`
- a verifier identity and `timeVerified`
- a `verificationResult` of `PASSED`/`FAILED`
- levels it is required to ignore

That is genuinely useful — `verificationResult` alone answers "did warden's gate
pass on this commit?" — and it is genuinely less than a Source VSA. Anyone
wanting a source level must get it from the forge.

## What would change this

`warden evidence` already reports branch protection, read from the forge API:
required approvals, stale-review dismissal, conversation resolution, admin
enforcement. That is the *evidence* Source L2 and L4 are about.

It is still not sufficient, for a reason the spec itself names: Source L3 asks
for **contemporaneous** evidence of how a revision was created. Reading branch
protection *now* does not establish what it was **when the revision was
merged**. A repository whose protection was
weakened, used, and restored would report the same as one that never changed.
Warden's branch-protection data is a *current-state* observation; a source level
is a *historical* claim about a specific merge.

Claiming a SLSA source level would therefore require either:

1. the forge attesting its own protection state at merge time (which is what
   SLSA expects an SCS to do, and is not warden's to provide), or
2. warden recording protection state at merge time, continuously, and being
   trusted as the record of what was true then — a much larger claim, and a new
   thing to attack.

Until one of those exists, `WARDEN_SOURCE_*` is the honest vocabulary.

## Consequences

- The `verifiedLevels` values stay warden-namespaced. Consumers ignoring them is
  correct behaviour, not a bug to work around.
- Documentation must not describe warden as SLSA Source Track conformant, or as
  producing a Source VSA. It produces a VSA-shaped statement.
- `verificationResult` is the field to point integrators at, since it is the one
  a conformant consumer will actually read.
- If the Source Track's `verifiedLevels` requirement is ever relaxed to admit
  verifier-specific values alongside a SLSA level, revisit — the shape would
  then let warden say both things without either being a lie.
