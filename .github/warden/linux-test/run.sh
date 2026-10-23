#!/bin/sh
# One identical command for local Warden and the remote shadow pilot.
set -eu
# Warden preserves the host UID. OpenSSH refuses to run when that UID has no
# passwd entry; provide a private lookup database without modifying the image.
if ! getent passwd "$(id -u)" >/dev/null; then
  identity_dir=$(mktemp -d)
  trap 'rm -rf "$identity_dir"' EXIT HUP INT TERM
  cp /etc/passwd "$identity_dir/passwd"
  cp /etc/group "$identity_dir/group"
  printf 'warden:x:%s:%s:Warden:%s:/bin/sh\n' "$(id -u)" "$(id -g)" "${HOME:-/tmp}" >> "$identity_dir/passwd"
  if ! getent group "$(id -g)" >/dev/null; then
    printf 'warden:x:%s:\n' "$(id -g)" >> "$identity_dir/group"
  fi
  wrapper=$(cat /opt/warden-nss-wrapper-path)
  test -r "$wrapper"
  export NSS_WRAPPER_PASSWD="$identity_dir/passwd" NSS_WRAPPER_GROUP="$identity_dir/group"
  export LD_PRELOAD="$wrapper${LD_PRELOAD:+:$LD_PRELOAD}"
fi
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOFLAGS=-mod=readonly CGO_ENABLED=1
test "$(go env GOVERSION)" = go1.26.7
test "$(go env GOOS)" = linux
command -v python3
command -v node
command -v jq
# Do not let an unavailable user mapping turn SSH interoperability into skips.
probe=$(mktemp -d)
ssh-keygen -q -t ed25519 -N '' -C warden-probe -f "$probe/key"
rm -rf "$probe"
go mod verify
# Cross-package instrumentation preserves coverage from CLI integration tests
# that exercise service/domain code through imports.
go test -count=1 -race -coverpkg=./... -covermode=atomic -coverprofile=coverage.out ./...
test -s coverage.out
coverctl check --from-profile --profile=coverage.out
