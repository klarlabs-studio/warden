# Reuse existing hook definitions

`warden import` previews a configuration built from existing automation.
`warden import --write` saves it to `.warden.yaml`; review the preview first,
especially when replacing an existing Warden configuration.

## Husky pre-commit

A regular `.husky/pre-commit` file becomes a dedicated `husky-pre-commit`
command, alongside any lint or test commands detected from other sources:

The generated command captures the staged tree, runs the script with
`HUSKY=1 PATH="node_modules/.bin:$PATH" sh -e .husky/pre-commit`, then checks
that tracked files and the index still match that snapshot. The generated
configuration schedules this command under `steps.pre_commit` and includes
`writes: [husky-pre-commit]` for conservative scheduling.

The hook runs in Warden's disposable staged worktree. Both file contents and
the worktree's index represent the staged snapshot. Unstaged edits in the
developer's checkout are excluded. The writer declaration serializes the hook
with other checks because a formatter or staged-file tool may edit files.
The bridge rejects hooks that change tracked files or the index, even when
the script exits successfully. Such changes stay in the disposable worktree.
Run formatters in your checkout and stage their output before retrying. This
prevents a successful check on rewritten files from approving the original
staged content. Review hooks that invoke Git operations before adoption.

The command keeps the original script and uses `sh -e`, with repository-local
Node binaries on PATH. A missing script or an unavailable command fails the
step. `HUSKY=0` disables Husky's own launcher but does not disable this explicit
Warden command. Personal Husky startup files are not loaded. Configure required
runtime initialization in the command.
This generated bridge requires host execution: it reads the disposable Git
index, and the current container adapter does not expose host Git metadata.
Container commands can run standalone checks, but a Git-index bridge for
containerized Husky hooks is separate work.

Track the hook and required helper files. Older hooks that source
`.husky/_/husky.sh` need that helper inside the disposable worktree; import
reports this dependency, and a missing helper fails execution. Prefer a
standalone hook when migrating. Symlinked hook files or directories are not
automatically imported. This importer recognizes the root `.husky` directory;
custom or nested hook directories require an explicit command.

### Activate the Git gate

Import generates configuration; it does not change Git's hook location.
Husky commonly sets `core.hooksPath` to `.husky/_` or `.husky`, while Warden
installs its shims in `.git/hooks`. Check the effective value and its origin:

```sh
git config --show-origin --get core.hooksPath
```

Warden checks Git's effective hook directory before `init` or hook enablement.
If another directory is selected, it refuses before installing shims or
recording an adoption point. `warden status` reports the hooks as inactive,
even if managed shims remain in the default directory. It does not change
hook ownership automatically.

Before `warden init`, deliberately select Warden's hook directory when ready to
switch gate ownership. For a normal checkout this can be set locally with
`git config --local core.hooksPath .git/hooks`. Linked worktrees share the
common Git directory's hooks; Warden installs there rather than beside a
worktree's private index and HEAD. Use an appropriate shared path if explicitly
selecting a directory for linked worktrees. Re-running Husky's installation or a
package `prepare` script may change this setting again; check it after installs.

This switch affects every Git hook. Only pre-commit is bridged automatically.
Import reports other recognized Husky hooks without translating them: pre-push
needs remote arguments and ref data on stdin, and commit-msg needs the message
file argument. Preserve their behavior deliberately before changing hook
ownership. Calling those scripts without their Git context could pass without
checking the intended input.

## Python pre-commit

The existing importer delegates `.pre-commit-config.yaml` and `.yml` to
`pre-commit run --all-files`, retaining pre-commit's environment provisioning.
It can also recognize local system test commands and known security hooks.
Existing Makefile and package scripts take precedence for those inferred
lint/test/security commands; review the import notes for the selected source.
The Husky command has its own step name so those scripts cannot replace it.

Neither bridge infers which checks are deterministic. They are not opted into
predictive caching automatically. Execution success records the outcome of the
configured command; it does not establish that every possible hook ran.

See [container execution](container-execution.md) and
[predictive staged validation](predictive-cache.md) for their separate setup.
