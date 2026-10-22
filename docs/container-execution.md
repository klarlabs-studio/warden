# Container execution

Configured shell checks (`lint`, `test`, and custom `commands:` steps) can run
inside ephemeral Linux containers while Warden continues to manage disposable
Git worktrees and provenance on the host.

```yaml
execution:
  mode: auto                   # default: host unless image is configured
  runtime: auto                # Apple on supported Macs; Docker/Podman/nerdctl
  image: local/warden-checks:dev # must already be pulled or built
  platform: linux/arm64         # optional; linux/amd64 also supported
  network: none                # default; bridge explicitly permits networking
commands:
  lint: npm ci --offline && npm run lint
  test: npm ci --offline && npm test
steps:
  pre_commit: [lint]
  pre_push: [lint, test]
```

The image must contain `/bin/sh`, `cat`, the required toolchains, and any
packages or package caches the commands need. Warden overrides the image's
entrypoint and runs the configured command in `/workspace`. Each check gets a
fresh container; parallel checks have separate worktrees. Install dependencies
in each check or bake an offline package cache into your image. Build images
outside the hook. Warden never guesses an image, builds one, or starts an
engine service automatically. Docker-compatible backends enforce `--pull=never`;
Apple has different fetch behavior described below.

| Setting | Result |
|---|---|
| No `execution` section | Existing host execution |
| `image` configured, mode/runtime omitted | Discover a reachable engine with that image available locally |
| `mode: container` | Require an image and a usable engine |
| `runtime: docker`, `podman`, `nerdctl`, or `apple` | Require that engine; no switch to another engine |
| `mode: host` | Explicit host override, even when an inherited image is configured |

On Apple-silicon macOS, discovery tries Apple `container` first, then Docker,
Podman, and nerdctl. Other hosts try Docker, Podman, then nerdctl. All probes
have a ten-second bound per probe. An installed CLI whose engine is stopped is skipped in auto mode. If no engine can inspect
the requested image, the gate fails. A configured container never silently
falls back to host execution. Execution settings inherit through `extends:`
field by field.

## Resource limits

`execution.limits` bounds what each container check may consume:

```yaml
execution:
  image: local/warden-checks:dev
  limits:
    memory: 4GiB   # whole MiB, GiB or TiB; swap is capped at the same size
    cpus: "1.5"    # decimal, at most three places
    pids: 1024     # processes and threads in the container
```

Every field is optional; one you leave out stays unbounded. Limits apply to
container shell checks only: host steps, native steps such as
`security-scan`, and agent steps (which run on the host) are not bounded.
Through `extends:` a child that names one limit keeps the others it inherits.

Docker, Podman and nerdctl get `--memory`, `--memory-swap`, `--cpus` and
`--pids-limit`. Apple `container` sizes each container's VM: it takes whole
CPUs and has no process limit, so a `pids` limit or a fractional `cpus` value
rules Apple out. With `runtime: auto` discovery then moves on to the next
engine; with `runtime: apple` the check fails naming the limit. A limit is
never dropped silently.

These flags are a request to the engine. An engine can ignore one: rootless
Podman on cgroup v1 warns and runs the container unbounded. This release
applies limits but does not record them in the provenance note, so a note
says nothing about them either way. Recording what the container actually
observed is planned for a later release.

Every machine and CI runner that reads the configuration needs this warden
release or later before a repository adds `execution.limits`: an older warden
rejects the key and blocks the commit.

## Apple container

Select Apple's CLI explicitly with:

```yaml
execution:
  runtime: apple
  image: local/warden-checks:dev
  platform: linux/arm64
  network: none
```

This backend runs Linux guests using Apple's lightweight VM runtime on Apple
silicon. It does not run macOS guests or validate Xcode-specific behavior.
The initial adapter supports `linux/arm64`; it does not implicitly enable
Rosetta or install translation support. The user must install the `container`
CLI and start its system service beforehand.

Apple uses different image-inspection JSON and lacks `--pull=never`. Warden
requires the requested image and platform to be present during discovery, but
the CLI can fetch kernel/init assets during creation and may fetch an image if
its cached entry disappears. Container `network: none` restricts the workload,
not the runtime's own fetches. Initialize Apple's runtime ahead of an offline
hook run; this adapter cannot promise a fetch-free startup.

