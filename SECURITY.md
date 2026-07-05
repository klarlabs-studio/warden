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

## Supply-chain integrity of the self-fetched binary

When a repo is adopted with no global install, the generated git hook fetches a
version-pinned `warden` binary on first use (and `install.sh` / `install.ps1`
fetch it too). That download is **verified before it is ever made executable**:

- The tarball/zip is checked against the **SHA-256 published in the release's
  `checksums.txt`**, fetched from the same pinned release tag. A mismatch, a
  missing checksum entry, or the absence of a `sha256sum`/`shasum` tool **fails
  closed** (exit 1) — warden refuses to run an unverified binary.
- The **cached binary is re-verified on every run** against the digest recorded
  at install time (`~/.warden/bin/<ver>/warden.sha256`), so post-install
  tampering or bitrot is caught and the binary is re-fetched. The cache lives in
  a **user-only (`0700`)** directory. The Windows installer downloads to an
  unpredictable temp file rather than a fixed, hijackable path.
- Releases **sign `checksums.txt` with cosign keyless** (Sigstore Fulcio +
  Rekor), emitting `checksums.txt.sig` and `checksums.txt.pem`, so the digest
  list is independently verifiable.

**Residual gap (follow-up).** The fetch path verifies the *checksum* but does not
yet verify the *cosign signature* on `checksums.txt` — `checksums.txt` is
retrieved over the same TLS/host as the archive. This defends against archive
corruption, single-asset/CDN tampering, and accidental drift, but not a
determined TLS-breaking MITM who can forge both files. Closing it fully means
having the hook and install scripts verify `checksums.txt.sig` against a pinned
cosign identity (`cosign verify-blob`) before trusting the digests. Until then,
the strongest guarantees come from installing via a channel that pins the binary
out-of-band (Homebrew cask, the per-platform npm packages) or `go install`.
