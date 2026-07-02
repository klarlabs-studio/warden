# warden v0.7.1

A patch release fixing one dogfooded bug.

## Fixed

- **Committing a binary file no longer fails the gate.** Worktree seeding
  captured and re-applied the staged diff without `--binary`, so staging an
  image or any binary asset failed pre-commit with *"cannot apply binary patch …
  without full index line"*. The staged-diff and auto-fix paths now round-trip
  binaries (`git diff --binary` / `git apply --binary`), with a regression test.

No config or API changes — a drop-in upgrade from 0.7.0.

## Upgrade

```bash
brew upgrade felixgeelhaar/tap/warden
npm i -g @klarlabs-studio/warden@latest
go install go.klarlabs.de/warden@latest
```
