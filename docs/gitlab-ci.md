# GitLab CI

warden's notes (`refs/notes/warden`) are plain git, so GitLab can check them the
same way GitHub does. `templates/verify.yml` is the GitLab counterpart of the
[`warden-verify` action](ci-provenance-skip.md). It is a CI/CD component that
adds one job, `warden-verify`. The job checks that the checked-out commit
carries a signed note bound to that commit, from a key you pinned. It can also
require [execution requirements](execution-contracts.md). It then publishes the
verdict, so later jobs can skip checks the developer's warden already ran, or so
the pipeline fails (enforcement).

## What exists today, and what does not

| | Status |
|---|---|
| `templates/verify.yml` in this repository | Shipped in 0.36.0 |
| `include: remote:` from a release tag's raw URL | Works from `v0.36.0`: pin the tag, or a commit SHA for an immutable include |
| `include: component:` from the GitLab CI/CD Catalog | **Not published.** No GitLab catalog project mirrors warden yet. The layout (`templates/<name>.yml` with `spec: inputs:`) is what a catalog project needs, so mirroring the repository is enough. The component path below is the shape it will have, not an address that resolves today |
| Range gate (`warden-gate`), `warden-doctor`, `warden-reattest` for GitLab | Not ported. This template verifies one commit |
| Monorepo scope (`scope`, `validated-workspaces` on the GitHub action) | Not ported. The verdict file carries no scope, so an affected-scope note means "run every workspace" here. Monorepo mode's design scoped this step to the GitHub action. A port would read `warden verify --json` the same way, and its consumers would have to read the verdict file, not the variables, for the reason given under [Skipping downstream work](#skipping-downstream-work) |
| Windows (PowerShell) runners | Not supported. The job needs a POSIX shell and bash |

The template was tested by running its job scripts the way GitLab
interpolates them, against real git repositories with real warden-signed notes
(`scripts/gitlab_template_test.go`, `e2e/gitlab_ci_test.go`). It was also run in
the default image on linux/amd64 and linux/arm64. Its YAML validates against
GitLab's published CI schema. **It has not run on a GitLab instance.**

## Quick start: skip checks a trusted warden already ran

```yaml
# .gitlab-ci.yml
include:
  - remote: "https://raw.githubusercontent.com/klarlabs-studio/warden/vX.Y.Z/templates/verify.yml"
    inputs:
      key: "641e75b15812b7b6"       # `warden key show` on each trusted machine
      on-unverified: run-checks     # an unverified commit runs the checks, not a red pipeline

test:
  stage: test
  needs: [warden-verify]
  before_script:
    - !reference [.warden-skip-if-verified, before_script]
  script:
    - make test
```

Once a catalog project exists, the include becomes
`- component: $CI_SERVER_FQDN/<catalog-group>/warden/verify@X.Y.Z` with the
same `inputs:`.

