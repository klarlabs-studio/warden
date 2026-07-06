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
        uses: ./.github/actions/warden-verify # or klarlabs-studio/warden/.github/actions/warden-verify@v0.16.0

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