To prevent a moved tag from authorizing the wrong checks, Warden creates a
stopped container, verifies its image index, Linux platform, runtime handler,
and network configuration, then starts it attached. Cleanup is explicit even
after a successful check. Deleting the container happens after its command has
finished, so it cannot change the verdict, which comes from the command's exit
status alone: a container that cannot be deleted is reported in the step's
summary when the check passed, and beside its output when it failed.
The shared worktree marker also verifies its bind mount. Apple's mount parser
cannot represent worktree paths containing commas, equals signs, or newlines;
Warden rejects them instead of interpreting a different mount.

The parser is checked against Apple's `ImageResource` and container inspection
schema at source revision `31e3a98e471e`. Unknown schemas fail closed. Live
validation passed with Apple `container` 1.5.0 on macOS 27.0.1 (Apple silicon),
using `docker.io/library/alpine:3.22`: the integration test observed Linux,
verified worktree writes and environment isolation, and the golden-fleet test
verified parallel worktree isolation and signed, commit-bound execution evidence.
Controlled CLI tests also assert lifecycle ordering, mismatch rejection, and
cleanup failures.

## Evidence and verification

Docker-compatible backends inspect the image, then run an immutable reference
rather than its mutable tag. Docker and Podman run the inspected config image
ID (Podman prints it without the `sha256:` prefix; Warden records it with one).
nerdctl cannot run a config ID, only a name or containerd's target digest (the
image index or manifest), so Warden runs the target digest. During discovery it
inspects that digest again for the selected platform and refuses the engine
unless it resolves to the same config ID; content addressing then fixes what
the run executes. Apple creates a stopped container and
checks its image index and platform before starting it. The signed RunRecord records the runtime name, requested
image,
resolved image ID, Linux architecture, network mode, Warden host OS, and Linux
guest OS **per executed check**. `image_id_kind` distinguishes a Docker config
digest from an Apple platform-manifest digest; Apple also records its image
index digest.
An unconfigured/skipped check does not receive container evidence. Existing
notes keep their existing signature payload when no environment is recorded.
Older verifiers that do not understand the new signed field reject the new
signature; upgrade them before relying on container provenance.

Require that specific checks ran in a container:

```bash
warden verify --key '<trusted-fingerprint>' --container-steps lint,test
```

