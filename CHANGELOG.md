# Changelog

All notable changes to warden are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and warden adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- **A successful push no longer looks like a failure.** On every successful
  pre-push, git prints `error: failed to push some refs` — Warden already pushed
  your gated commit and then fails the hook on purpose to stop git's own
  now-redundant push from racing it (that non-zero exit is what makes git emit
  the error). Warden now pre-empts it with a plain-language line so you know the
  push already succeeded. The underlying behavior is unchanged — it is
  load-bearing for the "only gated commits reach the remote" guarantee — this
  just stops the expected git error from reading as a real one. Documented in
  the README's "How it works".

## [0.13.0] — 2026-07-06

### Changed

- **Desktop pre-push notifications now fire only when useful.** A *passing*
  interactive pre-push notifies only once it has run at least `notify_after`
  (new config, default `10s`) — a fast green gate no longer pops a notification
  on every push (previously it fired after every run, despite the docs saying it
  was for long ones). A *failed/blocked* push always notifies regardless of
  duration, so a stopped push is never missed. `notify: false` still silences
  everything. A malformed or negative `notify_after` (e.g. `10` with no unit) is
  now rejected at config load with a clear error, instead of silently reverting
  to the default and leaving the threshold mysteriously ineffective.
- **A malformed `timeouts` value now fails config load** instead of silently
  meaning "no limit". A typo'd step timeout (`30` with no unit, `5mm`, or a
  negative duration) used to parse to nothing and leave the step with no limit at
  all — so a wedged test or agent could hang the gate unbounded, the exact
  opposite of what the timeout is for. `Validate` now rejects it at load with a
  clear error; `"0"` remains the explicit no-limit marker.

## [0.12.0] — 2026-07-05

### Changed

- **Coding-agent steps (`review`, `document`, `intent`) run in parallel again —
  safely.** Building on per-step worktree isolation, the scheduler now serializes
  a step only when its **writes must be kept** (a rebase, an auto-fix budget, or a
  step listed under `writes:`). Everything else — including agents — runs
  concurrently, each in its own ephemeral worktree, so they can't race. An agent's
  incidental tree writes are **discarded**; to persist a step's writes, give it an
  auto-fix budget or declare it under `writes:`. This also correctly scopes the
  pre-commit auto-fix capture to those barrier steps. Completes ADR-0001 Phase 3.

### Added

- **Internal: per-step worktree isolation (ADR-0001 Phase 3, part 1).** Steps in a
  parallel batch now each run in their own ephemeral worktree cloned from the
  canonical one, so a step's writes can't race a sibling; the clones are torn down
  after the batch (side-effects discarded). No scheduling change yet — this is the
  foundation for letting finding-producing agents parallelize.

### Changed

- **Internal: one source of truth for "does a step write the tree."**
  `ResolvedPolicy.WritesTree` now backs both the parallel-batch scheduler
  (`Concurrent`) and the kernel's axi effect level, so the two can no longer
  drift — that drift was the root cause of the parallel-step race fixed in
  v0.10.1. No behavior change to runs; see
  `docs/adr/0001-parallel-step-worktree-isolation.md`.

## [0.11.0] — 2026-07-05

### Changed

- **Gitignored dependencies (`node_modules`) are now materialized by default.**
  warden hardlink-copies them into the disposable worktree as real files instead
  of symlinking, so any tool works out of the box — including Next.js /
  Turbopack, which rejects an out-of-root `node_modules` symlink. Hardlinks are
  near-instant on the same filesystem (byte-copy fallback across filesystems).
  Set `symlink_deps: true` to force the old fast symlink (fine for
  tsc/eslint/vitest, cheaper for a large `node_modules` on a separate `/tmp`
  filesystem). The per-step `materialize_deps:` key is deprecated (materialization
  is now the default) but still parsed for compatibility.

## [0.10.1] — 2026-07-05

### Fixed

- **Linked worktrees with a symlinked `node_modules` now work.** When the live
  checkout is itself a git worktree (e.g. `.claude/worktrees/…`), `node_modules`
  is commonly a symlink back to the main checkout's copy. warden's dependency
  exposure only handled a real directory and silently skipped the symlink, so the
  disposable worktree got no `node_modules` and every JS step (typecheck / lint /
  test / build) failed. It now resolves a symlinked dependency dir to its real
  target and exposes the actual deps (or hardlink-copies them under
  `materialize_deps`).

### Security

