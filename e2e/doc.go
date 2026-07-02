// Package e2e holds warden's end-to-end tests, which build only under the `e2e`
// build tag (see e2e_test.go). This unconstrained file gives the package a
// buildable Go file under the default build so `go list ./...` and coverage
// tooling don't fail on an otherwise fully build-excluded directory.
package e2e
