# AGENTS.md — last updated: 2026-08-02
# Keep under 400 lines. Split overflow to memory/ files.

## Working Style
Output format: structured (tables + short sections); terse. Code/commits/PRs written normally.
Decision style: recommend directly with a clear default; surface real forks via a question, otherwise pick and note it.
When stuck: make a call and flag it; proceed to the obvious next step (tests, lint, push) without waiting.
Review mode: critique hard on correctness (esp. provenance/security invariants), then ship.

## Project Context
Project: warden (klarlabs OSS) — a local provenance git-gate.
Module: `go.klarlabs.de/warden` (vanity). Repo: klarlabs-studio/warden.
What it does: runs a validation pipeline in disposable per-step worktrees, records commit-bound provenance RunRecords, gates pushes on attestation. Cosign-keyless signed releases (goreleaser + SBOM + npm + GHCR, triggered by a `v*` tag).
Phase: active — v0.23.2 shipped 2026-08-02. Provenance classification and the CI-attestation loop are the live areas.
Stack: Go 1.26. Key subsystems: `internal/infrastructure/git` (worktrees, notes, dep exposure), `internal/domain` (policy, runrecord, audit states), `internal/infrastructure/kernel` (build/executor), `internal/application` (runner, ports), `internal/service` (verify, doctor, reattest, attest-external).

## Constraints
Never: weaken provenance verify to accept records not bound to the gated SHA — `Validated` MUST be `rec.Attests(sha)` (fail-closed: chain valid + evidence present + BindsTo).
Never: `git add -A` when untracked scratch dirs may exist (`.claude/`, agent worktree leftovers) — stage specific paths.
Never: make warden assert something it did not observe. Every over-claim shipped so far had this shape; see Known Failure Modes.
Always: before pushing a `v*` release tag, run `GOOS=windows GOARCH=amd64 go build ./...` (and arm64) — goreleaser cross-compiles Windows, but CI is ubuntu-only so a `//go:build unix` file passes CI and breaks the release build.
Always: run `gofmt -l .` + `golangci-lint run ./...` + `go test -race ./...` before pushing. `make e2e` needs `WARDEN_E2E=1` — without it the suite SKIPS and reports ok.

## The recurring defect: claiming more than the evidence supports
Every provenance bug this project has shipped is one defect wearing different
clothes. Worth knowing by name, because the next one will look new and will not
be:

| warden said | the truth |
|---|---|
| bypassed | the branch was never pushed (no remote → the pre-push gate never ran) |
| bypassed | a gated multi-commit push; only the tip carries a note, the span covers the rest |
| TAMPERED | a rebase left the note bound to another commit |
| verified | the note exists but does not attest the commit — `verify` refuses it |
| a PASS | for HEAD, when the caller named a different commit |
| pushed | from `--attest-only`, the mode built specifically not to push |
| attested | no note was written; the failure was swallowed as best-effort |

The lesson that generalises: **tests written from the same mental model as the
code cannot catch a wrong mental model.** All of these were found by running the
tool against real repositories and noticing a number that could not be true.
`e2e/goldenfleet_test.go` exists to make that systematic — it builds repos to a
KNOWN provenance shape, so the right answer exists independently of what warden
computes. Add a scenario there when you add a state.

Corollary: assert the artefact, not the exit status. A green run that produced
nothing is worse than a red one, because it stops anybody looking.