- **Parallel steps no longer share a worktree with a writer.** Coding-agent
  steps (`review`, `document`, `intent`, or any step a rule assigns an agent to)
  edit files, but were scheduled to run concurrently with `test`/`lint` in the
  same directory — a data race that could corrupt what the checks read. They now
  run as sequential barriers. New `writes: [step…]` config marks a custom step
  (codegen, formatter) as a tree-writer so it also runs alone. See
  `docs/adr/0001-parallel-step-worktree-isolation.md`.

### Changed

- The default **pre-push step order** is now `intent, rebase, review, document,
  test, lint` (was `…review, test, document, lint`), grouping the writing agents
  ahead of the read-only checks so `test`‖`lint` still share one parallel batch.
  Repos with an explicit `steps:` list are unaffected.

## [0.10.0] — 2026-07-05

A security-hardening release closing every finding from a deep multi-agent
review, plus the Next.js/Turbopack worktree fix. Several changes tighten
behavior (fail-closed) — see **Changed** before upgrading.

### Security

- **Provenance is now bound to the commit it attests.** The signed `RunRecord`
  gained a `CommitSHA` (covered by the signature), and `warden verify` requires
  `record.CommitSHA == <commit>`. Previously a validly-signed note could be
  transplanted onto — or replayed against — any other commit and still pass
  `verify --key <trusted>`, letting CI provenance-skip skip checks on
  attacker-controlled code.
- **`warden verify` fails closed.** It no longer treats an empty `{}` (or any
  no-evidence) note as validated, and `audit`/`doctor` only call a note "intact"
  when it actually attests its commit.
- **The self-fetched warden binary is verified before it runs.** The generated
  git hook, `install.sh`, and `install.ps1` now check the downloaded archive's
  SHA-256 against `checksums.txt` (from the pinned release tag) **before**
  `chmod +x`/extraction, re-verify the `~/.warden/bin/<ver>` cache on every run
  (dir created `0700`), and fail closed on mismatch. Releases now publish a
  **cosign-signed** `checksums.txt`. (Residual: the fetch verifies the checksum
  but not yet the cosign signature over the same channel — see `SECURITY.md`.)
- **`extends:` is contained to the repo.** A `.warden.yaml` can no longer inherit
  its `commands:`/rules from an absolute or `../`-escaping path outside the
  repository (which also read arbitrary files).
- **MCP `run_trigger` refuses by default.** The MCP/`axi` surface auto-approves,
  so running a possibly-untrusted repo's `.warden.yaml` commands now requires an
  explicit opt-in (`WARDEN_MCP_ALLOW_RUN=1`, or `--trust` for `axi run-trigger`).
  Read-only tools (`policy_explain`, `steps_list`) are unaffected.
- **Custom step names are validated** (`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`), closing a
  path-traversal where a name like `x/evil` resolved `warden-step-x/evil` to a
  repo-relative executable.
- **Auto-fixes are only written back when a step was authorized to fix.** A
  passing pre-commit no longer re-applies arbitrary tree mutations made by
  read-only steps to your working tree — capture happens only when a step holds
  an `auto_fix` budget.
- **Runs are cancellable and crash-safe.** Ctrl-C/SIGTERM now cancels a run
  (and can no longer auto-approve a push after you abort); a panic in a parallel
  step becomes a step error instead of crashing the gate and leaking the
  worktree; timed-out steps are killed by process group so children don't orphan.
- **Glob matching is linear-time**, closing a ReDoS where a crafted
  `paths:`/`cache:` pattern against a long path could hang the gate on push.

### Added

- **`materialize_deps:` — real (not symlinked) dependency dirs for build steps.**
  warden symlinks gitignored `node_modules` into the disposable worktree (fast,
  fine for `tsc`/`eslint`/`vitest`/Node), but Next.js 16 / Turbopack rejects a
  `node_modules` symlink resolving outside the worktree root
  (`TurbopackInternalError: Symlink node_modules is invalid…`). List the affected
  steps under `materialize_deps` (e.g. `[build]`) and warden hardlink-copies the
  deps as real files for runs that include one of them; other runs keep the fast
  symlink. Hardlinks fall back to a byte copy across filesystems; internal `.bin`
  symlinks are preserved.

### Changed

- **Legacy provenance notes (written before this release) fail `verify`** — they
  carry no `CommitSHA` binding, so they must be re-validated. Correct fail-closed
  behavior, but re-run warden on affected commits.
- Inherited (`extends`) step lists now **merge** (union) rather than being
  replaced, so a base can't silently have a required step dropped; partial
  `risk:` overrides now merge field-by-field.

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
