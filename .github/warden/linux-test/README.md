# Offline Linux test pilot

This opt-in pilot prepares a Linux environment before a hook runs. Existing CI
jobs and `.warden.yaml` remain enabled and unchanged. No CI savings are claimed.

## Build and execute

From the repository root:

```sh
# Apple silicon; Docker, Podman and nerdctl are also accepted build engines.
sh .github/warden/linux-test/build.sh container linux/arm64
# On a Linux Docker host:
sh .github/warden/linux-test/build.sh docker linux/amd64
```

The build context contains only the recipe, command, go.mod and go.sum. The base
image index, Go version and coverctl version are pinned. Build downloads happen
before validation. Module caches are baked in; validation uses GOPROXY=off,
GOSUMDB=off, GOTOOLCHAIN=local and -mod=readonly. Dependency changes require a new
image. The resulting image identity must be inspected and reviewed separately;
a pinned recipe does not establish identical rebuild outputs.
Python, Node and jq are installed for the reporting, npm and action regression
tests; resolved distro package versions are retained inside the image at
`/opt/warden-distro-packages.txt`. Those distro versions are resolved at build
time, so only the reviewed finished image establishes their exact identity.

Warden runs containers as the host UID, which may have no entry in the image's
passwd database. OpenSSH refuses that identity even when its binaries are
present. The image includes libnss-wrapper; the command supplies private,
temporary passwd/group files for an unmapped UID and checks real SSH key
generation before testing. This leaves the image's account files unchanged.
The image publishing and build-based pilot workflows validate as UID/GID 10001
with HOME=/tmp, rather than relying on a root-only pass. Coverage floors remain
unchanged, including the per-file SSH interoperability floors.

Images from publication run `39990134467` predate this fix. They passed the
root CI run but fail the SSH coverage floors under Warden's unmapped host UID.
Do not adopt those images for local-to-CI reuse. The profiles below pin the
replacement images from run `40009451684`, which passed non-root validation on
both architectures. Old signatures establish the publisher, not suitability for
Warden's non-root execution.

The `Linux test pilot` workflow runs manually or on pull requests changing the
pilot or dependency manifests. It runs the same command under Docker with
`--network none`, records its exact image inspection, and preserves coverage.
It is an additional shadow run, with no authority to suppress CI. It does not
publish an image. Manual runs should select a reviewed revision. Pull request runs have read-only
permissions and no persisted checkout credentials.

## Publish a reviewed image

After the publishing workflow is merged, dispatch `Publish Linux test image`
against `main`. Other branches and forks cannot run its publishing job. Each
native Linux runner builds and tests its own architecture with network disabled
during validation. Only a successful offline run reaches the registry push.
The workflow publishes separate amd64 and arm64 images under
`ghcr.io/klarlabs-studio/warden/linux-test`, with source commit, architecture,
run and attempt in the tag. There is no floating `latest` tag or combined index.

The workflow pulls the published digest, checks its Docker config identity
against the image tested locally, signs the registry digest using keyless
Cosign, and verifies that signature with the exact workflow identity. Each
architecture's artifact includes `receipt.json`, build/pull inspection,
signature verification output and coverage. A failed publication can leave an
unsigned image; require successful signature verification before adoption.
Package visibility and pull access must also be checked for the intended users.

Use `registry_reference` from a successful receipt as the immutable pull
reference. Verify it before pulling (Cosign v3):

```sh
cosign verify \
  --certificate-identity 'https://github.com/klarlabs-studio/warden/.github/workflows/linux-test-image.yml@refs/heads/main' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  ghcr.io/klarlabs-studio/warden/linux-test@sha256:REVIEWED_DIGEST
```

Signing establishes the publisher of that image digest. It does not approve a
CI job policy, attest a candidate commit, or sign the accompanying receipt.
Review the selected source commit and inspection evidence before pinning policy.
Docker's `docker_config_id` and the registry manifest digest remain distinct
identities. Apple users must inspect the pulled image's manifest identity;
never substitute the Docker config ID for it. Publishing does not change
`.warden.yaml`, the shadow policy, or any CI skip decision.