On a commit with a valid note, `test` starts, sees the verdict, and exits 0
without running `make test`. On any other commit it runs the checks. Skip only
checks that are **equivalent** to the ones the note records. A trusted
signature says who attested the commit. It does not say that their toolchain,
dependencies or environment match your CI job (see
[provenance-skip](ci-provenance-skip.md#trust-depth-pin---key-when-skipping-is-a-security-decision)).

## Enforcement: fail the pipeline

Leave `on-unverified` at its default, `fail`:

```yaml
include:
  - remote: "https://raw.githubusercontent.com/klarlabs-studio/warden/vX.Y.Z/templates/verify.yml"
    inputs:
      key: "641e75b15812b7b6,3a76a2b850d0e957"
```

The job fails unless the commit is validated. If `warden-verify` runs in `.pre`
(the default), no job that waits for earlier stages starts. A job with
`needs: []` does not wait, so it still runs. To block merges on it, turn
on "Pipelines must succeed" for the project. Read
[trust boundaries](#trust-boundaries) first: in a merge request pipeline the
candidate commit can edit `.gitlab-ci.yml`.

## Inputs

| Input | Default | Meaning |
|---|---|---|
| `key` | `''` | Trusted signer key(s) or fingerprint(s), comma-separated. **Without one, nothing is ever validated.** |
| `on-unverified` | `fail` | `fail`: the job fails unless the commit is validated. `run-checks`: the job succeeds with `WARDEN_VALIDATED=false`. |
| `commit` | `HEAD` | The commit to verify. `HEAD` is what the runner checked out, and no variable can redirect it. |
| `requirements` | `''` | Path to a protected JSON [execution-requirements](execution-contracts.md) file. Use a File-type CI/CD variable (`$WARDEN_REQUIREMENTS`). A path inside the checkout is refused. |
| `container-steps` | `''` | Checks that must carry trusted container execution evidence. |
| `container-image-id` | `''` | The inspected `sha256:` image ID required with `container-steps`. |
| `min-profile` | `''` | The minimum [compliance profile](agent-profiles.md#checking-a-profile-in-ci) the note must conform to: `permissive`, `standard` or `strict`, optionally `name@version`. Fixed text, not a variable reference. Needs `warden-version` 0.37.0 or newer; an older release rejects the flag and the job fails as a configuration error. |
| `remote` | `origin` | The remote that holds `refs/notes/warden`. |
| `warden-version` | `0.42.0` | The warden release to install. It must be an exact version, 0.29.0 or newer. |
| `stage` | `.pre` | The stage the job runs in. |
| `image` | `buildpack-deps:bookworm-scm` | The job image. It needs bash, git, curl, tar, mktemp and sha256sum or shasum. |

The inputs mirror the GitHub action where they mean the same thing. There is no
`latest` version, and there is no `go install` fallback. Both would break the
chain from your pinned include to the binary that decides the verdict.

GitLab puts every input into the job script **as written** when it creates the
pipeline, and each input has a regex that keeps it inside its shell quotes. So a
pipeline or project variable cannot replace a pinned key, commit or mode. An
input whose whole value is one variable reference (`$NAME` or `${NAME}`) is
expanded when the job runs. That is how you take the key or the requirements
file from a CI/CD variable. It also means the variable's precedence applies; see
[trust boundaries](#trust-boundaries).

## The verdict

The job's exit status:

| Exit | Meaning |
|---|---|
| `0` | Validated. With `run-checks`, also any verdict that is not validated. |
| `1` | Not validated, with `on-unverified: fail`. |
| `2` | Configuration or environment error. Nothing was verified, whatever the mode. |

The install step fails the job (exit `1`) in both modes, and says that nothing
was verified.

`warden-verify.env` is published as a dotenv report **and** as a plain artifact
file. It is kept even when the job fails:

```
WARDEN_VALIDATED=true|false
WARDEN_VERDICT=validated|unverified|no-key|no-notes|notes-unreachable|error|not-run
WARDEN_VERIFIED_COMMIT=<full sha>
```

The file is written as `false` first, and written again on every exit path. A
job that dies part-way therefore leaves "not validated", never a stale verdict.

## Skipping downstream work

**GitLab cannot use the verdict in `rules:`.** `rules:` are evaluated when the
pipeline is created, before any job has run. Dotenv variables exist only inside
the scripts of later jobs. So the skip happens at the start of each downstream
job: the job starts and exits early.

`.warden-skip-if-verified` holds a `before_script` that does that. GitLab runs
`before_script` and `script` in one shell, so its `exit 0` ends the job as
passed before the checks run. Keep your own `before_script` with `!reference`:

```yaml
lint:
  needs: [warden-verify]
  before_script:
    - !reference [.warden-skip-if-verified, before_script]
    - npm ci
  script: [npm run lint]
```

or, if the job has no `before_script` of its own, use
`extends: .warden-skip-if-verified`.

The helper skips only when all of these hold:

- `warden-verify.env` exists, is not a symlink, and is **not tracked** in the
  commit. A committed file cannot pose as the artifact.
- It says `WARDEN_VALIDATED=true`.
- It names the commit this job checked out (`git rev-parse HEAD`). It does not
  compare against `$CI_COMMIT_SHA`, which a variable could override.
- `git` is available to check the first and third conditions.

It reads the **file**, not the `WARDEN_VALIDATED` variable. A pipeline variable
outranks a dotenv variable, so anyone who may set pipeline variables could set
`WARDEN_VALIDATED=true`. Nobody can forge an artifact that way. Treat the
variables as information for job logs and scripts, not as permission to skip.

The job must receive the artifact: list `warden-verify` in `needs:` (or rely on
stage order with default `dependencies`). If the job gets no verdict, it runs
its checks. To skip whole jobs before they start, generate a child pipeline
from the verdict. This template does not provide one.

## Clones, depth and merge requests

GitLab clones shallow (`GIT_DEPTH`), detached, and without notes. The job
handles that itself:

- It fetches `refs/notes/warden` explicitly and force-replaces any local copy.
  The verdict rests on what the remote publishes. A notes ref left in a reused
  build directory (`GIT_STRATEGY: fetch`) is deleted first, not trusted.
- Verifying a single commit needs no history, so `GIT_DEPTH: 1` is enough. If
  `commit` names something outside the shallow clone, the job fetches that one
  object first.
- `GIT_STRATEGY: none` cannot work, because there is nothing to verify. The job
  exits `2`.

Which commit a pipeline has checked out:

| Pipeline | `HEAD` | Carries a note? |
|---|---|---|
| Branch pipeline | The pushed commit | Yes, if its pusher's warden gated it |
| Merge request pipeline | The MR source branch head | Yes, same as above |
| Merged results pipeline | GitLab's temporary merge commit | **No.** No developer's warden ever saw it |
| Pipeline on the target branch after a squash or merge | A commit GitLab created | **No** |

A merged results pipeline will therefore not verify with the default `commit`.
You can set `commit: $CI_MERGE_REQUEST_SOURCE_BRANCH_SHA` to verify the source
head instead. That is a weaker claim, because the merged tree was never
validated. The skip helper compares against `HEAD`, so it will still run the
checks. Commits GitLab creates have the same gap that GitHub-created merges have.
On GitHub, a post-merge attestation closes it
([closing the forge-merge gap](ci-provenance-gate.md#closing-the-forge-merge-gap-post-merge-attestation)).
This template has no GitLab equivalent yet.

## Install chain

The job runs nothing it has not verified:

1. It downloads cosign v3.0.6 for the runner's platform and checks it against a
   SHA-256 digest written into the template. Those digests come from
   sigstore's `cosign_checksums.txt`, after that file was verified against
   sigstore's own keyless release signature.
2. It downloads the warden archive, `checksums.txt` and `checksums.txt.bundle`
   for exactly `v<warden-version>`.
3. `cosign verify-blob` checks the bundle. The certificate must name
   `https://github.com/klarlabs-studio/warden/.github/workflows/release.yml@refs/tags/v<version>`,
   issued by `https://token.actions.githubusercontent.com`.
4. It checks the archive's SHA-256 against the now-verified `checksums.txt`.

Releases before 0.29.0 publish no bundle, so they cannot be installed this way.
The job needs outbound HTTPS to `github.com` (and its release-asset CDN) and to
Sigstore's TUF repository (`tuf-repo-cdn.sigstore.dev`). An air-gapped runner
cannot use this template.

## Trust boundaries

**The pipeline definition comes from the commit under test.** In a merge
request pipeline, the candidate's `.gitlab-ci.yml` decides whether this job
runs at all, and with which inputs. For provenance-skip that loses nothing:
someone who can edit the include can edit the checks out just as easily. For
**enforcement** it matters. Put the verify job where the candidate cannot edit
it:

- a [pipeline execution policy](https://docs.gitlab.com/user/application_security/policies/pipeline_execution_policies/)
  or compliance pipeline, or
- a project CI configuration file in a separate, protected project
  (Settings → CI/CD → General pipelines → CI/CD configuration file,
  e.g. `.gitlab-ci.yml@group/protected-ci`).

**Pin the template itself.** A tag in the raw URL can be moved. A commit SHA
cannot. GitLab 17.9 and later can also check the file's hash with
`integrity:`:

```yaml
include:
  - remote: "https://raw.githubusercontent.com/klarlabs-studio/warden/<commit-sha>/templates/verify.yml"
    integrity: "sha256-<base64 digest>"   # curl -fsSL <url> | openssl dgst -sha256 -binary | openssl base64 -A
    inputs:
      key: "641e75b15812b7b6"
```

**Write the key into the include.** A key written as a literal cannot be
overridden. If you use `key: $WARDEN_TRUSTED_KEY` instead, GitLab's variable
precedence applies, and **pipeline variables outrank project variables**.
Anyone who may run a pipeline with variables could then supply their own key.
Set Settings → CI/CD → Variables → "Minimum role to use pipeline variables" to
Owner or "no one allowed". Also, a *protected* variable is not exposed on
unprotected branches. The key is then empty there and the verdict is `no-key`.
Fingerprints are public, so the variable does not need to be protected or
masked.

**Requirements are policy, so they must not come from the candidate.** Use a
File-type CI/CD variable. GitLab writes it outside the checkout. A
`requirements` path that resolves inside `$CI_PROJECT_DIR`, including through a
symlink, is refused with exit `2`.

**What a validated verdict means.** It means the same as on GitHub, and no more:
a warden holding a pinned key ran the recorded checks on exactly this commit's
tree, and the note binds to this commit. A transplanted note, an unsigned note,
an untrusted signer, or a missing note never produce `WARDEN_VALIDATED=true`. The
roster in the candidate's `.warden.yaml` is never consulted. Only the `key`
input counts.
