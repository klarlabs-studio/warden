# Changelog

All notable changes to warden are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and warden adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- **Dependencies refreshed.** `axi` 1.4.0 → 1.5.0, `fortify` 1.8.1 → 1.10.0,
  `mcp` 1.24.0 → 1.27.0, `statekit` 1.13.2 → 1.14.0, `golang.org/x/crypto`
  0.54.0 → 0.55.0, and `golang.org/x/text` 0.40.0 → 0.41.0 transitively.

  The `go` and `toolchain` directives are deliberately unchanged: raising them
  turns every Lint job red until golangci-lint is bumped in the same commit, and
  that coupling is not worth carrying for a routine refresh.

  Verified beyond the test suite, because a library bump can pass every unit
  test and still break the wiring that composes it: the rebuilt binary was
  exercised across `version`, `status`, `verify`, `axi` and `steps`, and the MCP
  server — the largest jump at three minor versions — was driven over stdio
  through `initialize` and `tools/list`, returning all ten tools.

  `govulncheck` is unchanged at 0 vulnerabilities affecting warden. The one
  advisory against a required module, GO-2026-5932, is `x/crypto/openpgp` being
  unmaintained; it has no fix, and warden does not call it — commit signatures
  go through git and gpg.

### Added

- **`Tap credential check` — verify the Homebrew credential without cutting a
  release.** The inline guard added alongside it only runs *during* a release,
  which left "does this credential actually work?" answerable only by
  publishing something. This workflow answers it on demand: dispatch it before a
  release, after rotating the token, or after changing which repositories an org
  secret is shared with.

  It reports the distinct faults separately — empty (a name or access-list
  problem, since an undefined secret expands to an empty string), 401, 403, 404
  — and then asks GitHub explicitly whether the credential can **push**. Being
  able to read the tap repo does not imply being able to write to it, and
  goreleaser needs to write; checking only reachability would let a read-only
  token pass and still fail the cask push. It holds `contents: read` and
  publishes nothing.

### Fixed

- **The tap guard's error messages no longer trip the security scanner.** Two
  nox rules matched the new step's *error text* rather than its behaviour:
  SEC-163 read the secret's name as a high-entropy hex string, and IAC-314 read
  a quoted English sentence explaining what a 403 means as a permission the
  workflow grants. Both are false positives on prose — but SARIF is not
  baseline-filtered, so they arrived as PR review threads and blocked the merge
  under `required_conversation_resolution`, with every check green and no red
  tick to explain it.

  The messages are reworded to say the same things without the matched tokens,
  which fixes the cause rather than dismissing the symptom each time a line
  number shifts. Verified by scanning before and after: the two prose findings
  are gone and the file's genuine write-permission declarations are still
  flagged, so this narrows the scanner's noise without narrowing its reach.

- **The tap credential is now asserted before a release can half-ship.**
  goreleaser reaches the Homebrew tap push only at the END of a release, after
  the binaries are built, signed and uploaded, so a bad tap credential fails a
  release that has already published — which is how v0.31.0 shipped without its
  SBOMs. A **Check the tap credential** step now runs *before* goreleaser
  publishes anything, reporting empty, 401, 403 and 404 as the different faults
  they are, so a release that cannot finish is abandoned while abandoning it is
  still cheap. A regression test pins the guard's position and its branches.

  The v0.31.0 failure itself was an expired token, and rotating it fixed it.
  During this work that was re-diagnosed as a secret-name mismatch, and both
  workflows were briefly pointed at a name that does not exist — which broke
  the release path rather than repairing it, until the probe below caught it.
  The account here is the corrected one: there is exactly one tap secret,
  `HOMEBREW_TAP_TOKEN`, and it is the one the workflows read.

  The general mechanism that made the wrong story plausible is real and worth
  keeping in mind: an undefined secret expands to the empty string rather than
  failing, so **401** means no credential was presented (a name or access-list
  problem) while **403** means one was presented and refused. What was missing
  was the check that distinguishes them —
  `GET /repos/{owner}/{repo}/actions/organization-secrets` lists the org secrets
  available to a repository and needs only repo admin.

- **Re-driving a release no longer reports failure for work already done.** npm
  versions are immutable, so the documented recovery path — `workflow_dispatch`
  against the tag, used whenever a later channel fails — always died with "You
  cannot publish over the previously published versions". The v0.31.0 re-drive
  is the worked example: it repaired the Homebrew tap and attached the missing
  SBOMs, and the run still went red, so anyone reading the status alone would
  have hunted for a problem that had just been fixed.

  A version already on the registry is now skipped with a notice. The skip is
  deliberately narrow — it fires only when the registry holds that exact
  `name@version`, and every other publish failure (auth, OIDC, network) still
  fails the step. `npm view` erroring is treated as "not published", so a
  network blip makes warden ATTEMPT the publish rather than assume it happened:
  wrong in the safe direction. A re-drive that publishes nothing says so
  explicitly rather than leaving it to be inferred from a green tick.

- **A release no longer loses its SBOMs to an unrelated credential.** goreleaser
  publishes the GitHub Release and THEN pushes the Homebrew cask, so a bad tap
  credential fails a step that runs AFTER the release already exists — and every
  later step in that job was skipped by default. v0.31.0 shipped its binaries,
  checksums and cosign bundle with **no SBOMs at all**, because that push was
  rejected. For a tool whose subject is supply-chain provenance, losing the
  supply-chain artifacts to a packaging credential is the wrong thing to lose.

  The SBOM step now runs on `always() && !cancelled()` and decides from the
  RELEASE rather than from an exit code — attaching artifacts if and only if a
  release exists to attach them to, and warning rather than failing twice when
  goreleaser died before publishing. That is the rule the `major-tag` job in
  the same file already adopted after v0.19.0, where a bad tap credential
  stranded the floating `v0` tag; the lesson had simply never reached the step
  beside it.

  A test now enforces both halves for every step that uploads a release asset,
  so it reaches the next one automatically: the step must survive an earlier
  failure, and any step that swallows a tool's exit code with `|| true` must
  verify the tool produced something. `nox scan` exits non-zero on findings
  even when baselined, so its artifacts — not its status — are what stand
  between a green release and an empty SBOM.

  Also scoped `GOTOOLCHAIN: auto` to that step: nox's Go floor can move past
  warden's, and `setup-go` pins `GOTOOLCHAIN=local`, which turns that into a
  hard failure. The same shape already documented in `provenance-main.yml`.

## [0.31.0] - 2026-08-26

Two things warden could not previously say: what the forge REQUIRED, and what
the machine was doing. Both are the same omission — a verdict reported without
the context needed to judge it.

### Added

