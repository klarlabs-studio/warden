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
      - uses: klarlabs-studio/warden/.github/actions/warden-gate@v0
        with:
          require-signed: "true"
          key: "<fingerprint1>,<fingerprint2>" # your org's trusted signers
```

Mark the `gate` job a **required status check** (Settings → Branches → branch
protection) and un-provenanced PRs can no longer be merged.

### Pinning the action version

`@v0` is a **floating** major tag that points at the newest `v0.x` release, so a
consumer picks up fixes without editing their workflow each time. warden is
pre-1.0; once it reaches `v1.0.0` the release publishes a `@v1` tag and `@v0` is
frozen at its last `v0.x`.

Because warden is itself a supply-chain tool, a security-critical gate is better
served by an **immutable** reference — pin an exact version (`@v0.16.0`) or,
strongest, the tag's commit SHA and let Dependabot bump it:

```yaml
      - uses: klarlabs-studio/warden/.github/actions/warden-gate@v0.16.0 # or a commit SHA
```

Both forms are supported. The trade-off is convenience — `@v0` tracks new
releases — versus an audited, unchanging reference (`@v0.16.0` or a SHA).

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
org's). Inspect the effective roster with `warden key list`.

**The roster is read from the range's base, not its head.** A range gate
(`warden verify --range base..head`) resolves `trusted_keys` from the **base**
ref's committed `.warden.yaml` — the trusted side of the range — not from the
working tree it is checking. So a PR that edits `trusted_keys` to add its own
signer **cannot use that edit to pass its own gate**: the widened roster only
takes effect once merged, judging the *next* PR, on the trusted base. This
closes the self-certification gap where a change supplies its own trust anchor.
An explicit `key:` (pinned in the workflow, which lives on the protected base)
is stronger still and worth setting for a required gate. A single-commit
`warden verify` (provenance-skip) has no base to read from, so it uses the
working-tree roster — pin `--key` there when the commit being skipped isn't
already on trusted history.

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

### Multi-commit pushes: the note covers the span, not just the tip

A run validates **one tree** — the tip's, in the disposable worktree. Warden
therefore does not attest each commit of a multi-commit push individually;
those intermediate trees were never checked out, and a note claiming
"lint and test passed on this commit" would be false for all but the last.

What a passing run *can* honestly claim is the span: a trusted signer ran the
policy and published `(covers_from, commit_sha]` as one gated push. The note
records that span inside the signed payload, so it cannot be widened after
signing, and `warden verify --range` reads it:

```
verified 4 commit(s) in a1b2c3d..e4f5a6b (trusted-signed)
  (3 covered by a gated push's signed span, not individually attested)
```

Without this, an ordinary `commit → commit → commit → push` left two commits
reading `UNVERIFIED` forever, because the range gate demanded per-commit notes
warden never writes.

The coverage is not a weaker path to green:

- a covering note must clear **the same bar** it is being used to satisfy —
  same signature and trusted-key requirements at its own commit;
- the span is bounded by **real git history** (`rev-list base..tip`), not by
  anything the note asserts about reachability;
- a commit outside every trusted span keeps its original failure, so a
  `--no-verify` commit in the middle of a branch still fails the gate.

### Keeping the base branch green after a squash (`warden reattest`)

The gate assures every merge, but the *squash commit itself* on the base branch
has no note, so `warden doctor`/`audit` on `main` will flag it. Because a squash
commit reproduces the gated PR head's tree **exactly**, a maintainer can carry
the provenance across locally — no bot, no CI signing key:

```bash
git checkout main && git pull
warden reattest --all --push    # close every recoverable gap on the branch
warden reattest --push          # or just HEAD, right after a single merge
```

`reattest` finds a commit whose tree SHA matches HEAD and whose note is intact,
commit-bound, validly signed, **and signed by a trusted key** (a key in the
roster, or your own machine's), then carries that evidence onto the squash
commit, marks it `reattested_from: <source>`, and re-signs with your (trusted)
key. Requiring a *trusted* source — not merely any self-verifying signature —
stops an untrusted note (one an attacker could push to `refs/notes/warden` for a
tree-identical commit) from being laundered into a locally-trusted
re-attestation. It **fails safe**: if nothing content-identical is validated by
a trusted signer, it writes nothing — a re-attestation only relocates a real,
trusted validation onto byte-identical content, it never manufactures one.

**Do the sweep, not the SHA.** `--all` walks the branch from the adoption point
and re-attests every commit that has a validated tree-identical source, then
pushes the notes once. It exists because the per-merge form does not survive
contact with reality: a repo can run the gate on every PR and still show a
majority-unverified `main`, simply because nobody remembered to relocate a note
after each squash. `--all` applies exactly the same trust rules per commit — it
is a batch of the same safe operation, never a weaker one — and skips anything
that has no trusted source. Re-running it on a clean branch writes nothing.

`warden doctor` now points at this directly. A commit whose content *was* gated
under a different id reads as a recoverable binding gap rather than an
unchecked commit:

```
branch main since adoption a33fb962c94c:
  ✗ b573c53b6f31  docs: adopt Agent OS memory system (#26)   UNVERIFIED (reattestable from 9f2c1ab84d05)
  ✓ f8ae27d41f39  fix(config): keep secrets out of the config file (#25)  (run_2026…, 2 steps, chain-intact)
2 verified (2 chain-intact), 4 unverified since adoption
3 of the 4 were gated under a different commit id (squash-merge); recover them with:
  warden reattest --all --branch main --push
```

Doctor applies the *same* trust rule reattest does, so it never advertises a
repair reattest would then refuse, and never presents an untrusted note as
provenance. A commit with no trusted tree-identical source keeps reading
`UNVERIFIED (no warden note)` — that is a real hole, not a binding gap.

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