To require the exact inspected image, also pass
`--container-image-id sha256:<64-hex-characters>`. This is the digest in the
record's `image_id`: a config digest for Docker-compatible engines or a platform
manifest digest for Apple. It is not Apple's multi-platform index digest. These
requirements apply to single-commit verification; `--range` does not accept them.
`--container-steps` does not check the recorded network: a check that ran with
`network: bridge` satisfies it. To require `network: none` as well, use
`--requirements` or `--min-profile strict` (see
[compliance profiles](agent-profiles.md#checking-a-profile-in-ci)).
The GitHub `warden-verify` action also accepts `container-steps` and
`container-image-id` inputs. Select a released Warden version that includes
these flags; an older binary rejects them and the action runs CI checks.
Use an explicit trusted-key pin in protected CI configuration. Unsigned,
untrusted, legacy, or mismatched execution evidence cannot satisfy the
requirements. A note still must attest the exact requested commit.

Choose a platform and a digest-pinned image when the team needs the same
userspace across architectures. Without `platform`, Warden records the locally
inspected architecture rather than claiming all machines ran the same one.
An image controls userspace; it does not make host kernels, hardware, or
external services identical.

## Scope and isolation

Only the disposable worktree is bind-mounted. Warden does not expose host
`node_modules`, host build caches, credential directories, or the container
engine socket. It forwards only the five explicit step inputs (`WARDEN_HOOK`,
`WARDEN_BRANCH`, `WARDEN_CHANGED_FILES`, `WARDEN_FILES_TOUCHED`, and
`WARDEN_LINES_CHANGED`), sets a temporary home and lint-cache path, and leaves
other environment settings to the image.

On Unix the check runs as the invoking user, so files it writes in the worktree
belong to that user on the host. How that is expressed depends on the engine's
user-namespace mode, which Warden reads from the engine's `info` before
selecting it (an answer it cannot read fails selection):

| Engine mode | Container user |
|---|---|
| Rootful Docker, Podman, nerdctl | the invoking UID/GID |
| Rootless Podman | the invoking UID/GID, mapped with `--userns=keep-id` |
| Rootless Docker, nerdctl | container root, which the user namespace maps to the invoking user |

Rootless Docker and nerdctl map only container root to the invoking user; any
other UID lands on a subordinate ID that cannot write the worktree. Container
root there holds no host privilege beyond the invoking user's, and capabilities
are still dropped. Warden drives an engine as the user running the hook, so
rootful nerdctl (which requires root) is reachable only when Warden itself runs
as root.

Networking defaults to `none` and capabilities are dropped. Docker-compatible
backends additionally set `no-new-privileges`; Apple exposes no equivalent CLI
flag, so Warden does not claim it enforces that option. Cancellation attempts to
remove the named container after
terminating the client process. A marker check confirms that the engine mounted
the actual worktree, including when the CLI selects a VM or remote context.
The context must support accessing that path; a missing or incorrect mount
fails the check.

Every container is named `warden-<pid>-<nonce>` and labeled
`de.klarlabs.warden.pid=<pid>` and `de.klarlabs.warden.host=<host name>`,
naming the warden process that started it and the machine it ran on. A
process killed outright (SIGKILL, a CI timeout) never removes its container:
Docker-compatible engines remove it once its command ends (`--rm`), but
Apple's removal is a step the killed process never reaches. `warden watch`
removes such leftovers when it starts, and a gate run does the same in the
background for the engine it selected: containers labeled with this host
name whose owning process has exited. A container without those labels,
labeled by another machine, or whose owner is still running is never
touched. The same applies to a container whose deletion failed after its
check finished; it is removed once its owner has exited.

Native Git steps, credential inspection, coding-agent integrations, and
`warden-step-*` subprocess plugins continue to execute on the host. Git's shared
metadata is not mounted, so commands that require the worktree's external Git
directory may need adjustment. Container execution is an environment boundary
for configured commands, not a claim that the entire gate runs inside a
security sandbox.

Container security-scan commands use their exit status directly. Version pins
are checked inside the selected image when scanner version
checking is enabled. Host-side delta-report interpretation is disabled in this
mode, so the scanner command must enforce the required severity policy.
A nonzero exit still fails the gate.

The existing step-result cache is disabled for container runs: it does not yet
capture the full execution environment. `warden watch` under a container
configuration does not run in the live tree: each evaluation checks a snapshot
of the working tree (what `git add -A` would stage) in a disposable
dependency-free worktree, and runs only read-only shell checks there (see
[background feedback](background-feedback.md#live-mode)).
A future staged-index predictor must key results to the final staged tree,
command/configuration, resolved image, platform, and dependencies, and discard
stale work before it can authorize a commit. No latency reduction is promised
without measurements.

## Validation

The default test suite exercises both CLI adapters, discovery, fail-closed
selection, argument boundaries, cancellation, signatures, verification requirements, and clean
worktree cloning with controlled engine CLIs.

CI also runs the live integration test and the golden-fleet container scenarios
on every pull request against three engines on Ubuntu: Docker, rootless Podman
4.9.3 (from the runner image), and rootless nerdctl 2.4.1 (nerdctl-full, its
containerd run as a systemd user service). Each run asserts that a host
environment variable does not reach the check, that `network: none` leaves the
workload no IPv4 route, that the recorded runtime, image and config ID match
what discovery inspected, and that the check's output is written in the
worktree and owned by the invoking user. The first live runs found two adapter
defects that the controlled CLIs had encoded wrongly: Podman's unprefixed image
ID was rejected, and nerdctl could not run a config ID. Rootful Podman and
rootful nerdctl, and rootless Docker, are covered by argument tests only.

To exercise a real engine without downloading an image:

```bash
WARDEN_CONTAINER_TEST_IMAGE=<preloaded-image> go test ./internal/infrastructure/steps -run TestContainerExecutionIntegration -count=1
WARDEN_E2E=1 WARDEN_CONTAINER_TEST_IMAGE=<preloaded-image> go test ./e2e -run TestGoldenFleet_ContainerEvidenceRequiresObservedExecution -count=1
```

For another engine, set `WARDEN_CONTAINER_TEST_RUNTIME` to `podman`, `nerdctl`,
or `apple` alongside the same image variable in either command above. The image
must already be in that engine's own image store.

Runtime flag references: [Docker run](https://docs.docker.com/reference/cli/docker/container/run/),
[Podman run](https://docs.podman.io/en/latest/markdown/podman-run.1.html),
[Apple container](https://github.com/apple/container/blob/main/docs/command-reference.md), and
[nerdctl commands](https://github.com/containerd/nerdctl/blob/main/docs/command-reference.md).
