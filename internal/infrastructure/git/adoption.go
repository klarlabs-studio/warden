package git

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GitDir returns the repository's .git directory, resolving worktrees and
// custom layouts via `git rev-parse --git-dir`.
func (r *Repo) GitDir() (string, error) {
	out, err := r.run("rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	return out, nil
}

// adoptionPath is where the adoption commit is recorded. Kept under .git so it
// is local state, never committed, and travels with the clone's hooks.
func (r *Repo) adoptionPath() (string, error) {
	gitDir, err := r.GitDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(gitDir, "warden", "adoption"), nil
}

// WriteAdoption records sha as the adoption point: `warden doctor` only
// evaluates commits after it, so pre-adoption history is never flagged (§9).
func (r *Repo) WriteAdoption(sha string) error {
	path, err := r.adoptionPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create warden state dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(sha+"\n"), 0o644); err != nil {
		return fmt.Errorf("write adoption point: %w", err)
	}
	return nil
}

// ReadAdoption returns the recorded adoption commit, or "" if Warden was never
// initialized in this repo.
func (r *Repo) ReadAdoption() (string, error) {
	path, err := r.adoptionPath()
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