## Known Failure Modes
- A `//go:build unix` file passes ubuntu-only CI but breaks the goreleaser Windows cross-build. Correct by `GOOS=windows go build ./...` before tagging.
- Dep-exposure skips a symlinked `node_modules` because `if !d.IsDir()` skips symlinks. Correct with `filepath.EvalSymlinks`; when a materialize/Turbopack report persists, FIRST check `ls -ld node_modules` + `git worktree list`, not the build tool.
- `.gitignore` `node_modules/` (trailing slash) matches only directories — a `node_modules` SYMLINK stays tracked. In tests that need it ignored, use `node_modules` (no slash).
- Subagent `isolation:"worktree"` worktrees the CWD repo, not warden — give agents warden's ABSOLUTE path and have them make their own worktree there.
- The auto-mode classifier blocks self-merging your own PRs, `gh pr merge --admin`, and hand-editing `.nox/baseline.json` EVERY time — needs an explicit per-action user "proceed". A broad standing directive does NOT satisfy it. Prefer `nox baseline add -rule <ID> -reason "…"` over hand-editing: a fingerprint-only entry reads as an unexplained hash later and cannot be pruned by rule.
- warden's own nox `security-scan` gate scans `docs/` prose, and the AI-*/SEC-* rules keyword-match English. Reword rather than baseline a documentation false positive. A literal that looks like an email trips `DATA-001` — use a non-address identity (`warden-ci`, `warden-golden-fleet`); git does not validate the field.
- A "weird" desktop notification during dev is more likely a TEST with a real side effect than the app's runtime path. Check for tests invoking the real notifier before reasoning from the assumed runtime code path.
- **`refs/notes/warden` has two writers** (a developer's machine and CI, since 0.22.0). Before 0.23.2 a losing push was rejected non-fast-forward and the error discarded: the note stayed local, and the commit read as an ungated bypass everywhere else — including the CI gate, which then accused the author of a bypass that never happened. Fixed in 0.23.2 (fetch, merge, retry). **A machine still running an older warden keeps losing notes.** Manual recovery: fetch into a scratch notes ref, `git notes --ref=warden merge <scratch>`, `git push --no-verify origin refs/notes/warden:refs/notes/warden`. NEVER `git fetch … refs/notes/warden:refs/notes/warden --force` while an unpublished local note exists — it discards it.
- Publish a commit's note BEFORE opening its PR. Opening the PR triggers the provenance gate; publishing after it means a failing check and a re-run.
- `provenance-main.yml` installs the LATEST RELEASED warden (`WARDEN_VERSION: latest`), so a fix to warden does not reach it until it ships. The symptom reads exactly like a code bug: the gate runs green, every step passes, and the step still fails because the released binary behaves the old way. Check the "Install warden" step's version before debugging the gate.
- `go install` of a tool that requires a newer Go than warden targets fails under `setup-go`'s `GOTOOLCHAIN=local`. Scope `GOTOOLCHAIN: auto` to that step. This can only fail on the runner — a local `go install` uses whatever Go the developer has.
- The coverage gate is a real CI check (`cli`, 80% floor). Adding a command without exercising it fails the build; cover the paths that matter rather than padding.

## Exit codes
`0` pass (incl. `--attest-only`, which pushes nothing) · `1` the gate rejected the change · `2` bad invocation · `3` passed and **warden performed the push** (git must stand down; the following `error: failed to push some refs` is EXPECTED — confirm with `git ls-remote`, do not retry) · `75`/`78` the machine, not the change (lock contention / missing toolchain).

## Decision Summary
# 3–5 most consequential. Full log in memory/decisions.md
- 2026-07-05: provenance RunRecords are commit-bound + verify is fail-closed (`Validated: rec.Attests(sha)`) — the core security invariant.
- 2026-07-05: dependency materialization is the DEFAULT (`symlink_deps:true` opts out).
- 2026-08-02 (ADR 0003): an external-run attestation is a WEAKER claim and must be distinguishable at verify time. `verify` refuses it by default; `ExternalPolicy`'s zero value is Reject. It must be signed — an old warden drops the unknown field, recomputes `SigningPayload` without it, and fails the signature, so pinned-signer consumers fail closed by construction.
- 2026-08-02: warden's gate is client-side pre-push, so it can never note a commit the FORGE creates. On the measured fleet all eleven remaining "bypasses" were GitHub-authored merges. `provenance-main.yml` + `attest-external` close that.

## Active Patterns
- The Agent OS skills (`/brief`, `/capture`, `/mem-compact`) were REMOVED on 2026-07-21. Do not invoke them. Memory is handled by mnemos hooks; `./memory/*.md` here is a read-only archive — do not write to it.
