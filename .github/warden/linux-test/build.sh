#!/bin/sh
# Only these four files enter the image build context.
set -eu
engine=${1:-docker}
platform=${2:-linux/amd64}
tag=${3:-local/warden-linux-test:pilot}
case "$engine" in docker|podman|nerdctl|container) ;; *) echo 'unsupported build engine' >&2; exit 2;; esac
case "$platform" in linux/amd64|linux/arm64) ;; *) echo 'unsupported platform' >&2; exit 2;; esac
root=$(git rev-parse --show-toplevel)
context=$(mktemp -d)
trap 'rm -rf "$context"' EXIT HUP INT TERM
mkdir -p "$context/.github/warden/linux-test"
cp "$root/go.mod" "$root/go.sum" "$context/"
cp "$root/.github/warden/linux-test/Dockerfile" "$context/Dockerfile"
cp "$root/.github/warden/linux-test/run.sh" "$context/.github/warden/linux-test/run.sh"
"$engine" build --platform "$platform" -f "$context/Dockerfile" -t "$tag" "$context"
