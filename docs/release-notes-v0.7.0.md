# warden v0.7.0

**A configurable git commit/push gate installed as native git hooks** — `git commit` and `git push` themselves are the gated commands. Runs your checks in a disposable worktree, leaves signed cryptographic proof, and lets CI trust that proof.

This release is a big one: the gate got **faster**, the provenance got **cryptographically signed**, the TUI came **alive**, and a batch of new surfaces landed.

## Faster gate

- **Parallel steps** — independent read-only checks (lint ∥ test ∥ security) run concurrently, so the gate is as slow as the *slowest* check, not the sum. `parallel: false` opts out. `warden policy explain` prints the schedule: `intent → rebase → [review ∥ test ∥ lint]`.
- **Step-level cache** — declare a step's input globs under `cache:` and warden skips it when those files are byte-identical to its last passing run. Lives in `.git`, keyed by inputs + command.
- **Per-step timeouts** — `timeouts: { test: "5m" }` kills and fails a wedged step instead of hanging the gate.

## Signed, richer provenance

- **Signed notes** — every validated commit's note is signed with a per-machine ed25519 key (private key never leaves the machine). `warden key show` prints the fingerprint; `warden verify --key <fp>` turns CI provenance-skip from "a warden ran here" into "a warden **I trust** ran here". The bundled `warden-verify` action gained a `key:` input.
- **SBOM in the note** — each note carries a SHA-256 digest of every dependency lockfile present at validation (`go.sum`, `package-lock.json`, `Cargo.lock`, …), covered by the signature — a signed fingerprint of the exact dependency sets.
- **`warden why <commit>`** — explains what the gate did for any commit from its note: matched rules, steps, signer, SBOM — no re-run.

## Live TUI

- **Streamed step output** — a running step's stdout/stderr streams as a live tail under it, so a slow `go test` feels alive.
- **Collapsible findings + jump-to-file** — press `f` to fold findings, `1-9` to open the Nth at its line in `$EDITOR`.
- **Desktop notification** — an OS notification with the verdict when an interactive pre-push finishes.
- Per-step counting-up timers, total run time, animated spinner.

## New surfaces

- **`warden attach`** — watch a running gate live from another terminal over a per-repo Unix socket (read-only; the run still drives git).
- **`warden watch`** — re-run the fast checks on save, a continuous pre-commit feedback loop.
- **PR comment** — a passing push posts (and stickily updates) a gate-result comment on the PR: steps, provenance + signer, findings.
- **`warden recipes`** — paste-able check recipes (gitleaks, semgrep, trivy, coverage-delta, govulncheck, hadolint).
- **`extends:`** — inherit a base `.warden.yaml` across repos (org policy sync); the repo overrides only what it needs.

## Install (any developer, no Go toolchain required)

```bash
npx @klarlabs-studio/warden init                         # no install, works anywhere Node is
brew install felixgeelhaar/tap/warden                    # macOS / Linux
curl -fsSL https://raw.githubusercontent.com/klarlabs-studio/warden/main/scripts/install.sh | sh
go install go.klarlabs.de/warden@latest                  # Go devs
```

## Links

- Repo: https://github.com/klarlabs-studio/warden
- npm: https://www.npmjs.com/package/@klarlabs-studio/warden
