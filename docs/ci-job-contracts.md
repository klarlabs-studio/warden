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
          warden-version: 0.46.1
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
Select a reviewed component revision. The component defaults to v0.46.1;
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
      warden-version: 0.46.1
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

## Roll out in shadow mode

Start with the forge consumer's `mode: shadow` (GitHub) or `plan-mode: shadow`
(GitLab) from a reviewed v0.45.0 or newer consumer revision. The default remains `enforce` for existing integrations. Shadow mode
still verifies protected contracts, but writes `action: run` for every job,
puts every policy job in `run_jobs`, and leaves `reusable_jobs` empty. Existing
skip predicates therefore keep running all checks. `eligible_jobs` retains
matching names for analysis only; never use it as a skip condition.

The GitHub action also returns `eligible-jobs` and `plan-json`. Save the latter
as a run artifact using an environment variable, avoiding expression expansion
inside shell source:

```yaml
- id: plan
  uses: klarlabs-studio/warden/.github/actions/warden-plan@<reviewed-ref>
  with:
    policy: /protected/jobs.json
    key: ${{ vars.WARDEN_TRUSTED_KEYS }}
    mode: shadow
- name: Save plan for measurement
  env:
    PLAN: ${{ steps.plan.outputs.plan-json }}
  run: printf '%s\n' "$PLAN" > "$RUNNER_TEMP/warden-plan.json"
```

Upload that file with your reviewed artifact-upload action. Retain the run ID
and original plan; do not regenerate a plan after the workflow finishes.
GitLab already publishes `warden-plan.json` as its plan artifact. Switch to
`enforce` only after inspecting representative shadow runs, especially eligible
jobs that fail remotely. A failure identifies a difference to investigate;
it does not prove the local or remote result was wrong.

## Measure completed GitHub runs

The standalone, read-only `scripts/ci_report.py` uses Python 3.9+ and an
authenticated GitHub CLI with Actions read access. Run the script from a reviewed
Warden revision after the target workflow has completed. It does not change
workflow gates and does not require a new Warden binary. Retrieve the original
plan artifact from that run through a trusted artifact-download path; the
report checks its SHA but does not authenticate an arbitrary supplied plan.
Never consume the report as permission to skip checks.

Provide an explicit earlier successful baseline from the same workflow and a
JSON object mapping every policy name to one exact GitHub display name:

```json
{"unit-tests":"Unit tests","integration":"Integration","package":"Package"}
```

```sh
python3 scripts/ci_report.py \
  --repository klarlabs-studio/warden \
  --run "$COMPLETED_RUN_ID" --baseline "$BASELINE_RUN_ID" \
  --plan /trusted-artifacts/warden-plan.json \
  --jobs /protected/github-job-names.json > report.json
```

The reporter reads all pages from a specific attempt and checks that no rerun
started during collection. It refuses an incomplete run, wrong commit, different
workflow, invalid plan partition, or duplicate JSON keys. Missing or ambiguous
job names, including unexpanded matrices, produce unknown measurements instead
of zero durations. Map each matrix instance separately if its policy contract
represents that instance.

| Report field | Evidence and limit |
|---|---|
| `observed_skipped_eligible_jobs` | Matching policy jobs GitHub reports skipped; unrelated conditions may have caused the skip |
| `eligible_failed_jobs` | Eligible jobs GitHub reports failed or timed out; useful during shadow rollout |
| `observed_mapped_runner_seconds` | Sum of current mapped job timestamp durations; excludes unmapped workflow jobs |
| `observed_runner_seconds_difference` | Baseline mapped duration minus current mapped duration; can be negative and is not attributed to Warden |
| `estimated_eligible_runner_seconds` | Baseline duration for matching jobs; potential work, including in shadow mode |
| `estimated_avoided_runner_seconds` | Baseline duration of explicitly reusable jobs observed skipped; a counterfactual estimate, zero in shadow mode |
| `verification_ms` | Original planner verification measurement; excludes installation and note fetching |

Baseline workload, runner class and caches can differ. These durations are not
billed minutes, wall-clock speedup, local validation cost, or money saved. The
report does not calculate net savings: those require local validation and the
full workflow overhead as well as provider billing data. GitLab currently has
shadow plans but no completed-run measurement adapter.

## Warden's own CI measurement

