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
//
// Supply-chain integrity: the self-fetched tarball is verified against the
// SHA-256 published in the release's checksums.txt *before* it is ever made
// executable — a mismatch (or an absent tool/checksum) fails closed (exit 1).
// The cached binary is re-verified on every run against the digest recorded at
// install time, so post-install tampering or bitrot is caught and re-fetched.
// The cache lives in a user-only (0700) directory. Residual gap: checksums.txt
// is fetched over the same TLS/host as the tarball and is not yet signature-
// verified here, so this defends against corruption, CDN/asset tampering and
// accidental drift, but not a determined TLS-breaking MITM — signature
// verification of a cosign-signed checksums.txt is the follow-up that closes it.
func shim(hook domain.Hook, version string) string {
	return fmt.Sprintf(`#!/bin/sh
%s
# pinned: %s
hook=%s
ver=%q

# --- integrity helpers -------------------------------------------------------
# Print the SHA-256 of $1 as a bare hex digest (empty if no tool is available).
_wd_sha256() {
  if   command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | awk '{print $1}'
  elif command -v shasum    >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'
  else echo ""; fi
}
_wd_have_sha() { command -v sha256sum >/dev/null 2>&1 || command -v shasum >/dev/null 2>&1; }
_wd_fetch() { # $1 url  $2 dest-file
  if   command -v curl >/dev/null 2>&1; then curl -fsSL "$1" -o "$2"
  elif command -v wget >/dev/null 2>&1; then wget -qO "$2" "$1"
  else echo "warden: need curl or wget to fetch $1" >&2; return 1; fi
}

# Resolve the warden binary: prefer one on PATH, else the pinned cached binary
# (fetched once, checksum-verified), so a repo adopted with no global install
# keeps working — and the whole team runs the *same* verified binary.
if command -v warden >/dev/null 2>&1; then
  bin=warden
else
  case "$ver" in ""|*dev*|*snapshot*)
    echo "warden: not installed and no pinned release ($ver); install: npx @klarlabs-studio/warden, or https://github.com/%s" >&2
    exit 1 ;;
  esac
  bindir="$HOME/.warden/bin/$ver"
  bin="$bindir/warden"
  sha_file="$bindir/warden.sha256"

  # Re-verify the cached binary on *every* run against the digest recorded at
  # verified-install time. A mismatch (tamper, bitrot, partial write) discards
  # the cache and forces a fresh, checksum-verified download — we never exec an
  # unverified binary.
  if [ -x "$bin" ]; then
    _wd_want=$(cat "$sha_file" 2>/dev/null || echo "")
    _wd_got=$(_wd_sha256 "$bin")
    if [ -z "$_wd_want" ] || [ -z "$_wd_got" ] || [ "$_wd_want" != "$_wd_got" ]; then
      echo "warden: cached binary failed its integrity check — refetching" >&2
      rm -f "$bin" "$sha_file"
    fi
  fi

  if [ ! -x "$bin" ]; then
    if ! _wd_have_sha; then
      echo "warden: cannot verify the download (need sha256sum or shasum); put warden on PATH instead" >&2
      exit 1
    fi
    os=$(uname -s | tr '[:upper:]' '[:lower:]')
    arch=$(uname -m)
    case "$arch" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; esac
    archive="warden_${ver}_${os}_${arch}.tar.gz"
    base="https://github.com/%s/releases/download/v$ver"

    # Private, user-only cache dir (0700): keep other local accounts from
    # planting a binary we would later exec.
    mkdir -p "$bindir" || { echo "warden: cannot create $bindir" >&2; exit 1; }
    chmod 700 "$HOME/.warden" "$HOME/.warden/bin" "$bindir" 2>/dev/null || true

    tmp=$(mktemp -d "${TMPDIR:-/tmp}/warden.XXXXXX") || { echo "warden: mktemp failed" >&2; exit 1; }
    trap 'rm -rf "$tmp"' EXIT INT TERM

    echo "warden: fetching pinned binary $ver ($os/$arch)…" >&2
    _wd_fetch "$base/$archive"      "$tmp/$archive"      || { echo "warden: download failed ($base/$archive)" >&2; exit 1; }
    _wd_fetch "$base/checksums.txt" "$tmp/checksums.txt" || { echo "warden: checksums download failed ($base/checksums.txt)" >&2; exit 1; }

    # Fail closed: the archive digest MUST match the one published for this exact
    # release tag before we make anything executable.
    want=$(awk -v f="$archive" '$2==f {print $1}' "$tmp/checksums.txt" | head -n1)
    [ -n "$want" ] || { echo "warden: no checksum for $archive in checksums.txt — refusing to run" >&2; exit 1; }
    got=$(_wd_sha256 "$tmp/$archive")
    if [ "$want" != "$got" ]; then
      echo "warden: CHECKSUM MISMATCH for $archive" >&2
      echo "warden:   expected $want" >&2
      echo "warden:   got      $got" >&2
      echo "warden: refusing to execute an unverified binary" >&2
      exit 1
    fi

    # Verified — now (and only now) extract, record the binary's own digest for
    # future re-verification, mark executable, and install into the cache.
    tar -xzf "$tmp/$archive" -C "$tmp" warden || { echo "warden: extract failed ($archive)" >&2; exit 1; }
    _wd_sha256 "$tmp/warden" > "$tmp/warden.sha256"
    chmod +x "$tmp/warden"
    mv -f "$tmp/warden"        "$bin"
    mv -f "$tmp/warden.sha256" "$sha_file"
    rm -rf "$tmp"; trap - EXIT INT TERM
  fi
fi

# Preflight: a binary that can't start (Gatekeeper-quarantined, corrupt, blocked)
# hangs on exec and would wedge every commit/push. Time-box a trivial version
# call so a broken binary fails fast with an actionable message instead.
_wd_timeout() {
  _t=$1; shift
  if command -v timeout  >/dev/null 2>&1; then timeout  "$_t" "$@"; return $?; fi
  if command -v gtimeout >/dev/null 2>&1; then gtimeout "$_t" "$@"; return $?; fi
  if command -v perl     >/dev/null 2>&1; then
    perl -e '$t=shift; $SIG{ALRM}=sub{exit 124}; alarm $t; exit(system(@ARGV) >> 8)' "$_t" "$@"; return $?
  fi
  "$@"  # no timeout tool available — best effort
}
if ! _wd_timeout 15 "$bin" --version >/dev/null 2>&1; then
  echo "warden: '$bin' is installed but not runnable (Gatekeeper-quarantined, corrupt, or blocked)." >&2
  echo "warden: fix it (macOS: xattr -dr com.apple.quarantine \"$bin\"; or reinstall), then retry." >&2
  echo "warden: to commit once without the gate: git commit --no-verify" >&2
  exit 1
fi

exec "$bin" run "$hook"
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