For local Warden adoption, merge these settings into a dedicated test-only
configuration (the execution settings apply to every configured shell step):

```yaml
execution:
  mode: auto
  runtime: auto
  image: local/warden-linux-test:pilot
  platform: linux/arm64
  network: none
commands:
  test: /usr/local/bin/warden-linux-test
steps:
  pre_push: [test]
```

Runtime auto-discovery chooses Apple on supported Macs, then Docker-compatible
engines. Supplying the image opts into containers; no image keeps host execution.
The image here contains the test toolchain, not every tool used by Warden's own
lint and security steps. Do not enable it globally for those steps.

## Toolchain security refresh

The source recipe, repository toolchain and pinned images use Go 1.26.10.
Run `40009451684` published the replacements after native amd64 and arm64 offline
validation as UID 10001. Their manifests, config identities, platforms and exact
publisher signatures were independently verified before pinning. Earlier images
from run `39997734827` contain Go 1.26.7, for which the shared govulncheck job
reported reachable standard-library advisories. Updating source cannot update an
immutable image; use the refreshed references below.

## Independently prepare per-commit expectations

Reviewed publication pins are checked in under `profiles/amd64` and
`profiles/arm64`. Each contains a dedicated `.warden.yaml`, the observed
publication receipt and a Docker policy template. arm64 also has an Apple
template using the independently verified registry manifest identity. Verify
the pulled Apple identity against that manifest before using the template.
Both configurations retain runtime auto-discovery. Choose the template matching
the engine that actually ran; a Docker note will fail the Apple template and
vice versa. The effective configuration digests are pinned, with regression
checks against the normal config loader. Editing or merging these settings into
another configuration changes that digest and requires a separately reviewed
template.

Use these configurations only in a disposable test checkout: replacing this
repository's normal `.warden.yaml` would remove its lint and security steps.
Pull the pinned image before running Warden; hook validation never needs to
download a runtime or image. For Apple silicon:

```sh
container image pull ghcr.io/klarlabs-studio/warden/linux-test@sha256:e97a6c1dc0a1e3ad504528394bf675ed3aa58f66a75eedb73d561a3491090f24
# In a disposable checkout, copy profiles/arm64/.warden.yaml to .warden.yaml,
# commit it, then run the test-only pre-push gate.
warden run pre-push --attest-only
```

`Linux test contract` is a manual/reusable workflow using those same immutable
image references and `/usr/local/bin/warden-linux-test` command. It verifies the
publisher signature before pulling, checks the exact Docker config ID and
platform, and runs as UID/GID 10001 with HOME=/tmp and no network access. It
never rebuilds the image or reads
the candidate's policy/config to choose its command. Dependency changes that
need uncached modules fail offline and require a newly published, reviewed image.
It has no skipping logic and runs automatically only for changes to these pins,
their regression test or its workflow, rather than on every PR. The
separate build-based pilot remains available to test proposed recipe/module
changes before publishing a replacement image.

The main CI caller now selects this same signed amd64 Docker contract through
the shared workflow interface merged in `klarlabs-studio/.github#88`. It runs
the pinned command once, including the coverage check from the produced profile.
Its `test-reuse-sha` input comes only from the protected verification job described
below. The independent telemetry shadow policy stays all-run. The caller pins the
reviewed shared merge commit.

The shared container path tests the PR head, which is the commit local evidence
can attest. The previous host job tested GitHub's synthetic merge checkout.
The separate E2E/runtime jobs retain their normal merge checkout. Any additional
merge-result unit-test requirement needs its own gate; local head evidence cannot
establish that result. The shared pin also includes upstream timeout, nox and
govulncheck updates since the previous pinned revision.

## Protected test reuse in this repository

The read-only `Verify protected test reuse` job builds the verifier and preparer
from reviewed commit `369231d82fe3563979dbf0b4cd036de5a347bab8`, outside the
candidate checkout. It executes no candidate code. Its protected amd64 Docker
policy and publication receipt must match the exact contract supplied to shared
CI: image, config identity, platform, command, network and publisher.

