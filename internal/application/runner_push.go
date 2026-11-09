package application

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
)

// ErrPushRewritesHistory is returned when a push would discard commits the
// remote has and the repo's policy forbids rewriting (push.force: never).
var ErrPushRewritesHistory = errors.New("push would rewrite the remote branch's history")

// ErrPushDiscardsRemoteWork is returned when a forced push would delete commits
// that exist only on the remote — someone else's work on a shared branch, as
// opposed to our own commits in their pre-rewrite form.
//
// push.force: lease exists so a branch rebased onto an updated base can still be
// published (#85). It is permission to replace OUR history, and it is not a
// judgement that anything on the remote is expendable. --force-with-lease does
// not draw that line either: it only asserts the remote has not moved since our
// last fetch, so once we have fetched a colleague's commit the lease is
// satisfied and the push destroys it silently.
var ErrPushDiscardsRemoteWork = errors.New("push would discard commits that exist only on the remote")

// pushForce decides how to push a branch whose history may no longer
// fast-forward from the remote — the ordinary result of rebasing onto an
// updated base.
//
// Warden performs the push itself, so git's pre-push hook is handed no signal
// that the developer typed --force and a plain push fails as non-fast-forward
// regardless. The rewrite therefore has to be detected (the remote tip is not
// an ancestor of ours) and decided by policy rather than inherited from the
// command line.
//
// A fast-forward never forces, whatever the policy says: the force flag is
// reserved for the case that actually needs it, so an ordinary push carries no
// extra power. When a rewrite IS needed and policy forbids it, the run fails
// with a message naming the knob — the alternative the developer would
// otherwise reach for is `git push --no-verify`, which skips the gate and
// writes no provenance at all.
func (r *Runner) pushForce(cfg domain.Config, branch string) (domain.PushForce, string, error) {
	rewrites, err := r.Git.PushRewritesHistory(r.Settings.Remote, branch)
	if err != nil || !rewrites {
		// An unreadable ancestry is not a license to force: fall back to the plain
		// push and let git refuse it, as it would without warden.
		return domain.ForceNever, "", nil
	}
	mode := cfg.PushForceMode()
	if mode == domain.ForceNever {
		return "", "", fmt.Errorf("%w: %s has been rebased or amended, and this repo sets push.force: never. "+
			"Rewrite it deliberately (git push --force-with-lease) or allow it with push.force: lease in .warden.yaml",
			ErrPushRewritesHistory, branch)
	}
	// A lease is permission to replace OUR OWN pre-rewrite commits, never to
	// discard someone else's. Refuse when the remote carries work with no
	// patch-equivalent locally — see ErrPushDiscardsRemoteWork.
	if lost, err := r.Git.UnmergedRemoteCommits(r.Settings.Remote, branch); err != nil || len(lost) > 0 {
		if err != nil {
			// Cannot tell whose work is on the remote: do not force on a guess.
			return "", "", fmt.Errorf("%w: could not determine whether %s/%s carries work not in your history: %v",
				ErrPushDiscardsRemoteWork, r.Settings.Remote, branch, err)
		}
		// The scoped override. UnmergedRemoteCommits already suppresses the guard
		// for commits it can prove are our own pre-rewrite tips (committed by us
		// AND once reachable from this branch's reflog), which covers the ordinary
		// rebase. It cannot prove that when the reflog is absent — a fresh clone,
		// another machine, a rewrite older than the scan limit — or when the
		// commit was committed by someone else, as GitHub's web UI and "Update
		// branch" both do.
		//
		// Without a proportionate escape those cases leave `git push --no-verify`
		// as the only way through, which skips test, lint AND security-scan and
		// writes no provenance. A guard whose only override is the nuclear one
		// teaches people to reach for the nuclear one, so the scan stops running
		// exactly on the branches that just absorbed someone else's changes.
		//
		// This bypasses THIS check and nothing else: every step still runs and the
		// note is still written. The dropped commits are named on the way past so
		// the decision is recorded rather than silent.
		if allowDiscard() {
			return mode, fmt.Sprintf(
				"WARDEN_ALLOW_DISCARD set — force-pushed over %d commit(s) on %s/%s that were not in your history:\n  %s",
				len(lost), r.Settings.Remote, branch, strings.Join(lost, "\n  ")), nil
		}
		return "", "", fmt.Errorf("%w: %s/%s carries %d commit(s) that are not in your history:\n  %s\n"+
			"If they are someone else's, integrate first: `git pull --rebase`, then push.\n"+
			"If they are your own pre-rewrite commits that warden could not verify (a fresh clone\n"+
			"has no reflog to check against), re-push with WARDEN_ALLOW_DISCARD=1 — that skips this\n"+
			"one check and keeps test, lint and security-scan running, unlike --no-verify",
			ErrPushDiscardsRemoteWork, r.Settings.Remote, branch, len(lost), strings.Join(lost, "\n  "))
	}
	return mode, "", nil
}

