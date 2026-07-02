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
url="https://github.com/$REPO/releases/download/$VERSION/warden_${ver}_${os}_${arch}.tar.gz"

mkdir -p "$BIN_DIR"
echo "downloading warden $VERSION ($os/$arch)…"
curl -fsSL "$url" | tar -xz -C "$BIN_DIR" warden
chmod +x "$BIN_DIR/warden"

echo "installed: $BIN_DIR/warden"
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) echo "add to PATH:  export PATH=\"$BIN_DIR:\$PATH\"" ;;
esac
