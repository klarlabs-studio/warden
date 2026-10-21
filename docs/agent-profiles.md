# Compliance profiles

A compliance profile is a named, built-in set of defaults and floors for a
repository where coding agents commit and push. You pick one with a single key:

```yaml
profile: standard   # Requires warden >= 0.37.0 on every machine and CI runner
```

The decision record is [ADR 0007](adr/0007-agent-compliance-profiles.md). This
page covers what you can rely on **in the current release**, and says so
where a part of the design is not enforced yet.

> **Frozen since 0.37.0.** `permissive@1`, `standard@1` and `strict@1` never
> change again, and neither do their digests. A stricter level ships as `@2`
> and is never adopted automatically. In 0.36.0 the definitions were a
> preview, and 0.37.0 added floors to them.

## Before you add the key: upgrade every reader first

warden refuses a config that contains a key it does not know. A warden older
than 0.36.0 reading `profile:` fails every hook with a parse error. From 0.36.0
on, that error also says the key may need a newer warden. 0.36.0 itself reads
the key but writes notes without a profile, and its verifier reports a
profiled note's signature as invalid, so a fleet that mixes it with 0.37.0
fails the profile gate.

So the order matters:

1. Pin warden 0.37.0 or later in CI (`warden-version` inputs, the GitLab
   component) and update developer machines (npm, brew).
2. Only then commit `profile:`. Put the release next to the key, as above, so
   the next reader knows why a hook broke on an old machine.

## Levels

There are three levels, in order: `permissive < standard < strict`.

| Level | Pick it when |
|---|---|
| `permissive` | You want the profile on record and the bundled agent preset wired up, with no floors yet. A first step. |
| `standard` | Agents commit to the repository and the basic checks must always run. |
| `strict` | Agents commit autonomously, or the repository is regulated: checks must run in a network-less container and agents may not keep writes. |

A bare name means version 1 forever: `strict` is `strict@1`, and you may write
either. A later, tighter definition will ship as `strict@2`, and you adopt it by
changing the line. An unknown name or version (`strict@2` on this release, or a
typo) is a load error, never a fallback to something weaker.

## What each level does in this release

### Defaults (the lowest layer)

The profile's defaults sit underneath your whole `extends:` chain, so any file
can override them, and the floors below stop an override from weakening them.

| Default | permissive | standard | strict |
|---|---|---|---|
| `signing.required: true` | | yes | yes |
| `timeouts:` of `10m` for `intent`, `review`, `document` and any step a rule hands to an agent, when not set | | yes | yes |
| `execution.network: none` | | | yes |
| `agent:` applies to the built-in agent steps when it names a bundled preset (`claude`, `codex`) | yes | yes | yes |

`signing.required` only ratchets up, so under standard or strict no file can
turn it back off. The run refuses to write an unsigned note, as it does today
when you set the key yourself.

About `agent:`: without a profile it still does nothing, exactly as before,
because switching it on everywhere would start model calls on every push in
repositories that set it long ago. Under a profile, `agent: claude` or
`agent: codex` selects that preset for `intent`, `review` and `document` unless a
rule assigns another agent. `agent: auto` and custom agents stay inert under
permissive and standard; strict refuses them at load (see the floors below).

### Floors (checked at load)

Floors are checked against the merged config after the whole chain and the
defaults are applied. A config that misses one does not load: the hook fails
before any check runs, nothing is noted and nothing is pushed. The error lists
every unmet floor, the field, and the file that set it where one file did.

| Floor | standard | strict |
|---|---|---|
| `steps.pre_push` includes | `credentials, lint, test` | `credentials, lint, test, security-scan, review` |
| `steps.pre_commit` includes | `credentials` | `credentials, lint` |
| No rule's `then.steps.<hook>.skip` names a required step | yes | yes |
| `commands:` has a command for each required shell step | `lint`, `test` | `lint`, `test`, `security-scan` |
| `execution` resolves to a container: `mode` is not `host` and `image` is set | | yes |
| `execution.network` is `none` | | yes |
| No `auto_fix` budget and no `writes:` entry on an agent step | | yes |
| No agent step timeout of `"0"` | | yes |
| Every agent named in `agent:` or a rule's `then.agent` is a bundled preset (`claude`, `codex`) | | yes |