// allowDiscard reports whether the developer has explicitly accepted losing the
// remote-only commits on this push. Deliberately an environment variable and not
// a flag: the developer types `git push`, and warden runs as its pre-push hook,
// so there is no warden command line to put a flag on.
func allowDiscard() bool {
	v := strings.TrimSpace(os.Getenv("WARDEN_ALLOW_DISCARD"))
	return v != "" && v != "0" && !strings.EqualFold(v, "false")
}

// delegateToGit reports whether the push git is ALREADY about to perform is
// byte-for-byte the one warden would perform, so warden can stand aside and let
// git do it.
//
// Warden normally pushes itself and then fails the hook on purpose, because git
// pushes the ref value it captured BEFORE the hook ran: if a step rewrote the
// branch (an auto-fix, an amending agent step), git would publish the
// *unvalidated* pre-fix commit. Standing aside is only safe when nothing was
// rewritten.
//
// It is also unsafe when the push needs a force. Git's own push is not forced,
// so a rebased branch would simply be rejected — and warden's whole reason for
// supporting a lease (#85) is that the alternative is a gate bypass.
//
// Finally, it is unsafe when a PR is to be opened. PR creation runs AFTER the
// push, and git's push does not happen until the hook has exited — so a
// delegating run reaches `gh pr create` while the branch is still absent from
// the remote. On a first push that fails ("head branch not found"), and because
// PR creation is best-effort it fails SILENTLY: no PR, no error. Warden pushes
// itself in that case so the branch exists by the time the forge is called.
//
// When all three hold, delegating is strictly better than pushing ourselves: git
// reports the push it actually performed, with a real exit code, instead of
// warden succeeding and then having to warn the developer to ignore the
// `error:` git prints because warden deliberately failed the hook (#89).
func (r *Runner) delegateToGit(finalSHA, seedTip string, force domain.PushForce, willOpenPR bool) bool {
	return finalSHA == seedTip && force == domain.ForceNever && !willOpenPR
}

// pushedMessage states what landed where, so "did it push?" is answerable from
// the output alone rather than by running `git ls-remote` — which is what an
// operator had to do while success and failure printed the same thing (#89).
// A delegating run makes no claim: git is about to report that push itself, and
// warden asserting it first would be a second account of the same event, and a
// false one if git's push then fails.
func pushedMessage(sha, remote, branch string, gitCompletes, attestOnly bool) string {
	if gitCompletes {
		return "gate passed; git is completing the push"
	}
	// --attest-only pushes nothing. Reporting "pushed X to origin/main" here said
	// warden had done something it deliberately had not — and it said it about a
	// commit the FORGE created, which is the one case this mode exists for. The
	// note is the whole output of an attest-only run, so that is what to name.
	if attestOnly {
		short := sha
		if len(short) > 12 {
			short = short[:12]
		}
		if short == "" {
			short = "the merged commit"
		}
		return "gate passed; attested " + short + " (--attest-only: nothing pushed)"
	}
	short := sha
	if len(short) > 12 {
		short = short[:12]
	}
	if short == "" {
		// HEAD was unreadable, so no note was bound either; do not render a blank.
		short = "the gated commit(s)"
	}
	target := branch
	if remote != "" {
		target = remote + "/" + branch
	}
	return fmt.Sprintf("pushed %s to %s; local branch fast-forwarded", short, target)
}
