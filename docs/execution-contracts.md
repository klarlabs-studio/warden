# Execution contracts for CI reuse

`warden verify --requirements /protected/requirements.json --key <trusted-fingerprint>`
returns success only when trusted, commit-bound local provenance contains fresh
container observations matching every required step. A mismatch means CI should
run its checks. For independent decisions across named jobs, see
[protected CI job plans](ci-job-contracts.md). Ordinary `verify` and `--container-steps` remain available, but
neither establishes that the command and configuration match your CI policy.

Version 1 records the command digest, effective Warden configuration digest,
step-context digest, checked Git tree, tracked dependency-file content digests,
and inspected container environment. Contracts are covered by the RunRecord's
signature. The verifier independently reads the gated commit's Git tree.

A policy file has this shape (replace every illustrative digest):

```json
{
  "version": 1,
  "steps": {
    "test": {
      "command": "go test ./...",
      "configuration_digest": "sha256:<64 lowercase hex characters>",
      "context_digest": "sha256:<64 lowercase hex characters>",
      "inputs": {"go.mod": "sha256:<64 lowercase hex characters>"},
      "image_id": "sha256:<64 lowercase hex characters>",
      "image_id_kind": "config",
      "platform": "linux/arm64",
      "network": "none"
    }
  }
}
```

Use `manifest` for Apple's platform-manifest identity and `config` for the
Docker-compatible image configuration identity. These identities are distinct.
The complete recognized dependency-file map must match; use `{}` explicitly
when there are none. Recognized filenames follow Warden's SBOM collector.

Maintain requirements and trusted keys in protected consumer policy. Review
observations from a trusted reference run when preparing expectations. Never
construct expectations from the candidate note in the CI job that evaluates it,
or load them from an untrusted candidate checkout. The GitHub action accepts
`requirements: /protected/requirements.json` and passes it to this verifier.
Pin a Warden version that supports this flag; older releases reject the flag
and the action falls back to running CI.

The configuration digest hashes the loaded effective configuration using
Warden's JSON encoding. The context digest hashes the ordered values of the
five forwarded `WARDEN_*` step inputs: hook, branch, changed paths,
files touched, and lines changed. Expectations are exact: different contextual
inputs require different reviewed expectations. There is no automatic assertion
that a local command is equivalent to a remote CI stage.

## Version 2: a minimum profile

Version 2 adds an optional `profile` clause. The note must then conform to at
least that [compliance profile](agent-profiles.md#checking-a-profile-in-ci),
exactly as `--min-profile` checks it: recomputed from the note's signed step
outcomes, execution environments and approval record, never from the profile
name it carries. `steps` becomes optional when `profile` is present, and
anything in it is checked exactly as in version 1:

```json
{
  "version": 2,
  "profile": {"min": "strict"},
  "steps": {}
}
```

`min` is `permissive`, `standard` or `strict`, optionally `name@version`. A
version 2 file must name a profile, at least one step, or both. A version 1 file
that names a profile is refused. A file that demands a profile needs an
explicit `--key`. Passing `--min-profile` as well demands both minimums.
A requirements file is single-commit only; a range gate takes its profile from
its base ref instead (see
[Checking a profile across a range](agent-profiles.md#checking-a-profile-across-a-range)).

For `strict`, this release checks: trusted commit-bound local provenance; the
recorded profile's digest against this warden's definition; its rank; that
`credentials, lint, test, security-scan, review` each executed (cached and
predicted results do not count); that `lint`, `test` and `security-scan` each
have a complete Linux container record; that each of those ran with
network `none`; that every agent step that executed was isolated with its
tree unchanged and saw only the allowlisted environment; that no required
approval was auto-approved; and that a note recording an agent declaration
also records a notarized Agent Trace. A passing `verify` prints the clauses it
checked.

A warden older than version 2 rejects such a file: its version check refuses
`2` and its strict decoder refuses the unknown `profile` key. The action then
reports `validated=false` and CI runs its checks. Version 1 files behave
exactly as before.

## Capture limits

Only shell checks freshly executed in containers with `network: none` qualify.
Host execution, cached results, skipped checks, legacy records, and external
attestations do not satisfy requirements. Neither does a note of an
[affected-scope monorepo run](monorepo.md#how-verifiers-treat-a-partial-record):
requirements describe a full run, so such a note fails with the reason
`affected-scope` before any contract is compared, whatever the file asks for.
The same note conforms to no compliance profile, under a version 2 file or
`--min-profile`. Nor does a requirements file combine with per-workspace
reuse: with `requirements` set, the `warden-verify` action reports
`scope=none` and no `validated-workspaces` for an affected note, because the
note failed a check other than its scope. To skip the workspaces an affected
note checked, gate that reuse on a verification without a requirements file
(see [CI provenance-skip](ci-provenance-skip.md#monorepo-mode-skip-only-the-workspaces-already-checked)),
and keep any job a contract was meant to cover running. Initial worktrees must have clean
tracked contents and no untracked or ignored inputs. Hidden index entries and
submodules are refused. Tracked contents and the
recognized dependency-file map must remain unchanged after the check; generated
untracked outputs are permitted. Capture failure does not fail an otherwise
successful local check, but produces no reusable contract.

Dependency-file hashes describe files observed, not installed packages. An image
identity pins userspace content, not a host kernel or external service. Different
runtime implementations, execution limits, and native extensions can still
matter. Consumers must decide whether these observations establish sufficient
equivalence for their particular CI stage. Network-enabled execution cannot
satisfy version 1 requirements. This feature does not enable cached execution.
