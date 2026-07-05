# Changelog

All notable changes to warden are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and warden adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- **`materialize_deps:` — real (not symlinked) dependency dirs for build steps.**
  warden exposes gitignored dependency directories (`node_modules`) to steps by
  symlinking them from the live checkout into the disposable worktree — fast, and
  fine for `tsc`/`eslint`/`vitest`/Node. But Next.js 16 / Turbopack rejects a
  `node_modules` symlink whose target resolves outside the worktree root
  (`TurbopackInternalError: Symlink node_modules is invalid, it points out of the
  filesystem root`), so a `build` step failed as a false positive. List the
  affected steps under `materialize_deps` (e.g. `materialize_deps: [build]`) and
  warden hardlink-copies the deps into the worktree as real files for any run
  that includes one of them — Turbopack accepts it, and other runs keep the fast
  symlink. Hardlinks fall back to a byte copy across filesystems; internal
  symlinks (`.bin`) are preserved.

## [0.9.0] — 2026-07-04

### Added

- **`warden init` generates comprehensive, multi-ecosystem configs.** Instead of
  detecting a single top-level language, init now walks the repo for every
  buildable unit (skipping `node_modules`/`vendor`/build dirs) and composes a
  path-scoped lint + test step per ecosystem — so a Go module at `apps/api` and
  a TypeScript app at `web` both get gated (`cd apps/api && …`, `cd web && …`),
  with `pre_commit` running the lints and `pre_push` the tests + lints. A nox
  `security-scan` step is added when nox is on PATH. Single-language repos are
  unchanged (unprefixed `lint`/`test`). Language knowledge stays in
  `LanguageCommands` (Go, Rust, JS, TS, Python), so a new language is a table
  entry, not new code. (#13)

## [0.8.4] — 2026-07-04

### Fixed

- **Per-run golangci-lint cache (no more stale-cache phantom failures).**
  golangci-lint caches results keyed to absolute paths; because each gate run
  uses a fresh random worktree, a shared cache returned results referencing a
  deleted worktree path — so `//nolint` directives weren't honored and it
  reported failures on clean code (cleared only by `golangci-lint cache clean`).
  Steps now get a per-worktree `GOLANGCI_LINT_CACHE`, cleaned with the worktree.
  (#11)
- **The gate fails fast when the warden binary can't run.** If the resolved
  binary can't start (Gatekeeper-quarantined, corrupt, blocked), the hook shim
  used to hang on `exec`, wedging every commit/push. The shim now preflights a
  time-boxed `--version` and, on a hung/unrunnable binary, exits with an
  actionable message instead of hanging. (#12)

## [0.8.3] — 2026-07-03

### Fixed

- **A failing step in a parallel batch now reports cleanly.** When one step in a
  concurrent batch failed, the run went terminal and the record loop still tried
  to fold the remaining outcomes in, surfacing the opaque `record step X: run is
  already terminal` instead of a `step Y failed` naming the real culprit. The
  loop now stops at the terminal transition, so a parallel gate failure is
  legible.

## [0.8.2] — 2026-07-03

### Fixed

- **Steps no longer inherit the git hook environment.** git exports
  `GIT_INDEX_FILE`, `GIT_DIR`, etc. when running a pre-commit/pre-push hook.
  Steps inherited them, so a git-aware tool inside the disposable worktree —
  notably `golangci-lint --new-from-rev` — resolved git against the live hook
  index instead of the worktree and mis-reported (e.g. flagging the whole
  backlog instead of just the change). `stepEnv` now scrubs those vars, the same
  way warden's own git subcommands already did. This makes incremental linting
  (`new-from-rev`) reliable in the gate, so a strict linter can be adopted on a
  repo with existing debt without a big-bang refactor.

## [0.8.1] — 2026-07-03

### Fixed

- **Homebrew install no longer hangs on first run.** The cask binary isn't
  notarized, so macOS Gatekeeper quarantined it and the first `warden`
  invocation blocked on an unsigned-binary check (`spctl: rejected`). The cask
  now strips the quarantine attribute on install (`xattr -dr
  com.apple.quarantine`), so the CLI runs immediately after `brew install`.

## [0.8.0] — 2026-07-03

### Added

- **`node_modules` passthrough for JS/TS steps.** The validation worktree is a
  git worktree, so it only contained tracked files — gitignored `node_modules`
  was absent and steps like `tsc`, `eslint`, or `vitest` failed with "command
  not found". Warden now symlinks each `node_modules` from the live checkout
  into the worktree (root and nested — `web/`, `apps/*/`, `site/`), so JS/TS
  gates resolve their dependencies with no reinstall. This makes warden work
  out of the box for Node and Go+JS monorepos; commands no longer need an
  `npm ci &&` prefix.

## [0.7.1] — 2026-07-03

### Fixed

- **Staged binary files no longer fail the pre-commit gate.** Worktree seeding
  captured and applied the staged diff without `--binary`, so committing an
  image or other binary asset failed with "cannot apply binary patch … without
  full index line". The staged-diff and auto-fix diffs now round-trip binaries
  (`git diff --binary` / `git apply --binary`).

## [0.7.0] — 2026-07-02

### Added

- **Parallel steps** — independent read-only checks run concurrently; the gate
  is as slow as the slowest check, not the sum. `parallel: false` opts out.
- **Step-level cache** — `cache:` globs skip a step when its declared inputs are
  byte-identical to its last passing run.
- **Per-step timeouts** — `timeouts:` kills and fails a wedged step.
- **Signed provenance** — per-machine ed25519-signed notes; `warden key show`
  and `warden verify --key` add a trust gate. `warden-verify` action `key:` input.
- **SBOM in the note** — signed digest of every dependency lockfile at validation.
- **`warden why <commit>`** — explain what the gate did for a commit from its note.
- **Streamed step output** in the TUI, plus **collapsible findings** (`f`) and
  **jump-to-file** (`1-9` → `$EDITOR`).
- **Desktop notification** when an interactive pre-push finishes.
- **`warden attach`** — watch a running gate live from another terminal.
- **`warden watch`** — re-run the fast checks on save.
- **PR comment** — sticky gate-result comment on a passing push.
- **`warden recipes`** — paste-able check recipes (gitleaks, semgrep, trivy, …).
- **`extends:`** — inherit a base config across repos (org policy sync).

## [0.6.0] — 2026-07-01

- Initial public release: native `pre-commit`/`pre-push` hooks, worktree
  isolation, rule-based policy, hash-chained provenance + CI provenance-skip,
  config-driven custom steps and agents, interactive TUI, `warden import`,
  and multi-channel install (go / npx / brew / curl).

[0.7.1]: https://github.com/klarlabs-studio/warden/releases/tag/v0.7.1
[0.7.0]: https://github.com/klarlabs-studio/warden/releases/tag/v0.7.0
[0.6.0]: https://github.com/klarlabs-studio/warden/releases/tag/v0.6.0
