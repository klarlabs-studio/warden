// Package e2e holds warden's opt-in end-to-end tests, gated on WARDEN_E2E (see
// e2e_test.go). This file gives the package a regular, always-buildable Go file
// so `go list ./...` and coverage tooling never trip over a test-only package.
package e2e
