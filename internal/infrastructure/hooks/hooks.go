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

// releaseRepo is the GitHub repo the self-bootstrapping shim downloads pinned
// binaries from.
const releaseRepo = "klarlabs-studio/warden"

// shim is the hook script body. It is self-bootstrapping and version-pinned:
// prefer a `warden` on PATH; else use (or fetch, once) a pinned static binary
// cached under ~/.warden/bin/<version>. So a repo adopted with `npx
// @klarlabs-studio/warden init` keeps working on every later commit/push with no global
// install — and the whole team runs the *same* pinned warden. The script
// forwards git's stdin and exits with warden's status, so a failing gate blocks
// git. A version that is not a real release (empty/dev/snapshot) skips the
// download branch and requires a warden on PATH.
func shim(hook domain.Hook, version string) string {
	return fmt.Sprintf(`#!/bin/sh
%s
# pinned: %s
hook=%s
ver=%q

if command -v warden >/dev/null 2>&1; then
  exec warden run "$hook"
fi

case "$ver" in ""|*dev*|*snapshot*)
  echo "warden: not installed and no pinned release ($ver); install: npx @klarlabs-studio/warden, or https://github.com/%s" >&2
  exit 1 ;;
esac

cache="$HOME/.warden/bin/$ver/warden"
if [ ! -x "$cache" ]; then
  os=$(uname -s | tr '[:upper:]' '[:lower:]')
  arch=$(uname -m)
  case "$arch" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; esac
  url="https://github.com/%s/releases/download/v$ver/warden_${ver}_${os}_${arch}.tar.gz"
  mkdir -p "$(dirname "$cache")"
  echo "warden: fetching pinned binary $ver ($os/$arch)…" >&2
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL "$url" | tar -xz -C "$(dirname "$cache")" warden || { echo "warden: download failed ($url)" >&2; exit 1; }
  elif command -v wget >/dev/null 2>&1; then
    wget -qO- "$url" | tar -xz -C "$(dirname "$cache")" warden || { echo "warden: download failed ($url)" >&2; exit 1; }
  else
    echo "warden: need warden on PATH or curl/wget to fetch it" >&2; exit 1
  fi
  chmod +x "$cache"
fi
exec "$cache" run "$hook"
`, managedMarker, version, hook, version, releaseRepo, releaseRepo)
}

// Install writes shims for the given hooks under .git/hooks, making them
// executable. version is baked into each shim so the gate is pinned. It refuses
// to overwrite an existing hook Warden does not manage, surfacing the conflict
// rather than silently replacing the user's script.
func Install(gitDir string, hooks []domain.Hook, version string) error {
	dir := filepath.Join(gitDir, "hooks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create hooks dir: %w", err)
	}
	for _, h := range hooks {
		if err := writeHook(dir, h, version); err != nil {
			return err
		}
	}
	return nil
}

func writeHook(dir string, h domain.Hook, version string) error {
	path := filepath.Join(dir, string(h))
	if existing, err := os.ReadFile(path); err == nil {
		if !strings.Contains(string(existing), managedMarker) {
			return fmt.Errorf("%s already exists and is not warden-managed; refusing to overwrite", path)
		}
	}
	if err := os.WriteFile(path, []byte(shim(h, version)), 0o755); err != nil {
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