`.github/workflows/ci-shadow.yml` is an advisory `workflow_run` collector. After
an in-repository `CI` run completes, it reads its retained original plan (when
available) and compares nine
exact job names against successful baseline run `39917037757` (the v0.45.0
release PR). It can also backfill a completed run through `workflow_dispatch`
on `main`. Fork runs are excluded. The collector has read-only repository and
Actions permissions, executes tooling from the reviewed default-branch revision,
and never executes candidate commands. Original-plan artifacts are consumed only
as advisory JSON data; they cannot authorize a gate or a skip.

The protected inventory and mapping live in `.github/warden/shadow-policy.json`
and `shadow-jobs.json`. Every bootstrap policy entry is `null`: local host checks
do not establish equivalence to coverage checks, Linux execution, Windows
compilation, or the container-engine suites. Empty eligibility is intentional.
The benchmark is excluded because it is conditional; totals cover only the nine
mapped jobs, not all workflow overhead, billing, or developer-machine costs.

The artifact retains run metadata, plan and report for 30 days. The summary
shows verifier duration, observed job duration totals, historical differences
and eligible failures. Missing jobs remain unknown. A collector failure reports
measurement failure and cannot change the originating CI result or skip a job.
The historical baseline is explicit; changing it requires review.

The originating `CI` workflow now captures a shadow plan before its ordinary
jobs. The planner and policy come from pinned reviewed revision
`6942148ef6cd8608a5c23ad59130842f0ee27a1b`, outside the candidate checkout.
Review and bump that revision when adopting a new policy. Planner failures do
not suppress the ordinary CI jobs. The additional job introduces installation,
checkout and artifact overhead; existing totals exclude that overhead.

Original plans are retained as `warden-ci-plan-<run>-<attempt>`. The collector
selects the exact attempt and refuses ambiguous or expired matching artifacts.
The report validates commit/run/attempt metadata and corroborates timing against
the successful `Capture shadow plan` job's GitHub timestamps: that job must finish
before every mapped job starts. Missing, ambiguous or skipped jobs produce
`plan_source: capture_timing_unavailable` and a list of exact GitHub job names in
`capture_timing_unavailable_jobs`. Observed durations remain available; missing
matrix children remain unknown rather than being counted as zero. Invalid
metadata and evidence of late capture still fail measurement. These are advisory measurements, not authenticated
execution evidence. A candidate can change its workflow; this path must never
become skip authorization.

Older runs or partial reruns without the matching artifact use a freshly
computed retrospective plan, explicitly labelled `plan_source: retrospective`.
An invalid original artifact fails measurement instead of silently falling back.
`--capture /path/to/capture.json` enables the same validation in
`scripts/ci_report.py`; validated original plans report
`plan_source: pre_ci_capture`, `plan_captured_at` and the additional
`planner_runner_seconds` overhead, separate from mapped-job totals.

Before replacing any `null` entry, review environment and command equivalence.
The [offline Linux pilot](../.github/warden/linux-test/README.md) provides a
pinned image recipe with the Go race runtime, dependency cache and coverage
tooling, a network-disabled shadow run, and an independent per-commit
policy preparer. Each built image's immutable identity and effective
configuration still need protected review before adoption. The shared test job runs
`go test -count=1 -race -covermode=atomic -coverprofile=coverage.out ./...` followed
by the coverctl gate; passing race tests alone cannot replace it. Host macOS
results, different CPU platforms and Apple manifest identities cannot be treated
as an exact match for a Linux amd64 Docker image configuration identity. Keep
all checks enabled while measuring representative original plans and eligible
remote failures before considering enforce mode.

Initial manual measurement of run `39920603496` against baseline `39917037757`
observed all nine jobs: 646 versus 665 summed runner seconds. The 19-second
difference is normal observed variation; eligibility and estimated avoided work
were both zero. It does not demonstrate savings caused by Warden.

### Observed protected test reuse

The collector also reads the target attempt's `warden-test-reuse` plan when
present. `protected_test_reuse` is separate from the bootstrap shadow plan.
It requires a matching candidate SHA, a successful protected verifier finishing
before the test wrapper starts, a successful `Record caller-authorized reuse`
step, and skipped signed-container, host race-test and coverage steps. Missing
or duplicate jobs or steps cannot establish observed reuse. The wrapper job
itself succeeds; it is not a skipped job.

The report retains wrapper time, protected verifier time and historical test
job time separately. `historical_test_runner_seconds_difference` compares the
historical test job with the current wrapper only; it excludes verifier and
local validation costs. It is not a bill reduction or a matched benchmark.
Artifact and step observations are advisory and grant no execution authority.
