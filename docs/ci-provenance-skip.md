# CI provenance-skip

Warden writes hash-chained validation notes (`refs/notes/warden`) for validated
commit tips. CI can verify a note against a pinned signer and **skip equivalent
checks** when the team has established that local validation covers the CI job.

The `warden-verify` action reports whether a commit is validated; gate your
expensive jobs on its output. Keep both the key pin and the action implementation
under protected control. For untrusted pull requests, reference the action at a
reviewed full commit SHA rather than executing its copy from the candidate
checkout. The local action below is suitable for a trusted checkout.

```yaml
# .github/workflows/ci.yml
jobs:
  gate:
    runs-on: ubuntu-latest
    outputs:
      validated: ${{ steps.warden.outputs.validated }}
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0 # notes ride on history
      # No setup-go needed: the action installs a released, checksum-verified
      # binary, so this works in any repo — including one with no root go.mod.
      - id: warden
        uses: ./.github/actions/warden-verify # or klarlabs-studio/warden/.github/actions/warden-verify@v0 (see gate doc on pinning)
        with:
          key: "<trusted-signer-fingerprint>" # pin on the protected branch

  test:
    needs: gate
    if: needs.gate.outputs.validated != 'true' # skip when warden already validated
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v6
      - run: go test -race ./...
```

A validated commit skips the `test` job entirely; an unvalidated one (pushed
with `--no-verify` or from a machine without warden) runs the checks as usual —
provided the skipped jobs are equivalent to the checks recorded locally.
A trusted signature establishes who attested the commit; it does not establish
that local commands, toolchain, dependencies, or environment match a CI job.
Keep environment-specific and additional checks running in CI.

The action returns `validated=false` when `key` is omitted, blank, or contains
only separators. This intentionally changes the earlier unsigned-skip behavior:
existing callers must pin a trusted key before they can skip jobs again.

On GitLab, use the `templates/verify.yml` component instead. GitLab cannot
evaluate a job's verdict in `rules:`, so downstream jobs start and then exit
early; [GitLab CI](gitlab-ci.md) explains how.

## Monorepo mode: skip only the workspaces already checked

In a repository with [monorepo mode](monorepo.md) and `select: affected`, a
push can carry an **affected-scope** note: the per-workspace checks ran for
the workspaces the change affected, not all of them. `validated` is `false`
for such a note, as before, and every job gated on it runs. Two more outputs
let a workflow do better than "run everything":

| Output | Value |
|---|---|
| `scope` | `full` when `validated` is `true`; `affected` when a sound note from a pinned signer was refused **only** because it attests an affected-scope run; `none` otherwise |
| `validated-workspaces` | JSON array of the workspace paths checked on exactly this commit's tree: every workspace for `full` (`[]` in a repository without monorepo mode), the affected selection for `affected`, `[]` for `none` |

The action reads both from `warden verify --json`, never from the text it
prints, and reports them only when the JSON agrees with the exit code.
Everything else gives `scope=none` and `[]`: no key, an untrusted signer, any
other refusal (`requirements`, `container-steps` and `min-profile` all refuse
an affected note), a warden release older than single-commit `--json`, or a
runner without `jq`. An affected note also names no workspace when its checks
ran on a tree other than the commit's (read from git, not from the note), or
when its scope is one this warden cannot read (a newer selection version, a
sparse checkout).

`validated-workspaces` makes one claim, and it needs no trust in earlier
runs: these workspaces were checked by the trusted signer on this exact tree.
It says nothing about the others. So a job may skip a workspace in the list,
**provided its check is equivalent to the per-workspace step warden ran**, and
must run every other workspace. Take the list of all workspaces from the
checkout (`warden workspaces --json`), never from this output: an empty or
missing output then means "run everything", which is the safe direction.
Repository-wide jobs (not per workspace) are not covered by an affected note
and run whenever `validated` is not `true`.

The workflow below runs `pnpm test` for each JavaScript workspace the note
did not check, and ends in one `ci` job that is green only when every
workspace was checked on this commit, by warden or by CI. Make `ci` the
required status check.

