// Package domain holds Warden's core value objects and policy model — the
// ubiquitous language shared across the policy engine, kernel, git, and
// delivery layers. It has no dependency on axi-go or any infrastructure.
package domain

import "fmt"

// Hook identifies a git hook point that Warden gates.
type Hook string

const (
	// PreCommit runs the fast, local step subset before a commit is recorded.
	PreCommit Hook = "pre-commit"
	// PrePush runs the full pipeline before commits leave for origin.
	PrePush Hook = "pre-push"
)

// AllHooks lists every hook Warden can install, in install order.
var AllHooks = []Hook{PreCommit, PrePush}

// Valid reports whether h is a hook Warden understands.
func (h Hook) Valid() bool {
	return h == PreCommit || h == PrePush
}

// ConfigKey returns the snake_case key used for this hook in .warden.yaml.
func (h Hook) ConfigKey() string {
	switch h {
	case PreCommit:
		return "pre_commit"
	case PrePush:
		return "pre_push"
	default:
		return string(h)
	}
}

// ParseHook converts a user-supplied hook name (either "pre-commit" or the
// snake_case "pre_commit" form) into a Hook, rejecting anything unknown.
func ParseHook(s string) (Hook, error) {
	switch s {
	case "pre-commit", "pre_commit":
		return PreCommit, nil
	case "pre-push", "pre_push":
		return PrePush, nil
	default:
		return "", fmt.Errorf("unknown hook %q: want pre-commit or pre-push", s)
	}
}
