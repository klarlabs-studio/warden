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
      - uses: klarlabs-studio/warden/.github/actions/warden-gate@v1
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

(Closing the loop for the post-squash base branch — re-attesting the squash
commit — is ADR-0002 Phase 3.)

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

## Gate vs. skip

They are complements, not alternatives:

- **[provenance-skip](ci-provenance-skip.md)** (`warden-verify`) — a *speed*
  optimization: a validated commit **skips** re-running checks. Never fails CI.
- **provenance-gate** (`warden-gate`) — an *enforcement* control: an un-gated
  commit **fails** a required check. This page.

A repo can use both: skip the expensive test matrix on already-validated
commits, and gate the merge so nothing un-validated lands.
