#!/bin/sh
# warden installer — no toolchain required. Downloads a prebuilt static binary
# for this OS/arch from GitHub Releases and drops it in ~/.warden/bin.
#
#   curl -fsSL https://raw.githubusercontent.com/klarlabs-studio/warden/main/scripts/install.sh | sh
#
# Env: WARDEN_VERSION (default latest), WARDEN_BIN_DIR (default ~/.warden/bin).
set -eu

REPO="klarlabs-studio/warden"
VERSION="${WARDEN_VERSION:-latest}"
BIN_DIR="${WARDEN_BIN_DIR:-$HOME/.warden/bin}"

os="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$os" in linux | darwin) ;; *) echo "unsupported OS: $os" >&2; exit 1 ;; esac

arch="$(uname -m)"
case "$arch" in
  x86_64 | amd64) arch="amd64" ;;
  aarch64 | arm64) arch="arm64" ;;
  *) echo "unsupported arch: $arch" >&2; exit 1 ;;
esac

if [ "$VERSION" = "latest" ]; then
  VERSION="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p')"
fi
[ -n "$VERSION" ] || { echo "could not resolve latest version" >&2; exit 1; }

ver="${VERSION#v}"
archive="warden_${ver}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$VERSION"

# Pick a SHA-256 tool up front — without one we cannot verify the download, so
# refuse rather than run an unverified binary.
if   command -v sha256sum >/dev/null 2>&1; then sha_cmd="sha256sum"
elif command -v shasum   >/dev/null 2>&1; then sha_cmd="shasum -a 256"
else echo "cannot verify download: need sha256sum or shasum" >&2; exit 1; fi

tmp="$(mktemp -d "${TMPDIR:-/tmp}/warden-install.XXXXXX")" || { echo "mktemp failed" >&2; exit 1; }
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "downloading warden $VERSION ($os/$arch)…"
curl -fsSL "$base/$archive"      -o "$tmp/$archive"      || { echo "download failed ($base/$archive)" >&2; exit 1; }
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt" || { echo "checksums download failed" >&2; exit 1; }

# Fail closed: verify the archive against the published checksum before extract.
want="$(awk -v f="$archive" '$2==f {print $1}' "$tmp/checksums.txt" | head -n1)"
[ -n "$want" ] || { echo "no checksum for $archive in checksums.txt" >&2; exit 1; }
got="$($sha_cmd "$tmp/$archive" | awk '{print $1}')"
if [ "$want" != "$got" ]; then
  echo "checksum mismatch for $archive" >&2
  echo "  expected $want" >&2
  echo "  got      $got" >&2
  exit 1
fi

# Verified — extract into a user-only (0700) cache dir and mark executable.
mkdir -p "$BIN_DIR"
chmod 700 "$BIN_DIR" 2>/dev/null || true
tar -xzf "$tmp/$archive" -C "$BIN_DIR" warden
chmod +x "$BIN_DIR/warden"

echo "installed: $BIN_DIR/warden"
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo "add to PATH:  export PATH=\"$BIN_DIR:\$PATH\"" ;;
esac
