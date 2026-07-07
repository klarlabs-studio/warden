# CI provenance gate

Warden writes a signed, commit-bound validation note (`refs/notes/warden`) for
every commit it gates — but a note nobody checks enforces nothing. `git push
--no-verify`, an uninstalled hook, or a commit made outside warden all slip past
a purely *local* gate. This is the **enforcement** counterpart to
[provenance-skip](ci-provenance-skip.md): make an un-gated commit **fail a
required check** so it cannot merge.

The `warden-gate` action runs `warden verify --range` over a PR (see
[ADR-0002](adr/0002-provenance-enforcement.md)). It fails unless *every* commit
in the range carries trustworthy provenance.

```yaml
# .github/workflows/provenance.yml
name: provenance
on:
  pull_request:

jobs:
  gate:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0 # notes + the full PR range ride on history
      - uses: actions/setup-go@v6
        with:
          go-version: stable
      - uses: klarlabs-studio/warden/.github/actions/warden-gate@v0.16.0
        with:
          require-signed: "true"
          key: "<fingerprint1>,<fingerprint2>" # your org's trusted signers
```

Mark the `gate` job a **required status check** (Settings → Branches → branch
protection) and un-provenanced PRs can no longer be merged.

## What "trustworthy" means, and how to tune it

Each commit in the range is classified, and the gate fails on the first problem:

| Reason | Meaning |
|---|---|
| `missing` | no `refs/notes/warden` record (a `--no-verify` push, or a commit made outside warden) |
| `broken-chain` | a note exists but does not attest this commit — its evidence chain is broken, empty, or transplanted |
| `unsigned` | `require-signed`/`key` was set but the note is unsigned or its signature does not verify |
| `untrusted` | the signature verifies but the signer is not in the pinned `key` set |

- Default (no `key`, `require-signed: false`): every commit must carry an
  **intact, commit-bound** note. This already rejects `missing` and
  `broken-chain` — the tampered/transplanted cases `warden doctor` lets through.
- `require-signed: "true"`: the note must additionally carry a **valid
  signature** (any key).
- `key: "<fp1>,<fp2>"`: the signature must be from one of your **trusted**
  signers — "a warden *I trust* ran here". Publish each machine's fingerprint
  with `warden key show`.

### The committed roster (recommended over passing `key:`)

Instead of hand-passing fingerprints to every workflow, commit them once to
`.warden.yaml`:

```yaml
# .warden.yaml
trusted_keys:
  - 3a76a2b850d0e957   # alice's laptop
  - fedcba9876543210   # ci signer
```

