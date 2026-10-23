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
Do not adopt those images for local-to-CI reuse. Publish and review replacement
image identities after this fix merges; old signatures establish the publisher,
not suitability for Warden's non-root execution.

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

## Independently prepare per-commit expectations

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
replace an amd64 Docker job. The shared CI job currently installs an unpinned
coverctl version, so this pilot cannot replace that whole job until its command,
toolchain and image expectations are aligned. End-to-end runtime suites remain
separate; `go test ./...` does not enable WARDEN_E2E.
Optional platform/tool suites (for example PowerShell) can still skip inside
this Linux image. A matching Go command alone does not establish CI equivalence.
The pilot instruments `-coverpkg=./...` so CLI integration tests contribute to
service coverage in the race profile. The existing shared workflow instead
collects coverage in a second coverctl test pass. This difference must also be
reviewed before declaring the whole remote job equivalent.
