package cli

import "runtime/debug"

// Version is the warden version, set at build time via -ldflags.
//
// The literal here is a last resort, and it used to be the only one: a binary
// installed with `go install` — which sets no ldflags — reported whatever
// number happened to be in the source, indefinitely. That is a nuisance in
// `warden --version` and a defect in `warden evidence`, where the producing
// tool's version is part of the artifact an auditor relies on. A stale one
// there is a false statement in an evidence document.
//
// So when ldflags did not set it, ask the binary what it was built from.
var Version = resolveVersion(defaultVersion, debug.ReadBuildInfo)

// defaultVersion is what a source tree that was never stamped reports.
const defaultVersion = "0.9.0"

// resolveVersion prefers the module version the binary records over the
// compiled-in literal. Build info is absent for `go run` and for test
// binaries, in which case the literal stands.
func resolveVersion(literal string, readInfo func() (*debug.BuildInfo, bool)) string {
	if literal != defaultVersion {
		return literal // ldflags won; they are the authority
	}
	info, ok := readInfo()
	if !ok || info == nil {
		return literal
	}
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	// A locally-built binary knows its commit even when it has no version.
	// "dev" plus a revision beats a version number that is simply untrue.
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 12 {
			return "dev+" + s.Value[:12]
		}
	}
	return literal
}
