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

The `Linux test pilot` workflow runs manually or on pull requests changing the
pilot or dependency manifests. It runs the same command under Docker with
`--network none`, records its exact image inspection, and preserves coverage.
It is an additional shadow run, with no authority to suppress CI. It does not
publish an image. Manual runs should select a reviewed revision. Pull request runs have read-only
permissions and no persisted checkout credentials.

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
