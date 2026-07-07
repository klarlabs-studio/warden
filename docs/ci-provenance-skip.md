# CI provenance-skip

Warden writes a hash-chained validation note (`refs/notes/warden`) for every
commit it gates. CI can trust that note and **skip re-running the checks warden
already ran** — turning a 10-minute CI round-trip into a few seconds for
already-validated commits, and cutting CI minutes.

The `warden-verify` action reports whether a commit is validated; gate your
expensive jobs on its output.

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
      - uses: actions/setup-go@v6
        with:
          go-version: stable
      - id: warden
        uses: ./.github/actions/warden-verify # or klarlabs-studio/warden/.github/actions/warden-verify@v0 (see gate doc on pinning)

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
so the gate never *weakens* CI, it only *fast-paths* what warden already proved.

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