- **A step that failed on a starved machine says so, without pretending to know
  it.** A command that fails because the box was oversubscribed is
  indistinguishable from one that fails because the code is broken: both are a
  non-zero exit, and "step test failed" is a claim about the change. On a box
  carrying 10.9x its cores that claim was false — a suite that fits a 10-minute
  budget when idle hit it under contention, and the author was told their commit
  had been rejected (#249).

  A failing step now reports the machine's state beside the verdict when the
  load average is at least 4x the core count:

      This ran at 109.00 on 10 core(s) — roughly 10.9x oversubscribed. A
      wall-clock failure here may be the machine rather than your change;
      warden cannot tell which, so the verdict above stands.

  It lands in the finding's `Why` — warden's own explanation — and never in
  `Message`, which carries the tool's output verbatim.

  **Deliberately not a reclassification.** Mapping a timeout to `EX_TEMPFAIL`
  was the obvious request and is refused: an infinite loop times out too, and
  *sooner* when starved, so warden would be telling an author their genuine
  deadlock was a busy machine — the same over-claim reported here, facing the
  other way. The status, the verdict and the exit code are untouched. Only the
  evidence a human needs to judge it is added, which warden was not collecting
  at all: nothing in the codebase read load, and no record carried machine
  context.

  A platform with no load average — Windows has none — reports UNKNOWN and
  prints nothing, rather than a zero that would read as an idle machine.

  Note this reaches the developer at the moment of failure and not through the
  attestation, because a rejected commit has no attestation: the run returns
  before a note is written.


- **`warden evidence --approvals` reports the branch RULE, not just the
  outcomes.** Two repositories produced an identical line — twelve changes,
  twelve independent approvals — where one required review and enforced it and
  the other merely happened not to skip it. The first has a control; the second
  has a habit, and an auditor could not tell them apart. warden's own CC8.1
  limits text already conceded the gap: "a change merged with administrator
  privileges past a required review appears as unapproved" is warden admitting
  it could see the outcome and not the rule.

  The report now states, before the counts, what the forge requires: approving
  reviews, stale-review dismissal, last-push approval, conversation resolution
  — and whether any of it binds administrators. That last one decides whether
  the rest is a requirement or a default, so it is never omitted:

      Branch rule (`main`). pull request required, but no approving review is;
      stale approvals dismissed on new commits; all review threads must be
      resolved. NOT enforced against administrators, so each rule above is a
      default an admin may merge past.

  The CC8.1 control text changes with the rule rather than describing approvals
  generically, so "0 independent approvals" reads as the configuration choice it
  is instead of sixteen control failures.

  **Three states, kept apart.** Reading branch protection needs admin rights, so
  an ordinary token gets 403 — and that is NOT the same as a branch having no
  rule. A 404 is the forge answering that there is none, which is a finding; a
  403 is warden failing to look, which is not. Folding them together would put
  a control-gap accusation in an evidence document on the strength of a
  permission warden does not hold.

  **Its own limit, stated in the report.** Forges expose no history of their
  protection settings, so this is the rule in force WHEN THE REPORT WAS
  PRODUCED, not the rule that applied to each historical change. A rule enabled
  last week reads as though it always applied, and the document says so.

## [0.30.2] - 2026-08-24

Ships what 0.30.1 could not: the npm channel, and binaries built against a
patched standard library.

### Security

- **Built with a patched Go toolchain.** warden pinned `toolchain go1.26.4`, and
  govulncheck reported REACHABLE call paths from warden's own code into that
  toolchain's standard library — `GO-2026-6218` (net/url), `GO-2026-6090`
  (crypto/tls) and `GO-2026-5972` (encoding/asn1). Now `go1.26.7`; verified
  before and after under the resolution CI performs, 4 reachable
  vulnerabilities and exit 3 becoming none and exit 0.

  The `go` directive is untouched, so nothing importing warden has its floor
  raised. A stale toolchain line is worse than a missing one: the file looks
  like somebody already considered it, and nothing re-checks when a patch lands.
  (#242)

### Fixed

- **The npm channel could miss a release the other channels shipped.** 0.30.1
  published binaries, checksums, SBOMs and the Homebrew tap, and did not publish
  to npm — `npx @klarlabs-studio/warden` stayed on 0.30.0 while every other
  channel moved. The release run reported failure; the release itself existed,
  so nothing downstream noticed.

  The npm job built its binaries with `go run
  github.com/goreleaser/goreleaser/v2@latest`, which is two faults in one line.
  Unpinned, so the release toolchain can change under a tag that was already
  tested; and built FROM SOURCE, which binds the release to goreleaser's own Go
  floor. That floor moved to 1.27, warden targets 1.26, `setup-go` pins
  `GOTOOLCHAIN=local`, and the job died before it built anything:

      goreleaser/v2@v2.18.0 requires go >= 1.27.0 (running go 1.26.6)

  It now uses the same pinned `goreleaser-action` the sibling job has always
  used — the action ships a goreleaser binary, so publishing no longer depends
  on the runner's Go being new enough to compile one. `provenance-main.yml`
  already carried the same fix for the same failure with nox; the release
  workflow never got it.

## [0.30.1] - 2026-08-24

One diagnosis, reported about warden's own trunk, that named a cause which had
not occurred — and a report that now says which of its exceptions can never be
closed.

### Fixed

- **A note that names no commit is no longer reported as one naming a different
  one.** `warden doctor` said `UNBOUND (note describes another commit — history
  was rewritten)` about seven commits on this repository's own trunk. Their
  notes describe no commit at all: `commit_sha` is empty, and every one was
  written by warden 0.8.3–0.9.0 between 4 and 6 July 2026 — before records were
  bound to commits at all (0.10.0). Nothing had been rebased.

  `BindsTo` is false in two different worlds and `AttestDefect` returned the
  same defect for both, so the label asserted a cause that never occurred and
  pointed the reader at `reattest`, which can do nothing for a note with no
  binding to repair. The new `DefectUnbindable` says what is true and that it is
  permanent.

  Fourth instance of this shape, and the verdict is unchanged: both still fail,
  and `verify` still refuses them. Naming a cause is not softening it.

- **`warden evidence` says which exceptions can never be closed.** An exception
  list reads as a backlog. Where some of it is not — a pre-binding note cannot
  be repaired by any action — the report now says so, and says how many.

  Deliberately NOT reclassified as "outside the control", which was the obvious
  alternative: the control WAS operating, a gate ran and recorded it, and moving
  those commits would shrink the exception count by editing the definition
  rather than by fixing anything. An exception count that falls because the bar
  moved is exactly what an auditor is entitled to be suspicious of. The
  reasoning is recorded on `PreBindingExceptions` so the decision is not
  rediscovered as an oversight.

## [0.30.0] - 2026-08-23

A gate that could not tell a bypass from a commit no human ever touched, and
four claims it made that its evidence did not support.

Every fix here is one defect wearing different clothes: warden asserting more
than it observed. The worst of them had reached a signed compliance artifact,
where an expired token turned ten reviewed changes into ten reported review
bypasses — at exit 0, with a stable digest and a control mapping attached.

### Added

- **`forge.accept_authored`: a gate that can tell "a human bypassed me" from
  "no human was ever here".** warden's gate is a client-side pre-push hook, so
  a commit the FORGE creates — a squash merge, a web edit, a Dependabot or nox
  remediation commit — was never on a machine where warden could run. Under a
  required gate those commits can never pass, so a repository either stops
  merging bot pull requests or reaches for the admin override; this repository
  had two dependency PRs blocked for days, and `docs/ci-provenance-gate.md`
  had predicted exactly that.

  Two halves, and the first ships regardless of configuration. A forge-authored
  commit is now REPORTED as one instead of as `no warden note (pushed with
  --no-verify, or made outside warden)` — two developer-bypass causes, both
  wrong, aimed at whoever opened the pull request. Naming the cause is not
  permission: the default still fails.

  The second half, opt-in, lets such a commit pass. What makes that safe is the
  signal it keys on. The committer field says `GitHub <noreply@github.com>` and
  is free text any attacker sets with one flag; a gate reading it would enforce
  nothing. GitHub *signs* the commits it creates, so warden instead requires a
  signature **git could verify** against a pinned FULL fingerprint. Measured:
  for a commit whose key is absent from the keyring git reports `%G?=E` and an
  empty `%GF`, while `%GK` — the 64-bit key id — stays populated, because it
  travels inside the signature packet. So the key id is exactly as trustworthy
  as the thing being checked, and is never matched on.

  Default off, and read from the range's BASE ref like the trusted-signer
  roster, for the same reason: a pull request that could enable this in its own
  head would be deciding whether it has to be gated at all. Accepted commits
  are counted and reported separately — *"N accepted as forge-authored — the
  forge signed them; warden ran NO checks on them"* — because a weaker claim
  that reads as a stronger one is the failure this project exists to prevent.

  GitHub-specific by design. A self-hosted GitLab or Gitea may not sign what it
  creates, and there is nothing safe to key on when it does not.

### Fixed

- **`warden verify` named two causes that were both the opposite of the truth.**
  A commit with NO note reported `note is unsigned but a trusted key was
  required`, and a commit WITH a note that does not attest it reported `no
  intact warden note`. Each message described the other one's situation.

  `res.Signed` is false in two different worlds — a note carrying no signature,
  and no note at all — so with a roster configured the absent case was caught by
  `pinned && !res.Signed` and the honest branch below it became unreachable.
  Anything that fell past the signature checks then got "no intact warden note",
  including notes sitting right there that simply bind elsewhere.

  Both now read from `Record`, which is nil only when the note is absent —
  `VerifyWithPolicy` returns an ERROR when a note cannot be READ, so absent and
  unreadable stay distinct. An unbound note is now named as one, and points at
  `warden why`, which prints it. `warden doctor` already classified this case
  correctly as UNBOUND; the two surfaces no longer disagree about one fact.

  Third instance of this shape in this file's history — 0.24.1 fixed a note-read
  error collapsing into the absent case. The tests are why it survived: three of
  the six `printVerify` fixtures described notes that exist (signed, invalid
  signature, unsigned) while constructing no `Record` at all, so they asserted
  note-property messages about the absent-note state and agreed with the code.
  Fixtures corrected and both directions covered.


- **`warden evidence --approvals` no longer reports a forge it could not read
  as a forge with no pull requests.** A non-zero `gh` exit was folded into "no
  pull request", conflating *the forge has no PR for this commit* with *warden
  could not look*, and `Available()` only checked that `gh` was on `PATH`. With
  a valid `gh` but no working credential, the same ten commits that report
  "merged through a pull request nobody approved" instead reported "not
  associated with a pull request" — exit 0, nothing on stderr, and a signed
  evidence document asserting that ten changes bypassed review entirely. Every
  one of them had gone through a pull request.

  Three changes. A preflight (`Reachable`) proves the repository is actually
  readable before anything is written; when it fails the run exits 2 naming
  gh's own cause and produces no document, because an evidence artifact that is
  silently wrong is worse than none. Per-commit outcomes are now read from the
  HTTP status rather than the exit code, so a genuine 404 stays the real
  finding it is. And what the preflight cannot cover — a rate limit partway
  through a long run — becomes an explicit **undetermined** state that outranks
  the other four, gets its own row, and is excluded from the approval
  population, so the report says "these counts describe 7 changes, not 10"
  rather than quietly answering for three it never read.

  This is the defect this project is named for: warden asserting more than its
  evidence supports. It is worth recording that it reached a compliance
  surface.

- **The evidence document identifies the repository, not the machine that
  produced it.** The header, the JSON `repository` field and the OSCAL title
  all carried `os.Getwd()` — an absolute local path, which two clones of one
  repository disagree about, an auditor cannot resolve, and which puts the
  producer's home directory in a document that leaves the organization. It now
  uses the `origin` remote, the same identity `warden attest` already used, and
  a repository with no remote says so instead of naming a path. The evidence
  digest never covered this field and still does not, so it stays reproducible
  across clones.

- **A hook preflight that timed out no longer reports the binary as corrupt.**
  The shim time-boxes a `warden --version` call so a binary that cannot start
  fails fast instead of wedging every commit. But it treated *any* non-zero
  result as proof the binary was broken, and told the developer to strip a
  Gatekeeper quarantine or reinstall — so a machine merely busy enough to blow
  the 15s budget sent someone to fix something that was never wrong. It now
  distinguishes the timeout convention (124) from a genuine failure to run and
  names what actually happened. Both still fail closed; they no longer share a
  cause.

  Found because it was also failing warden's own test suite (#228): the two
  shim tests execute the shim end to end, so under a full gate run — race
  suite, lint and scanner competing for one machine — they were asserting on
  wall clock they did not control, taking 60s where they take 0.3s alone. The
  15s budget is a real product decision for a git hook and was not widened to
  suit them; the tests now bypass the timeout wrapper instead.

- **A signed VSA no longer carries the remote's credential.** `normalizeRemote`
  passed any URL-form remote through untouched, so a CI checkout's
  `https://x-access-token:<token>@…` origin — or the equally valid bare
  `https://<token>@…` form — reached `resourceUri` and the policy URI verbatim,
  and `--sign` then signed it into an envelope built to be handed to somebody
  else. All userinfo is now stripped: the credential is as often the username
  as the password, and these URIs identify a repository rather than being clone
  commands, so they lose nothing by it. `warden evidence` makes the same call;
  the two artifacts must not disagree about what is safe to publish.

## [0.29.2] - 2026-08-22

Evidence someone other than its author can use: the approval half of change
management, and an audit that no longer depends on the machine that ran
`warden init`.

### Added

- **`warden evidence --approvals`: the other half of CC8.1.** warden observes
  the gate, not the review, so change-management evidence stopped at "these
  checks ran". This reads the forge's record — the pull request each change
  arrived through, its author, and who approved it — and reports four states
  kept deliberately apart: approved by someone else, self-approved only, nobody
  approved, and no pull request at all.

  A self-approval is a record of a review that did not happen, and is counted as
  such. A bot approval never counts on its own. Expect an uncomfortable number
  the first time you run it: a single-maintainer repository shows zero
  independent approvals, because that is true, and the report says so instead of
  leaving an auditor to find out.

  Opt-in — one forge call per commit.

### Fixed

- **An audit no longer needs the machine that ran `warden init`.** The adoption
  point is written to `.git/warden/adoption` — local, untracked, per-clone
  state — so a fresh clone got "warden was never initialized in this repo" and
  no report at all. Tolerable for `warden doctor`; fatal for `warden evidence`,
  where an artifact only one laptop can produce is not evidence, because the
  person checking it cannot reproduce it. Six of eight repositories in one
  fleet were in exactly that state.

  When the file is absent, the adoption point is now derived from
  `refs/notes/warden`, which is shared: the parent of the earliest noted commit
  on the branch — the point at which the gate demonstrably began operating.
  Same population, same digest, on any clone. A repository with no notes at all
  says nothing has been gated rather than inventing a start date.

## [0.29.1] - 2026-08-22

Rolling 0.29.0 across a fleet found the ordering bug below within the hour,
and the evidence command is the other half of the same question: a gate is
only useful if someone else can read what it did.

### Added

- **`warden evidence`: the audit, shaped for the person who signs the opinion.**
  `warden audit` answers "is this branch's provenance intact". An auditor asks
  a different question — over this period, what changed, which changes went
  through the control, and what are the exceptions — and answering it from
  `audit` output meant a spreadsheet and a covering note every quarter.

  Same records, three readers: `--format md` for the auditor, `json`
  (versioned `warden.evidence/v1`) for a GRC platform to ingest on a schedule,
  and `oscal` for tooling that speaks NIST assessment-results. Scope it with
  `--from` / `--to`, and select control catalogues with `--frameworks`.

  What makes it evidence rather than a report: a complete **population** rather
  than a sample, reconcilable against `git log`; exceptions carrying the
  *specific* reason warden could not vouch for each commit, distinguishing a
  bypass from a never-pushed branch from a squash-merge binding gap; a stable
  digest so a re-run can be told from an edit; and the verification command, so
  the verdicts can be re-derived without trusting the document.

  Every mapped control states what it does **not** evidence, next to what it
  does — approval, check adequacy, deployment, key custody. Evidence is
  rejected for claiming too much far more often than for proving too little.
  See [docs/grc-evidence.md](docs/grc-evidence.md).

### Fixed

- **An unstamped binary reports the version it was built from.** `Version` fell
  back to a literal in the source, so anything installed with `go install`
  reported that number indefinitely. A nuisance in `warden --version`, and a
  defect in `warden evidence`, where the producing tool's version is part of an
  artifact somebody signs. It now reads the module version — or `dev+<commit>`
  — from the embedded build info when ldflags did not set one.

## [0.29.0] - 2026-08-22

A gate that cannot report is a gate nobody can rely on. This release is about
the case where the forge itself is the thing that stopped working.

### Added

- **`status.enabled`: publish the gate verdict where CI cannot run.** A private
  repository past its Actions spending limit does not fail its jobs — it never
  starts them. Every required check reports `failure` with zero steps executed,
  and every pull request is blocked indefinitely. The gate still ran locally and
  still wrote a signed note; it had no way to tell the forge.

  With `status: {enabled: true}`, a passing, pushed run posts a commit status
  through the `gh` CLI already used for pull requests — no Actions minutes, no
  runner, no token held by warden. The context is `warden/gate` — a name warden
  alone writes. Publishing under the Action's own job name was the first
  attempt and does not work: GitHub keeps a status and a check run as separate
  entries under a shared name and requires both to pass, so the green status
  just sat beside the Action's red check run. Requiring `warden/gate` is
  therefore an explicit protection change, which is the honest shape — a repo
  is choosing to accept a locally-produced verdict.

  Off by default: publishing writes to a surface other people read as CI, and
  nobody should acquire that behaviour by upgrading. A failing gate publishes
  nothing. A forge that refuses the status produces a warning, never a rollback
  — the push has already happened by then. See
  [docs/status-without-ci.md](docs/status-without-ci.md).

- **`warden attest --sign`: the claim survives being carried.** `warden attest`
  emitted a bare statement, and in practice whatever carried that statement
  onward did the signing — so a build platform attaching warden's verdict to a
  container image produced an envelope signed by the build platform, leaving a
  consumer able to conclude only "the builder says warden said this". Which is
  not what warden said.

  `--sign` emits a DSSE envelope signed with the same key that signs notes, so
  one trust decision — the `trusted_keys` roster in `.warden.yaml` — governs
  both. The envelope names the signing fingerprint as its `keyid`, so a verifier
  holding a roster selects the right key rather than trying each in turn, and
  DSSE's PAE binds the payload type and each field's length into what is signed
  rather than the payload alone — without it, identical bytes could be
  re-presented under a different media type and still verify. Signing with no
  key available is an error rather than an unsigned envelope: a caller who asked
  for a signature and silently got none would ship something nobody can verify.

  (#215. Shipped in 0.29.0; these notes omitted it until 2026-08-23.)

### Changed

- **Releases sign `checksums.txt` into one `.bundle` instead of a `.sig` plus a
  `.pem`.** cosign 3 deprecated `--output-signature`/`--output-certificate` and
  refuses them outright without `--new-bundle-format`; releases only kept
  working because the installer action was pinned to a version that still ships
  cosign 2. The signing format was therefore one dependency bump away from
  failing a release, at the step that runs after the GitHub Release already
  exists. The bundle also carries the Rekor inclusion proof, so a verifier
  fetches one asset rather than two and can check transparency-log membership
  offline.

  Verifying a release from this version on:

  ```bash
  cosign verify-blob --bundle checksums.txt.bundle \
    --certificate-identity \
      "https://github.com/klarlabs-studio/warden/.github/workflows/release.yml@refs/tags/vX.Y.Z" \
    --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
    checksums.txt
  ```

  v0.28.0 and earlier are unaffected and still verify with `--signature` and
  `--certificate`.

- **cosign and GoReleaser versions are named, not inherited.** The cosign
  installer's default moved from v2 to v3 between its own releases, so which
  cosign signed a release was a fact you had to infer from an action SHA. It is
  now `cosign-release: v3.0.6` in the workflow, and GoReleaser is pinned to an
  exact version rather than `~> v2`.

- **The shared CI bar is pinned to a commit.** It was `@main`, which meant
  anyone able to push to another repository could change this one's gate.
  Dependabot now bumps the actions weekly, so pinning produces reviewable diffs
  rather than rot.

## [0.28.0] - 2026-08-17

A gate should not spend its output on notices nobody can act on, and should not
leave litter in the repositories it runs in. Both of the below were found by
using warden rather than by reading it.

### Added

- **An upgrade re-pins the hook shims itself.** Installing a new warden left
  every armed shim recording the previous version, and the shim reported it on
  every single run until someone ran `warden hooks repin` by hand:

  ```
  warden: hook pins 0.26.0, PATH has 0.27.0 - running 0.27.0
  ```

  The notice described a difference that changed nothing — a warden on PATH runs
  whatever the pin records, so the number was merely stale. A permanent warning
  with no action behind it is how a gate teaches people to read its output past
  rather than at, which is the opposite of what a gate is for.

  A run now re-pins its own shims, before gating, so a run that exits early
  still does it.

  **Only forward.** `warden hooks repin` still moves the pin in either direction,
  because a person asked for it. This decides on its own, and pinning BACKWARD
  would be a real change rather than bookkeeping: a checkout with no warden on
  PATH downloads exactly the pinned version, so a silent backward pin would turn
  one developer running an old binary into a repository that fetches an old
  binary for everyone.

### Fixed

- **Re-pinning could move the pin and say nothing.** It went through `SetHook`,
  which also maintains the hooks selection in `.warden.yaml` — installing the
  shim first and writing the config second. On a repository whose config could
  not be parsed, the pin had already been rewritten when the config write
  failed; `SetHook` returned an error, the caller skipped its `repinned X -> Y`
  line, and the pin moved silently.

  `RepinHook` rewrites the shim and nothing else, which is all a re-pin ever
  meant. It also stops re-pinning from touching a user's config at all, and
  makes `warden hooks repin` work on a repository whose config is broken —
  which is a moment when you would rather the gate still functioned.

- **A killed run left its worktree registration behind forever.** Teardown is a
  deferred `Remove`, and a deferred `Remove` does not run when the process is
  killed — a CI timeout, a cancelled hook, a Ctrl-C. The directory leaks too,
  but it lives under the OS temp dir and is reaped eventually; the registration
  under `.git/worktrees` has no such janitor.

  Found in a repository carrying nineteen of them, every one reported by
  `git worktree list` as prunable, together pinning nineteen detached HEADs so
  `git gc` could not release the objects they reached. Nothing was visibly
  broken, which is why they had accumulated.

  Creating a worktree now sweeps the dead ones first, so recovery is automatic
  rather than a command someone has to know about. Deliberately not
  `git worktree prune`: that drops every entry whose directory is missing,
  including worktrees warden did not create — someone else's, on an unmounted
  volume, absent rather than dead. Warden runs inside other people's
  repositories and collects only its own litter. A worktree still on disk is
  left alone whatever its name, so a concurrent run is never disturbed.

## [0.27.0] - 2026-08-13

Provenance now survives `gh pr merge --squash --delete-branch`, which is the
default merge path on most repositories and previously destroyed it silently. A
field report (#212) from a repo that had gated every commit for months and still
showed 100% UNVERIFIED on its trunk drove all of the below.

### Added

- **Attested commits are anchored, so gc cannot collect the evidence.** A git
  note SURVIVES garbage collection; the commit it annotates does not. Deleting
  the merged branch — the default, and what removing a worktree does too — makes
  the attested commit unreachable, the next gc prunes the object, and the note is
  left dangling over nothing.

  Re-attestation then failed in the least helpful way available: `NotedCommits`
  still listed the sha, `TreeSHA` errored, and the candidate was skipped
  silently, so the squash commit reported "no warden note" and read as though it
  had never been gated. The field report blamed notes staying local; the gate
  already pushes them, so that was never the mechanism.

  Attested commits now get a ref under `refs/warden/attested/`, written at gate
  time and at re-attestation and pushed alongside the notes. `reattest --all`
  backfills anchors for notes written before this existed — worth running once on
  an older repository, before its next gc. Commits already collected are skipped;
  there is nothing left to save.

- **Re-attestation carries the original attestation, so CI needs no signing
  key.** `reattest` re-signed with the LOCAL key. That is invisible on a
  developer machine, where the local key is usually the key that signed the
  original, and fatal on a runner, where it is ephemeral: notes came out "signed
  by untrusted key" and the repository's own roster correctly rejected them. A
  merge-time repair job would have needed a provenance-signing key as a CI
  secret.

  Copying the source signature is not available — it covers a payload containing
  `CommitSHA`, so a copied note would verify while attesting the WRONG commit,
  and commit binding is exactly what stops a signed note being transplanted.

  Instead a re-attestation carries the original record whole in
  `carried_original`, and a verifier judges it on facts it can check itself: the
  carried record is validly signed by a roster key and attests the source, and
  the two commits have IDENTICAL TREES, read from git objects rather than claimed
  by the note. The re-attester contributes only a pointer, and a wrong pointer
  fails the tree comparison — so it needs no trust and no key.

  `carried_original` is deliberately OUTSIDE the signing payload. Any new field
  changes the bytes every version signs over, so a warden that predates it drops
  the field, re-marshals, and reports "signature does not verify" — which reads
  as tampering rather than version skew. Excluded, an older verifier sees the
  payload it always saw.

- **`warden-reattest` action** for `pull_request: [closed]` — the only moment the
  squash commit exists and the branch has not been deleted, which is what makes
  the repair automatic instead of something to remember. Needs no signing key,
  per the above. Warns when the repository pins no `trusted_keys`, since a runner
  recognizes a validated source only by its signer.

- **`warden-doctor` action** audits the branch itself. `warden-verify` checks one
  commit — normally the PR head, which still carries its note and passes — so it
  could stay green permanently while trunk provenance was zero. `warden-verify`
  now also states on every PR what it does not cover.

- **`warden doctor --ci`** exits 3 on drift. Drift and a doctor that could not run
  were both exit 1, so an unadopted repository or a shallow clone would have been
  reported as tidy, actionable drift — the same "looks like coverage" failure the
  new action exists to fix. The flag carries the new code so gating on
  `warden doctor` exiting 1 keeps working.

- **`warden reattest --all --dry-run`.** Omitting `--push` was not a dry run and
  was widely read as one: it still wrote local notes, and the field report watched
  its note count climb from 222 to 301 during what it took to be a preview. The
  plan is produced by the sweep's own rules, so it is the run's decision made
  twice minus the writing. The sweep also prints a line per commit — ~94 commits
  produced no output for over ten minutes, which is indistinguishable from a hang.

- **`warden hooks repin`.** `warden status` reported pin drift and named
  `warden hooks enable <hook>` as the fix. That works and reads wrong: "enable"
  describes arming, so the remedy for a stale pin looked like it would change
  whether the hook runs. `repin` rewrites armed hooks and skips disabled ones.

- **`warden status` reports which provenance mode the repository is in.** With no
  `trusted_keys` a note proves "a warden ran here", not "a warden I trust ran
  here" — a distinction that took reading the verify action's YAML to discover,
  while hooks reported armed and the repository looked healthy. The line also says
  whether this machine's key is on the roster: a repository can enforce a roster
  this machine is not on, in which case the gate still runs but verify will reject
  its notes.

### Fixed

- **An untrusted note no longer squats a commit.** Re-attestation stopped at
  `Attests()` — evidence, chain, and commit binding — which says nothing about WHO
  signed, so anything able to write a note could permanently block a trusted one.
  The only repair was removing the note and force-pushing the shared notes ref,
  which a protected repository may refuse.

  The asymmetry was the tell: `treeEqualSource` already refused to copy FROM an
  untrusted note while this path refused to REPLACE one, so warden was stricter
  about what it carried than about what it defended. Whether the repository
  enforces at all is decided by the configured roster, so a repository that pins
  no `trusted_keys` is unaffected.

## [0.26.0] - 2026-08-10

### Added

- **Dependency drift is recorded in the provenance note.** `RunRecord` gains
  `dependencies_drifted`, populated on a pre-push run when the installed
  packages did not match the commit's lockfile.

  `dependencies` digests the lockfiles the **commit** carries. Because warden
  exposes `node_modules` from the live checkout rather than reinstalling, those
  digests were never a claim that the run resolved against them — a caveat that
  lived only in a source comment, where no verifier could read it. A signed
  note showing digested lockfiles and nothing else overclaims by omission.

  A terminal warning would not have fixed that: terminal output is ephemeral,
  and the note is the durable artifact warden exists to produce. The field sits
  inside the evidence chain and the signature, so the attestation says what it
  actually knows (#204).

  Detection is best-effort and the record does not pretend otherwise: an absent
  field means nothing was detected, not that the install was verified. yarn and
  pnpm write no equivalent manifest, and a never-installed tree reports
  nothing.

## [0.25.0] - 2026-08-10

### Added

- **Dependency-drift detection.** Warden exposes `node_modules` from the live
  checkout rather than reinstalling — a per-run `npm ci` is the dominant cost
  on a large JS repo — so the tracked tree comes from the commit and the
  dependency tree comes from the machine. After a branch switch without a
  reinstall, a local `npm link`, or a half-applied upgrade, the checks run
  against dependencies the commit does not specify, and CI can legitimately
  disagree. The README said "warden cannot currently detect this"; it can now.

  `git.DetectDepDrift` compares each committed lockfile against npm's
  `node_modules/.package-lock.json`, the manifest of what is actually
  installed — two file reads and a map diff per lockfile, no install and no
  network.

  Best-effort by construction: no lockfile, no install, or a package manager
  that writes no hidden lockfile reports nothing rather than a false alarm.
  Silence means "nothing detected", not "verified clean". Not yet surfaced in
  run output — where the warning belongs is a product decision tracked in
  #204 (#210).

## [0.24.4] - 2026-08-08

### Changed

- **`go.klarlabs.de/statekit` v1.12.0 → v1.13.2.** v1.13.0 regressed
  `viz.ParseNativeJSON` — statekit's own XState exporter collapses a group of
  one eventless transition to a JSON object, and the parser accepted only an
  array, so statekit emitted output statekit could not read. v1.13.1 fixed
  that; v1.13.2 additionally removed a 12 MB darwin/arm64 executable that had
  been committed at statekit's module root since June and shipped in every
  release through v1.13.1, taking the module zip from 12.6 MB to 6.2 MB.
  warden does not import `statekit/viz`, so the regression was never
  reachable here — the download size is the reason this lands on v1.13.2
  (#209).
- **`golang.org/x/crypto` promoted to a direct requirement.** warden imports
  it directly, so `go mod tidy` on main was not a no-op; `golang.org/x/term`
  is a transitive of it that `go.sum` was missing (#202).

## [0.24.3] - 2026-08-08

### Changed

- **`cli` coverage: 81.3% → 83.0%.** `.coverctl.yaml` exempts `internal/cli`
  from the per-file floor for a sound reason — a command entry point blocks on
  stdio or a server loop, so a per-file rule there would measure how testable a
  `main()` is — with the consequence that `cli` is enforced at the domain level
  only, and it was the domain closest to its 80% floor while being where every
  new command lands. `why`, `attach` and `reattest --all` are now covered
  through their real output rather than left to the domain average: the trace
  notarization line's three states, the base-policy and unsigned wordings, the
  no-live-run answer, the sweep that actually writes, and reattest's usage
  errors. The exemption is unchanged (#194).

### Fixed

- **The gate no longer aborts with `.git/index: Not a directory` when it runs
  from a git hook.** Git exports `GIT_INDEX_FILE`/`GIT_DIR` to hook processes,
  usually as paths relative to the live checkout. The `rebase` step's git
  subprocesses (and the coding-agent step's shell) did not scrub them, so with
  their working directory inside the disposable worktree — where `.git` is a
  gitfile, not a directory — `.git/index` resolved *through* that file and git
  died with ENOTDIR. Every `git commit` and `git push` in an affected repo was
  blocked before `vet`/`test` ever ran, and because `warden run pre-commit`
  carries no such variables the failure was invisible to manual testing.
  Warden's own git wrapper and the shell steps already scrubbed these
  (`git.ScrubHookEnv`); the fix routes the two remaining subprocess sites
  through the same baseline (#205).

### Documentation

- **The "runs clean" claim now carries its dependency caveat.** The README's
  first Makefile-comparison bullet said every check runs in a worktree "seeded
  from the commit, so passing in warden means passing in CI — reproducibly".
  The *tracked* tree is seeded from the commit; gitignored dependency
  directories are exposed from the live checkout instead, deliberately, because
  a per-run reinstall is the dominant cost on a large JS repo. A `node_modules`
  that has drifted from the committed lockfile is therefore what the checks see,
  and CI installing fresh can legitimately disagree — the exact failure the
  sentence promised to eliminate.
  The bullet is now narrowed to the tracked tree, a new
  *Dependencies come from your checkout* section states the trade and how to
  rule it out, and `RunRecord.Dependencies` says in the type what it does and
  does not attest. Also noted there: hardlink exposure shares inodes, so a tool
  that rewrites a dependency file in place writes through to the live checkout
  (#204).

## [0.24.2] — 2026-08-03

### Fixed

- **`warden doctor` no longer exits 1 on a correctly gated history.** `Counts`
  re-derived its classification from `HasNote` rather than from the commit-state
  partition, so every note-less commit landed in `unverified` — including the
  states the partition exists to keep out of it. A span-covered commit (every
  commit below the tip of a gated multi-commit push) printed
  `✓ (covered by the gated push …)` and was counted unverified one function
  later, so an ordinary three-commit PR printed all ✓ and exited 1. A branch
  with no remote-tracking ref printed `These are not bypasses` directly above
  the exit code that failed on it.

  `Counts` now returns a `Tally` whose buckets switch on the same predicates as
  the partition: `Verified`, `Covered`, `Unverified` and `Unknown` account for
  every commit exactly once, with `Defective` a subset of `Unverified`. The two
  new buckets are surfaced in the summary line, the audit JSON (`covered`,
  `unknown`), the axi output and the MCP audit shape. `Unverified` still covers
  a defective note, a squash-merge binding gap and a real bypass, so the gate is
  aimed rather than loosened.

- **A gated push that could not write its note now says so.** The write error
  was only consulted under `--attest-only`; in the gate path it was swallowed
  entirely. Worse, because signing runs first, such a run reported
  `provenance note written UNSIGNED: …` — asserting a note had been written when
  none was. `git notes add` fails without a committer identity, which is the
  same root cause fixed for `--attest-only` in #183. The write stays best-effort
  (the push has already happened) but is no longer silent, and the unsigned
  warning is suppressed when nothing was written.

## [0.24.1] — 2026-08-02

### Fixed

- **A corrupt provenance note is no longer reported as a missing one.**
  `verdictFor` collapsed a read error into the absent case, so a note that
  existed but would not decode was reported as `no warden note (pushed with
  --no-verify, or made outside warden)`. Both causes named there are wrong for a
  malformed note, and the message sent the reader to hook configuration while
  the actual remedy is to restore the blob — re-committing through the gate does
  not touch it. `ReadNote` already distinguished the two; the distinction was
  discarded one line later.

  Adds the `unreadable` verify reason, whose hint names the real cause and the
  real fix. Encountered in practice after `git notes merge -s cat_sort_uniq`
  concatenated two records for one commit, doubling the blob and breaking JSON
  parsing. (#195, #196)

### Changed

- Two doc comments in `internal/domain/externalrun.go` pointed at
  `service.VerifyPolicy`, which does not exist. The type is
  `service.ExternalPolicy`. That file defines the weaker-claim boundary and its
  comments are where a reader learns that `verify` refuses external attestations
  by default, so the pointer being wrong mattered more than its size. (#193)

## [0.24.0] — 2026-08-02

### Added

- **`warden attest-external` records an existing CI run as the attestation.**
  A post-merge job can now attest the merged commit by naming the run that
  already did the work, instead of re-running the same checks a second time on
  the same tree:

  ```sh
  warden attest-external --checks lint,test --push
  ```

  It is a separate command rather than a flag on `run`. ADR 0003 sketched
  `run --attest-external`, but `run` means "run the hook pipeline", and a flag
  on it meaning "run nothing" would put the strong claim and the weak one on a
  single code path. Keeping the two impossible to confuse is the design.

  Detection copies only what the platform states about itself — run id, attempt,
  repository, url. `--checks` is **required and never inferred**: every other
  field is a fact warden observed, while what passed is a claim about work
  warden did not do, and warden does not manufacture that claim on the
  operator's behalf. (#189, ADR 0003 Phase 1; read side landed in #185)

### Changed

- **`AGENTS.md` is tracked instead of excluded per-clone.** It sat in
  `.git/info/exclude` — a local rule — so it had never been committed and
  existed on exactly one machine. Its contents are project knowledge rather
  than personal preference: the provenance invariants, the failure modes that
  cost hours to rediscover, the exit-code table, the notes-race recovery. A
  fresh clone got none of it. Rewritten in the same pass, having drifted far
  enough to mislead — it claimed v0.12.0 against a shipped v0.23.2, and pointed
  at `/brief` and `/capture` skills removed on 2026-07-21. (#190)

## [0.23.2] — 2026-08-02

### Fixed

- **A losing notes push is reconciled instead of dropped.** Since 0.22.0 two
  things write `refs/notes/warden`: a developer's machine on every gated push,
  and CI on every merge to `main`. `PushNotes` is a plain non-forced push, so the
  second writer was rejected non-fast-forward — and the caller discarded the
  error.

  The note then existed on exactly one machine. The commit verified there and
  read as an **ungated bypass everywhere else**, including in the CI gate, which
  accused the author of a bypass that never happened. That is not hypothetical:
  it failed this repo's own PRs #185 and #187, and the accusation was wrong both
  times.

  Notes are per-object, so the ordinary case — one machine notes its commit, CI
  notes a different one — is a clean union. `PushNotes` now fetches, merges and
  retries once. A genuine conflict (two different records for the SAME commit) is
  **reported, not resolved**: auto-resolving would silently discard one side's
  record of a run that actually happened. The uncontended push is unchanged and
  does no extra fetch.

  **Upgrade to get the fix.** The binary your git hooks call is the one that
  writes notes, so a repo still running an older warden keeps losing them.

- **A note that reaches no remote now says so.** Publication stays best-effort in
  the gate path — a failed note must not block a developer whose push already
  happened (§9) — but it is no longer silent:

  ```
  provenance note written locally but NOT published: …
  This commit will read as ungated to everyone else until the note reaches the remote.
  ```

  Staying quiet about that was survivable while there was one writer.

## [0.23.1] — 2026-08-02

### Fixed

- **A `--attest-only` run that pushes nothing no longer exits "warden pushed".**
  The post-merge gate ran green in CI — all five steps against the merged tree —
  and the step still failed:

  ```
  warden: pushed ec0667e2aec1 to origin/main; local branch fast-forwarded
  ##[error]Process completed with exit code 3
  ```

  Both halves are false. `--attest-only` pushes nothing, and that SHA is a commit
  **GitHub** created on merge, which is the one case the mode exists for. Exit 3
  means "passed; warden performed the push, git must stand down", so the CI step
  failed, the publish step never ran, and the commit stayed unattested after a
  gate that passed.

  A passing attest-only run now exits 0 — there is no stale push to guard
  against — and reports what it did: `attested <sha> (--attest-only: nothing
  pushed)`.

  The two end-to-end tests around this assert the EFFECT (the branch does not
  move; the commit stops counting as a bypass) and both passed throughout,
  including on the run that failed in production. Neither asserted the interface
  a caller keys on. The new one does, and was verified to reproduce exit 3 with
  only the fix reverted.

- **`provenance-main.yml` installs the toolchain warden's own steps need.**
  Installing warden was not enough: `pre_push` is
  `[credentials, rebase, lint, security-scan, test]`, so on a bare runner `lint`
  reported `step/missing-toolchain` and warden refused to attest a step that
  never ran — the Blocker model working as designed. Go now comes from `go.mod`,
  with `golangci-lint` and `nox` pinned alongside a note that the canonical pins
  live centrally (#112). The nox install needs `GOTOOLCHAIN=auto`, scoped to that
  step: nox requires a newer Go than warden targets, and `setup-go` pins
  `GOTOOLCHAIN=local`.

  This duplicates the shared CI's work on every merge to `main`. That cost buys
  an attestation that re-ran the checks against the merged tree rather than
  taking another workflow's word for it; removing it needs warden to attest an
  EXTERNAL run, tracked in #177.

## [0.23.0] — 2026-08-02

**A version-number correction, and the CI signer.**

0.22.1 shipped a **breaking change as a patch**: it renamed `intact` to
`defective` in the audit JSON, the MCP `AuditOutput` and the axi payload, and
inverted what the value counts — the field now tallies the failures, not the
successes. `audit --format=json`'s per-commit `validated` changed meaning in the
same release, from "a note exists" to "the note attests this commit".

A consumer reading `intact` gets a silent break on what its version number
advertises as a routine patch. The code was right; the number was not. 0.23.0
carries the same tree so the semver signal matches what actually changed —
**if you pin warden's JSON output, read the 0.22.1 entry below, not this one.**

### Added

- **The CI signer joined `.warden.yaml`'s trusted roster** — the one genuine
  change since 0.22.1. `provenance-main.yml` shipped in 0.22.0 but refused to
  attest without `WARDEN_SIGNING_KEY`, on purpose: warden's signer mints a
  throwaway keypair when it finds none, so an unguarded run would have written
  notes signed by a signer that dies with the runner — commits reading as
  attested while trusted by nobody.

  The key is deliberately a dedicated one rather than a developer's. Had CI
  signed as the primary dev key, `doctor` could no longer distinguish "a human
  ran the checks locally" from "a runner attested the merge result", and those
  are different claims with different trust properties.

  It does not backfill: only commits merged from here on are attested. The
  existing unverified history stays as it is, which is correct — nothing ran
  checks on those trees in CI.

## [0.22.1] — 2026-08-02

Three fixes to commands that answered confidently and wrongly.

### Fixed

- **`warden mcp serve` no longer dies when it starts outside a repository.** It
  exited 1 before speaking a word of MCP, which over stdio reaches the user as
  "server exited" or "failed to connect" — the reason went to a stderr stream
  most clients discard, so the one piece of information needed was the one
  piece not visible. The working directory is the client's choice here, not the
  user's: it is whatever the editor or agent launched with, which makes landing
  outside a repository a likely first run rather than an unusual mistake.

  The handshake now completes, the tools list, and every call returns a
  sentence naming the directory and saying how to fix it. Nothing is pretended
  to work — each call fails, with `isError` set.

  Reaching the client needed the existing `errVisible` seam: the dispatcher
  flattens a raw handler error to a bare "internal error" before it leaves the
  process, which is right for a failure that might leak internals and wrong for
  a refusal the caller is meant to resolve. Without it the explanation landed
  only in the server's own log, where nobody looks. That was caught by driving
  a real MCP handshake against the built binary rather than by reading the code.

  `git.ErrNotARepository` is new, so the degraded surface is chosen by matching
  a sentinel rather than a message. Startup failures that are not "there is no
  repository here" still exit, because relaunching elsewhere would not fix them.

- **"verified" now means the note attests the commit, not merely that one
  exists.** `Counts()` reported every commit carrying a note as verified, with
  the failures relegated to a parenthetical. So a commit whose note does not
  attest it — one `warden verify` refuses outright — was counted as verified in
  the summary line, in `warden audit --format=json`'s `validated` field, and in
  the MCP/axi payloads an agent reads.

  That is the tool contradicting itself about the same commit, on the line most
  readers stop at. On this repo it was 7 of 88.

  `Counts()` now returns `(verified, defective, unverified)`, where unverified
  deliberately INCLUDES defective — `doctor` gates its exit code on it, and a
  repo whose notes no longer attest anything is exactly a repo that should be
  flagged. **Breaking for consumers:** the `intact` field is now `defective` in
  the audit JSON, the MCP `AuditOutput`, and the axi payload; it counts the
  failures rather than the successes, so a field that kept the old name would
  invert its own meaning. `audit --format=json`'s per-commit `validated` is now
  the attestation result rather than "has a note".

- **`fleet status` printed a summary that did not add up.** The new `Defective`
  bucket was tallied in the JSON but never rendered, so the human line accounted
  for 124 of 131 commits — the same shape as the dropped-bucket bug in 0.21.2,
  in the other direction.

  `TestGoldenFleet_BucketsAccountForEveryCommit` could not catch it: it reads the
  JSON report, which balanced. A new e2e now parses the RENDERED line and sums
  it, because that is the artifact people actually read.

## [0.22.0] — 2026-08-02

A minor rather than a patch, for one reason: **`warden verify <sha>` now exits 2
instead of silently verifying HEAD.** That was never intended behaviour, but it
was reachable, and anything scripted against it changes meaning — see the first
entry under Fixed.

The rest continues 0.21.2's theme. That release corrected what the bypass rate
*counted*; this one corrects what warden *claims*. A branch that was never pushed
is not a bypass, a rebased note is not tampering, and a commit the forge created
can now be attested rather than merely accused. On the fleet this was measured
against the rate went **26.6% → 3.9%**, and inspecting what survived showed every
one of the eleven remaining "bypasses" was committed by GitHub, not by a person
evading anything. `--attest-only` exists to close exactly that.

### Added

- **`warden run pre-push --attest-only`, and a post-merge CI workflow using it.**
  warden's gate is client-side pre-push, so it can only note commits it pushed.
  Anything the forge creates on its own — a GitHub squash-merge, a web edit, a
  merged Dependabot PR — is a new commit object warden never saw, and it lands on
  the default branch carrying no proof at all.

  Not hypothetical: on the fleet this was measured against, **every one of the
  eleven remaining "bypassed" commits across three repositories was committed by
  GitHub**, not by a person evading anything. The pre-push gate was never in that
  path and could not have been.

  `--attest-only` runs the configured steps against the merged tree and writes the
  note, but does **not** move or push the branch — pushing from CI would race the
  next human push and fail on a stale ref, and the branch is already published by
  the trigger. It refuses outright if a step rewrites the tree: the note binds to
  HEAD, so attesting a rewritten tree would claim the checks passed on a tree they
  never saw.

  `.github/workflows/provenance-main.yml` wires it to `push: main`. It skips
  commits that already carry a note (so it fires only for the forge-created gap),
  serialises against itself, and **refuses to attest when `WARDEN_SIGNING_KEY` is
  unset** — warden's signer mints a fresh keypair when it finds no key, so an
  unguarded run would write notes signed by a throwaway signer that dies with the
  runner. Those commits would read as attested while being signed by nobody
  trusted, which manufactures provenance rather than recording it.

  This is the SLSA model: the attestation of record is produced by CI, against the
  exact tree that landed, on infrastructure the author does not control. The
  long-lived signing secret is the weak part and is named as such — keyless OIDC
  (sigstore/Fulcio) is the right answer, still tracked as ADR-0002 Phase 2.5.

### Fixed

- **Commands no longer silently answer a question other than the one asked.**
  Every flag-parsing command discarded its leftover positional arguments, so
  `warden verify <sha>` parsed cleanly, verified **HEAD** instead, and printed

  ```
  validated a80e2707237a (…, chain-intact, signed by trusted 139e6eb9e261)
  ```

  for a commit the caller never named. That is worse than an error: the answer
  looks authoritative, it is about a different commit, and nothing in the output
  says so. It was found only by noticing that the SHA in the reply did not match
  the SHA in the request.

  All fourteen such commands now refuse an unexpected argument and name the flag
  the caller most likely meant (``did you mean `--commit <sha>`?``). `fleet
  status` is the deliberate exception — its positional arguments are the
  repositories to survey. `attest` already refused an unknown `--predicate` on
  exactly this reasoning; this extends the rule from flag values to arguments.

- **"TAMPERED" is no longer printed for two innocent causes.** It was rendered
  from a single boolean that folds three distinct failures together: no evidence
  recorded, a broken hash chain, and a note that is internally sound but
  describes a *different* commit. Only the middle one suggests tampering; the
  last is what a rebase or a squash routinely leaves behind.

  Each is now named for what it is — `TAMPERED (evidence chain broken)`,
  `UNBOUND (note describes another commit — history was rewritten)`,
  `NO EVIDENCE (note records no steps)`. On the repo this was found in, **all
  seven commits warden was calling TAMPERED were UNBOUND, and none had been
  tampered with.**

  The gate is unchanged: all three still fail `Attests`, and `verify` still
  refuses them. Only the accusation changed. The glyph went with it — those lines
  printed `✓ … TAMPERED`, which reads as a pass at a glance, and a glance is all
  most of them get.

- **A branch that was never pushed is no longer reported as bypassed.** warden's
  note is written by the PRE-PUSH gate. A branch with no remote-tracking ref has
  never reached it, so its commits carry no note for a reason that has nothing to
  do with anyone routing around anything — but `doctor` fell back to walking the
  local branch, found no notes, and called every commit a bypass.

  This is the same unevidenced accusation as the pre-span case fixed in 0.21.2,
  with a different missing signal: the absence of a note is evidence of a bypass
  only once warden had both the ABILITY and the OPPORTUNITY to write one. Such
  commits are now reported as **unpushed**, counted as neither verified nor
  bypassed.

  Measured on the fleet this was written against, it was 61 of 74 reported
  bypasses — one local-only repo, renamed away weeks earlier, with no remote at
  all — taking the rate from 26.6% to **4.7%**. A second repo moved from
  *unattributable* to *unpushed*, which is the more precise reading: "never
  pushed" is a definite fact about the branch, where pre-span provenance is an
  ambiguity, so it takes precedence.

  `Unpushed` is the sixth commit state, and the exhaustive partition test added
  in 0.21.2 did exactly what its comment promised — it failed until the new state
  was ordered against the other five, rather than letting it silently skew a
  number someone acts on.

- **The golden fleet no longer assumes every repo has a remote.** `newGoldenRepo`
  built a bare remote for every fixture, so the suite structurally could not have
  caught the bug above — the helper encoded an assumption instead of testing it.
  A no-remote fixture now covers that shape.

## [0.21.2] — 2026-08-01

Three corrections to one number: the bypass rate `doctor` and `fleet status`
report. On 0.21.0 and 0.21.1 it was wrong in both directions at once —
**inflated**, by calling gaps bypasses that warden had no evidence to call
anything, and **lossy**, by dropping a whole category of commit out of the
tally. Anyone acting on that number should re-run against this release.

### Added

- **A golden-fleet end-to-end suite.** Every provenance-classification bug this
  project has shipped had the same shape — the unit tests asserted the code did
  what its author intended, and the intent was what was wrong. Tests written from
  the same mental model as the code cannot catch a wrong mental model; what
  caught the earlier ones was running warden across real repositories and
  noticing a number that could not be true.

  The suite builds repositories to a KNOWN provenance shape — a multi-commit
  push, a real `--no-verify`, a never-gated history, a fully gated one — so the
  correct classification is known independently of what warden computes. It also
  asserts the buckets sum to the commit count, which is the invariant that
  catches a state added to the domain and forgotten in a report.

  It found the dropped-bucket bug below on its first run.

### Fixed

- **`fleet status` silently dropped span-covered commits.** The domain
  distinguishes five states; the rollup enumerated four. A commit a gated push
  published — no note of its own, vouched for by the span — fell into no bucket
  at all, so the buckets stopped accounting for the commits. On the fleet this
  was measured against, two commits were vanishing.

  Found on the first run of the new golden-fleet suite, which is exactly what it
  was built for: the type-level partition test could not catch it, because the
  hole was in the CONSUMER that has to enumerate every partition.

- **The commit states now partition.** `Covered`, `Reattestable`,
  `Unattributable` and `Bypassed` were each added separately as an independent
  predicate, and nothing forced them to be mutually exclusive — a commit both
  span-covered and tree-identical to a validated one satisfied two at once, and
  the fleet rollup sums the buckets, so it was counted twice. `markReattestable`
  no longer treats an already-covered commit as a gap needing repair, and a new
  exhaustive test asserts exactly one state holds across every combination of
  the underlying fields.

  This is the guard the three preceding state additions did not have. It is also
  what makes a fifth state safe to add: the test fails until the new one is made
  exclusive, rather than silently skewing a number someone acts on.

- **A gap is now only called a bypass when warden could have proved otherwise.**
  warden validates ONE tree per run, so the intermediate commits of a
  multi-commit push get no note — the span vouches for them. Spans arrived in
  v0.19.0. Beside an older note, an intermediate commit of a gated push and a
  real `--no-verify` are indistinguishable, because the distinguishing
  information was never recorded.

  `doctor` and `fleet status` called all of them bypasses. They are now reported
  as **unattributable** and counted as neither verified nor bypassed. On the
  fleet this was measured against, the bypass rate went from 52.6% to 27.0%, and
  one repo from 65.5% to 5.5% — turning a number that looked like systemic
  neglect into one that correctly identifies the single repo with a real problem.

  An unreadable or absent warden version is treated as pre-span: reporting a
  commit as unattributable when it might have been a bypass is a smaller error
  than accusing a perfectly gated push of going round the gate.

## [0.21.1] — 2026-08-01

### Fixed

- **`doctor` now honors a gated push span**, and so does `fleet status`. warden
  validates ONE tree per run — the tip's — so it deliberately writes no note for
  the intermediate commits of a multi-commit push, recording the span instead.
  `verify --range` has read that back since #86; `doctor` never did. A perfectly
  ordinary commit-commit-commit-push therefore reported its earlier commits as
  UNVERIFIED forever, and `fleet status` counted them as BYPASSED — inflating the
  one number that is supposed to trigger an intervention.

  Found by chasing a repo reporting 65.5% bypass whose gated and "bypassed"
  commits interleaved **ten seconds apart**; the un-noted one was the direct
  parent of the noted one. One push, two commits, one tree validated.

  `CommitStatus` now separates the three ways a commit can lack a note —
  `Covered()` (a gated push published it), `Reattestable()` (a squash-merge
  unbound it), and `Bypassed()` (it really did go round the gate) — and only the
  last is counted. A covering note must itself attest its own commit, exactly as
  in the range gate, so a span is never a cheaper path to "verified" than a note.

### Changed

- **The README leads with what warden proves, not what it is.** It opened with
  "a configurable git commit/push gate", which is accurate and reads as
  husky-with-extras — the category, not the claim. The differentiated thing
  (a signed, hash-chained note that lets CI skip re-running checks, and makes an
  agent's own attribution record tamper-evident) was buried below the fold.

  Now it opens on the three questions warden can answer about a commit *and
  prove to someone who was not there*, adds a section positioning gittuf,
  sigstore, SLSA and Agent Trace as things warden composes with rather than
  competes against, and states plainly why warden does not claim
  `slsa.dev/provenance`. Every claim in the new text was checked against shipped
  code before it went in.

### Removed

- **Six dead nox baseline entries.** Five were the SEC-163 false positive on Go
  method calls, baselined in #158 when suppression was the only option; the rule
  was fixed upstream the same day (nox-hq/nox#432, released in 1.25.1) and the
  fleet moved to it, so the suppressions match nothing. The sixth is a DATA-001
  on a fixture email that 1.25.0 stopped reporting. Pruned rather than left: a
  suppression for a finding that no longer exists is indistinguishable, to the
  next reader, from an accepted risk.

## [0.21.0] — 2026-08-01

Provenance the rest of your tooling can read: agents can interrogate the gate,
notes can be signed with an SSH key, and a fleet-wide view says how much of it
is actually enforced. Plus a security fix to the installers and a round of
coverage work on the things the numbers were hiding.

### Added

- **Warden's provenance is now readable by agents** (#144). The MCP/axi surface
  was three working tools against eighteen CLI commands, so an agent could
  *execute* the gate but never *interrogate* it — the single most useful question
  warden answers, "is this commit validated?", had no agent-facing answer at all.
  `verify`, `verify_range`, `doctor`, `audit` and `status` are now exposed on both
  surfaces, marked read-only, and need no trust opt-in: that checkpoint exists to
  stop arbitrary shell from an untrusted `.warden.yaml`, and reading a note cannot
  do that. No new service logic was required — every operation already existed;
  only the adapter was missing.

- **A per-repository trust grant** (#144), `warden trust add|list|remove`.
  `WARDEN_MCP_ALLOW_RUN=1` authorized a *process*, not a repository, so an MCP
  server trusted for one checkout stayed trusted for every repo it was later
  pointed at — including one cloned minutes afterwards. The same fix git made
  with `safe.directory`. The env var remains for containers and CI, where there
  is no persistent config dir and the workspace is disposable.

- **Findings carry their remediation** (#146). `domain.Finding` and
  `stepsdk.Finding` gain `Rule`, `Why` and `Fix{Command,Patch}`, all optional and
  omitted when unset. A failed gate becomes read → fix → re-run instead of
  guess → re-run. Populated where warden already knew the answer and only said it
  in prose: the install command for a missing toolchain, `--allow-parallel-runners`
  for lock contention, the `timeouts:` key. Fixes are advisory — a step cannot
  escalate itself into a tree write by attaching a patch.

- **Runs are asynchronous over MCP** (#149). `run_trigger` returns immediately
  with a `run_id`; `run_status` reports finished steps and then the verdict.
  A five-minute test step was a five-minute silent tool call that most clients
  timed out on. Measured: `run_trigger` returns in 0.03s against a step that
  sleeps 2s. `phase: complete` means the run finished, not that the gate passed;
  a pipeline that could not run at all reports `errored`, which is a different
  thing from a rejected change.

- **`warden fleet status`** (#150) — gate coverage and **bypass rate** across many
  repositories. A gate that is routinely bypassed protects nothing *and* removes
  the signal that it ever ran, and `doctor` answers that one repo at a time, which
  is exactly the granularity at which fleet-wide drift stays invisible. Bypassed
  deliberately excludes squash-merge gaps (reported as reattestable) — counting
  them would inflate the number that is supposed to trigger an intervention.

- **`warden attest --predicate vsa`** (#147) emits a SLSA Verification Summary
  Attestation. A warden note already says "a verifier ran a policy against this
  and here is the result", which is what a VSA says — one layer earlier than
  SLSA's artifact-level definition. Verified levels are warden-namespaced on
  purpose: warden does not produce a SLSA build level and will never claim one.

- **Signing with an SSH key** (#155), `signing.signer: ssh`. Warden's own key is
  per-machine and means nothing to anyone else, so a `trusted_keys` roster had to
  be hand-maintained with no way to check an entry against any identity system.
  An SSH key is one a forge already publishes and can revoke, and `ssh_key`
  defaults to git's `user.signingkey` — so a repo already signing commits needs no
  warden-specific configuration. Signatures are namespaced `warden-provenance`, so
  one can never be replayed as a git commit signature or the reverse.

- **Agent Trace notarization** (#156), `agent_trace.path`. Every Agent Trace
  implementation is a self-report by the agent that wrote the code. Warden
  notarizes instead: it hashes the record at gate time and binds that digest into
  the signed note, so rewriting a `contributor: ai` range as `human` afterwards is
  detectable. Deliberately not schema-validated beyond the fields that identify a
  trace — the RFC is a draft and will move, and failing gates over a spec change
  would be worse than notarizing a record warden does not fully understand.

- **`signing.required`** (#151) refuses to write an unsigned note instead of
  degrading silently. Signing was already optional by accident — three paths left
  a note unsigned and none of them said so, and a repo could accumulate unsigned
  notes for months before a CI `--require-signed` started failing. The degrade is
  now announced with its reason whatever the setting.

- **`warden import` reads `.pre-commit-config.yaml`** (#144), the largest
  installed base warden migrates from. It maps to `pre-commit run` rather than
  reconstructing each hook's command: those hooks execute in environments
  pre-commit provisions, so a "faithfully reconstructed" command would name a
  binary that is not on PATH.

- **Build caches survive the worktree** (#153). A worktree holds tracked files
  only, so a compiled language rebuilt from scratch on every gated push. Measured
  on a six-crate Rust workspace, the real gate went from 86s to 4s. It is a
  redirection, not a copy: a compiler writes to its cache and hardlinks share
  inodes, so copying one could corrupt the developer's live cache.

- **`internal/tui` and `stepsdk` are now enforced by a coverage minimum**
  (#157). Neither belonged to any domain, so no minimum applied to either — the
  gate reported "all 7 domains pass" while 204 statements sat outside every
  policy. Both were healthy (83.0% and 86.4%) but nothing held them there, and
  `stepsdk` is the public SDK third-party step binaries compile against.

  Found by the unmatched-directory warning added in coverctl#127, on its first
  run. `main.go` is excluded rather than given a domain: two statements handing
  off to `cli.Run` belong to no domain by design, and excluding it keeps a
  future warning meaningful instead of routine.

- **A per-file coverage floor the domain average cannot hide** (#154). The
  domain minimums are the quality bar; this is the smoke alarm for the failure
  they structurally cannot see. `internal/infrastructure/kernel/subprocess.go`
  sat at 0% — every one of its six functions — inside an `infrastructure`
  domain reporting 87%, and `internal/infrastructure/forge/gh.go` did the same
  before it. Both passed the gate for months.

  Demonstrated rather than asserted: zeroing a small file's coverage leaves
  `service` at 83.2% PASS and the whole check exiting **0** without the floor,
  and exiting **1** with it. 71 files are governed at a 50% minimum, the lowest
  currently at 61.5%.

  `internal/cli` and `internal/mcp` are deliberately excluded. Their command
  entry points block on stdio or a server loop (`cmd_mcp.go` is 23%), so a
  per-file floor there would measure how testable a `main()` is rather than
  whether the logic is tested — and coverctl's `exclude:` would have dropped
  them from domain coverage too, inflating it. Their 80% domain minimum still
  applies.

- **The shipped installers are now covered by the Go suite** (#148),
  `scripts/version_guard_test.go`. It executes the real scripts — a test
  asserting a copy of the regex against itself would pass forever while the
  shipped script drifted, which is how this class survived. `pwsh` is
  preinstalled on GitHub-hosted runners, so `install.ps1` is exercised in CI
  despite CI being ubuntu-only; locally it skips unless PowerShell is present,
  which is precisely why nothing in the repo had ever run it.

  That skip is a convenience locally and a *failure* under `CI`. `install.ps1`
  is the one installer no other job can execute, so a silent skip there would
  leave its guard permanently unverified while the suite still reported green —
  the same shape of hole that let the flaw survive.

- **The `gh` forge adapter is covered end to end** (#142), 34.7% → 100%. Its
  untested paths were the ones that matter: `gh pr view` can exit 0 while
  describing no PR, an empty base must omit `--base` rather than pass it empty,
  a PR comment falls back to a fresh post when there is none to edit, and
  `gh pr checks` exits non-zero precisely when checks fail or are pending — so
  its JSON is read regardless of exit code.

### Changed

- **A push warden performed itself now exits `3`, not `1`** (#144). That case is a
  *success* — the commits are on the remote — and it shared an exit code with "the
  gate rejected your change", making the most common successful outcome
  indistinguishable from a rejection. Git does not propagate a hook's exit status,
  so this is for what calls warden directly: retry wrappers, CI, and the agent
  surfaces. **If you have a wrapper keying on `1`, treat `3` as success.**

- **`blocker` and `retryable` reach the agent surfaces** (#146). The domain has
  distinguished "the machine was not ready" from "your change is wrong" since the
  exit-code split; the agent surfaces dropped it, leaving an agent to infer
  whether to retry by parsing English.

- The CI-gate pin example is documented against a pin that will not rot (#139),
  and `actions/checkout` is bumped to v7.0.1 (#138).

- **Every module warden builds against is current** (#143). Ten bumps, two of
  them direct — `mattn/go-isatty` to v0.0.24 and `go.klarlabs.de/statekit` to
  v1.12.0 — plus `logr`, `runewidth`, `x/net`, `x/sys`, `x/text`, `x/exp`,
  `genproto` and `grpc`. No source change was needed. `go list -m -u all` still
  reports modules behind, and that is correct rather than work left undone:
  `go.sum` hashes the whole module graph, so it lists modules that reach warden
  only through other modules' requirements and are never compiled in.

### Removed

- **44 dead entries pruned from the nox baseline** (#145), which was written in
  July against nox 1.7.1 and had drifted badly by 1.24.0: two thirds of it
  matched nothing in a current scan, including 28 `high` and 2 `critical`
  suppressions for findings that no longer exist. Dead suppressions are not
  inert — they make the baseline unreadable, and a reviewer cannot tell an
  accepted risk from a fingerprint that rotted.

  The 18 findings added in their place were each read before being accepted
  rather than bulk-approved: fake SHAs and fixture email addresses in
  `_test.go` files, plus `workflow_dispatch` on the release workflow, which is
  deliberate and documented in the workflow itself.

  The one `high` the baseline carries — `TAINT-006`, `WARDEN_VERSION` reaching
  `Invoke-WebRequest` in `scripts/install.ps1` — was re-examined here and kept,
  then **fixed outright in #148 above**. Its entry is retained because a taint
  analyzer cannot see that a regex constrains the value, so it now suppresses a
  mitigated flow rather than an accepted one.

- **Three stale release-notes files** (`docs/release-notes-v0.6.0.md`,
  `v0.7.0`, `v0.7.1`) from thirteen releases ago. Nothing linked to them, no
  release since 0.7.1 produced one, and their content is in this file.

### Fixed

- **An SSH-signed note reported an empty signer** (#155). `SignerFingerprint`
  called the ed25519 fingerprint function unconditionally, so the note was
  verifiable but impossible to pin or roster — which removes the only reason to
  sign with an SSH key.

- **Five doc comments described the wrong function** (#141). Inserting a
  function between an existing comment and its declaration leaves the comment
  behind, silently rebinding it to whatever now follows. `Worktree.Remove` came
  off worst: its doc was cut mid-sentence onto `HeadSHA` above it, and `Remove`
  itself was left holding the orphan fragment `// block removal.` The same
  drift put `GH.Checks`'s doc on `GH.Comment`, a stale `Push` paragraph on
  `Repo.ApplyPatch`, a duplicated opener on `remoteTrackingSHA`, and
  `Runner.result`'s doc on `pushedMessage`. Four of the five are exported, so
  the wrong text was what pkg.go.dev served.

  `gofmt`, `go vet` and `golangci-lint` were all silent throughout: nothing is
  syntactically wrong with a comment in the wrong place. golangci-lint excludes
  staticcheck's doc-comment-form checks by default, so `ST1020`/`ST1021`/`ST1022`
  are now enabled — `ST1020` flagged exactly these and nothing else, so the
  guard against a recurrence costs no ongoing noise.

- **A stray `data/` cache directory is now ignored**. Tooling run from the repo
  root writes a durability cache there. Nothing in warden produces it, but
  leaving it untracked-and-unignored parked it permanently in `git status`,
  where an `-A`-style stage would sweep it into a commit.

- **Test isolation for `WARDEN_ALLOW_DISCARD`** (#140). The override leaked out
  of the push that set it and into the gate's own `go test` step, failing the
  guard's tests — an env override asserting *absence* has to clear the variable
  explicitly.

### Security

- **Two nox findings resolved** (#158, #155). An entropy rule flagged the Go
  expression `domain.PrePush.ConfigKey(` as a possible secret key, and
  `GO-2026-5932` surfaced once `x/crypto` became a direct dependency for SSHSIG
  verification. Both are baselined with a recorded reason rather than reworded:
  the first is a method call that cannot be renamed for a scanner's benefit, and
  the second is unreachable — `x/crypto/openpgp` is not linked, govulncheck
  confirms it, and no fixed version exists because the advisory is that the
  package is unmaintained by design.

- **`WARDEN_VERSION` could redirect an install to another GitHub repository**
  (#148). All three installers interpolated the version straight into the
  download URL, so a value of `../../../../someone/else/releases/download/v1`
  traversed out of this repo's path: both the archive *and* `checksums.txt`
  then resolved to `github.com/someone/else`. The checksum step could not catch
  it, because it verified the download against a `checksums.txt` fetched from
  the same redirected base — confirming the attacker's file matched the
  attacker's digest. The host stays pinned to `github.com` by the literal URL
  prefix, so this is a wrong-repository fetch rather than an arbitrary-origin
  one, and reaching it requires control of the environment the installer runs
  in. It should still never have been reachable.

  All three now validate the version against a release-tag pattern before it
  touches a URL, after the `latest` lookup so the resolved tag is checked too:
  `scripts/install.sh`, `scripts/install.ps1` and
  `.github/actions/install-warden.sh`.

  Two anchoring traps were found while testing the fix rather than after
  shipping it. `grep` matches line by line, so `^…$` accepted `v0.20.4\nid` on
  its first line while the second still reached the URL — the shell guard
  therefore rejects the character alphabet before checking the shape. And .NET's
  `$` also matches before a trailing newline, so the PowerShell guard anchors
  with `\z`.

## [0.20.4] — 2026-07-27

A single fix, to the one warden command that was editing your config badly.

### Fixed

- **Toggling a hook no longer reflows the whole config** (#134). `warden hooks
  enable` rewrote formatting it had no business touching: a blank separator
  line was dropped and the aligned trailing comments on `trusted_keys` were
  collapsed to single-space. The intent was already right — `SetHooks` edits
  the YAML node tree rather than re-serializing the config, precisely so
  comments survive — but `yaml.Node` round-trips comments and *not* blank lines
  or intra-line spacing, so the closing re-encode reflowed the document anyway.

  The hook values are now spliced directly into the original bytes, and a
  toggle that changes nothing does not write at all — re-pinning hooks after an
  upgrade is exactly that case, and an identical write still bumps mtime for
  every watcher and build cache. Configs that predate the `hooks` block, or use
  flow style, still route to the node encoder: reformatting is an acceptable
  cost when the alternative is failing to record the setting.

## [0.20.3] — 2026-07-27

Two gates stop refusing work they should have allowed, and the release re-drive
gets the fix 0.20.2 claimed but did not deliver.

### Fixed

- **The scanner version check now judges drift by its effect, not by the version
  string** (#131). The check refused to scan the moment the local binary
  differed from the version CI pins. The reasoning was sound — a scanner that
  renumbers rule ids between releases invalidates every baseline fingerprint at
  once — but it asserted a *proxy*, and the proxy is wrong far more often than
  it is right: releases mostly do not renumber rules. Measured on one repo,
  same tree, same committed baseline, nox 1.17.0, 1.20.0 and 1.22.0 all
  produced 0 findings and 969 suppressed. Identical. Every push in between was
  refused for a harm that was not occurring.

  It was also unsatisfiable in practice. The scanner is brew-installed and
  auto-upgrades — it moved 1.20.0 → 1.22.0 during a single working session, an
  hour after the pin had been bumped to match. The only ways past were to
  hand-install a version-matched binary or to bump a pin that drifts again by
  morning. A gate that cannot be satisfied is a gate people escape with
  `--no-verify`, which also disables the tests and the security scan — leaving
  the tree *less* protected than not having the check at all.

- **The discard guard now has a proportionate override** (#124). Refusing a
  force-push that would delete remote-only commits is right, but its only
  escape was `git push --no-verify` — which skips test, lint *and*
  security-scan and writes no provenance. A guard whose sole escape hatch is
  the nuclear one teaches people to reach for the nuclear one, so the scan
  stops running exactly on the branches that just absorbed someone else's
  changes. `WARDEN_ALLOW_DISCARD=1` now bypasses **this check alone**, names
  the commits it force-pushed over, and leaves every other step running. It is
  an environment variable rather than a flag because the developer types
  `git push` — warden runs as the hook and has no command line of its own. It
  deliberately does **not** override `push.force: never`, which is a repo
  policy decision rather than a per-push safety check.

- **A re-driven release now actually replaces its artifacts** (#129). 0.20.2
  claimed #125 had made the re-drive idempotent. It had not: `release.mode` governs the
  release *notes* (`keep-existing`/`append`/`prepend`/`replace`), while the
  uploaded *assets* are governed by a separate `replace_existing_artifacts`
  key. Only the first was set, so the v0.20.2 re-drive failed identically to
  v0.20.1 — `422 already_exists` on every asset, before reaching the homebrew
  step it was re-driven for. Both keys are set now.

  The claim in 0.20.2 below is left as written rather than rewritten, since it
  describes what was believed at the time; this entry is the correction. This
  release is what proves it: a `workflow_dispatch` re-drive reads the config at
  the *tag*, so v0.20.2 could never have exercised the fix.

### Documentation

- **The README installs from the org tap, and trusts it first** (#132). The
  documented install pointed at `felixgeelhaar/tap`, whose copy of the formula
  was removed when the casks moved to the org tap — it still resolved only via
  a `tap_migrations.json` redirect, one deprecation away from breaking. And
  Homebrew refuses to load a cask from a third-party tap it has not been told
  to trust, so the command as written failed outright on any machine that had
  not already trusted this one. That gate arrived *with* the move to the org
  tap, and its error explains the refusal without explaining the move.

## [0.20.2] — 2026-07-26

Fixes a gate deadlock that could make a branch unpushable through warden, and
completes the tap move 0.20.1 was cutting the ground for.

### Fixed

- **The push guard no longer refuses your own rebased commit** (#127). A branch
  that went `BEHIND`, was replayed by warden's own pre-push `rebase` step, and
  then pushed could be refused as *"push would discard commits that exist only
  on the remote"* — naming a commit you wrote. The advice it printed, `git pull
  --rebase`, could not help: the next push rebases again and reproduces the
  divergence, so the branch became unpushable and the only way out was `git push
  --no-verify`, a bypass with no provenance — the precise outcome
  `push.force: lease` exists to prevent.

  The guard asked `git cherry` whether the remote's copy had a patch-equivalent
  locally. patch-id ignores hunk line numbers but **not** the context lines
  quoted around a hunk, so a base commit editing anything within three lines of
  your change moves the patch-id while leaving the change itself untouched.
  Adjacent edits are ordinary, so this fired often rather than rarely — #125,
  landing directly above the cask block, is what triggered it here.

  When patch-id cannot match, a commit is now treated as yours to replace only
  if it was once reachable from this branch locally (its reflog) **and** is
  committed by you. Either test alone is too generous: the reflog would let a
  colleague's commit be discarded after you pulled it in and dropped it during
  an interactive rebase, and the identity says nothing about whether the commit
  was ever on this branch. Anything unreadable — no reflog, no configured
  identity — leaves the commit reported, because refusing to force is the safe
  direction.

  The pre-existing test for this path rebased an **empty** commit, whose
  patch-id cannot diverge, so it passed either way. The new one rebases a real
  change across an insertion inside its context window, and asserts the
  patch-ids actually diverge so the scenario cannot silently stop reproducing.

- **A re-driven release no longer dies before it reaches the step it was
  re-driven for** (#125). `release.yml` has `workflow_dispatch` precisely so a
  release whose brew push failed can be re-run without re-tagging, but with
  goreleaser's default mode every asset was re-uploaded and the run died on
  `422 already_exists` before reaching the homebrew step — so the recovery path
  could never recover. `release.mode: replace` makes it idempotent. Note the npm
  job is still not: it refuses with *"cannot publish over the previously
  published versions"*, which is correct for a re-drive and harmless, but it
  means a re-driven run still ends red.

### Changed

- **The cask publishes to `klarlabs-studio/homebrew-tap`** (#126). warden is a
  klarlabs-studio repo and the tap credential is a klarlabs-studio org secret,
  but the cask was published to a personal tap that secret cannot reach. That is
  the other half of the history recorded in 0.20.1: consolidating onto one org
  secret fixed the credential living under three names, and left the
  *destination* outside the credential's scope. Existing
  `brew install felixgeelhaar/tap/warden` users are carried over by a
  `tap_migrations.json` entry in the old tap.

## [0.20.1] — 2026-07-26

No user-facing change. warden behaves identically to 0.20.0; this release
exists to carry internal quality and release-plumbing work, and to prove the
rotated Homebrew tap credential end to end after 0.19.0 shipped with a 401 and
0.20.0 with a 403.

### Changed

- **Adopted the two linters warden was missing** from the org's golden
  `golangci.reference.yml`, `errcheck` and `unparam` (#121). 54 findings, no
  bugs — the one that looked dangerous, an unchecked `f.Close` after a write in
  `writeTarFile`, was already correct because the function returns a checked
  `f.Close`. 46 unchecked returns are now explicit at the call site rather than
  suppressed by configuration.
- **`remoteTrackingSHA` no longer claims a failure mode it does not have.** It
  returned `(string, error)` with an error that was always nil, so two callers
  carried conditions that could never be false and a `return false, err` that
  could only return nil. It returns a plain string now. Behaviour is unchanged;
  the signature stops lying about it. Similarly `runStdin` returned combined
  output no caller read, and `exitForBlocker` took a parameter every call site
  set to `1`.
- **The scanner version-drift check now resolves the central nox pin** (#119).
  warden's nox version is pinned once in the shared reusable workflow, so
  scanning this repo for a pin found nothing and the check was silently inert
  here. `security_scan.pin_file` names where that pin lives.
- **The release workflow reads the tap credential from an org secret** (#122).
  The same credential previously lived under three names across the repos
  publishing to the tap, so rotating it meant updating all of them and missing
  one stayed silent until that repo's next release.

### Documentation

- **Closed the 0.18.x gap in this changelog** (#120). Seventeen released
  versions had no entry, so the history read as truncated between 0.19.0 and
  0.17.0. They were all npm OIDC trusted-publishing iteration plus two
  dependency bumps — recorded as one entry rather than seventeen empty ones.

## [0.20.0] — 2026-07-25

### Fixed

- **`warden init` no longer rewrites an existing config.** Authorship was
  inferred from the *parsed* config — "has rules or commands" — so a policy
  built entirely on built-in steps read as "no config" and was regenerated from
  defaults. A `.warden.yaml` setting `steps:` and `trusted_keys:` but no
  `commands:` had its steps reset **and its trusted-signer roster emptied**,
  silently dropping the repo from trusted-signed to attested depth. That is the
  one config whose loss is a security change rather than an inconvenience, and
  the function's own doc comment already promised it "never rewrites the file".
  Whether a file exists is no longer deduced from its contents.
- **A failed step's output survives.** The TUI inlined a finding's entire
  message into a frame that is redrawn in place, so a failing `go test` made the
  frame taller than the terminal and everything above the last screenful was
  lost — leaving the tail, often the bare word `FAIL`, with the failing package,
  test name and assertion gone. Without a test name an intermittent test is
  indistinguishable from an environment problem or a real failure that happened
  to clear, so the only available response was to retry until green: the exact
  habit a gate exists to prevent. The frame now previews a finding (keeping the
  *head*, where a test failure names itself) and a failed run reprints its
  findings to stdout after teardown, matching what the non-interactive path
  always did.
- **A reusable workflow's input `default:` is read as a scanner pin.** Only
  scalar values were read, so warden could see a caller that *overrode* a pin
  but never the definition that *set* it.

### Added

- **`security_scan.pin_file` accepts a cross-repository reference**, for a fleet
  that pins its scanner once in a shared reusable workflow:

  ```yaml
  security_scan:
    pin_file: my-org/.github/.github/workflows/go-ci.yml@main
  ```

  The version-drift check searched only *this* repo's workflows, so it no-opped
  for exactly the repos that followed the advice to centralize the pin. This
  names **where** the pin lives, never what it is — there is still one copy of
  the version, in the workflow that already defines it. Resolution is bounded by
  a 3s timeout and cached for an hour, and every failure (offline, 404, moved,
  malformed) degrades to "no pin found" rather than failing a push.
- **`warden status` reports an inert scanner version check.** Silence used to
  mean both "checked, the versions agree" and "found no pin, compared nothing".

### Changed

- warden's own gate runs the `credentials` step, and its provenance `gate` check
  is now required — the three defects that made it fail on legitimate PRs
  (0.19.0) are fixed.
- Release plumbing: a brew-cask failure can no longer strand the floating `v0`
  tag, the release workflow can be re-driven with `workflow_dispatch`, and the
  tap credential has one name instead of two.

## [0.19.0] — 2026-07-25

### Added

- **A provenance note covers the span its push published, not just the tip.** A
  run writes one note, on `HEAD`, so an ordinary commit → commit → commit → push
  left the earlier commits reading `UNVERIFIED` forever while
  `warden verify --range` demanded a note on every one — the range gate was
  checking for provenance the gate never produces. Attesting each commit
  individually would be a lie (a run validates one tree, the tip's; the
  intermediate trees were never checked out), so the note now records the span
  `(covers_from, commit_sha]` *inside the signed payload*, and `verify --range`
  reads it. A covering note must clear the same signature and trust bar it is
  being used to satisfy, the span is bounded by real git history rather than by
  what the note claims, and a commit outside every trusted span keeps its
  failure. `verify --range` reports how many commits passed by span rather than
  by their own note. Closes #86.
- **`push:` config block** — `force: lease` (default) or `never`. Warden performs
  the push itself, and git's pre-push hook is handed no signal that you typed
  `--force`, so a rebased branch could not be pushed through the gate at all; the
  only way out was `git push --no-verify`, which skips the gate and writes no
  provenance. Warden now detects the rewrite and decides by policy. Closes #85.
- **`secrets` step** — refuses a change in which a tracked file the change
  touched carries a live credential. Narrow on purpose (changed files only,
  high-confidence shapes; `${VAR}` placeholders never match) because a false
  positive is a wall in front of an unrelated commit. Findings name file and line
  but never echo the value.
- **`warden reattest --all [--branch b]`** sweeps a branch from the adoption
  point and closes every recoverable squash-merge gap in one pass, applying
  exactly the same trust rules as the single-commit form. `doctor` and `audit`
  now read `UNVERIFIED (reattestable from <sha>)` where a validated
  tree-identical commit exists, so a recoverable binding gap is distinguishable
  from a genuinely unchecked commit. On warden's own `main` this recovered 19 of
  the unverified commits. Closes #76.
- **`security-scan` gates on the diff, not the repo's absolute state.** When the
  step's command is a nox scan, warden now reads the scan's `findings.json` and
  fails only on findings absent from the merge-base; pre-existing ones are
  reported as a counted warning. Gating on total state meant an unrelated
  one-line change inherited the repo's whole backlog as a precondition — across
  a fleet rollout, five repos had a finished config commit blocked by 7/16/21/44/71
  pre-existing findings, and the gate was red in 5 of 7 repos sampled while
  commits kept landing, i.e. it was being bypassed with `--no-verify`. A
  routinely bypassed gate protects nothing and removes the signal that it ever
  ran. `security_scan.mode: total` keeps the old strict behavior for a repo that
  has reached zero. Closes #87.
- **Warden refuses to scan on scanner version drift.** A scanner that renumbers
  rule IDs between releases gives the same hit a different fingerprint, so every
  committed baseline entry stops matching at once. One repo pinned `NOX_VERSION:
  1.3.0` in CI while developers ran 1.15.0: none of its 729 baseline entries
  matched, CI reported 240 phantom criticals, and every release failed for a
  month because the job only ran on tags. The step now reads the pin out of
  `.github/workflows/*.yml` (a single source of truth — not a second copy in
  `.warden.yaml`) and fails at pre-push with both versions and the pinning file
  named. A baseline matching *zero* current findings is likewise reported as
  drift rather than as hundreds of new criticals. Opt out with
  `security_scan.version_check: false`; point at one workflow with
  `security_scan.pin_file`. Closes #88.
- **`security_scan:` config block**: `mode` (`delta` default / `total`), `base`,
  `version_check`, `pin_file`. It inherits through `extends:`, and a child config
  cannot relax an org base's `total` to `delta` — the same "a child may add
  strictness, never drop it" rule `writes:` and `trusted_keys:` already follow.
- **`credentials` step: a push can no longer carry a secret out of a tracked
  file.** New built-in step, on by default at pre-push, that reads the files the
  change touched and refuses the push when one holds a live-looking credential
  (prefix-tagged GitHub / npm / AWS / Slack / Stripe / OpenAI / Anthropic /
  Google tokens, and PEM private keys). It closes a trap the gate itself was
  creating: a JS step fails for want of dependencies, installing them needs
  private-registry auth, and `npm config set …_authToken "$(gh auth token)"` —
  the command every guide reaches for — writes a live token into `.npmrc`, a
  file most repos *track* with a `${NODE_AUTH_TOKEN}` placeholder in it. The
  path of least resistance out of a red gate ended in a staged credential.
  Matches are redacted in the output; lines deferring to a variable
  (`${NODE_AUTH_TOKEN}`, `{{ secrets.X }}`) and obvious dummies
  (AWS's documented `AKIA…EXAMPLE` key) are not findings. Deliberately shallow — changed
  files only, issuer prefixes only, no entropy heuristics — because a check that
  cries wolf gets deleted. For real coverage add `warden recipes gitleaks`; to
  opt out, leave `credentials` out of `steps.pre_push`. Note the upgrade order:
  an *explicit* `steps.pre_push` list naming `credentials` is a hard error on a
  warden that predates this release ("define commands.credentials … or install
  warden-step-credentials"), so upgrade the binary before adding the name.
  Repos that don't pin `steps` pick it up automatically with the new binary.
  (#91)
- **Distinct exit codes for a gate that never ran.** `75` (`EX_TEMPFAIL`) when a
  step was blocked by another process's lock — retry later — and `78`
  (`EX_CONFIG`) when its toolchain or dependencies are not installed, where
  retrying is pointless. Previously everything collapsed onto `1`, which on
  pre-push *already* means both "passed and pushed" and "failed", so no wrapper
  could tell a lock it should wait out from a verdict it must not retry.
  (#90, #91)

### Changed

- **A successful pre-push now exits 0.** Warden performs the push itself and
  used to fail the hook on purpose, so every successful push ended on
  `error: failed to push some refs` and a non-zero status — automation read
  success as failure, and warden had to print a disclaimer telling people to
  ignore the next line, training them to ignore `error:` from a gate. When
  nothing rewrote the branch and no force is needed, the push git already has
  queued *is* warden's push, so warden now stands aside and lets git report the
  real outcome. It still pushes itself (and still exits non-zero, with the
  disclaimer) when a step rewrote the branch — git would otherwise publish the
  unvalidated pre-fix commit — or when a force is needed, or when a PR is to be
  opened. **If you script `warden run pre-push` or the hook's exit status, this
  is a contract change.** Closes #89.
- **`rebase` targets the branch's integration base, not its own remote ref.**
  It rebased onto `@{upstream}`, which is right until the branch is rewritten:
  after a local rebase onto an updated `main` — the standard way to satisfy
  "head branch is not up to date with the base branch" — `origin/<branch>` still
  holds the commit just replaced, so `origin/<branch>..HEAD` contains *main's*
  commits and the step replayed main onto the superseded tip, failing on main's
  own conflicts and refusing a push that was never wrong. It now resolves
  `pr.base` → the remote's default head → an `@{upstream}` pointing elsewhere,
  and never the branch's own ref. Note this also means the step *runs* on a real
  pre-push for the first time: warden seeds a detached worktree, where
  `@{upstream}` never resolved, so it had been reporting "no upstream, skipped"
  throughout. Closes #102.


- The base scan is cached per base commit under the repo's git dir, so a repo
  with a standing backlog does not pay for re-scanning an unchanged base on every
  push. Any command whose report warden cannot read (`make audit`, `npm audit`, a
  nox invocation that sets its own `-output`/`-format`) keeps the previous
  run-it-and-check-the-exit-code behavior, as does any case where the report,
  the base ref, or the base scan is unavailable — the step degrades toward
  failing, never toward passing.

### Fixed

- **Security: a range gate read its trust roster from the head it was checking.**
  `warden verify --range` resolved `trusted_keys` from the working tree — the PR
  head under gate — so a change could add its own key to `.warden.yaml` and
  self-certify. The roster is now resolved from the range's **base** ref (the
  trusted side) and a malformed base roster fails closed. Separately,
  `reattest` carried provenance from any self-verifying note, letting an
  untrusted note pushed for a tree-identical commit be laundered into a
  locally-trusted re-attestation; the source must now also be signed by a
  trusted key.
- **Security: two known CVEs in indirect dependencies.** `google.golang.org/grpc`
  → v1.82.1 (GHSA-hrxh-6v49-42gf, high) and `golang.org/x/text` → v0.39.0
  (GO-2026-5970, flagged reachable).
- **`warden reattest --push` publishes even when it writes nothing.** The push
  was gated on *this* invocation having written a note, so the obvious two-step
  workflow — sweep, inspect, then publish — left every note local while the
  command reported success. Found by using it: 19 notes stayed local and the
  remote notes ref sat 20 commits behind. `--push` now means "make the remote
  match". The single-commit form had the same trap on its already-noted path.
- **A gated push no longer deletes a colleague's commits.** With
  `push.force: lease`, warden force-pushed whenever the remote tip was not an
  ancestor of the local branch — a test that cannot tell "I rebased my own
  commits" from "someone else pushed to this shared branch". The second case
  silently destroyed their work, and `--force-with-lease` did not catch it: the
  lease only asserts the remote has not moved since your last fetch, so once you
  have fetched their commit the lease is satisfied and the push deletes it.
  Reproduced against a real remote — a colleague's commit and its file vanished
  from the branch with no warning. Warden now compares patch-ids (`git cherry`)
  before forcing and refuses when the remote carries work with no equivalent in
  your history, naming the commits at risk and pointing at `git pull --rebase`.
  A branch rebased onto an updated base still publishes exactly as before: the
  remote holds only your own pre-rewrite commits, which are patch-equivalent, so
  nothing is at risk. An unreadable comparison refuses rather than guesses.


- **A step that never ran is no longer reported as a step that failed.** The
  step-level message was fixed in v0.17.0, but the run's own verdict still read
  `warden: step lint failed` — the line the developer actually sees — when the
  truth was that a sibling repo held golangci-lint's machine-global lock. The
  verdict now names the obstacle: `step lint could not run: another process
  holds its lock`. (#90)
- **A missing toolchain is reported as an environment failure, not a build
  failure.** `sh: astro: command not found` from a `js-build`/`js-check` step
  meant the checkout has no `node_modules`, but read as a broken build — and
  blocked pushes whose diff touched no JS at all. Warden now recognizes every
  common shell's wording for a missing executable (plus `Cannot find module`),
  says plainly that nothing is wrong with the change, and names the exact
  install command derived from the lockfile actually present (`npm ci`,
  `pnpm install --frozen-lockfile`, `yarn install --immutable`,
  `bun install --frozen-lockfile`), scoped to the right package in a monorepo.
  It withholds the advice when `node_modules` is already there, since a
  reinstall would not be the fix. The gate still fails — an unbuilt tree is not
  a validated tree. (#91)
- **golangci-lint contention now comes with the permanent cure.** The failure
  message suggests `--allow-parallel-runners` when the lint command doesn't
  already use it. That lock guards a *shared* cache, and warden already gives
  every run its own `GOLANGCI_LINT_CACHE` — so only warden is in a position to
  know the flag is safe here. (#90)
- **`NODE_AUTH_TOKEN` is documented as the supported private-registry path**,
  with an explicit warning against `npm config set`, in the README and in the
  missing-dependency failure message itself. (#91)
- **The pre-commit pass line names the steps that ran.** Under a split policy
  `warden: pre-commit passed.` meant *lint* passed while the suite was unrun, but
  read as "my tree is green" — a commit went through clean while `go test -race`
  was red. Now: `pre-commit passed (lint) — test runs at pre-push.` Closes #78.
- **Hook version-pin skew is visible.** The shim prefers a `warden` on `PATH`, so
  the pin only ever governed developers with no global install — while the shim's
  comment claimed the whole team ran the same verified binary. The pin is a
  bootstrap floor, not a lock; the shim now says
  `hook pins 0.17.0, PATH has 0.18.16 — running 0.18.16`, and `warden status`
  shows each hook's pin and names any divergence. Closes #77.
- **`warden status` surfaces `warden watch`** when a step is deferred to a later
  hook, which is exactly the gap watch exists to close. Closes #79.
- **Desktop notifications are usable.** They were posted via `osascript`, so
  macOS filed them under *Script Editor* — silently suppressed unless that app
  had notification access, and clicking one opened an empty script. Warden now
  prefers `terminal-notifier` (its own identity, click returns to the terminal
  the gate ran in, grouped per repo) and falls back to `osascript`. The content
  is structured too — `warden: pre-push failed` / `repo · branch` /
  `step lint failed` — and `warden status` names a degraded setup, since the
  macOS fallback fails invisibly.
- **A tool that refuses to run concurrently is no longer reported as a lint
  failure.** `golangci-lint` declines to start while another copy holds its lock;
  warden reported that refusal as `step lint failed`, sending developers hunting
  an error that did not exist. It now waits the lock out and, if the budget
  expires, says `could not run (lock contention)`. Closes #81.
- **The `warden-gate` / `warden-verify` actions no longer need a Go toolchain.**
  They ran `go install`, so the documented `setup-go` with
  `go-version-file: go.mod` killed the job *before verifying anything* in any
  repo without a root `go.mod` — a permanently red check that provided zero
  verification while appearing enforced. They now install a released,
  checksum-verified binary, failing closed on a mismatch. Closes #92.

## [0.18.0] – [0.18.16] — 2026-07-11

Seventeen releases in one day, none of which changed how warden behaves. The
series was a single sustained attempt to get npm **trusted publishing (OIDC)**
working, and each failure mode only revealed itself on a real publish — hence a
release per attempt. Recorded as one entry rather than seventeen empty ones,
and noted here because the jump from 0.17.0 to 0.19.0 otherwise looks like
missing history.

### Changed

- **npm packages now publish via OIDC trusted publishing** instead of a
  long-lived token (#63–#75). Getting there needed, in order: publishing with
  `--provenance`; dropping `registry-url` and later restoring it; adding
  `repository.url` to every per-platform package; pinning npm 11.x, because npm
  12 returns `ENEEDAUTH` under trusted publishing; and finally clearing the
  `setup-node` placeholder token, which was overriding OIDC with an empty
  credential and producing a 404.
- Bumped `go.klarlabs.de/mcp` to v1.22.0 (#62) and v1.24.0 (#69).

If you are on 0.17.0, 0.18.16 behaves identically — there is nothing to read
into the gap beyond release plumbing.

## [0.17.0] — 2026-07-07

### Added

- **Floating `v0` action tag.** The reusable `warden-gate` / `warden-verify`
  actions can be referenced as `@v0`, a major-version tag that tracks the latest
  `v0.x` release (a `major-tag` release job keeps it current; it becomes `@v1`
  once warden reaches 1.0.0). Docs recommend `@v0` for convenience and an exact
  version or commit SHA for a security-critical, immutable gate.

### Changed

- **Parallel batches isolate only tree-writers.** Per-step worktree isolation now
  clones an ephemeral worktree only for steps that write the tree (agents);
  read-only checks (`test`/`lint`/scan) share the canonical worktree, which the
  policy contract already guarantees they don't mutate. The common `test‖lint`
  batch materializes no extra dependency copies — the per-batch clone count drops
  from N to the number of writers, usually zero, cutting setup cost most on large
  cross-filesystem JS repos. The v0.10.1 write-race fix is unchanged (ADR-0001
  Phase 4).
- **Docs: guidance on choosing a gate posture and operating the roster.** The CI
  provenance gate doc now covers advisory-vs-required (required suits shared /
  high-assurance / org repos; advisory is usually right for a solo repo), backing
  up your signer against single-key lock-in, and why a bot/CI signing key is best
  avoided (it moves the trust boundary off a human). No code change.

### Fixed

- **A push that advances no branch no longer re-runs the gate.** The pre-push hook
  ignored git's stdin and always gated the current branch's HEAD, so a notes push
  (`refs/notes/warden`), a tag, a branch deletion, or an unrelated ref needlessly
  re-ran the whole validation pipeline and made warden push HEAD. warden now reads
  the pushed-ref list and exits 0 (letting git complete the push) when no
  `refs/heads/*` ref is created or updated. Fail-safe toward gating: it reads
  stdin only when it isn't a terminal (a manual run is never blocked), and an
  empty or unparseable payload still gates. Branch pushes gate exactly as before.
- **The notification test no longer pops a desktop notification.** A unit test
  called the real `notify.Send`, which shells out to `osascript`/`notify-send`, so
  every `go test` run on a machine with a notifier fired a stray "title / body"
  desktop notification. The shell-out is now behind a seam the test stubs.

## [0.16.0] — 2026-07-06

### Added

- **`warden attest` — export provenance as an in-toto attestation** (ADR-0002
  Phase 3). Projects a commit's `refs/notes/warden` record into an in-toto
  `Statement/v1` with a warden predicate type, so provenance can feed sigstore /
  GUAC / policy engines instead of staying a warden-only note. Read-only; pipe it
  to `cosign attest` to sign the statement. Uses a warden predicate (not
  `slsa.dev/provenance`) because warden attests *source*, not *build*, provenance.
- **`warden reattest` — close the squash-merge provenance gap** (ADR-0002
  Phase 3). A squash-merge commit reproduces the gated PR head's tree exactly but
  has no note, so `doctor`/`audit` on the base branch flag it. `warden reattest`
  (run locally by a maintainer whose key is trusted) finds the tree-identical,
  intact, validly-signed source note, carries its evidence onto the squash commit
  marked `reattested_from`, and re-signs locally — **no hosted bot or CI signing
  key**, a materially simpler design than originally sketched. It fails safe:
  with no content-identical validated source it writes nothing, never asserting a
  validation that didn't happen.

### Fixed

- The CI-provenance docs referenced the bundled actions at a `@v1` tag that does
  not exist; they now pin to a real release tag (`@v0.16.0`), matching warden's
  pin-actions-to-a-version convention.

## [0.15.0] — 2026-07-06

### Added

- **Committed trusted-signer roster: `trusted_keys` in `.warden.yaml`**
  (ADR-0002 Phase 3). Instead of hand-passing `--key <fingerprint>` on every
  call, list your trusted signers once — a bare `warden verify` / `verify
  --range` (and so `warden-gate`) then requires a trusted signer automatically.
  Because it rides on config it **inherits through `extends:`**: an org base
  policy names its signers once and every repo unions them in (a repo may add
  its own in a reviewed diff; it can't silently drop the org's). An explicit
  `--key` still overrides. Malformed entries are rejected at config load. New
  `warden key list` shows the effective roster and flags whether this machine is
  in it; `warden key show` now points at the roster.
- **`warden-gate` action — provenance enforcement as a required check**
  (ADR-0002 Phase 2). The enforcement counterpart to the `warden-verify`
  (provenance-skip) reporter: it runs `warden verify --range` on a PR and
  **fails the check** when any commit lacks trustworthy provenance, so un-gated
  commits can't merge. It runs on the PR *head* — whose commits still carry
  their notes — gating the merge *before* GitHub's squash rewrites history (the
  pragmatic answer to the squash-merge break). Inputs: `key` (trusted roster),
  `require-signed`, `skip-merges`, `base`/`head` (auto from the PR/push event),
  `remote`, `warden-version`. Also ships a self-hosted **pre-receive** recipe
  that rejects the push at the remote. See
  [docs/ci-provenance-gate.md](docs/ci-provenance-gate.md).

## [0.14.0] — 2026-07-06

### Added

- **`warden verify --range BASE..HEAD` — a true provenance gate.** Verifies
  *every* commit in a range and exits non-zero if any lacks trustworthy
  provenance, with a per-commit reason (`missing` / `broken-chain` / `unsigned`
  / `untrusted`) and `--json` for CI. Unlike `doctor` — which flags only
  *missing* notes — this fails a **tampered or transplanted** note too, and
  `--require-signed` / `--key <roster>` escalate the bar from "a warden ran" to
  "a *trusted* warden ran and the note is intact." `--skip-merges` (default on)
  omits merge commits, whose parents are gated individually. This is the
  primitive a PR required-check or a pre-receive hook wraps — see
  [ADR-0002](docs/adr/0002-provenance-enforcement.md). Read-only: the push and
  signing paths are untouched.

### Changed

- **A successful push no longer looks like a failure.** On every successful
  pre-push, git prints `error: failed to push some refs` — Warden already pushed
  your gated commit and then fails the hook on purpose to stop git's own
  now-redundant push from racing it (that non-zero exit is what makes git emit
  the error). Warden now pre-empts it with a plain-language line so you know the
  push already succeeded. The underlying behavior is unchanged — it is
  load-bearing for the "only gated commits reach the remote" guarantee — this
  just stops the expected git error from reading as a real one. Documented in
  the README's "How it works".

## [0.13.0] — 2026-07-06

### Changed

- **Desktop pre-push notifications now fire only when useful.** A *passing*
  interactive pre-push notifies only once it has run at least `notify_after`
  (new config, default `10s`) — a fast green gate no longer pops a notification
  on every push (previously it fired after every run, despite the docs saying it
  was for long ones). A *failed/blocked* push always notifies regardless of
  duration, so a stopped push is never missed. `notify: false` still silences
  everything. A malformed or negative `notify_after` (e.g. `10` with no unit) is
  now rejected at config load with a clear error, instead of silently reverting
  to the default and leaving the threshold mysteriously ineffective.
- **A malformed `timeouts` value now fails config load** instead of silently
  meaning "no limit". A typo'd step timeout (`30` with no unit, `5mm`, or a
  negative duration) used to parse to nothing and leave the step with no limit at
  all — so a wedged test or agent could hang the gate unbounded, the exact
  opposite of what the timeout is for. `Validate` now rejects it at load with a
  clear error; `"0"` remains the explicit no-limit marker.

## [0.12.0] — 2026-07-05

### Added

- **Internal: per-step worktree isolation (ADR-0001 Phase 3, part 1).** Steps in a
  parallel batch now each run in their own ephemeral worktree cloned from the
  canonical one, so a step's writes can't race a sibling; the clones are torn down
  after the batch (side-effects discarded). No scheduling change yet — this is the
  foundation for letting finding-producing agents parallelize.

### Changed

- **Coding-agent steps (`review`, `document`, `intent`) run in parallel again —
  safely.** Building on per-step worktree isolation, the scheduler now serializes
  a step only when its **writes must be kept** (a rebase, an auto-fix budget, or a
  step listed under `writes:`). Everything else — including agents — runs
  concurrently, each in its own ephemeral worktree, so they can't race. An agent's
  incidental tree writes are **discarded**; to persist a step's writes, give it an
  auto-fix budget or declare it under `writes:`. This also correctly scopes the
  pre-commit auto-fix capture to those barrier steps. Completes ADR-0001 Phase 3.


- **Internal: one source of truth for "does a step write the tree."**
  `ResolvedPolicy.WritesTree` now backs both the parallel-batch scheduler
  (`Concurrent`) and the kernel's axi effect level, so the two can no longer
  drift — that drift was the root cause of the parallel-step race fixed in
  v0.10.1. No behavior change to runs; see
  `docs/adr/0001-parallel-step-worktree-isolation.md`.

## [0.11.0] — 2026-07-05

### Changed

- **Gitignored dependencies (`node_modules`) are now materialized by default.**
  warden hardlink-copies them into the disposable worktree as real files instead
  of symlinking, so any tool works out of the box — including Next.js /
  Turbopack, which rejects an out-of-root `node_modules` symlink. Hardlinks are
  near-instant on the same filesystem (byte-copy fallback across filesystems).
  Set `symlink_deps: true` to force the old fast symlink (fine for
  tsc/eslint/vitest, cheaper for a large `node_modules` on a separate `/tmp`
  filesystem). The per-step `materialize_deps:` key is deprecated (materialization
  is now the default) but still parsed for compatibility.

## [0.10.1] — 2026-07-05

### Fixed

- **Linked worktrees with a symlinked `node_modules` now work.** When the live
  checkout is itself a git worktree (e.g. `.claude/worktrees/…`), `node_modules`
  is commonly a symlink back to the main checkout's copy. warden's dependency
  exposure only handled a real directory and silently skipped the symlink, so the
  disposable worktree got no `node_modules` and every JS step (typecheck / lint /
  test / build) failed. It now resolves a symlinked dependency dir to its real
  target and exposes the actual deps (or hardlink-copies them under
  `materialize_deps`).

### Security

- **Parallel steps no longer share a worktree with a writer.** Coding-agent
  steps (`review`, `document`, `intent`, or any step a rule assigns an agent to)
  edit files, but were scheduled to run concurrently with `test`/`lint` in the
  same directory — a data race that could corrupt what the checks read. They now
  run as sequential barriers. New `writes: [step…]` config marks a custom step
  (codegen, formatter) as a tree-writer so it also runs alone. See
  `docs/adr/0001-parallel-step-worktree-isolation.md`.

### Changed

- The default **pre-push step order** is now `intent, rebase, review, document,
  test, lint` (was `…review, test, document, lint`), grouping the writing agents
  ahead of the read-only checks so `test`‖`lint` still share one parallel batch.
  Repos with an explicit `steps:` list are unaffected.

## [0.10.0] — 2026-07-05

A security-hardening release closing every finding from a deep multi-agent
review, plus the Next.js/Turbopack worktree fix. Several changes tighten
behavior (fail-closed) — see **Changed** before upgrading.

### Security

- **Provenance is now bound to the commit it attests.** The signed `RunRecord`
  gained a `CommitSHA` (covered by the signature), and `warden verify` requires
  `record.CommitSHA == <commit>`. Previously a validly-signed note could be
  transplanted onto — or replayed against — any other commit and still pass
  `verify --key <trusted>`, letting CI provenance-skip skip checks on
  attacker-controlled code.
- **`warden verify` fails closed.** It no longer treats an empty `{}` (or any
  no-evidence) note as validated, and `audit`/`doctor` only call a note "intact"
  when it actually attests its commit.
- **The self-fetched warden binary is verified before it runs.** The generated
  git hook, `install.sh`, and `install.ps1` now check the downloaded archive's
  SHA-256 against `checksums.txt` (from the pinned release tag) **before**
  `chmod +x`/extraction, re-verify the `~/.warden/bin/<ver>` cache on every run
  (dir created `0700`), and fail closed on mismatch. Releases now publish a
  **cosign-signed** `checksums.txt`. (Residual: the fetch verifies the checksum
  but not yet the cosign signature over the same channel — see `SECURITY.md`.)
- **`extends:` is contained to the repo.** A `.warden.yaml` can no longer inherit
  its `commands:`/rules from an absolute or `../`-escaping path outside the
  repository (which also read arbitrary files).
- **MCP `run_trigger` refuses by default.** The MCP/`axi` surface auto-approves,
  so running a possibly-untrusted repo's `.warden.yaml` commands now requires an
  explicit opt-in (`WARDEN_MCP_ALLOW_RUN=1`, or `--trust` for `axi run-trigger`).
  Read-only tools (`policy_explain`, `steps_list`) are unaffected.
- **Custom step names are validated** (`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`), closing a
  path-traversal where a name like `x/evil` resolved `warden-step-x/evil` to a
  repo-relative executable.
- **Auto-fixes are only written back when a step was authorized to fix.** A
  passing pre-commit no longer re-applies arbitrary tree mutations made by
  read-only steps to your working tree — capture happens only when a step holds
  an `auto_fix` budget.
- **Runs are cancellable and crash-safe.** Ctrl-C/SIGTERM now cancels a run
  (and can no longer auto-approve a push after you abort); a panic in a parallel
  step becomes a step error instead of crashing the gate and leaking the
  worktree; timed-out steps are killed by process group so children don't orphan.
- **Glob matching is linear-time**, closing a ReDoS where a crafted
  `paths:`/`cache:` pattern against a long path could hang the gate on push.

### Added

- **`materialize_deps:` — real (not symlinked) dependency dirs for build steps.**
  warden symlinks gitignored `node_modules` into the disposable worktree (fast,
  fine for `tsc`/`eslint`/`vitest`/Node), but Next.js 16 / Turbopack rejects a
  `node_modules` symlink resolving outside the worktree root
  (`TurbopackInternalError: Symlink node_modules is invalid…`). List the affected
  steps under `materialize_deps` (e.g. `[build]`) and warden hardlink-copies the
  deps as real files for runs that include one of them; other runs keep the fast
  symlink. Hardlinks fall back to a byte copy across filesystems; internal `.bin`
  symlinks are preserved.

### Changed

- **Legacy provenance notes (written before this release) fail `verify`** — they
  carry no `CommitSHA` binding, so they must be re-validated. Correct fail-closed
  behavior, but re-run warden on affected commits.
- Inherited (`extends`) step lists now **merge** (union) rather than being
  replaced, so a base can't silently have a required step dropped; partial
  `risk:` overrides now merge field-by-field.

## [0.9.0] — 2026-07-04

### Added

- **`warden init` generates comprehensive, multi-ecosystem configs.** Instead of
  detecting a single top-level language, init now walks the repo for every
  buildable unit (skipping `node_modules`/`vendor`/build dirs) and composes a
  path-scoped lint + test step per ecosystem — so a Go module at `apps/api` and
  a TypeScript app at `web` both get gated (`cd apps/api && …`, `cd web && …`),
  with `pre_commit` running the lints and `pre_push` the tests + lints. A nox
  `security-scan` step is added when nox is on PATH. Single-language repos are
  unchanged (unprefixed `lint`/`test`). Language knowledge stays in
  `LanguageCommands` (Go, Rust, JS, TS, Python), so a new language is a table
  entry, not new code. (#13)

## [0.8.4] — 2026-07-04

### Fixed

- **Per-run golangci-lint cache (no more stale-cache phantom failures).**
  golangci-lint caches results keyed to absolute paths; because each gate run
  uses a fresh random worktree, a shared cache returned results referencing a
  deleted worktree path — so `//nolint` directives weren't honored and it
  reported failures on clean code (cleared only by `golangci-lint cache clean`).
  Steps now get a per-worktree `GOLANGCI_LINT_CACHE`, cleaned with the worktree.
  (#11)
- **The gate fails fast when the warden binary can't run.** If the resolved
  binary can't start (Gatekeeper-quarantined, corrupt, blocked), the hook shim
  used to hang on `exec`, wedging every commit/push. The shim now preflights a
  time-boxed `--version` and, on a hung/unrunnable binary, exits with an
  actionable message instead of hanging. (#12)

## [0.8.3] — 2026-07-03

### Fixed

- **A failing step in a parallel batch now reports cleanly.** When one step in a
  concurrent batch failed, the run went terminal and the record loop still tried
  to fold the remaining outcomes in, surfacing the opaque `record step X: run is
  already terminal` instead of a `step Y failed` naming the real culprit. The
  loop now stops at the terminal transition, so a parallel gate failure is
  legible.

## [0.8.2] — 2026-07-03

### Fixed

- **Steps no longer inherit the git hook environment.** git exports
  `GIT_INDEX_FILE`, `GIT_DIR`, etc. when running a pre-commit/pre-push hook.
  Steps inherited them, so a git-aware tool inside the disposable worktree —
  notably `golangci-lint --new-from-rev` — resolved git against the live hook
  index instead of the worktree and mis-reported (e.g. flagging the whole
  backlog instead of just the change). `stepEnv` now scrubs those vars, the same
  way warden's own git subcommands already did. This makes incremental linting
  (`new-from-rev`) reliable in the gate, so a strict linter can be adopted on a
  repo with existing debt without a big-bang refactor.

## [0.8.1] — 2026-07-03

### Fixed

- **Homebrew install no longer hangs on first run.** The cask binary isn't
  notarized, so macOS Gatekeeper quarantined it and the first `warden`
  invocation blocked on an unsigned-binary check (`spctl: rejected`). The cask
  now strips the quarantine attribute on install (`xattr -dr
  com.apple.quarantine`), so the CLI runs immediately after `brew install`.

## [0.8.0] — 2026-07-03

### Added

- **`node_modules` passthrough for JS/TS steps.** The validation worktree is a
  git worktree, so it only contained tracked files — gitignored `node_modules`
  was absent and steps like `tsc`, `eslint`, or `vitest` failed with "command
  not found". Warden now symlinks each `node_modules` from the live checkout
  into the worktree (root and nested — `web/`, `apps/*/`, `site/`), so JS/TS
  gates resolve their dependencies with no reinstall. This makes warden work
  out of the box for Node and Go+JS monorepos; commands no longer need an
  `npm ci &&` prefix.

## [0.7.1] — 2026-07-03

### Fixed

- **Staged binary files no longer fail the pre-commit gate.** Worktree seeding
  captured and applied the staged diff without `--binary`, so committing an
  image or other binary asset failed with "cannot apply binary patch … without
  full index line". The staged-diff and auto-fix diffs now round-trip binaries
  (`git diff --binary` / `git apply --binary`).

## [0.7.0] — 2026-07-02

### Added

- **Parallel steps** — independent read-only checks run concurrently; the gate
  is as slow as the slowest check, not the sum. `parallel: false` opts out.
- **Step-level cache** — `cache:` globs skip a step when its declared inputs are
  byte-identical to its last passing run.
- **Per-step timeouts** — `timeouts:` kills and fails a wedged step.
- **Signed provenance** — per-machine ed25519-signed notes; `warden key show`
  and `warden verify --key` add a trust gate. `warden-verify` action `key:` input.
- **SBOM in the note** — signed digest of every dependency lockfile at validation.
- **`warden why <commit>`** — explain what the gate did for a commit from its note.
- **Streamed step output** in the TUI, plus **collapsible findings** (`f`) and
  **jump-to-file** (`1-9` → `$EDITOR`).
- **Desktop notification** when an interactive pre-push finishes.
- **`warden attach`** — watch a running gate live from another terminal.
- **`warden watch`** — re-run the fast checks on save.
- **PR comment** — sticky gate-result comment on a passing push.
- **`warden recipes`** — paste-able check recipes (gitleaks, semgrep, trivy, …).
- **`extends:`** — inherit a base config across repos (org policy sync).

## [0.6.0] — 2026-07-01

- Initial public release: native `pre-commit`/`pre-push` hooks, worktree
  isolation, rule-based policy, hash-chained provenance + CI provenance-skip,
  config-driven custom steps and agents, interactive TUI, `warden import`,
  and multi-channel install (go / npx / brew / curl).

[0.7.1]: https://github.com/klarlabs-studio/warden/releases/tag/v0.7.1
[0.7.0]: https://github.com/klarlabs-studio/warden/releases/tag/v0.7.0
[0.6.0]: https://github.com/klarlabs-studio/warden/releases/tag/v0.6.0
