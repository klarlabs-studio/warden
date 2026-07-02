# warden (npm)

`npx @klarlabs/warden` — a configurable git commit/push gate with native hooks, worktree
isolation, and cryptographic provenance. No Go toolchain required.

```bash
npx @klarlabs/warden init            # set up the gate in the current repo
npx @klarlabs/warden import --write   # or: generate config from your existing CI/Makefile
```

This package is a thin launcher: it ships the prebuilt `warden` binary per
platform (via `optionalDependencies`, the esbuild pattern) and execs it. All
logic lives in the binary; there is no JavaScript reimplementation.

Full docs: <https://go.klarlabs.de/warden>
