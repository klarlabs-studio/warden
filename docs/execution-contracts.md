# Execution contracts for CI reuse

`warden verify --requirements /protected/requirements.json --key <trusted-fingerprint>`
returns success only when trusted, commit-bound local provenance contains fresh
container observations matching every required step. A mismatch means CI should
run its checks. Ordinary `verify` and `--container-steps` remain available, but
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

## Capture limits

Only shell checks freshly executed in containers with `network: none` qualify.
Host execution, cached results, skipped checks, legacy records, and external
attestations do not satisfy requirements. Initial worktrees must have clean
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
