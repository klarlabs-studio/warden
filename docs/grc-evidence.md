# Using warden as audit evidence (SOC 2, ISO 27001)

`warden audit` answers a developer's question: is this branch's provenance
intact. `warden evidence` answers an auditor's:

> For the period under review, what changed, which changes went through the
> control, and what are the exceptions?

Same underlying records — deliberately, because two commands that could
disagree about what happened would make both useless.

```bash
# The artifact you hand an auditor
warden evidence --from 2026-01-01 --to 2026-03-31 > q1-change-gate.md

# The same thing for a GRC platform to ingest on a schedule
warden evidence --from 2026-01-01 --to 2026-03-31 --format json

# NIST OSCAL assessment-results, for tooling that speaks it
warden evidence --from 2026-01-01 --to 2026-03-31 --format oscal

# Narrow the control mapping
warden evidence --frameworks soc2
```

## What makes it evidence rather than a report

**A population, not a sample.** Every commit in the window appears, classified
into exactly one of four states — gated and verified, covered by a gated push,
exception, outside the control. An auditor can reconcile the total against
`git log` and get the same number. This matters more than it sounds: the usual
failure of home-grown evidence is a list of the changes that went well.

**Exceptions with specific reasons.** Every commit warden could not vouch for
is listed with *why*, distinguishing cases that look identical from the outside:

| Reason | What it means |
|---|---|
| no warden note | Pushed with `--no-verify`, or committed outside warden. A genuine bypass. |
| never pushed | The pre-push gate was never reachable. Not a bypass — nothing to bypass. |
| unattributable | Gated by a warden too old to record a push span; a gated intermediate commit and a bypass are indistinguishable, and the information was never written down. |
| note does not attest | A record exists but is unbound, chain-broken, or empty. |
| squash-merge binding gap | The content demonstrably was gated under a different commit id. Remediable with `warden reattest`. |

Rows that offer a remediation are to-dos. Rows that do not are findings.

**Limits stated on every control.** See below.

**Independently verifiable.** The report asserts the verdicts; it is not the
proof. The proof is the signed records in the repository, and the report prints
the command that re-checks them without trusting the document or whoever
produced it:

```
warden verify --range <adoption>..main
```

**A stable digest.** The evidence digest covers the population and its
verdicts, not the timestamps around them. Re-running the report over the same
window reproduces the same digest; a different one means the underlying records
changed. That is what lets someone tell an edited report from a re-run one.

## What it does not evidence

This is the part that decides whether an auditor keeps reading. Evidence is
rejected for claiming too much far more often than for proving too little.

warden observes a machine-checked gate on source changes. It does **not**
evidence:

- **That the configured checks are adequate.** warden runs what `.warden.yaml`
  defines. Whether that set is sufficient is a separate control with separate
  evidence.
- **That a second person reviewed the change.** warden records the gate, not
  the review. Approval and separation of duties are evidenced by your forge's
  branch protection and pull-request review records — pair the two.
- **That production corresponds to these commits.** Deployment is a downstream
  control.
- **That the signing key was held by an authorized person.** Key custody and
  offboarding are access-control evidence, not warden's.
- **Anything before the adoption commit**, when the control did not operate.

Each mapped control repeats its own limits rather than relying on this list,
because a limit in a general disclaimer is a limit somebody skips.

## Control mapping

Short on purpose. Every control here is one where the gate's output is *direct*
evidence; a longer list built on "adjacent to" is how an evidence package stops
being read.

**SOC 2**

| Control | Evidenced | Not evidenced |
|---|---|---|
| CC8.1 Change management | The *tested* and *documented* elements, per change | *Authorized* and *approved* — pair with forge review records |
| CC7.1 Detection of vulnerabilities | That the configured scan ran on every gated change, and refused the change when it failed | The scanner's coverage or finding policy |
| CC6.8 Unauthorized software | Detection of changes that bypassed the gate | Preventive enforcement — that is the forge-side required check |

**ISO/IEC 27001:2022**

| Control | Evidenced | Not evidenced |
|---|---|---|
| A.8.32 Change management | A defined procedure with recorded, signed verification, and enumerable deviations | Infrastructure, configuration and data changes |
| A.8.29 Security testing | That configured security testing ran as a precondition of publication | Depth or currency of those tests |
| A.8.25 Secure development life cycle | That an enforced machine-checked stage exists, with evidence of operation | Design review, threat modeling, dependency governance |

## Feeding a GRC platform

`--format json` emits a versioned document (`"schema": "warden.evidence/v1"`)
carrying the population, exceptions, controls, assertions, digest and
verification command. Most platforms accept a custom-evidence upload; run it on
a schedule and attach the output:

```bash
warden evidence --from "$(date -v-1m +%Y-%m-01)" --format json > evidence.json
# then POST evidence.json to your platform's custom-evidence endpoint
```

Keep the `assertions.unsupported` field when you ingest. A platform that
displays the population without the limits turns an honest artifact into an
overclaim.

`--format oscal` emits NIST OSCAL `assessment-results`: gated commits become
observations, exceptions become findings, and the limits ride in the result
remarks. Identifiers are derived from content rather than randomly generated,
so consecutive periods can be diffed — which is most of what a
continuous-monitoring program does with these.

## A worked caveat

Run it against a repository that adopted warden recently and the "outside the
control" count will be large. That is correct and worth leaving visible: the
control did not operate then, and evidence that implied otherwise would be the
first thing to fall apart under questioning.
