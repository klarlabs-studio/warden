# Changelog

All notable changes to warden are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and warden adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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
