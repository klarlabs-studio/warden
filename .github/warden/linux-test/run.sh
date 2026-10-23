#!/bin/sh
# One identical command for local Warden and the remote shadow pilot.
set -eu
export GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off GOFLAGS=-mod=readonly CGO_ENABLED=1
test "$(go env GOVERSION)" = go1.26.7
test "$(go env GOOS)" = linux
go mod verify
go test -count=1 -race -covermode=atomic -coverprofile=coverage.out ./...
test -s coverage.out
coverctl check --from-profile --profile=coverage.out
