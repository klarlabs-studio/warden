package service

import (
	"slices"

	"go.klarlabs.de/warden/internal/domain"
)

// ScopeClaimKind says what a verification lets CI rely on (design 0006,
// Phase B): the whole commit, some of its workspaces, or nothing.
type ScopeClaimKind string

const (
	// ScopeClaimFull: the commit verified. Every check the note records ran
	// on this commit's tree, for every workspace.
	ScopeClaimFull ScopeClaimKind = "full"
	// ScopeClaimAffected: the note is sound, bound to this commit, signed by
	// a trusted key and passed every other check asked for, and was refused
	// only because it attests an affected-scope run.
	ScopeClaimAffected ScopeClaimKind = "affected"
	// ScopeClaimNone: nothing can be relied on; CI runs everything.
	ScopeClaimNone ScopeClaimKind = "none"
)

// ScopeClaim is the per-workspace reading of one commit's verification.
type ScopeClaim struct {
	Kind ScopeClaimKind
	// ValidatedWorkspaces lists the workspace paths every per-workspace check
	// the note records ran for, on exactly this commit's tree. Never nil.
	ValidatedWorkspaces []string
}

// ClaimScope reads a finished single-commit verification per workspace.
//
// accepted is the caller's final verdict, after every check it asked for.
// othersHeld reports that every check the caller asked for beyond the
// service's own verification held (container evidence, a required
// signature); requirement and profile failures are read from res itself.
//
// An affected claim is the Phase B claim, which needs no induction: "these
// workspaces were checked on this exact tree". It says nothing about the
// workspaces outside the selection, so it never turns the verdict green, and
// it is made only when everything else about the note is beyond doubt:
// refused for its scope alone, trusted by a pinned key, local rather than
// external, its scope one this warden can read (a known version, an affected
// kind, no sparse cone), and its checks run on the tree the commit holds,
// read from git rather than taken from the note.
func (s *Service) ClaimScope(res VerifyResult, accepted, othersHeld bool) ScopeClaim {
	none := ScopeClaim{Kind: ScopeClaimNone, ValidatedWorkspaces: []string{}}
	rec := res.Record
	if accepted {
		claim := ScopeClaim{Kind: ScopeClaimFull, ValidatedWorkspaces: []string{}}
		if rec != nil && rec.Scope != nil {
			claim.ValidatedWorkspaces = append(claim.ValidatedWorkspaces, rec.Scope.Workspaces...)
			slices.Sort(claim.ValidatedWorkspaces)
		}
		return claim
	}
	if !res.ScopeRefused || !othersHeld || len(res.RequirementFailures) > 0 ||
		!res.Trusted || res.External || rec == nil || rec.Scope == nil {
		return none
	}
	claim := ScopeClaim{Kind: ScopeClaimAffected, ValidatedWorkspaces: []string{}}
	scope := rec.Scope
	if scope.Kind != domain.SelectionAffected || scope.Version < 1 || scope.Version > domain.SelectionVersion || scope.Sparse != nil {
		return claim
	}
	tree, err := s.repo.TreeSHA(res.SHA)
	if err != nil || tree == "" || tree != scope.HeadTree {
		return claim
	}
	for _, w := range scope.Selected {
		if slices.Contains(scope.Workspaces, w.Path) && !slices.Contains(claim.ValidatedWorkspaces, w.Path) {
			claim.ValidatedWorkspaces = append(claim.ValidatedWorkspaces, w.Path)
		}
	}
	slices.Sort(claim.ValidatedWorkspaces)
	return claim
}
