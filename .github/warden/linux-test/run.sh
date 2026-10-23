#!/bin/sh
# One identical command for local Warden and the remote shadow pilot.
set -eu
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOFLAGS=-mod=readonly CGO_ENABLED=1
test "$(go env GOVERSION)" = go1.26.7
test "$(go env GOOS)" = linux
command -v python3
command -v node
command -v jq
go mod verify
# Cross-package instrumentation preserves coverage from CLI integration tests
# that exercise service/domain code through imports.
go test -count=1 -race -coverpkg=./... -covermode=atomic -coverprofile=coverage.out ./...
test -s coverage.out
coverctl check --from-profile --profile=coverage.out
