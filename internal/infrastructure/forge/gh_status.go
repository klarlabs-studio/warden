package forge

import (
	"context"
	"errors"
)

// maxStatusDescription is GitHub's limit on a status description. Over it, the
// API truncates silently — and a step list grows without bound, so warden
// clips deliberately rather than letting the surface decide what to drop.
const maxStatusDescription = 140

// clip shortens s to at most limit bytes, ending in an ellipsis when it had to
// cut. It drops whole runes: slicing a byte at a time would leave a truncated
// UTF-8 sequence, which the API rejects rather than trims.
func clip(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	const ellipsis = "…"
	out := []rune(s)
	for len(out) > 0 && len(string(out))+len(ellipsis) > limit {
		out = out[:len(out)-1]
	}
	return string(out) + ellipsis
}

// StatusUpdate is one commit status to publish.
type StatusUpdate struct {
	// SHA is the commit the status attaches to. Must be the PR head for
	// branch protection to see it.
	SHA string
	// State is one of GitHub's success / failure / error / pending.
	State string
	// Context is the check name protection matches on.
	Context string
	// Description is the one line shown beside the check.
	Description string
	// TargetURL is optional; GitHub links the check name to it.
	TargetURL string
}

// PublishStatus posts a commit status through gh.
//
// This is how a gate that ran on a laptop reports to a repository whose
// Actions cannot run. It publishes only what this machine actually did — the
// signed note remains the evidence; a status is a pointer to it that branch
// protection can read.
//
// The gh CLI resolves the repository from the working directory and carries
// its own auth, so warden holds no token.
func (g *GH) PublishStatus(ctx context.Context, sha, state, statusContext, description string) error {
	return g.publish(ctx, StatusUpdate{SHA: sha, State: state, Context: statusContext, Description: description})
}

// publish is the full-fidelity form; PublishStatus is the port's narrower one.
func (g *GH) publish(ctx context.Context, s StatusUpdate) error {
	if s.SHA == "" {
		return errors.New("publish status: no commit")
	}
	if s.State == "" {
		return errors.New("publish status: no state")
	}

	desc := clip(s.Description, maxStatusDescription)

	args := []string{
		"api", "-X", "POST", "repos/{owner}/{repo}/statuses/" + s.SHA,
		"-f", "state=" + s.State,
		"-f", "context=" + s.Context,
		"-f", "description=" + desc,
	}
	if s.TargetURL != "" {
		args = append(args, "-f", "target_url="+s.TargetURL)
	}
	_, err := g.run(ctx, args...)
	return err
}
