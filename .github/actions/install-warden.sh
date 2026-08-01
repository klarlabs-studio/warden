#!/usr/bin/env bash
# Install the warden CLI for a composite action, from a released and
# checksum-verified binary.
#
# Deliberately NOT `go install`. Verifying provenance is not a Go build step,
# and requiring a Go toolchain coupled every consumer of these actions to a root
# go.mod: in a monorepo whose module lives in a subdirectory, `setup-go` with
# `go-version-file: go.mod` fails, and the job dies BEFORE verifying anything.
# A check that is permanently red is indistinguishable from a broken one, so it
# was providing zero verification while appearing to be enforced (#92).
#
# WARDEN_VERSION: a release tag (v0.18.16 / 0.18.16) or "latest".
set -euo pipefail

repo="klarlabs-studio/warden"
ver="${WARDEN_VERSION:-latest}"
ver="${ver#v}"

if [ -z "$ver" ] || [ "$ver" = "latest" ]; then
  # Resolve the newest release tag. GITHUB_TOKEN, when present, avoids the
  # unauthenticated rate limit on busy runners.
  auth=()
  if [ -n "${GITHUB_TOKEN:-}" ]; then auth=(-H "Authorization: Bearer ${GITHUB_TOKEN}"); fi
  # ${a[@]+"${a[@]}"} so an EMPTY array does not trip `set -u` on bash < 4.4
  # (macOS ships 3.2); runners have bash 5, but this script should not be
  # quietly unrunnable anywhere someone tries it by hand.
  ver=$(curl -fsSL ${auth[@]+"${auth[@]}"} "https://api.github.com/repos/${repo}/releases/latest" |
    sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"v\{0,1\}\([^"]*\)".*/\1/p' | head -n1)
  [ -n "$ver" ] || { echo "warden: could not resolve the latest release tag" >&2; exit 1; }
fi

# $ver is interpolated straight into the download URL below, so constrain it to
# a release tag before it gets there. Without this, WARDEN_VERSION of
# "../../../../someone/else/releases/download/v1" traverses out of this repo's
# path and both the archive AND checksums.txt resolve to github.com/someone/else
# — so the digest check further down passes against the attacker's own checksum
# file and verifies nothing. Validating here covers the API-resolved tag too.
if [[ ! $ver =~ ^v?[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$ ]]; then
  echo "warden: refusing version '$ver': expected a release tag like 0.20.4" >&2
  exit 1
fi

os=$(uname -s | tr '[:upper:]' '[:lower:]')
arch=$(uname -m)
case "$arch" in x86_64 | amd64) arch=amd64 ;; aarch64 | arm64) arch=arm64 ;; esac

archive="warden_${ver}_${os}_${arch}.tar.gz"
base="https://github.com/${repo}/releases/download/v${ver}"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

if curl -fsSL "$base/$archive" -o "$tmp/$archive" &&
  curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt"; then
  # Fail closed: the digest must match the one published for this exact tag
  # before anything is made executable.
  want=$(awk -v f="$archive" '$2 == f {print $1}' "$tmp/checksums.txt" | head -n1)
  got=$(sha256sum "$tmp/$archive" | awk '{print $1}')
  if [ -z "$want" ] || [ "$want" != "$got" ]; then
    echo "warden: CHECKSUM MISMATCH for $archive (expected ${want:-<none published>}, got $got)" >&2
    echo "warden: refusing to execute an unverified binary" >&2
    exit 1
  fi
  bindir="${RUNNER_TEMP:-$tmp}/warden-bin"
  mkdir -p "$bindir"
  tar -xzf "$tmp/$archive" -C "$bindir" warden
  chmod +x "$bindir/warden"
  echo "$bindir" >>"$GITHUB_PATH"
  "$bindir/warden" --version
elif command -v go >/dev/null 2>&1; then
  # No published binary for this platform/version — fall back to a source build
  # when a toolchain happens to be available, rather than failing outright.
  echo "warden: no release binary for ${os}/${arch} at v${ver}; falling back to go install" >&2
  GOTOOLCHAIN=auto go install "go.klarlabs.de/${repo#*/}@v${ver}"
  echo "$(go env GOPATH)/bin" >>"$GITHUB_PATH"
  "$(go env GOPATH)/bin/warden" --version
else
  echo "warden: cannot install — no release binary for ${os}/${arch} at v${ver}, and no Go toolchain to build from source" >&2
  exit 1
fi