A step list you never configured counts as the built-in default, which for
`pre_commit` is `[lint]`. So standard and strict need an explicit
`steps.pre_commit` that names `credentials`. The profile refuses a config that
lacks a required step rather than adding it for you, because adding it would
reorder your own list.

An agent step is one of `intent`, `review` and `document`, or any step a rule
hands to an agent. A step any rule assigns an agent counts, even if that rule
does not match every push.

`permissive` has no floors.

### Approval on the agent surfaces (runtime)

The MCP `run_trigger` tool and `warden axi run-trigger` have no person at a
prompt, so without a profile they approve the gate themselves, as
`warden-auto`. That means a rule with `require_approval: true` is met by the
same agent that started the run. Each run they approve this way says so in its
warnings (`summary.warnings` on MCP, `warnings` in axi output).

That is going away ([#295](https://github.com/klarlabs-studio/warden/issues/295)):
a later minor release will make these surfaces stop approving in every
repository, with or without a profile, so a run that needs approval ends
`needs_approval` as it does under `standard` and `strict` below. Where no
human approval was intended, drop `require_approval` from the rule; a
high-severity finding will still need a person.

Under `standard` and `strict` these surfaces do not approve. When a run they
started reaches an approval gate (a rule's `require_approval`, or a
high-severity finding), it stops there:

- the outcome is `needs_approval`, not `rejected`, because nobody declined;
- nothing is pushed and no note is written;
- the summary's `message` tells the agent that a person must approve, and how:
  run `git push` from an interactive terminal and answer the approval prompt.

`axi run-trigger` reports this, like every other outcome, in its output rather
than its exit status. The CLI hook path exits 1, as for any run that does not
pass.

Under `git push`, git hands the hook its ref list on stdin, so warden asks on
the controlling terminal (`/dev/tty`) instead, as git hooks conventionally do.
A process with no controlling terminal (CI, launchd, a tool an agent runs
without one) gets no prompt and the gate declines. On Windows the hook still
declines when stdin is not a terminal. An unanswered prompt declines after five
minutes.

This shows that a terminal answered, not that a person did: an agent that
drives a pseudo-terminal can type the answer. ADR 0007 records that limit, and
it is why the agent surfaces themselves refuse rather than rely on the prompt.

`permissive` keeps today's behavior, so a repository can record a profile
before it changes who approves. Repositories without a profile are unchanged,
until the release #295 describes.

### Agent step boundaries (checked at run time)

An agent step is one of `intent`, `review` and `document`, or a step a rule
hands to an agent. Under standard and strict, warden enforces these around
each one while the gate runs:

| Boundary | standard | strict |
|---|---|---|
| An agent step with no agent, or no command for its agent, fails with exit 78 instead of passing as skipped | yes | yes |
| The step runs in its own disposable clone; if the clone cannot be made, the step fails instead of using the validated worktree | yes | yes |
| HEAD, the index tree and the tracked files of the clone and of the validated worktree must be unchanged after the step; untracked output is allowed | yes | yes |
| The agent sees only the base environment plus its preset's credential variables | | yes |

The exit-78 failure names the profile and the fix:

```
profile standard@1 requires review to run; set `agent: claude` or `agent: codex` in .warden.yaml, or assign one with rules[].then.agent.review
warden: step review could not run: its compliance profile cannot run it as configured
```

A tree-guard failure fails the run before anything is noted or pushed, and
says what the agent changed (for example `moved HEAD`, `changed tracked
files`). Under standard, a step whose writes the config keeps (`writes:` or an
`auto_fix` budget) runs where it always has, without a clone, because a clone
would throw away the writes the config allowed. strict refuses such writes at
load, so under strict every agent step is isolated.

Under strict the agent process gets these variables, if they are set, and no
others: `PATH HOME USER LOGNAME LANG LC_* TERM TMPDIR SHELL`, plus the table for
its preset. Each name comes from the CLI's own documentation:

| Preset | Also passed | Source |
|---|---|---|
| `claude` | `ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL`, `CLAUDE_CODE_OAUTH_TOKEN`, `CLAUDE_CONFIG_DIR` | [Claude Code environment variables](https://code.claude.com/docs/en/env-vars) |
| `codex` | `CODEX_HOME`, `CODEX_API_KEY`, `CODEX_ACCESS_TOKEN`, `CODEX_CA_CERTIFICATE`, `SSL_CERT_FILE` | [Codex environment variables](https://developers.openai.com/codex/environment-variables) |

On Windows, variable names match regardless of case (`Path` is `PATH`), and
the agent also keeps what a Windows process needs to start and to find its
configuration: `SystemRoot`, `windir`, `ComSpec`, `PATHEXT`, `TEMP`, `TMP`,
`USERPROFILE`, `HOMEDRIVE`, `HOMEPATH`, `APPDATA`, `LOCALAPPDATA` and
`USERNAME`. None of them holds a credential.

A login stored by the CLI itself (`claude` `/login`, `codex login`) lives
under the home or config directory, or in the OS keychain, so it keeps
working. Codex does not read `OPENAI_API_KEY` by a fixed name, so that
variable is not passed: store the key with `codex login --with-api-key` or
use `CODEX_API_KEY`. Routing Claude Code through Bedrock, Vertex or Foundry
needs cloud credentials in the environment, and those are what the allowlist
removes, so strict does not support that yet. `agent_commands` may still
replace a preset's command; the command keeps the preset's name, and with it
the preset's table.

Agent steps still run on the host with the network they need to reach their
model. See "What strict does not prove" below.

Each agent step under standard or strict reports what warden observed:
whether it was isolated, whether the tree was unchanged, and whether its
environment was allowlisted. The signed note records it (see
[What the signed note records](#what-the-signed-note-records)).

### Profiles and `extends:`

`profile` ratchets through `extends:`. The effective profile is the highest one
in the chain, and a file that names a lower profile than the one it inherits is
a load error naming both files:

```
profile standard in .warden.yaml cannot lower strict inherited from policy/base.yaml
```

Naming the same or a higher profile is fine.

### Seeing it

`warden policy explain` prints the active profile, its digest, and each floor's
status. The MCP `policy_explain` tool returns the same data in the resolved
policy's `Profile` field, and `warden axi policy-explain` includes the profile,
its digest and any unmet floors. Without a profile, all three read exactly as
they did before.

## What the signed note records

Starting with 0.37.0, every note the gate writes under
a profile carries three more fields, all inside the signed payload (and a
fourth, `authorship`, described below):

- `profile`: the level, its version and the digest of the definition the
  writing warden had built in.
- `step_outcomes`: for each step that ran through the gate, whether it was
  `executed`, `cached` (the host input cache), `predicted` (a matching staged
  prediction) or `skipped` (nothing was configured to perform it), and the
  agent warden invoked when an agent executed it. For an agent step that
  executed, it also records the boundary warden observed: `isolated: true`
  when the step ran in its own guarded clone, `tree_unchanged` with what the
  tree guard found, and `env_allowlisted: true` when the agent saw only the
  allowlisted environment.
- `approval`: whether the approval gate needed a decision, and if so the
  channel that answered it (`tty`, `tui` or `auto`) and the name the approver
  gave itself. `none` means no decision was needed.

They state only what warden observed. A boundary field that was not observed
is left out, never written as `false`: an agent step that was not isolated
has no `isolated` or `tree_unchanged` key at all, and a step whose agent did
not run now (cached, skipped) has none of them. `warden why`, MCP `verify` and
`warden axi verify` show all of it.

### Agent declarations

A profiled note also carries `authorship` when warden saw an agent declare
itself. Two kinds of declaration are read, at gate time:

- **Commit trailers** on the commits the note attests: everything in
  `(covers_from, commit_sha]`, merge commits included, or the tip alone when
  the note claims no span. git's own trailer parser decides what a trailer is,
  so a `Co-Authored-By:` line in the middle of a message body does not count.
- **Environment markers** in the environment of the warden process that runs
  the gate, which an agent harness sets for the commands it starts.

Each match is recorded with the commit (for a trailer) or the variable name
(for a marker) and the tool that documents it. No variable value and nothing
else from the environment is written. When nothing matches, the field is left
out.

The tables are built in and cannot be configured in `@1`. Each entry comes
from the tool's own documentation:

| Signal | Tool | Matches | Source |
|---|---|---|---|
| Trailer | Claude Code | `Co-Authored-By` whose value ends in `<noreply@anthropic.com>` (any display name, any key case) | [attribution.commit](https://code.claude.com/docs/en/settings-reference#attribution-commit) |
| Trailer | Claude Code | `Claude-Session`, any value | [attribution.sessionUrl](https://code.claude.com/docs/en/settings-reference#attribution-sessionurl) |
| Marker | Claude Code | `CLAUDE_CODE_CHILD_SESSION=1` | [environment variables](https://code.claude.com/docs/en/env-vars) |
| Marker | Claude Code | `CLAUDE_CODE_REMOTE=true` (cloud sessions) | [environment variables](https://code.claude.com/docs/en/env-vars) |
| Marker | Gemini CLI | `GEMINI_CLI=1` | [shell tool](https://geminicli.com/docs/tools/shell) |

`CLAUDECODE` is not in the table: Claude Code's documentation says IDE
extensions also set it in their integrated terminals, so it would mark a person
pushing from such a terminal. Codex, Cursor and Aider are not in the table
either, because their documentation does not state a marker or the exact
trailer text. A tool that is missing only means its declarations are not seen.

These are declarations, not authorship. A trailer can be typed by a person or
switched off in the agent's settings, and a marker says what started the
process, not who wanted the push. warden never records or reports a change as
written by a person because nothing was declared.

**Under strict**, a declaration requires an Agent Trace: when the attested
commits or the gate's environment declare an agent and no trace was notarized
into the note (`agent_trace.path`, see the Agent Trace docs), the run fails
with exit 1, names the declaration and says how to configure the trace.
Nothing is pushed or noted. The check is made before any step runs, so a
refused push costs nothing, and again on the attested span before signing.
Declarations only tighten the gate: a change that declares nothing gets every
other strict check, so hiding a declaration removes only the trace requirement.
Below strict, declarations are recorded and require nothing.

`warden why` prints an `authorship:` line for every profiled note, and MCP
`verify` and `warden axi verify` return the field.

A note written without a profile has none of them and is byte for byte what it
was before. A warden older than this feature that meets a profiled note drops
the fields it does not know, computes a different payload and reports the
signature as invalid. A verifier that pins a key therefore rejects a profiled
note it cannot read, so upgrade verifiers before adopting a profile.

## Checking a profile in CI

A profile name in a note is a claim. `warden verify --min-profile` does not
take the claim; it recomputes conformance from what the note's signature
covers:

```bash
warden verify --key '<trusted-fingerprint>' --min-profile strict
```

It exits 0 only when every clause below holds for the exact commit, and 1
otherwise, listing each clause that failed. For a single commit it refuses to
run (exit 2) without an explicit `--key`: the `.warden.yaml` roster lives in
the checkout being judged, so it cannot vouch for that checkout. A range is
checked against its base ref's profile; see
[Checking a profile across a range](#checking-a-profile-across-a-range).

| Clause | permissive | standard | strict |
|---|---|---|---|
| The note attests this commit, is signed by a pinned key, and is not an external-run attestation | yes | yes | yes |
| The note records a profile whose name, version and digest match this warden's built-in definition. An unknown version or a different digest fails | yes | yes | yes |
| The recorded profile ranks at least the minimum | yes | yes | yes |
| Each required pre-push step (`credentials, lint, test`; strict adds `security-scan, review`) has a recorded outcome that counts | | executed, cached or predicted | executed only |
| Each required shell step (`lint`, `test`, `security-scan`) has a complete Linux container execution record | | | yes |
| Each required shell step ran with network `none` | | | yes |
| Every agent step that executed has `isolated: true` and `tree_unchanged: true` | | yes | yes |
| Every agent step that executed has `env_allowlisted: true` | | | yes |
| Where the approval gate needed a decision, it was answered by `tty` or `tui`, not `auto` | | yes | yes |
| Where the note records an agent declaration (`authorship`), it also records a notarized Agent Trace | | | yes |

The network clause is checked separately from the container record:
`--container-steps` accepts a container check that ran with `network: bridge`,
and strict does not.

A cached or predicted result reuses a pass recorded for identical inputs on an
earlier run, so standard counts it. Strict does not: the note does not
describe that earlier run.

An agent step is one the note says an agent ran, a built-in agent step, or a
step the policy handed to an agent. A missing boundary observation fails the
same way a `false` one does.

The trace clause reads the same `authorship` field the push-time rule
writes. A profiled note without it means no declaration in the built-in
tables was seen, so the clause holds; it does not mean a person wrote the
change.

A [version 2 requirements file](execution-contracts.md#version-2-a-minimum-profile)
can demand the same minimum from protected CI policy. The GitHub
`warden-verify` action and the GitLab component take a `min-profile` input.
A warden release older than this feature rejects the flag, so the GitHub action
reports `validated=false` and CI runs its checks. The `warden-gate` action's
`min-profile` input is covered in the next section.

## Checking a profile across a range

`warden verify --range BASE..HEAD` enforces the profile of the branch a change
is merged into. It reads `profile:` from the **base** ref's `.warden.yaml`,
through its `extends:` chain at that same ref, the way it already reads
`trusted_keys`. A pull request that lowers `profile:` in its own head is still
judged by the base's profile. Every noted commit in the range must pass the
clauses in the table above for that profile, recomputed from its own signed
observations:

```bash
warden verify --range origin/main..HEAD
warden verify --range origin/main..HEAD --min-profile strict   # also demand strict
```

- **`--min-profile` adds a minimum; it does not replace the base's.** Both
  apply, and a commit must conform to each. A flag cannot lower the base's
  profile.
- **The base profile is read on every range gate**, with or without `--key`,
  because it is policy, not a trust anchor. A base config that does not load,
  names an unknown profile, or misses its profile's floors fails the gate
  (exit 1, with the error) rather than dropping to no profile.
- **Conformance needs trusted signers.** A note counts as trusted when its
  signature verifies against the pinned `--key` set or, without `--key`, the
  base ref's `trusted_keys` roster. With a profile in force and neither, every
  noted commit fails the trust clause, and the message says a profile needs a
  pinned key or a base roster.
- **Span coverage.** A commit without a note of its own, inside a gated push's
  signed span, is judged by the covering note. That note must conform too, or
  it covers nothing.
- **Forge-accepted commits are not profile-checked.** A commit the forge
  authored and the base config accepts (`forge.accept_authored`) has no warden
  record whose observations could be recomputed. It stays accepted, since the
  base config made that decision explicitly, and is marked
  `profile_unchecked: true` in `--json`, MCP `verify_range` and
  `warden axi verify-range`; the human summary counts it as not
  profile-checked.
- **Bootstrap.** Until the base carries a profile, and without `--min-profile`,
  no profile is enforced: the gate is exactly what it was, and its output does
  not mention a profile. The first pull request that introduces `profile:` is
  therefore not checked against it, as with the roster.

A failing commit reports reason `profile` with `profile_failures`, one entry
per clause that did not hold (`profile standard@1: <clause>: <reason>`). The
`--json` export, MCP `verify_range` and `warden axi verify-range` also list
each enforced profile with `from_base` and `from_flag`. MCP `verify_range`
takes `min_profile`, and `warden axi verify-range` takes `--min-profile`.

The `warden-gate` action forwards its `min-profile` input to
`verify --range --min-profile`. A warden release that predates range checking
refuses that combination, or the flag, so the gate fails rather than passing a
range it did not check. Pin a `warden-version` with range checking when you set
the input.

## Not yet enforced

A profile does **not** yet refuse host cache hits or predictions for required
steps while the gate runs. `verify --min-profile strict` refuses them
afterwards (see [Checking a profile in CI](#checking-a-profile-in-ci)), and
under strict the required shell steps run in a container, where the cache is
off. Refusing them at run time is gate behaviour, not part of a definition,
so adding it later leaves `strict@1` unchanged.

## What strict does not prove

Even with every part in place, strict@1 does not prove:

- that an agent's reasoning was correct (agent steps produce advisory findings);
- that agent steps had no network (they run on the host);
- that an approval came from a person (`tty` means a terminal answered);
- that a commit without agent signals was written by a person, or that a
  commit with one was written by an agent (a declaration is a claim, and the
  tables only know the tools listed above).

## Adopting a profile

1. Upgrade every reader (above).
2. Commit `profile: permissive`. Run `warden policy explain` and read the floors
   for the next level.
3. Fix what standard needs, commit `profile: standard`, and let it settle.
4. Move to `strict` once your checks run in a container image.

Each step is a reviewed diff. A profile change alters the effective config, so
its configuration digest changes too: regenerate any `--requirements` file you
keep.
