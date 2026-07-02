# Contributing to warden

Thanks for your interest in improving warden. This project follows a
straightforward, test-first workflow.

## Getting started

```bash
git clone https://github.com/klarlabs-studio/warden
cd warden
go build ./...
make ci        # the full local pipeline (see below)
```

warden requires **Go 1.26+**. It dogfoods itself: after `warden init`, your own
commits and pushes run through the gate.

## The pipeline (`make ci`)

Every change must pass the same gate CI runs, in order:

| Step | Command | What it checks |
|---|---|---|
| format | `make fmt-check` | gofmt-clean |
| vet | `make vet` | `go vet` |
| lint | `make lint` | golangci-lint (govet, staticcheck, gocritic, misspell) |
| gocritic | `make gocritic` | diagnostic/style/performance |
| security | `make sec` | nox scan (findings baselined in `.nox/baseline.json`) |
| vulnerabilities | `make vuln` | govulncheck |
| tests | `make test` | unit tests with `-race` + coverage |
| coverage | `make cover` | coverctl — **80% per domain** |
| e2e | `make e2e` | end-to-end hook runs (`WARDEN_E2E=1`) |

Run the whole thing with `make ci`. New code needs tests; coverage gates at 80%
across each domain (application, cli, domain, infrastructure, mcp, policy,
service).

## Architecture

warden is a hexagonal / DDD codebase:

- `internal/domain` — pure domain model + services (no I/O).
- `internal/application` — use-case orchestration (the pipeline Runner) behind ports.
- `internal/infrastructure` — adapters (git, kernel, forge, signing, cache, …).
- `internal/policy` — the rule-resolution domain service.
- `internal/service` — the composition root wiring it together.
- `internal/cli` / `internal/tui` — delivery.

Keep the dependency direction pointing inward: domain depends on nothing;
infrastructure depends on ports, never the reverse.

## Commits & PRs

- **Conventional commits** — `feat:`, `fix:`, `docs:`, `chore:`, `refactor:`, `test:`.
- Keep commits atomic; each should pass `make ci` on its own.
- Open a PR against `main`; fill in the template. CI must be green.
- Prefer a proper fix over a workaround; add a regression test for every bug.

## Custom steps & recipes

Adding a check is usually config, not code — see the **Custom steps** and
`warden recipes` sections in the [README](README.md). The subprocess SDK
(`stepsdk`) is the escape hatch for steps that need structured findings.

## Reporting bugs / security

- Bugs and features: open an issue (templates provided).
- Security vulnerabilities: **do not** open a public issue — see [SECURITY.md](SECURITY.md).
