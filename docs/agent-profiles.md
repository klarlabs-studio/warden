# Compliance profiles

A compliance profile is a named, built-in set of defaults and floors for a
repository where coding agents commit and push. You pick one with a single key:

```yaml
profile: standard   # Requires warden >= 0.36.0: older releases reject the key
```

The decision record is [ADR 0007](adr/0007-agent-compliance-profiles.md). This
page covers what you can rely on **in the current release**. Several parts of
the design land in later releases, and this page says so wherever that applies.

> **Preview.** Until warden records the profile in signed notes, the
> definitions are not frozen: `standard@1` and `strict@1` gain floors as the
> later parts land, so a repository that adopts a level now will get a
> stricter gate on some upgrades. Each addition is listed in the changelog.
> The release that first signs the profile into notes freezes `@1` and its
> digest; from then on a stricter level ships as `@2` and is never adopted
> automatically.

## Before you add the key: upgrade every reader first

warden refuses a config that contains a key it does not know. A warden older
than 0.36.0 reading `profile:` fails every hook with a parse error. From 0.36.0
on, that error also says the key may need a newer warden.

So the order matters:

1. Pin warden 0.36.0 or later in CI (`warden-version` inputs, the GitLab
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
same agent that started the run.

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
before it changes who approves. Repositories without a profile are unchanged.

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
environment was allowlisted. In this release that report is on the step
result, not in the signed note.

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

Starting with the first release after 0.36.0, every note the gate writes under
a profile carries three more fields, all inside the signed payload:

- `profile`: the level, its version and the digest of the definition the
  writing warden had built in.
- `step_outcomes`: for each step that ran through the gate, whether it was
  `executed`, `cached` (the host input cache), `predicted` (a matching staged
  prediction) or `skipped` (nothing was configured to perform it), and the
  agent warden invoked when an agent executed it.
- `approval`: whether the approval gate needed a decision, and if so the
  channel that answered it (`tty`, `tui` or `auto`) and the name the approver
  gave itself. `none` means no decision was needed.

They state only what warden observed. The agent step boundary observations
(isolated, tree unchanged, environment allowlisted) are not in the note yet;
they are reported on the step result only. `warden why`, MCP
`verify` and `warden axi verify` show the three fields.

A note written without a profile has none of them and is byte for byte what it
was before. A warden older than this feature that meets a profiled note drops
the fields it does not know, computes a different payload and reports the
signature as invalid. A verifier that pins a key therefore rejects a profiled
note it cannot read, so upgrade verifiers before adopting a profile.

## Not yet enforced

These parts of the design need runtime or verifier work that later releases
add. In this release a profile does **not**:

- record agent isolation (the boundary observations above) or authorship
  signals in the signed note;
- give CI a way to demand a minimum profile (`verify --min-profile`, the range
  gate, the action inputs);
- refuse host cache hits or predictions for required steps.

Until the release that records the profile in signed notes, a later release may
add the checks above to the existing `standard@1` and `strict@1` definitions.
From that release on, a published definition never changes again.

## What strict does not prove

Even with every part in place, strict@1 does not prove:

- that an agent's reasoning was correct (agent steps produce advisory findings);
- that agent steps had no network (they run on the host);
- that an approval came from a person (`tty` means a terminal answered);
- that a commit without agent signals was written by a person.

## Adopting a profile

1. Upgrade every reader (above).
2. Commit `profile: permissive`. Run `warden policy explain` and read the floors
   for the next level.
3. Fix what standard needs, commit `profile: standard`, and let it settle.
4. Move to `strict` once your checks run in a container image.

Each step is a reviewed diff. A profile change alters the effective config, so
its configuration digest changes too: regenerate any `--requirements` file you
keep.
