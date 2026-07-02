# Security Policy

## Supported versions

warden is pre-1.0; security fixes land on the latest minor release. Always run
the newest tagged version.

| Version | Supported |
|---|---|
| latest `0.x` | ✅ |
| older | ❌ |

## Reporting a vulnerability

**Do not open a public issue for security vulnerabilities.**

Report privately via GitHub's [private vulnerability
reporting](https://github.com/klarlabs-studio/warden/security/advisories/new)
(Security → Report a vulnerability), or email **felix.geelhaar@gmail.com** with
`warden security` in the subject.

Please include:

- affected version (`warden version`),
- a description and, if possible, a minimal reproduction,
- the impact you foresee.

You'll get an acknowledgement within a few days. Fixes are developed privately,
released, and disclosed once users can upgrade.

## Security model — what warden does and doesn't guarantee

- **Provenance notes are tamper-evident and signed.** Each validated commit
  carries a hash-chained, ed25519-signed `refs/notes/warden` record. Verifying a
  chain (`warden verify`) proves it wasn't reordered/truncated; pinning a key
  (`warden verify --key`) proves *which* signer produced it. The private signing
  key never leaves the machine.
- **`--no-verify` bypasses the gate — by design.** git lets any hook be skipped.
  warden records an adoption point and `warden doctor` flags commits since
  adoption that carry no note, so a bypass is *detectable*, not *prevented*. CI
  is the enforcement point (gate merges on `warden verify`).
- **Steps run arbitrary configured commands.** warden runs the commands in your
  `.warden.yaml` (and coding-agent CLIs). Treat `.warden.yaml` and any
  `extends:` base as trusted code; review changes to it like any other code.
- **The gate is not a sandbox.** Steps run with your permissions in a disposable
  worktree. Worktree isolation protects your working tree, not the host.
