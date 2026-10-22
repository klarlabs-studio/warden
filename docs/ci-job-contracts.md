# Protected CI job plans

`warden ci plan` maps named CI jobs to protected execution requirements. Each
job gets an independent `run` or `skip` decision for one resolved commit. A
`skip` means the observed local execution matches the protected expectations;
it does not mean the forge has actually skipped anything.

## Policy

Keep the policy, trusted keys and planner implementation under protected
control, outside an untrusted candidate checkout. Load them from a reviewed
revision or trusted runner configuration. Never generate expectations from the
candidate note being evaluated. The command requires an explicit `--key` and
does not use the candidate repository's trusted-key roster.

A version 1 job policy maps names to complete
[execution requirements](execution-contracts.md). Requirements versions 1, 2
and 3 are supported. `null` means the job must always run in CI. A reusable job
must require at least one execution contract; a profile-only requirement is
insufficient to establish job equivalence.

```json
{
  "version": 1,
  "jobs": {
    "unit-tests": {
      "version": 1,
      "steps": {
        "test": {
          "command": "go test ./...",
          "configuration_digest": "sha256:<reviewed configuration digest>",
          "context_digest": "sha256:<reviewed step context digest>",
          "inputs": {"go.mod": "sha256:<reviewed file digest>"},
          "image_id": "sha256:<reviewed image digest>",
          "image_id_kind": "config",
          "platform": "linux/amd64",
          "network": "none"
        }
      }
    },
    "integration": null,
    "package": null
  }
}
```

Replace illustrative digests with reviewed `sha256:` identities containing 64
lowercase hexadecimal characters. All the existing requirements comparisons
apply: exact command, effective configuration, step context, complete recognized
dependency-file map, inspected image, platform, network and checked Git tree.
A version 2 or 3 entry can additionally require a profile; version 3 can require
initial observed limits. The policy expresses a team's reviewed equivalence
claim, rather than discovering equivalence from a job name or shell text.
Toolchain expectations must be provided by the reviewed image and inputs;
Warden does not independently measure every installed runtime or dependency.

Context expectations remain exact, including branch and changed-file inputs.
This initial job planner does not normalize those inputs or generate policies.
A mismatch safely leaves a job running and explains the difference.

## Plan

```sh
warden ci plan --commit "$CANDIDATE_SHA" \
  --policy /protected/ci-jobs.json --key "$TRUSTED_FINGERPRINT" --json
```

Text output is useful while adopting contracts:

```text
CI plan for <resolved-sha> (verification 24ms)
integration: run — job requires CI execution
package: run — job requires CI execution
unit-tests: skip — matching trusted commit-bound execution contracts
```

JSON version 1 includes:

| Field | Meaning |
|---|---|
| `sha` | The resolved commit evaluated, not a moving branch name |
| `jobs` | Alphabetically ordered name, action, reasons and `verification_ms` |
| `run_jobs` | Alphabetically ordered jobs that must run |
| `reusable_jobs` | Alphabetically ordered jobs eligible for reuse |
| `verification_ms` | Total measured planner time, including shared verification |

Per-job times measure requirement evaluation; they exclude shared note fetching
and signature verification. Millisecond rounding can produce zero. No field
claims saved minutes, actual forge job status or continuous container isolation.

The planner fetches and verifies notes once, then applies every job's
requirements to that same snapshot. It checks trusted commit binding, local
full-scope provenance, and execution contracts using the existing verifier.
Missing notes, invalid signatures, external attestations, partial scope,
missing contracts and mismatches cannot make a job reusable. Explicit cached or
skipped step outcomes contradict a fresh contract and are refused.

## Consume safely

Exit zero means **a complete plan**, even when every job must run. It does not
mean validation passed. Invalid policy or missing explicit keys exits 2;
operational failure exits nonzero and emits no JSON plan. The consumer must
run its checks or fail on any planner error, unsupported JSON version, invalid
output, missing job, unexpected action or SHA mismatch. Never treat absence
from `run_jobs` alone as permission to skip.

For an existing CI job, this conservative shell pattern only skips an explicit
matching decision. `plan.json` must be a fresh output file from this invocation:

```sh
if warden ci plan --commit "$CANDIDATE_SHA" \
    --policy /protected/ci-jobs.json --key "$TRUSTED_FINGERPRINT" \
    --json > plan.json; then
  if jq -e --arg sha "$CANDIDATE_SHA" '
    .version == 1 and .sha == $sha and
    ([.jobs[] | select(.name == "unit-tests")] |
      length == 1 and .[0].action == "skip")
  ' plan.json > /dev/null; then
    exit 0
  fi
fi
# Any failure, absent decision or mismatch runs the ordinary job.
go test ./...
```

Supply the full candidate commit SHA, and keep the ordinary CI command under
protected control too. A job absent from policy remains a CI obligation.
The same plan can drive a matrix, provided a protected inventory supplies the
complete job set and every unrecognized or missing decision defaults to run.
The existing GitHub verification action and GitLab component remain compatible;
this increment supplies a CLI planner, not new automatic forge wiring.
