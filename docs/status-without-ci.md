# Publishing the gate verdict where CI cannot run

The [provenance gate](ci-provenance-gate.md) makes an un-gated commit fail a
required check. It assumes something on the forge can run `warden verify`.

Sometimes nothing can. A private repository past its Actions spending limit
does not fail its jobs — it never starts them. Every job reports `failure`
with zero steps executed, and because those jobs are required checks, every
pull request is permanently blocked. The gate still ran on the developer's
machine, still wrote a signed note, still proved exactly what it always proved.
It just has no way to say so.

Setting `status.enabled` lets warden report its own verdict as a **commit
status** after a passing push:

```yaml
# .warden.yaml
status:
  enabled: true
```

A commit status is not an Actions job. It costs no minutes, runs no runner, and
is posted through the `gh` CLI already used for pull requests — so warden holds
no token of its own.

## Requiring it is a deliberate change

The default context is **`warden/gate`**, and it has to be a name warden alone
writes.

The first attempt published under `Warden provenance` — the `warden-verify`
action's job name — on the theory that branch protection matches a required
check by name and does not care what satisfied it. That is wrong, and the
measurement is worth keeping: GitHub holds a commit status and a check run as
**separate entries** even when they share a name, and a required check passes
only when *every* entry under it passes. The green status simply appeared
beside the Action's red check run, and the pull request stayed blocked.

So make it its own check and require it:

```
Settings → Branches → main → Require status checks to pass
  warden/gate
```

While Actions cannot run, that is the required check. The jobs that cannot
start keep reporting failure, so remove them from the required list for as long
as the outage lasts — leaving them required is what blocks every pull request
regardless of the code. Note down what you removed; putting it back is the
last step of the cleanup below.

Override the context if you want a different name:

```yaml
status:
  enabled: true
  context: "gate"
```

## What it does and does not claim

It publishes only what this machine did, and only after the gate passed *and*
the push succeeded. A failing gate publishes nothing — there is no state in
which warden reports success for a commit it did not clear.

The description names the steps that ran (`warden gate passed (test, lint,
security-scan)`), because "passed" on its own invites a reader to assume checks
that were never configured.

The status is **a pointer, not the evidence**. The evidence is the signed note,
and `warden verify` remains the thing that checks it. Anyone with push access
can post a status saying anything; nobody without the signing key can produce a
note that `warden verify` accepts. Treat the status as what unblocks the merge
button and the note as what you would audit.

## Failure is a warning, never a rollback

The push has already happened by the time the status is posted. If the forge
refuses — no `gh`, no auth, a 403 — warden warns:

```
gate passed but its commit status was NOT published: <reason>.
This commit will read as ungated to branch protection until it is.
```

and exits successfully. A reporting failure never unwinds a push that already
succeeded, for the same reason a pull-request failure does not.

## When to turn it off again

This is a workaround for a forge that cannot run your checks, and it is off by
default because it writes to a surface other people read as CI. When Actions
work again, the Action posts the same check under the same name, and you can
drop `status.enabled` without touching branch protection.
