# Changelog

All notable changes to warden are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and warden adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- **The discard guard now has a proportionate override** (#124). Refusing a
  force-push that would delete remote-only commits is right, but its only
  escape was `git push --no-verify` — which skips test, lint *and*
  security-scan and writes no provenance. A guard whose sole escape hatch is
  the nuclear one teaches people to reach for the nuclear one, so the scan
  stops running exactly on the branches that just absorbed someone else's
  changes. `WARDEN_ALLOW_DISCARD=1` now bypasses **this check alone**, names
  the commits it force-pushed over, and leaves every other step running. It is
  an environment variable rather than a flag because the developer types
  `git push` — warden runs as the hook and has no command line of its own. It
  deliberately does **not** override `push.force: never`, which is a repo
  policy decision rather than a per-push safety check.


### Fixed

- **A re-driven release now actually replaces its artifacts.** 0.20.2 claimed
  #125 had made the re-drive idempotent. It had not: `release.mode` governs the
  release *notes* (`keep-existing`/`append`/`prepend`/`replace`), while the
  uploaded *assets* are governed by a separate `replace_existing_artifacts`
  key. Only the first was set, so the v0.20.2 re-drive failed identically to
  v0.20.1 — `422 already_exists` on every asset, before reaching the homebrew
  step it was re-driven for. Both keys are set now.

  The claim in 0.20.2 below is left as written rather than rewritten, since it
  describes what was believed at the time; this entry is the correction.

## [0.20.2] — 2026-07-26

Fixes a gate deadlock that could make a branch unpushable through warden, and
completes the tap move 0.20.1 was cutting the ground for.

### Fixed

- **The push guard no longer refuses your own rebased commit** (#127). A branch
  that went `BEHIND`, was replayed by warden's own pre-push `rebase` step, and
  then pushed could be refused as *"push would discard commits that exist only
  on the remote"* — naming a commit you wrote. The advice it printed, `git pull
  --rebase`, could not help: the next push rebases again and reproduces the
  divergence, so the branch became unpushable and the only way out was `git push
  --no-verify`, a bypass with no provenance — the precise outcome
  `push.force: lease` exists to prevent.

  The guard asked `git cherry` whether the remote's copy had a patch-equivalent
  locally. patch-id ignores hunk line numbers but **not** the context lines
  quoted around a hunk, so a base commit editing anything within three lines of
  your change moves the patch-id while leaving the change itself untouched.
  Adjacent edits are ordinary, so this fired often rather than rarely — #125,
  landing directly above the cask block, is what triggered it here.

  When patch-id cannot match, a commit is now treated as yours to replace only
  if it was once reachable from this branch locally (its reflog) **and** is
  committed by you. Either test alone is too generous: the reflog would let a
  colleague's commit be discarded after you pulled it in and dropped it during
  an interactive rebase, and the identity says nothing about whether the commit
  was ever on this branch. Anything unreadable — no reflog, no configured
  identity — leaves the commit reported, because refusing to force is the safe
  direction.

  The pre-existing test for this path rebased an **empty** commit, whose
  patch-id cannot diverge, so it passed either way. The new one rebases a real
  change across an insertion inside its context window, and asserts the
  patch-ids actually diverge so the scenario cannot silently stop reproducing.

- **A re-driven release no longer dies before it reaches the step it was
  re-driven for** (#125). `release.yml` has `workflow_dispatch` precisely so a
  release whose brew push failed can be re-run without re-tagging, but with
  goreleaser's default mode every asset was re-uploaded and the run died on
  `422 already_exists` before reaching the homebrew step — so the recovery path
  could never recover. `release.mode: replace` makes it idempotent. Note the npm
  job is still not: it refuses with *"cannot publish over the previously
  published versions"*, which is correct for a re-drive and harmless, but it
  means a re-driven run still ends red.

### Changed

- **The cask publishes to `klarlabs-studio/homebrew-tap`** (#126). warden is a
  klarlabs-studio repo and the tap credential is a klarlabs-studio org secret,
  but the cask was published to a personal tap that secret cannot reach. That is
  the other half of the history recorded in 0.20.1: consolidating onto one org
  secret fixed the credential living under three names, and left the
  *destination* outside the credential's scope. Existing
  `brew install felixgeelhaar/tap/warden` users are carried over by a
  `tap_migrations.json` entry in the old tap.

## [0.20.1] — 2026-07-26

No user-facing change. warden behaves identically to 0.20.0; this release
exists to carry internal quality and release-plumbing work, and to prove the
rotated Homebrew tap credential end to end after 0.19.0 shipped with a 401 and
0.20.0 with a 403.

### Changed

- **Adopted the two linters warden was missing** from the org's golden
  `golangci.reference.yml`, `errcheck` and `unparam` (#121). 54 findings, no
  bugs — the one that looked dangerous, an unchecked `f.Close` after a write in
  `writeTarFile`, was already correct because the function returns a checked
  `f.Close`. 46 unchecked returns are now explicit at the call site rather than
  suppressed by configuration.
- **`remoteTrackingSHA` no longer claims a failure mode it does not have.** It
  returned `(string, error)` with an error that was always nil, so two callers
  carried conditions that could never be false and a `return false, err` that
  could only return nil. It returns a plain string now. Behaviour is unchanged;
  the signature stops lying about it. Similarly `runStdin` returned combined
  output no caller read, and `exitForBlocker` took a parameter every call site
  set to `1`.
- **The scanner version-drift check now resolves the central nox pin** (#119).
  warden's nox version is pinned once in the shared reusable workflow, so
  scanning this repo for a pin found nothing and the check was silently inert
  here. `security_scan.pin_file` names where that pin lives.
- **The release workflow reads the tap credential from an org secret** (#122).
  The same credential previously lived under three names across the repos
  publishing to the tap, so rotating it meant updating all of them and missing
  one stayed silent until that repo's next release.

### Documentation

- **Closed the 0.18.x gap in this changelog** (#120). Seventeen released
  versions had no entry, so the history read as truncated between 0.19.0 and
  0.17.0. They were all npm OIDC trusted-publishing iteration plus two
  dependency bumps — recorded as one entry rather than seventeen empty ones.

## [0.20.0] — 2026-07-25

### Fixed

- **`warden init` no longer rewrites an existing config.** Authorship was
  inferred from the *parsed* config — "has rules or commands" — so a policy
  built entirely on built-in steps read as "no config" and was regenerated from
  defaults. A `.warden.yaml` setting `steps:` and `trusted_keys:` but no
  `commands:` had its steps reset **and its trusted-signer roster emptied**,
  silently dropping the repo from trusted-signed to attested depth. That is the
  one config whose loss is a security change rather than an inconvenience, and
  the function's own doc comment already promised it "never rewrites the file".
  Whether a file exists is no longer deduced from its contents.
- **A failed step's output survives.** The TUI inlined a finding's entire
  message into a frame that is redrawn in place, so a failing `go test` made the
  frame taller than the terminal and everything above the last screenful was
  lost — leaving the tail, often the bare word `FAIL`, with the failing package,
  test name and assertion gone. Without a test name an intermittent test is
  indistinguishable from an environment problem or a real failure that happened
  to clear, so the only available response was to retry until green: the exact
  habit a gate exists to prevent. The frame now previews a finding (keeping the
  *head*, where a test failure names itself) and a failed run reprints its
  findings to stdout after teardown, matching what the non-interactive path
  always did.
- **A reusable workflow's input `default:` is read as a scanner pin.** Only
  scalar values were read, so warden could see a caller that *overrode* a pin
  but never the definition that *set* it.

### Added

- **`security_scan.pin_file` accepts a cross-repository reference**, for a fleet
  that pins its scanner once in a shared reusable workflow:

  ```yaml
  security_scan:
    pin_file: my-org/.github/.github/workflows/go-ci.yml@main
  ```

  The version-drift check searched only *this* repo's workflows, so it no-opped
  for exactly the repos that followed the advice to centralize the pin. This
  names **where** the pin lives, never what it is — there is still one copy of
  the version, in the workflow that already defines it. Resolution is bounded by
  a 3s timeout and cached for an hour, and every failure (offline, 404, moved,
  malformed) degrades to "no pin found" rather than failing a push.
- **`warden status` reports an inert scanner version check.** Silence used to
  mean both "checked, the versions agree" and "found no pin, compared nothing".

### Changed

- warden's own gate runs the `credentials` step, and its provenance `gate` check
  is now required — the three defects that made it fail on legitimate PRs
  (0.19.0) are fixed.
- Release plumbing: a brew-cask failure can no longer strand the floating `v0`
  tag, the release workflow can be re-driven with `workflow_dispatch`, and the
  tap credential has one name instead of two.

## [0.19.0] — 2026-07-25

### Changed

- **A successful pre-push now exits 0.** Warden performs the push itself and
  used to fail the hook on purpose, so every successful push ended on
  `error: failed to push some refs` and a non-zero status — automation read
  success as failure, and warden had to print a disclaimer telling people to
  ignore the next line, training them to ignore `error:` from a gate. When
  nothing rewrote the branch and no force is needed, the push git already has
  queued *is* warden's push, so warden now stands aside and lets git report the
  real outcome. It still pushes itself (and still exits non-zero, with the
  disclaimer) when a step rewrote the branch — git would otherwise publish the
  unvalidated pre-fix commit — or when a force is needed, or when a PR is to be
  opened. **If you script `warden run pre-push` or the hook's exit status, this
  is a contract change.** Closes #89.
- **`rebase` targets the branch's integration base, not its own remote ref.**
  It rebased onto `@{upstream}`, which is right until the branch is rewritten:
  after a local rebase onto an updated `main` — the standard way to satisfy
  "head branch is not up to date with the base branch" — `origin/<branch>` still
  holds the commit just replaced, so `origin/<branch>..HEAD` contains *main's*
  commits and the step replayed main onto the superseded tip, failing on main's
  own conflicts and refusing a push that was never wrong. It now resolves
  `pr.base` → the remote's default head → an `@{upstream}` pointing elsewhere,
  and never the branch's own ref. Note this also means the step *runs* on a real
  pre-push for the first time: warden seeds a detached worktree, where
  `@{upstream}` never resolved, so it had been reporting "no upstream, skipped"
  throughout. Closes #102.

### Fixed

- **Security: a range gate read its trust roster from the head it was checking.**
  `warden verify --range` resolved `trusted_keys` from the working tree — the PR
  head under gate — so a change could add its own key to `.warden.yaml` and
  self-certify. The roster is now resolved from the range's **base** ref (the
  trusted side) and a malformed base roster fails closed. Separately,
  `reattest` carried provenance from any self-verifying note, letting an
  untrusted note pushed for a tree-identical commit be laundered into a
  locally-trusted re-attestation; the source must now also be signed by a
  trusted key.
- **Security: two known CVEs in indirect dependencies.** `google.golang.org/grpc`
  → v1.82.1 (GHSA-hrxh-6v49-42gf, high) and `golang.org/x/text` → v0.39.0
  (GO-2026-5970, flagged reachable).
- **`warden reattest --push` publishes even when it writes nothing.** The push
  was gated on *this* invocation having written a note, so the obvious two-step
  workflow — sweep, inspect, then publish — left every note local while the
  command reported success. Found by using it: 19 notes stayed local and the
  remote notes ref sat 20 commits behind. `--push` now means "make the remote
  match". The single-commit form had the same trap on its already-noted path.
- **A gated push no longer deletes a colleague's commits.** With
  `push.force: lease`, warden force-pushed whenever the remote tip was not an
  ancestor of the local branch — a test that cannot tell "I rebased my own
  commits" from "someone else pushed to this shared branch". The second case
  silently destroyed their work, and `--force-with-lease` did not catch it: the
  lease only asserts the remote has not moved since your last fetch, so once you
  have fetched their commit the lease is satisfied and the push deletes it.
  Reproduced against a real remote — a colleague's commit and its file vanished
  from the branch with no warning. Warden now compares patch-ids (`git cherry`)
  before forcing and refuses when the remote carries work with no equivalent in
  your history, naming the commits at risk and pointing at `git pull --rebase`.
  A branch rebased onto an updated base still publishes exactly as before: the
  remote holds only your own pre-rewrite commits, which are patch-equivalent, so
  nothing is at risk. An unreadable comparison refuses rather than guesses.

### Added

- **A provenance note covers the span its push published, not just the tip.** A
  run writes one note, on `HEAD`, so an ordinary commit → commit → commit → push
  left the earlier commits reading `UNVERIFIED` forever while
  `warden verify --range` demanded a note on every one — the range gate was
  checking for provenance the gate never produces. Attesting each commit
  individually would be a lie (a run validates one tree, the tip's; the
  intermediate trees were never checked out), so the note now records the span
  `(covers_from, commit_sha]` *inside the signed payload*, and `verify --range`
  reads it. A covering note must clear the same signature and trust bar it is
  being used to satisfy, the span is bounded by real git history rather than by
  what the note claims, and a commit outside every trusted span keeps its
  failure. `verify --range` reports how many commits passed by span rather than
  by their own note. Closes #86.
- **`push:` config block** — `force: lease` (default) or `never`. Warden performs
  the push itself, and git's pre-push hook is handed no signal that you typed
  `--force`, so a rebased branch could not be pushed through the gate at all; the
  only way out was `git push --no-verify`, which skips the gate and writes no
  provenance. Warden now detects the rewrite and decides by policy. Closes #85.
- **`secrets` step** — refuses a change in which a tracked file the change
  touched carries a live credential. Narrow on purpose (changed files only,
  high-confidence shapes; `${VAR}` placeholders never match) because a false
  positive is a wall in front of an unrelated commit. Findings name file and line
  but never echo the value.
- **`warden reattest --all [--branch b]`** sweeps a branch from the adoption
  point and closes every recoverable squash-merge gap in one pass, applying
  exactly the same trust rules as the single-commit form. `doctor` and `audit`
  now read `UNVERIFIED (reattestable from <sha>)` where a validated
  tree-identical commit exists, so a recoverable binding gap is distinguishable
  from a genuinely unchecked commit. On warden's own `main` this recovered 19 of
  the unverified commits. Closes #76.
- **`security-scan` gates on the diff, not the repo's absolute state.** When the
  step's command is a nox scan, warden now reads the scan's `findings.json` and
  fails only on findings absent from the merge-base; pre-existing ones are
  reported as a counted warning. Gating on total state meant an unrelated
  one-line change inherited the repo's whole backlog as a precondition — across
  a fleet rollout, five repos had a finished config commit blocked by 7/16/21/44/71
  pre-existing findings, and the gate was red in 5 of 7 repos sampled while
  commits kept landing, i.e. it was being bypassed with `--no-verify`. A
  routinely bypassed gate protects nothing and removes the signal that it ever
  ran. `security_scan.mode: total` keeps the old strict behavior for a repo that
  has reached zero. Closes #87.
- **Warden refuses to scan on scanner version drift.** A scanner that renumbers
  rule IDs between releases gives the same hit a different fingerprint, so every
  committed baseline entry stops matching at once. One repo pinned `NOX_VERSION:
  1.3.0` in CI while developers ran 1.15.0: none of its 729 baseline entries
  matched, CI reported 240 phantom criticals, and every release failed for a
  month because the job only ran on tags. The step now reads the pin out of
  `.github/workflows/*.yml` (a single source of truth — not a second copy in
  `.warden.yaml`) and fails at pre-push with both versions and the pinning file
  named. A baseline matching *zero* current findings is likewise reported as
  drift rather than as hundreds of new criticals. Opt out with
  `security_scan.version_check: false`; point at one workflow with
  `security_scan.pin_file`. Closes #88.
- **`security_scan:` config block**: `mode` (`delta` default / `total`), `base`,
  `version_check`, `pin_file`. It inherits through `extends:`, and a child config
  cannot relax an org base's `total` to `delta` — the same "a child may add
  strictness, never drop it" rule `writes:` and `trusted_keys:` already follow.
- **`credentials` step: a push can no longer carry a secret out of a tracked
  file.** New built-in step, on by default at pre-push, that reads the files the
  change touched and refuses the push when one holds a live-looking credential
  (prefix-tagged GitHub / npm / AWS / Slack / Stripe / OpenAI / Anthropic /
  Google tokens, and PEM private keys). It closes a trap the gate itself was
  creating: a JS step fails for want of dependencies, installing them needs
  private-registry auth, and `npm config set …_authToken "$(gh auth token)"` —
  the command every guide reaches for — writes a live token into `.npmrc`, a
  file most repos *track* with a `${NODE_AUTH_TOKEN}` placeholder in it. The
  path of least resistance out of a red gate ended in a staged credential.
  Matches are redacted in the output; lines deferring to a variable
  (`${NODE_AUTH_TOKEN}`, `{{ secrets.X }}`) and obvious dummies
  (AWS's documented `AKIA…EXAMPLE` key) are not findings. Deliberately shallow — changed
  files only, issuer prefixes only, no entropy heuristics — because a check that
  cries wolf gets deleted. For real coverage add `warden recipes gitleaks`; to
  opt out, leave `credentials` out of `steps.pre_push`. Note the upgrade order:
  an *explicit* `steps.pre_push` list naming `credentials` is a hard error on a
  warden that predates this release ("define commands.credentials … or install
  warden-step-credentials"), so upgrade the binary before adding the name.
  Repos that don't pin `steps` pick it up automatically with the new binary.
  (#91)
- **Distinct exit codes for a gate that never ran.** `75` (`EX_TEMPFAIL`) when a
  step was blocked by another process's lock — retry later — and `78`
  (`EX_CONFIG`) when its toolchain or dependencies are not installed, where
  retrying is pointless. Previously everything collapsed onto `1`, which on
  pre-push *already* means both "passed and pushed" and "failed", so no wrapper
  could tell a lock it should wait out from a verdict it must not retry.
  (#90, #91)

### Changed

- The base scan is cached per base commit under the repo's git dir, so a repo
  with a standing backlog does not pay for re-scanning an unchanged base on every
  push. Any command whose report warden cannot read (`make audit`, `npm audit`, a
  nox invocation that sets its own `-output`/`-format`) keeps the previous
  run-it-and-check-the-exit-code behavior, as does any case where the report,
  the base ref, or the base scan is unavailable — the step degrades toward
  failing, never toward passing.

### Fixed

- **A step that never ran is no longer reported as a step that failed.** The
  step-level message was fixed in v0.17.0, but the run's own verdict still read
  `warden: step lint failed` — the line the developer actually sees — when the
  truth was that a sibling repo held golangci-lint's machine-global lock. The
  verdict now names the obstacle: `step lint could not run: another process
  holds its lock`. (#90)
- **A missing toolchain is reported as an environment failure, not a build
  failure.** `sh: astro: command not found` from a `js-build`/`js-check` step
  meant the checkout has no `node_modules`, but read as a broken build — and
  blocked pushes whose diff touched no JS at all. Warden now recognizes every
  common shell's wording for a missing executable (plus `Cannot find module`),
  says plainly that nothing is wrong with the change, and names the exact
  install command derived from the lockfile actually present (`npm ci`,
  `pnpm install --frozen-lockfile`, `yarn install --immutable`,
  `bun install --frozen-lockfile`), scoped to the right package in a monorepo.
  It withholds the advice when `node_modules` is already there, since a
  reinstall would not be the fix. The gate still fails — an unbuilt tree is not
  a validated tree. (#91)
- **golangci-lint contention now comes with the permanent cure.** The failure
  message suggests `--allow-parallel-runners` when the lint command doesn't
  already use it. That lock guards a *shared* cache, and warden already gives
  every run its own `GOLANGCI_LINT_CACHE` — so only warden is in a position to
  know the flag is safe here. (#90)
- **`NODE_AUTH_TOKEN` is documented as the supported private-registry path**,
  with an explicit warning against `npm config set`, in the README and in the
  missing-dependency failure message itself. (#91)
- **The pre-commit pass line names the steps that ran.** Under a split policy
  `warden: pre-commit passed.` meant *lint* passed while the suite was unrun, but
  read as "my tree is green" — a commit went through clean while `go test -race`
  was red. Now: `pre-commit passed (lint) — test runs at pre-push.` Closes #78.
- **Hook version-pin skew is visible.** The shim prefers a `warden` on `PATH`, so
  the pin only ever governed developers with no global install — while the shim's
  comment claimed the whole team ran the same verified binary. The pin is a
  bootstrap floor, not a lock; the shim now says
  `hook pins 0.17.0, PATH has 0.18.16 — running 0.18.16`, and `warden status`
  shows each hook's pin and names any divergence. Closes #77.
- **`warden status` surfaces `warden watch`** when a step is deferred to a later
  hook, which is exactly the gap watch exists to close. Closes #79.
- **Desktop notifications are usable.** They were posted via `osascript`, so
  macOS filed them under *Script Editor* — silently suppressed unless that app
  had notification access, and clicking one opened an empty script. Warden now
  prefers `terminal-notifier` (its own identity, click returns to the terminal
  the gate ran in, grouped per repo) and falls back to `osascript`. The content
  is structured too — `warden: pre-push failed` / `repo · branch` /
  `step lint failed` — and `warden status` names a degraded setup, since the
  macOS fallback fails invisibly.
- **A tool that refuses to run concurrently is no longer reported as a lint
  failure.** `golangci-lint` declines to start while another copy holds its lock;
  warden reported that refusal as `step lint failed`, sending developers hunting
  an error that did not exist. It now waits the lock out and, if the budget
  expires, says `could not run (lock contention)`. Closes #81.
- **The `warden-gate` / `warden-verify` actions no longer need a Go toolchain.**
  They ran `go install`, so the documented `setup-go` with
  `go-version-file: go.mod` killed the job *before verifying anything* in any
  repo without a root `go.mod` — a permanently red check that provided zero
  verification while appearing enforced. They now install a released,
  checksum-verified binary, failing closed on a mismatch. Closes #92.

## [0.18.0] – [0.18.16] — 2026-07-11

Seventeen releases in one day, none of which changed how warden behaves. The
series was a single sustained attempt to get npm **trusted publishing (OIDC)**
working, and each failure mode only revealed itself on a real publish — hence a
release per attempt. Recorded as one entry rather than seventeen empty ones,
and noted here because the jump from 0.17.0 to 0.19.0 otherwise looks like
missing history.

### Changed

- **npm packages now publish via OIDC trusted publishing** instead of a
  long-lived token (#63–#75). Getting there needed, in order: publishing with
  `--provenance`; dropping `registry-url` and later restoring it; adding
  `repository.url` to every per-platform package; pinning npm 11.x, because npm
  12 returns `ENEEDAUTH` under trusted publishing; and finally clearing the
  `setup-node` placeholder token, which was overriding OIDC with an empty
  credential and producing a 404.
- Bumped `go.klarlabs.de/mcp` to v1.22.0 (#62) and v1.24.0 (#69).

If you are on 0.17.0, 0.18.16 behaves identically — there is nothing to read
into the gap beyond release plumbing.

## [0.17.0] — 2026-07-07

### Added

- **Floating `v0` action tag.** The reusable `warden-gate` / `warden-verify`
  actions can be referenced as `@v0`, a major-version tag that tracks the latest
  `v0.x` release (a `major-tag` release job keeps it current; it becomes `@v1`
  once warden reaches 1.0.0). Docs recommend `@v0` for convenience and an exact
  version or commit SHA for a security-critical, immutable gate.

### Changed

- **Parallel batches isolate only tree-writers.** Per-step worktree isolation now
  clones an ephemeral worktree only for steps that write the tree (agents);
  read-only checks (`test`/`lint`/scan) share the canonical worktree, which the
  policy contract already guarantees they don't mutate. The common `test‖lint`
  batch materializes no extra dependency copies — the per-batch clone count drops
  from N to the number of writers, usually zero, cutting setup cost most on large
  cross-filesystem JS repos. The v0.10.1 write-race fix is unchanged (ADR-0001
  Phase 4).
- **Docs: guidance on choosing a gate posture and operating the roster.** The CI
  provenance gate doc now covers advisory-vs-required (required suits shared /
  high-assurance / org repos; advisory is usually right for a solo repo), backing
  up your signer against single-key lock-in, and why a bot/CI signing key is best
  avoided (it moves the trust boundary off a human). No code change.

### Fixed

- **A push that advances no branch no longer re-runs the gate.** The pre-push hook
  ignored git's stdin and always gated the current branch's HEAD, so a notes push
  (`refs/notes/warden`), a tag, a branch deletion, or an unrelated ref needlessly
  re-ran the whole validation pipeline and made warden push HEAD. warden now reads
  the pushed-ref list and exits 0 (letting git complete the push) when no
  `refs/heads/*` ref is created or updated. Fail-safe toward gating: it reads
  stdin only when it isn't a terminal (a manual run is never blocked), and an
  empty or unparseable payload still gates. Branch pushes gate exactly as before.
- **The notification test no longer pops a desktop notification.** A unit test
  called the real `notify.Send`, which shells out to `osascript`/`notify-send`, so
  every `go test` run on a machine with a notifier fired a stray "title / body"
  desktop notification. The shell-out is now behind a seam the test stubs.

## [0.16.0] — 2026-07-06

### Added

- **`warden attest` — export provenance as an in-toto attestation** (ADR-0002
  Phase 3). Projects a commit's `refs/notes/warden` record into an in-toto
  `Statement/v1` with a warden predicate type, so provenance can feed sigstore /
  GUAC / policy engines instead of staying a warden-only note. Read-only; pipe it
  to `cosign attest` to sign the statement. Uses a warden predicate (not
  `slsa.dev/provenance`) because warden attests *source*, not *build*, provenance.
- **`warden reattest` — close the squash-merge provenance gap** (ADR-0002
  Phase 3). A squash-merge commit reproduces the gated PR head's tree exactly but
  has no note, so `doctor`/`audit` on the base branch flag it. `warden reattest`
  (run locally by a maintainer whose key is trusted) finds the tree-identical,
  intact, validly-signed source note, carries its evidence onto the squash commit
  marked `reattested_from`, and re-signs locally — **no hosted bot or CI signing
  key**, a materially simpler design than originally sketched. It fails safe:
  with no content-identical validated source it writes nothing, never asserting a
  validation that didn't happen.

### Fixed

- The CI-provenance docs referenced the bundled actions at a `@v1` tag that does
  not exist; they now pin to a real release tag (`@v0.16.0`), matching warden's
  pin-actions-to-a-version convention.

## [0.15.0] — 2026-07-06

### Added

- **Committed trusted-signer roster: `trusted_keys` in `.warden.yaml`**
  (ADR-0002 Phase 3). Instead of hand-passing `--key <fingerprint>` on every
  call, list your trusted signers once — a bare `warden verify` / `verify
  --range` (and so `warden-gate`) then requires a trusted signer automatically.
  Because it rides on config it **inherits through `extends:`**: an org base
  policy names its signers once and every repo unions them in (a repo may add
  its own in a reviewed diff; it can't silently drop the org's). An explicit
  `--key` still overrides. Malformed entries are rejected at config load. New
  `warden key list` shows the effective roster and flags whether this machine is
  in it; `warden key show` now points at the roster.
- **`warden-gate` action — provenance enforcement as a required check**
  (ADR-0002 Phase 2). The enforcement counterpart to the `warden-verify`
  (provenance-skip) reporter: it runs `warden verify --range` on a PR and
  **fails the check** when any commit lacks trustworthy provenance, so un-gated
  commits can't merge. It runs on the PR *head* — whose commits still carry
  their notes — gating the merge *before* GitHub's squash rewrites history (the
  pragmatic answer to the squash-merge break). Inputs: `key` (trusted roster),
  `require-signed`, `skip-merges`, `base`/`head` (auto from the PR/push event),
  `remote`, `warden-version`. Also ships a self-hosted **pre-receive** recipe
  that rejects the push at the remote. See
  [docs/ci-provenance-gate.md](docs/ci-provenance-gate.md).

## [0.14.0] — 2026-07-06

### Added

- **`warden verify --range BASE..HEAD` — a true provenance gate.** Verifies
  *every* commit in a range and exits non-zero if any lacks trustworthy
  provenance, with a per-commit reason (`missing` / `broken-chain` / `unsigned`
  / `untrusted`) and `--json` for CI. Unlike `doctor` — which flags only
  *missing* notes — this fails a **tampered or transplanted** note too, and
  `--require-signed` / `--key <roster>` escalate the bar from "a warden ran" to
  "a *trusted* warden ran and the note is intact." `--skip-merges` (default on)
  omits merge commits, whose parents are gated individually. This is the
  primitive a PR required-check or a pre-receive hook wraps — see
  [ADR-0002](docs/adr/0002-provenance-enforcement.md). Read-only: the push and
  signing paths are untouched.

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