Only same-repository pull requests are eligible. The candidate SHA, comparison
base SHA and branch come from GitHub event metadata. Candidate Git objects supply
dependency inputs; notes never supply expectations. The signed, trusted,
commit-bound planner must approve the complete one-job test inventory before the
job emits the candidate SHA. The shared workflow accepts that SHA only if it
matches its own candidate. This is an accepted caller decision, not a new test
observation. Lint, builds, vulnerability checks, E2E and runtime tests still run.

Missing notes, errors, partial plans or contract/context mismatches emit no reuse
SHA. Failed or skipped verification jobs do not suppress ordinary CI. Forks and
push-to-main runs always execute the container test. Policy/plan observations are
retained as artifacts; an eligible result is not a claim that a forge job skipped.

The expected comparison is the pull request's base SHA, not a base copied from
the note. A local pre-push run against an earlier remote feature-branch tip or
another branch context will not qualify. The normal host gate and Apple arm64
profiles cannot replace the Linux amd64 Docker test. Use a dedicated matching
profile and inspect actual CI execution before claiming savings. To refresh the
contract, review and advance the protected source pin as well as the caller and
profile pins; runtime comparisons reject drift between them.

GitHub must protect changes to the caller workflow itself through repository
review rules. A pinned verifier cannot secure a caller that has been changed to
ignore its result.

Build the preparer from a reviewed checkout, outside the candidate:

```sh
go build -o /tmp/warden-ci-policy ./scripts/ci-policy
/tmp/warden-ci-policy --repo /path/to/candidate \
  --template /protected/test-policy-template.json \
  --base FULL_PRE_PUSH_BASE_SHA --commit FULL_CANDIDATE_SHA \
  --branch EXPECTED_LOCAL_BRANCH > /tmp/expected-policy.json
```

The template has the CI job policy schema. A reusable step must specify the
reviewed command, effective configuration digest, image identity, identity kind,
platform and network. Omit `context_digest` and `inputs`: the preparer fills
those from Git objects, never from a candidate note or working file. Null job
entries retain their always-run meaning. For example, replace the descriptive
values below with reviewed exact expectations before using it:

```json
{
  "version": 1,
  "jobs": {
    "test": {
      "version": 1,
      "steps": {
        "test": {
          "command": "/usr/local/bin/warden-linux-test",
          "configuration_digest": "REVIEWED_EFFECTIVE_CONFIG_SHA256",
          "image_id": "REVIEWED_IMAGE_SHA256",
          "image_id_kind": "manifest",
          "platform": "linux/arm64",
          "network": "none"
        }
      }
    },
    "lint": null
  }
}
```

The template must resolve outside the candidate checkout and be supplied by the
protected consumer. Path separation alone does not establish trust: candidate
code must not execute with write access to the protected checkout or template.
Unknown/duplicate fields, mutable revisions, submodules and non-regular manifests
are rejected. The generated policy must still pass the normal signed,
commit-bound verification and exact execution-contract comparison.

The base must be the comparison base used before the local push, not the remote
branch tip after that push. CI must establish it independently. Branch names
also participate in context identity. A mismatch keeps CI running. This helper
does not infer either value from the note.

Apple reports a manifest identity; Docker reports a config identity. arm64 and
amd64 differ too. These are separate expectations. An Apple arm64 pass does not
replace an amd64 Docker job. The opted-in shared CI job uses the pinned image
and command; ordinary callers without a container contract retain host testing. End-to-end runtime suites remain
separate; `go test ./...` does not enable WARDEN_E2E.
Optional platform/tool suites (for example PowerShell) can still skip inside
this Linux image. A matching Go command alone does not establish CI equivalence.
The pilot instruments `-coverpkg=./...` so CLI integration tests contribute to
service coverage in the race profile. The previous shared host job collected coverage in a
second coverctl test pass. The opt-in contract consumes the existing profile;
coverage floors are unchanged.
