// Package hooks installs and manages the native git hook shims that trigger
// Warden (§4.1). Each shim is a tiny script that execs the warden binary for
// the matching hook; the real work stays in the daemon, never the shim.
package hooks

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.klarlabs.de/warden/internal/domain"
)

// managedMarker identifies a hook file Warden owns, so enable/disable can
// safely overwrite or remove it without clobbering a hand-written hook.
const managedMarker = "# warden-managed-hook"

// shim is the hook script body. It forwards git's stdin (the pre-push ref list)
// to warden and exits with warden's status, so a failing gate blocks git.
func shim(hook domain.Hook) string {
	return fmt.Sprintf(`#!/bin/sh
%s
exec warden run %s
`, managedMarker, hook)
}

// Install writes shims for the given hooks under .git/hooks, making them
// executable. It refuses to overwrite an existing hook Warden does not manage,
// surfacing the conflict rather than silently replacing the user's script.
func Install(gitDir string, hooks []domain.Hook) error {
	dir := filepath.Join(gitDir, "hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create hooks dir: %w", err)
	}
	for _, h := range hooks {
		if err := writeHook(dir, h); err != nil {
			return err
		}
	}
	return nil
}

func writeHook(dir string, h domain.Hook) error {
	path := filepath.Join(dir, string(h))
	if existing, err := os.ReadFile(path); err == nil {
		if !strings.Contains(string(existing), managedMarker) {
			return fmt.Errorf("%s already exists and is not warden-managed; refusing to overwrite", path)
		}
	}
	if err := os.WriteFile(path, []byte(shim(h)), 0o755); err != nil {
		return fmt.Errorf("write %s hook: %w", h, err)
	}
	return nil
}

// Remove deletes a Warden-managed hook shim. A non-managed or absent file is
// left untouched, so disabling never destroys a user's own hook.
func Remove(gitDir string, h domain.Hook) error {
	path := filepath.Join(gitDir, "hooks", string(h))
	existing, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !strings.Contains(string(existing), managedMarker) {
		return fmt.Errorf("%s is not warden-managed; leaving it in place", path)
	}
	return os.Remove(path)
}

// Installed reports which hooks currently have a Warden-managed shim.
func Installed(gitDir string) map[domain.Hook]bool {
	out := map[domain.Hook]bool{}
	for _, h := range domain.AllHooks {
		path := filepath.Join(gitDir, "hooks", string(h))
		if data, err := os.ReadFile(path); err == nil && strings.Contains(string(data), managedMarker) {
			out[h] = true
		}
	}
	return out
}
