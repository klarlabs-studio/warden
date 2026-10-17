package service

import (
	"errors"
	"path/filepath"

	"go.klarlabs.de/warden/internal/infrastructure/git"
)

// HookPathConflict reports a directory selection that prevents Git from
// reaching Warden's managed hooks. It never changes the selection itself.
func (s *Service) HookPathConflict() (string, error) {
	expected, effective, err := s.repo.HookPaths()
	if err != nil {
		return "", err
	}
	if git.SameHookPath(expected, effective) {
		return "", nil
	}
	return git.HookPathConflict(expected, effective), nil
}

// hooksGitDir uses Git's common directory: linked worktrees share hooks even
// though their index and HEAD live under a separate per-worktree Git directory.
func (s *Service) hooksGitDir(requireActive bool) (string, error) {
	expected, effective, err := s.repo.HookPaths()
	if err != nil {
		return "", err
	}
	if requireActive && !git.SameHookPath(expected, effective) {
		return "", errors.New(git.HookPathConflict(expected, effective))
	}
	return filepath.Dir(expected), nil
}
