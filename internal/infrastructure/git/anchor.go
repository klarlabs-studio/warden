package git

import (
	"fmt"
	"strconv"

	"go.klarlabs.de/warden/internal/domain"
)

// FirstParentAncestors returns up to limit first-parent ancestors of rev,
// nearest first, rev itself excluded. A root commit has none, which is an
// empty answer rather than an error.
func (r *Repo) FirstParentAncestors(rev string, limit int) ([]string, error) {
	if limit <= 0 {
		return nil, nil
	}
	parent := rev + "^1"
	if _, err := r.run("rev-parse", "--verify", "--quiet", parent+"^{commit}"); err != nil {
		return nil, nil // a root commit: no parent to walk
	}
	out, err := r.run("rev-list", "--first-parent", "--max-count="+strconv.Itoa(limit), parent)
	if err != nil {
		return nil, fmt.Errorf("git: walk first parents of %s: %w", rev, err)
	}
	return splitLines(out), nil
}

// NotedCandidate is a noted commit offered to an anchor search.
type NotedCandidate struct {
	SHA    string
	Tree   string
	Record domain.RunRecord
}

// FirstNotedAncestor walks the first-parent ancestors of rev (nearest first,
// rev excluded, at most limit) and returns the first commit whose warden note
// accept takes. Commits without a note are skipped without reading one; a
// note that cannot be parsed, or a commit whose tree cannot be read, is
// skipped too, since neither can vouch for anything.
func (r *Repo) FirstNotedAncestor(rev string, limit int, accept func(NotedCandidate) bool) (NotedCandidate, bool, error) {
	ancestors, err := r.FirstParentAncestors(rev, limit)
	if err != nil || len(ancestors) == 0 {
		return NotedCandidate{}, false, err
	}
	noted, err := r.NotedCommits()
	if err != nil {
		// No notes ref yet is the ordinary state of a repository nothing has
		// attested: nothing can anchor.
		return NotedCandidate{}, false, nil
	}
	hasNote := make(map[string]bool, len(noted))
	for _, sha := range noted {
		hasNote[sha] = true
	}
	for _, sha := range ancestors {
		if !hasNote[sha] {
			continue
		}
		rec, err := r.ReadNote(sha)
		if err != nil || rec == nil {
			continue
		}
		tree, err := r.TreeSHA(sha)
		if err != nil {
			continue
		}
		c := NotedCandidate{SHA: sha, Tree: tree, Record: *rec}
		if accept(c) {
			return c, true, nil
		}
	}
	return NotedCandidate{}, false, nil
}
