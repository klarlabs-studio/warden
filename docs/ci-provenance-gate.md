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
      # No setup-go needed: the action installs a released, checksum-verified
      # binary, so this works in any repo — including one with no root go.mod.
      - uses: klarlabs-studio/warden/.github/actions/warden-gate@v0
        with:
          require-signed: "true"
          key: "<fingerprint1>,<fingerprint2>" # your org's trusted signers
```

### No toolchain required

The action installs warden from a **released, checksum-verified binary**, not
via `go install`. That matters beyond convenience: verifying provenance is not a
Go build step, and requiring a toolchain coupled every consumer to a root
`go.mod`. In a monorepo whose module lives in a subdirectory, `setup-go` with
`go-version-file: go.mod` fails and the job dies *before verifying anything* —
leaving a permanently-red check that is indistinguishable from a broken one, and
which therefore provides no verification at all while appearing to be enforced.

Pin a version with `warden-version:` (default `latest`). The download is
digest-checked against the release's `checksums.txt` and **fails closed** on a
mismatch; if no binary exists for the runner's platform it falls back to
`go install` only when a toolchain happens to be present.

Mark the `gate` job a **required status check** (Settings → Branches → branch
protection) and un-provenanced PRs can no longer be merged.

### Pinning the action version

`@v0` is a **floating** major tag that points at the newest `v0.x` release, so a
consumer picks up fixes without editing their workflow each time. warden is
pre-1.0; once it reaches `v1.0.0` the release publishes a `@v1` tag and `@v0` is
frozen at its last `v0.x`.

Because warden is itself a supply-chain tool, a security-critical gate is better
served by an **immutable** reference — pin an exact release, or strongest, that
tag's commit SHA and let Dependabot bump it:

```yaml
      - uses: klarlabs-studio/warden/.github/actions/warden-gate@vX.Y.Z # or a commit SHA
```

Substitute the release you actually audited — the current list is on the
[releases page](https://github.com/klarlabs-studio/warden/releases). The
placeholder is deliberate: a concrete version here would be a number to copy,
and picking the version *is* the point of pinning. (It is also the line most
likely to be copied verbatim, so a real number in it goes stale on every
release and quietly sends readers to an old gate.)

Both forms are supported. The trade-off is convenience — `@v0` tracks new
releases — versus an audited, unchanging reference (an exact tag or a SHA).

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
warden reattest --all --dry-run # preview: prints the plan, writes nothing
warden reattest --all --push    # close every recoverable gap on the branch
warden reattest --push          # or just HEAD, right after a single merge
```

Warden also anchors each attested commit under `refs/warden/attested/`, and
pushes those refs alongside the notes. This is what makes the repair still
possible later: a git note *survives* garbage collection but the commit it
annotates does not, so deleting the merged branch (the default in
`gh pr merge --delete-branch`, and what removing a worktree does too) leaves the
note dangling over a pruned object. Reattest then finds nothing to copy from and
the squash commit reads as though it was never gated. `warden reattest --all`
backfills anchors for notes written before this existed — worth running once on
an older repository, before its next `gc`.

Reach for `--dry-run` first on a trunk. Omitting `--push` is *not* a preview —
it still writes notes locally, it only declines to publish them. `--dry-run` is
the flag that writes nothing. The sweep also prints a line per commit, because a
backlog of a hundred commits is otherwise minutes of silence.

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

> **warden's own repo runs it required** as of 0.19.0. It was advisory for the
> first six months, and the thing that blocked promotion was not the trust model
> — it was three defects that made the gate fail on *legitimate* PRs: a note
> covered only its push's tip, so an ordinary multi-commit push read as
> unverified; a rebased branch could not be pushed through the gate at all,
> forcing a `--no-verify` bypass that wrote no provenance; and the `rebase` step
> targeted the branch's own remote ref. Promote when the gate stops failing on
> work that *is* properly gated — not merely when you trust the idea of it. A
> required check people routinely override is worse than an advisory one.

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

## Closing the forge-merge gap (post-merge attestation)

The gate above runs on the PR *head*, because that is where the notes are. But
the commit that actually lands on `main` is a **different object**: GitHub
creates it server-side when you squash-merge, and warden — a client-side
pre-push gate — was never in that path. The same is true of a web edit or a
merged Dependabot PR.

This is measurable, not theoretical. Across one three-repository fleet, **all
eleven remaining "bypassed" commits were committed by GitHub**; not one was a
person going round the gate.

`.github/workflows/provenance-main.yml` closes it by attesting the merged commit
from CI:

```yaml
on:
  push:
    branches: [main]
# …
- run: warden run pre-push --attest-only
- run: git push origin 'refs/notes/warden:refs/notes/warden'
```

`--attest-only` runs the configured steps against the merged tree and writes the
note, but does **not** move or push the branch. Pushing from CI would race the
next human push and fail on a stale ref, and the branch is already published —
publishing it is what triggered the job.

It **refuses** if a step rewrote the tree. The note binds to `HEAD`, so attesting
a rewritten tree would assert the checks passed on a tree they never saw. In CI
your steps must be checks, not formatters.

### The signing key is the part to think about

Set `WARDEN_SIGNING_KEY` (a base64 ed25519 seed — see `warden key show`) and add
its fingerprint to `.warden.yaml` `trusted_keys`.

**The workflow refuses to attest without it, deliberately.** warden's signer
generates a fresh keypair when it finds no key, so an unguarded CI run would
write notes signed by a throwaway signer that is discarded when the runner dies.
Those commits would read as attested while being signed by nobody trusted —
manufacturing provenance rather than recording it, which is worse than leaving
the gap visible.

A long-lived secret is the weak link here. Keyless OIDC (sigstore/Fulcio) is the
right answer — no standing credential, org identity, transparency log — and is
tracked as ADR-0002 Phase 2.5. Until then this is a real improvement on an
unsigned note, not the end state.

### Why this order matters

Once `main` is covered, the PR-head `warden-gate` check can go back to being
**required**. The reason it was downgraded to advisory — that it locked out
Dependabot, web edits, and machines without warden installed — disappears when
CI produces the attestation, because every one of those paths generates one
naturally.

### It runs the RELEASED warden, not your working tree

`provenance-main.yml` installs warden with `WARDEN_VERSION: latest`, which
resolves to the latest **GitHub release**. A fix to warden itself therefore does
not reach this job until it ships.

That bites in exactly one place, and it is worth naming because the symptom
looks like a code bug: the gate runs green, every step passes against the merged
tree, and the step still fails — because the *released* binary behaves the old
way. Check the run's "Install warden" step for the version before debugging the
gate.

Pin `warden-version:` if you want a job that does not move under you.

### The duplication, and the way out

These steps re-run what your CI already ran on the same commit — `.warden.yaml`
in this repo says as much, describing `lint` and `test` as mirroring CI. That is
deliberate: the note asserts "these checks ran and passed on this tree", and the
only way to assert it honestly is to have run them.

The alternatives are worse, not cheaper:

- attesting after a `workflow_run` without executing anything makes the note
  claim checks this job never ran
- attesting with a reduced step set produces a note that looks identical to a
  full gate at `verify` time while meaning far less

Both manufacture provenance rather than record it. Removing the duplication
properly needs warden to be able to attest an **external** run — which checks
ran, where, and a verifiable reference to them. That is tracked in
[#177](https://github.com/klarlabs-studio/warden/issues/177).