```yaml
# .github/workflows/ci.yml
jobs:
  gate:
    runs-on: ubuntu-latest
    outputs:
      validated: ${{ steps.warden.outputs.validated }}
      todo: ${{ steps.plan.outputs.todo }}
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - id: warden
        uses: klarlabs-studio/warden/.github/actions/warden-verify@<full-sha>
        with:
          key: "<trusted-signer-fingerprint>"
      # The action put warden on PATH. List the workspaces at the commit
      # just verified, and drop the ones the note checked on this tree.
      - id: plan
        if: steps.warden.outputs.validated != 'true'
        env:
          CHECKED: ${{ steps.warden.outputs.validated-workspaces }}
        run: |
          todo=$(warden workspaces --json |
            jq -c --argjson checked "${CHECKED:-[]}" \
              '[.workspaces[] | select(.ecosystem == "js") | .id] - $checked')
          echo "todo=$todo" >> "$GITHUB_OUTPUT"

  test:
    needs: gate
    if: needs.gate.outputs.validated != 'true' && needs.gate.outputs.todo != '[]'
    runs-on: ubuntu-latest
    strategy:
      matrix:
        workspace: ${{ fromJSON(needs.gate.outputs.todo) }}
    steps:
      - uses: actions/checkout@v4
      - uses: pnpm/action-setup@v4
      - run: pnpm install --frozen-lockfile
      - env:
          WORKSPACE: ${{ matrix.workspace }}
        run: pnpm --dir "$WORKSPACE" test

  ci:
    needs: [gate, test]
    if: always()
    runs-on: ubuntu-latest
    steps:
      - env:
          GATE: ${{ needs.gate.result }}
          VALIDATED: ${{ needs.gate.outputs.validated }}
          TODO: ${{ needs.gate.outputs.todo }}
          TEST: ${{ needs.test.result }}
        run: |
          [ "$GATE" = success ] || exit 1
          [ "$VALIDATED" = true ] && exit 0
          [ "$TODO" = '[]' ] && exit 0
          [ "$TEST" = success ]
```

Why this is correct:

- `actions/checkout` and the action's default `commit` (`github.sha`) name
  the same commit, so `warden workspaces --json` lists the graph of the tree
  the note was checked against.
- A `full` note sets `validated=true`: the plan step is skipped, `test` is
  skipped, and `ci` passes on `VALIDATED`.
- An `affected` note leaves `validated=false`; `todo` is every JavaScript
  workspace minus the ones checked on this tree, and `ci` needs each of those
  to pass in `test`.
- `scope=none` gives `[]` to subtract, so `todo` is every JavaScript
  workspace. If the plan step fails, `gate` fails and so does `ci`.
- `todo` is `[]` only when every JavaScript workspace was checked; a matrix
  cannot be empty, so `test` is skipped by its `if:` and `ci` passes on
  `TODO`.
- Workspace paths reach the shell only through `env:`, never inline, so a
  path is data whatever it contains.

`warden workspaces` discovers every ecosystem it knows, whatever
`workspaces.discover` says, so it can list more workspaces than the note
does; those are never in `validated-workspaces`, and CI checks them. Filter
by `.ecosystem` to the workspaces your job applies to, as above.

Locally, the same primitive is one command:

```bash
warden verify && echo "already validated" || make ci
```

## Trust depth: pin `--key` when skipping is a security decision

A bare `warden verify` reports a commit **validated** when it carries an intact,
commit-bound note — proof a warden *ran*, but not proof of *who* ran it. An
unsigned note's evidence chain is internally consistent by construction, so
anyone who can write the note can produce a `validated: true` verdict. That is
fine for local convenience (`warden verify || make ci` on your own checkout),
but if a CI job **skips real checks** on the strength of the verdict, require a
trusted signature:

```bash
warden verify --key "<fp1>,<fp2>"      # skip only what a trusted key validated
```

Pass `--key` explicitly (pinned in the workflow, on the protected branch) rather
than relying on the working-tree roster: single-commit verify has no base ref to
read a roster from, so an in-tree `trusted_keys` is only as trustworthy as the
commit you are about to skip. The range gate (`--range`) does read its roster
from the trusted base automatically — see
[provenance-gate](ci-provenance-gate.md).
