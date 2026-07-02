# warden v0.6.0

**A configurable git commit/push gate installed as native git hooks** — `git commit` and `git push` themselves are the gated commands. No second remote, no changed muscle memory. Runs your checks in a disposable worktree, leaves cryptographic proof, and lets CI trust that proof.

## Why not just a Makefile?

`make ci` runs your checks — but in your dirty working tree ("passes locally, fails CI"), only when you remember to run it, leaving no trace. warden does what a Makefile can't:

- **Runs clean.** Every check runs in a disposable worktree seeded from the commit — passing in warden means passing in CI, reproducibly.
- **Can't be forgotten.** Native git hooks fire automatically.
- **Leaves proof.** Each gated commit gets a hash-chained validation note that travels with the repo — so **CI can trust it and skip re-running the checks**, cutting minutes and cost.
- **Scales with risk.** Rules match on branch / path / diff size, so heavy checks and human approval apply only where they matter.

It complements your checks rather than replacing them — point it at the commands you already run.

## Install (any developer, no Go toolchain required)

```bash
npx @klarlabs-studio/warden init                         # no install, works anywhere Node is
brew install felixgeelhaar/tap/warden                    # macOS / Linux
curl -fsSL https://raw.githubusercontent.com/klarlabs-studio/warden/main/scripts/install.sh | sh
go install go.klarlabs.de/warden@latest                  # Go devs
```

Adopt an existing repo in one command — `warden import` reads your Makefile, npm scripts, lefthook, or CI into `.warden.yaml`. Or just `warden init`, which auto-detects the language (Go, Rust, JS/TS, Python) and pre-fills lint/test.

## Highlights

- **Native `pre-commit` / `pre-push` hooks** — the gate is the commands you already run; no gate remote.
- **Worktree isolation** — runs never touch your working tree; a passing pre-push fast-forwards your branch and performs the push itself.
- **Rule-based policy** — stack overrides by branch, path glob, and a risk heuristic; `warden policy explain` (and an XState chart) shows the resolved gate.
- **Cryptographic provenance** — hash-chained `refs/notes/warden` records; `warden doctor` audits them, `warden audit` exports them, `warden verify` powers **CI provenance-skip** (a GitHub Action included).
- **Custom steps with no code** — any step name with a command in config runs it; a `stepsdk` subprocess protocol is the advanced escape hatch.
- **Config-driven agents** — `claude` and `codex` presets built in; any agent via `agent_commands`.
- **Interactive TUI** for pre-push, plus agent surfaces (`warden axi` TOON output, `warden mcp serve`).
- **PR + CI** — opens/updates a pull request on a passing push; `warden ci` reports check status.

## Links

- Repo: https://github.com/klarlabs-studio/warden
- Docs / provenance-skip: https://github.com/klarlabs-studio/warden/tree/main/docs
- npm: https://www.npmjs.com/package/@klarlabs-studio/warden
