# Adoption guide

Rolling warden out onto a real repo — one with existing lint debt, an
established CI, and bots reviewing pull requests — surfaces a few interactions
that aren't warden bugs but will trip you up if you don't plan for them. This
guide collects the patterns that worked across a 13-repo dogfooding rollout.

- [Gate the change, not the history](#gate-the-change-not-the-history) — adopt a
  strict linter on a debt-laden repo without a big-bang refactor.
- [Automated PRs vs. Copilot review + conversation resolution](#automated-prs-vs-copilot-review--conversation-resolution) —
  keep self-maintaining PRs from stalling.

---

## Gate the change, not the history

**The problem.** You want to turn on a strict linter — say golangci-lint with a
dozen extra linters — on a repo that has hundreds of pre-existing findings.
Gating the whole tree fails every commit until someone burns down the backlog,
so the linter gets turned off and never comes back.

**The pattern.** Baseline the debt and hold only *new* code to the full bar. The
gate then fails a commit for findings it introduced, not for findings it
inherited. Two tools carry this, one per ecosystem.

### Go — golangci-lint `new-from-rev`

golangci-lint can report only the findings on lines that changed relative to a
base ref:

```yaml
# .golangci.yml
issues:
  # Report issues only for code changed since this ref. Existing debt is
  # grandfathered; new code is held to the full linter set.
  new-from-rev: origin/main
  # Count a finding as new only if its own line changed, not just its file.
  whole-files: false
```

Wire it into the gate like any command:

```yaml
# .warden.yaml
commands:
  lint: "golangci-lint run"
steps:
  pre_commit: [lint]
  pre_push: [rebase, lint, test]
```

> **Requires warden ≥ 0.8.2.** git exports `GIT_INDEX_FILE`/`GIT_DIR` while a
> hook runs. Before 0.8.2, a step inherited them, so `golangci-lint
> --new-from-rev` resolved git against the live hook index instead of the
> disposable validation worktree and flagged the entire backlog instead of the
> change. 0.8.2 scrubs those vars in `stepEnv`; on older warden, `new-from-rev`
> is unreliable inside the gate. See the [CHANGELOG](../CHANGELOG.md) entry for
> 0.8.2.

**How the base ref behaves in the gate.** Warden validates in a worktree seeded
from your change (staged tree on `pre_commit`, branch tip on `pre_push`), so
`origin/main` resolves to the same base your PR will diff against. Keep
`origin/main` fetched (`fetch-depth: 0` in CI; a normal local clone already has
it) so the diff base exists.

### Everything else — a security/lint baseline

For tools that scan the whole tree rather than a diff (secret/vuln scanners,
some linters), snapshot today's findings as an accepted baseline and let the
gate flag only what's net-new. warden pairs cleanly with
[nox](https://github.com/nox-hq/nox) here:

```yaml
# .warden.yaml
commands:
  security-scan: "nox scan . -severity-threshold high"
steps:
  pre_push: [rebase, lint, security-scan, test]
```

```sh
# One-time: accept the current findings as the baseline (commit .nox/baseline.json).
nox baseline write
git add .nox/baseline.json && git commit -m "chore: baseline existing findings"
```

> **Write the baseline from a clean checkout, and use nox with the [#140
> fix](https://github.com/nox-hq/nox/pull/141).** warden validates in a git
> *worktree* (where `.git` is a pointer file, not a directory). nox releases
> before that fix failed to load `.gitignore` inside a worktree and scanned
> ignored subtrees, so a baseline written from your normal checkout didn't match
> the gate's worktree rescan and every push showed phantom net-new findings. On
> a fixed nox, dir and worktree scans are identical and a single committed
> baseline just works — no per-repo workaround.

### Why this beats a one-shot cleanup

The debt is recorded, not hidden: `new-from-rev`'s base ref and the committed
baseline are both reviewable, and both shrink naturally as touched code gets
cleaned. New code is held to the full bar from day one. Nobody has to land a
thousand-line "fix all the lints" PR to turn the linter on.

---

## Automated PRs vs. Copilot review + conversation resolution

**The problem.** Two GitHub settings that are each reasonable combine badly for
*automated* pull requests (dependency bumps, remediation, warden-generated
fixes):

1. **Automatic Copilot code review** leaves a review thread on the PR.
2. **Branch protection → "Require conversation resolution before merging"**
   blocks merge until every thread is resolved.

A human resolves the thread and merges. A bot can't — so a PR that was supposed
to be self-maintaining stalls indefinitely, waiting on a conversation only a
person can close. This is not a warden defect; it's an interaction of two repo
settings. But if you're running warden or any remediation bot alongside Copilot
review, you'll hit it.

**Recommended settings.** Pick whichever fits your risk posture:

- **Scope Copilot review to human PRs.** Disable automatic Copilot review on
  automation branches (e.g. exclude `dependabot/*`, `warden/*`, your
  remediation prefix) so bot PRs never accrue a blocking thread. Human PRs keep
  full review.
- **Drop conversation-resolution where zero approvals are required.** If a
  ruleset already lets a 0-approval automated PR merge, requiring conversation
  resolution on that same ruleset is the thing that traps it. Relax
  conversation-resolution for the automation path (a separate ruleset or a
  branch-name condition) while keeping it on the human path.
- **Or gate on warden instead of a review thread.** Let warden's validation note
  be the merge signal for automated PRs (see
  [provenance-skip](ci-provenance-skip.md)) and don't require conversation
  resolution on that path — the gate already ran the checks.

Whichever you choose, the goal is the same: an automated PR's merge condition
must be something an automation can actually satisfy.
