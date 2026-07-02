// Package forge adapts a code-hosting provider to the application's Forge port.
// The only implementation is GitHub via the `gh` CLI, so warden inherits gh's
// auth and config rather than embedding tokens or an API client. A repo without
// gh installed simply reports Available() == false and PR creation is skipped.
package forge

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
)

// GH drives GitHub through the gh CLI, run in the repository directory so gh
// resolves the right remote and auth.
type GH struct {
	dir string
}

// NewGH returns a gh-backed forge rooted at repo dir.
func NewGH(dir string) *GH { return &GH{dir: dir} }

// Available reports whether gh is installed. Auth is checked lazily — an
// unauthenticated gh surfaces its own error on first use, which the runner
// swallows (PR creation is best-effort).
func (g *GH) Available() bool {
	_, err := exec.LookPath("gh")
	return err == nil
}

func (g *GH) run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Dir = g.dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// EnsurePR returns the open PR for branch, opening one onto base if none
// exists. `gh pr view` succeeds when a PR is already open; otherwise
// `gh pr create --fill` derives title/body from the commits.
func (g *GH) EnsurePR(ctx context.Context, branch, base string) (domain.PRInfo, error) {
	if out, err := g.run(ctx, "pr", "view", branch, "--json", "url,number"); err == nil {
		var pr struct {
			URL    string `json:"url"`
			Number int    `json:"number"`
		}
		if json.Unmarshal([]byte(out), &pr) == nil && pr.URL != "" {
			return domain.PRInfo{URL: pr.URL, Number: pr.Number, Created: false}, nil
		}
	}

	args := []string{"pr", "create", "--head", branch, "--fill"}
	if base != "" {
		args = append(args, "--base", base)
	}
	out, err := g.run(ctx, args...)
	if err != nil {
		return domain.PRInfo{}, err
	}
	// gh pr create prints the PR URL on its last line.
	url := lastURL(out)
	return domain.PRInfo{URL: url, Created: true}, nil
}

// Checks returns the CI status for branch's PR by reading `gh pr checks`'s
// machine-readable JSON. A non-zero exit (failing/pending checks) is expected,
// so the JSON is parsed regardless of exit code.
func (g *GH) Checks(ctx context.Context, branch string) (domain.CIStatus, error) {
	out, _ := g.run(ctx, "pr", "checks", branch, "--json", "state")
	var rows []struct {
		State string `json:"state"`
	}
	if out == "" || json.Unmarshal([]byte(out), &rows) != nil {
		return domain.CIStatus{State: domain.CINone}, nil
	}
	return tally(rows), nil
}

// tally aggregates individual check states into a CIStatus.
func tally(rows []struct {
	State string `json:"state"`
}) domain.CIStatus {
	s := domain.CIStatus{Total: len(rows)}
	for _, r := range rows {
		switch strings.ToUpper(r.State) {
		case "SUCCESS", "NEUTRAL", "SKIPPED":
			s.Passed++
		case "FAILURE", "ERROR", "CANCELLED", "TIMED_OUT", "ACTION_REQUIRED":
			s.Failed++
		default: // PENDING, QUEUED, IN_PROGRESS, EXPECTED, …
			s.Pending++
		}
	}
	switch {
	case s.Total == 0:
		s.State = domain.CINone
	case s.Failed > 0:
		s.State = domain.CIFailing
	case s.Pending > 0:
		s.State = domain.CIPending
	default:
		s.State = domain.CIPassing
	}
	return s
}

// lastURL returns the last whitespace token that looks like a URL, which is how
// gh reports the created PR.
func lastURL(out string) string {
	fields := strings.Fields(out)
	for i := len(fields) - 1; i >= 0; i-- {
		if strings.HasPrefix(fields[i], "http") {
			return fields[i]
		}
	}
	return ""
}