Then omit `key:` — the gate (and a bare `warden verify --range`) reads the
roster automatically, so committing `trusted_keys` turns on trusted-signed
enforcement repo-wide. Because the roster rides on config, it **inherits through
`extends:`**: an org base policy names its signers once and every repo unions
them in (a repo can add its own in a reviewed diff; it cannot silently drop the
org's). Inspect the effective roster with `warden key list`. The roster is
protected the same way as the rest of `.warden.yaml` — a PR-reviewed change,
itself gated by warden.

Merge commits are skipped by default (`skip-merges: "true"`) — a merge
introduces no tree change warden authored and its parents are gated on their
own. Set `skip-merges: "false"` to require a note on merges too.

## Why it runs on the PR head (the squash-merge story)

GitHub's **Squash and merge** creates a *new* commit on the base branch with a
new SHA and no note — so a gate that ran on the base branch *after* the merge
would flag every squash forever. `warden-gate` instead runs on the **PR head**,
whose commits still carry their notes, and gates the **merge itself**: the check
must pass *before* the platform rewrites history. Enforce before the squash, not
after.

### Keeping the base branch green after a squash (`warden reattest`)

The gate assures every merge, but the *squash commit itself* on the base branch
has no note, so `warden doctor`/`audit` on `main` will flag it. Because a squash
commit reproduces the gated PR head's tree **exactly**, a maintainer can carry
the provenance across locally — no bot, no CI signing key:

```bash
git checkout main && git pull
warden reattest --push          # re-attest HEAD from its tree-identical validated source
```

`reattest` finds a commit whose tree SHA matches HEAD and whose note is intact,
commit-bound, and validly signed, then carries that evidence onto the squash
commit, marks it `reattested_from: <source>`, and re-signs with your (trusted)
key. It **fails safe**: if nothing content-identical is validated, it writes
nothing — a re-attestation only relocates a real validation onto byte-identical
content, it never manufactures one.

### Interop: export provenance as an in-toto attestation (`warden attest`)

To feed warden provenance into the wider supply-chain ecosystem (sigstore,
GUAC, policy engines), project a commit's note into an in-toto Statement:

```bash
warden attest --commit HEAD | cosign attest-blob --predicate - …   # sign + publish
```

It emits an in-toto `Statement/v1` with a warden predicate
(`https://warden.klarlabs.de/provenance/v1`) carrying the steps run, evidence
chain, SBOM, signer, and verification status. It is a read-only projection —
warden attests *source* provenance (reviewed/tested under policy), which is why
the predicate is warden's own and not `slsa.dev/provenance` (build provenance).

## Self-hosted: a pre-receive gate

Where you control the Git server (Gitea, GitLab, a bare repo), enforce the same
range verify in a `pre-receive` hook so a bad push is **rejected at the remote**,
not merely flagged in CI:

```bash
#!/usr/bin/env bash
# pre-receive — reject any push whose new commits lack trusted provenance.
set -euo pipefail
KEY="<fingerprint1>,<fingerprint2>"   # your org's trusted signers
while read -r oldrev newrev refname; do
  # New branch (oldrev all-zero): no base to gate against.
  case "$oldrev" in *[!0]*) ;; *) continue;; esac
  if ! warden verify --range "$oldrev..$newrev" --require-signed --key "$KEY" --quiet; then
    echo "warden: push to $refname rejected — commits lack trusted provenance." >&2
    echo "        run: warden verify --range $oldrev..$newrev" >&2
    exit 1
  fi
done
```

The warden binary must be on the server's `PATH` and `refs/notes/warden` must be
fetched/received alongside the branch (configure note replication for your
host).

## Choosing a posture: advisory vs. required

Making the gate a **required** status check is right for a **shared repo with
contributors you can't fully trust**, a **regulated / high-assurance** project,
or an **org rolling warden across many repos** (where the roster + `extends:`
inheritance earns its keep). It is often *too much* for a **solo or small repo**
that already has branch protection (required signatures, required CI checks):
the marginal security is small, and a required trusted-signed gate blocks
**Dependabot/Renovate** (no warden note), **web-UI edits**, and any machine whose
key isn't in the roster — so you end up reaching for the admin override, and a
check you routinely override isn't really enforcing.

**Advisory** (run the workflow, but don't add `gate` to branch protection's
required checks) keeps the visibility and the trusted-signed signal without the
lock-in. Start there; promote to required once the trust model (below) is solid
and the friction is acceptable.

## Operating the roster: keys, backup, automation

- **Back up your signer.** warden's key is per-machine (`signing.key` in the
  config dir). With a single-key roster, losing that machine means you can no
  longer produce trusted provenance — and adding a new key needs a gated commit
  signed by a key you no longer have. Avoid the chicken-and-egg: generate a
  **recovery key now** (point `WARDEN_CONFIG_DIR` at a scratch dir, run
  `warden key show`), add its fingerprint to `trusted_keys` while your primary
  still works, and store its seed **offline** (a password manager) — not on the
  same machine as the primary. To recover, drop the seed into a new machine's
  `WARDEN_CONFIG_DIR`.
- **Don't stand up a bot/CI signing key.** A key in a CI secret that can mint
  *trusted* provenance moves the trust boundary from "a human ran warden on their
  machine" to "anything that can trigger CI or read the secret" — a much larger
  attack surface that dilutes exactly what the gate asserts. For automation like
  Dependabot under a required gate, prefer re-pushing the bot's branch **through
  warden locally** (warden validates and signs it with *your* trusted key), or
  run the gate advisory. Keep the trust boundary at a human.

## Gate vs. skip

They are complements, not alternatives:

- **[provenance-skip](ci-provenance-skip.md)** (`warden-verify`) — a *speed*
  optimization: a validated commit **skips** re-running checks. Never fails CI.
- **provenance-gate** (`warden-gate`) — an *enforcement* control: an un-gated
  commit **fails** a required check. This page.

A repo can use both: skip the expensive test matrix on already-validated
commits, and gate the merge so nothing un-validated lands.
