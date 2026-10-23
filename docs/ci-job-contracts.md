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
The existing verification action and default GitLab verification mode remain
compatible.

## GitHub Actions

The composite action `.github/actions/warden-plan` installs a released binary
with the shared checksum-verifying installer. Pin the action to a reviewed full
commit SHA and select Warden v0.44.0 or newer.
Older binaries or invalid output disable reuse. The action needs Bash, Git, jq
and realpath on the runner. Installation failures fail the planner job.

Keep the workflow, action reference, trusted key and policy source protected.
Do not execute workflow code from an untrusted pull request or use
`pull_request_target` to run candidate commands with secrets. Provision the
policy outside the candidate checkout, for example through reviewed runner
configuration at `/protected/ci-jobs.json`.

```yaml
jobs:
  plan:
    runs-on: ubuntu-latest
    outputs:
      valid: ${{ steps.warden.outputs.plan-valid }}
      reusable: ${{ steps.warden.outputs.reusable-jobs }}
      commit: ${{ steps.warden.outputs.commit }}
    steps:
      - uses: actions/checkout@<reviewed-full-commit-sha>
      - id: warden
        uses: klarlabs-studio/warden/.github/actions/warden-plan@<reviewed-full-commit-sha>
        with:
          policy: /protected/ci-jobs.json
          key: ${{ vars.WARDEN_TRUSTED_KEYS }}
          commit: ${{ github.sha }}
          warden-version: 0.44.0
  unit-tests:
    needs: plan
    if: >-
      ${{ always() && (needs.plan.result != 'success' ||
          needs.plan.outputs.valid != 'true' ||
          needs.plan.outputs.commit != github.sha ||
          !contains(fromJSON(needs.plan.outputs.reusable || '[]'), 'unit-tests')) }}
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@<reviewed-full-commit-sha>
      - run: go test ./...
```

Every job needs its own explicit membership test. Keep always-run jobs
unconditional. Do not build the entire required job inventory from `run-jobs`:
that list is unknown when planning fails. Required status checks may need an
always-running aggregate job when conditional jobs are used.

Outputs are `plan-valid`, `reusable-jobs`, `run-jobs`, `commit` and
`verification-ms`. The summary reports measured verification overhead and
eligibility counts; it does not measure actual skipped jobs or saved CI time.

## GitLab CI

The existing component opts into planning with `job-policy`. It requires
`on-unverified: run-checks` and cannot combine planning with the global
requirements/profile inputs. The default verification mode is unchanged.
Select a reviewed component revision. The component defaults to v0.44.0;
v0.43.0 cannot produce plans and safely enables no reuse.

Provide policy through a protected File-type CI/CD variable outside the
candidate checkout. Ensure untrusted pipelines cannot replace policy, keys,
component revision or downstream job scripts. The runtime needs jq and realpath
in addition to the component's existing dependencies.

```yaml
include:
  - component: <trusted-component-location>@<reviewed-revision>
    inputs:
      key: '$WARDEN_TRUSTED_KEYS'
      job-policy: '$WARDEN_JOB_POLICY'
      warden-version: 0.44.0
      on-unverified: run-checks

unit-tests:
  needs:
    - job: warden-verify
      artifacts: true
  script:
    - !reference [.warden-planned-job, before_script]
    - if warden_job_reusable unit-tests; then exit 0; fi
    - go test ./...
```

The producer writes `warden-plan.json` only after validating a complete plan.
Planning never sets global `WARDEN_VALIDATED=true`. The predicate requires an
explicit literal job name, a matching checkout SHA and an untracked regular
artifact file. Missing, malformed, stale or unsupported plans keep checks
running. Download the artifact only from the trusted producer through `needs`;
a candidate-supplied artifact is not a trusted plan. The helper exits the job
script early; GitLab still schedules that job, so this avoids its checks rather
than claiming to eliminate runner startup or billed minutes.
