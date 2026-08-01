// Package trust records which repositories the operator has authorized the
// non-interactive agent surfaces to execute.
//
// `run_trigger` on the MCP and axi surfaces runs the repository's own
// `.warden.yaml` commands as shell with no human at an approval prompt, so the
// opt-in that permits it is the only thing standing between an agent and
// arbitrary code execution from a cloned repo's config.
//
// That opt-in used to be one process-wide environment variable. It answers the
// wrong question: WARDEN_MCP_ALLOW_RUN=1 says "this server may run commands",
// not "this server may run THIS repo's commands". An MCP server is long-lived
// and an agent moves between checkouts, so trusting one repo silently trusted
// every repo the server was later pointed at — including one cloned minutes
// later from anywhere. The grant has to name its subject, exactly as git's own
// `safe.directory` does for the same class of problem.
//
// The store lives beside the signing key under the per-user config dir, not in
// the repo: a repository must never be able to declare itself trusted.
package trust

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// fileName is the allowlist's name inside the config dir. One repo path per
// line; blank lines and #-comments are ignored, so it stays hand-editable and
// reviewable — an operator must be able to read what they have granted.
const fileName = "trusted-repos"

// Store is an allowlist of trusted repository paths rooted at a config dir.
type Store struct{ dir string }

// New returns a Store rooted at dir (typically signing.DefaultDir(), which
// honors WARDEN_CONFIG_DIR so tests and CI stay hermetic).
func New(dir string) *Store { return &Store{dir: dir} }

// path is the allowlist file's location.
func (s *Store) path() string { return filepath.Join(s.dir, fileName) }

// Trusted reports whether repoPath has been explicitly trusted.
//
// It fails CLOSED: any error reading the allowlist — missing, unreadable,
// malformed — yields false. A trust check that cannot read its own policy has
// not established trust, and the safe reading of "I don't know" is "no".
func (s *Store) Trusted(repoPath string) bool {
	want, err := canonical(repoPath)
	if err != nil {
		return false
	}
	entries, err := s.List()
	if err != nil {
		return false
	}
	for _, e := range entries {
		// Compare canonically: Add() writes a canonical path, but the file is
		// documented as hand-editable and a hand-written entry can name the same
		// repo by another spelling — most commonly a symlinked prefix (/var vs
		// /private/var on macOS). Matching the raw string would silently refuse a
		// grant the operator can plainly see in the file.
		if sameRepo(e, want) {
			return true
		}
	}
	return false
}

// List returns the trusted paths, sorted. A missing allowlist is an empty list,
// not an error: nothing trusted yet is the normal initial state.
func (s *Store) List() ([]string, error) {
	f, err := os.Open(s.path())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read trust list: %w", err)
	}
	defer func() { _ = f.Close() }()

	var out []string
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !seen[line] {
			seen[line] = true
			out = append(out, line)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read trust list: %w", err)
	}
	sort.Strings(out)
	return out, nil
}

// Add trusts repoPath. It is idempotent, and it resolves the path first so that
// a relative path, a symlinked checkout and the path itself all name one entry —
// otherwise the same repo could be trusted under several spellings and revoking
// one would leave the others granting access.
func (s *Store) Add(repoPath string) (string, error) {
	canon, err := canonical(repoPath)
	if err != nil {
		return "", err
	}
	entries, err := s.List()
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if sameRepo(e, canon) {
			return canon, nil // already trusted, possibly under another spelling
		}
	}
	entries = append(entries, canon)
	return canon, s.write(entries)
}

// Remove revokes trust for repoPath. Removing an untrusted path is not an error:
// the caller's intent — "this must not be trusted" — is satisfied either way.
func (s *Store) Remove(repoPath string) (string, error) {
	canon, err := canonical(repoPath)
	if err != nil {
		return "", err
	}
	entries, err := s.List()
	if err != nil {
		return "", err
	}
	// Drop EVERY spelling of the repo, not just the exact string. A revoke that
	// leaves a differently-spelled entry behind still grants shell execution
	// while reporting that it does not — the worst possible outcome for this
	// particular file.
	kept := make([]string, 0, len(entries))
	for _, e := range entries {
		if !sameRepo(e, canon) {
			kept = append(kept, e)
		}
	}
	return canon, s.write(kept)
}

// sameRepo reports whether a stored entry names the same repository as an
// already-canonical path, resolving the entry first so two spellings of one repo
// compare equal. An entry that cannot be resolved falls back to a literal
// comparison rather than silently matching nothing.
func sameRepo(entry, canon string) bool {
	if resolved, err := canonical(entry); err == nil {
		return resolved == canon
	}
	return entry == canon
}

// write persists the allowlist 0600 inside a 0700 dir, matching how the signing
// key beside it is stored: the file names every repo an agent may execute code
// from, so it is not world-readable.
func (s *Store) write(entries []string) error {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	sort.Strings(entries)
	var b strings.Builder
	b.WriteString("# Repositories whose .warden.yaml commands the non-interactive agent\n")
	b.WriteString("# surfaces (mcp serve, axi run-trigger) may execute. One path per line.\n")
	b.WriteString("# Manage with: warden trust add|list|remove\n")
	for _, e := range entries {
		b.WriteString(e)
		b.WriteString("\n")
	}
	if err := os.WriteFile(s.path(), []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("write trust list: %w", err)
	}
	return nil
}

// canonical resolves a repo path to the single spelling the allowlist stores:
// absolute, with symlinks resolved and any trailing separator dropped.
//
// The resolution matters for the security property, not just for tidiness. A
// grant recorded as one spelling and checked as another either fails open (two
// entries, revoking one leaves the other) or fails to match at all. EvalSymlinks
// additionally stops a symlink planted at a trusted path from redirecting the
// grant to a different tree.
func canonical(repoPath string) (string, error) {
	if strings.TrimSpace(repoPath) == "" {
		return "", errors.New("trust: empty repository path")
	}
	abs, err := filepath.Abs(repoPath)
	if err != nil {
		return "", fmt.Errorf("resolve repo path: %w", err)
	}
	// Resolve symlinks when the path exists. A path that does not exist yet
	// still canonicalizes (to its absolute, cleaned form) so it can be listed or
	// removed — only its symlink resolution is unavailable.
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	return filepath.Clean(abs), nil
}
