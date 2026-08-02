package cli

import (
	"context"
	"errors"
	"strings"
	"testing"

	mcpclient "go.klarlabs.de/mcp"
	"go.klarlabs.de/warden/internal/domain"
	"go.klarlabs.de/warden/internal/infrastructure/git"
	mcpserver "go.klarlabs.de/warden/internal/mcp"
)

// Starting outside a repository used to exit 1 before a word of MCP was spoken.
// Over stdio that reaches the user as "server exited", with the reason on a
// stderr stream most clients discard — and the working directory is the
// client's choice, not theirs, so it is a likely first run rather than a rare
// mistake.
func TestUnavailableFacade_EveryToolExplainsItself(t *testing.T) {
	u := &unavailableFacade{dir: "/some/dir", cause: git.ErrNotARepository}

	calls := map[string]func() error{
		"PolicyExplain": func() error { _, err := u.PolicyExplain(domain.PreCommit, "main", nil); return err },
		"StepsList":     func() error { _, _, err := u.StepsList(); return err },
		"RunTrigger":    func() error { _, err := u.RunTrigger(context.Background(), domain.PrePush); return err },
		"RunTriggerStreaming": func() error {
			_, err := u.RunTriggerStreaming(context.Background(), domain.PrePush, func(mcpserver.StepProgress) {})
			return err
		},
		"Verify":      func() error { _, err := u.Verify("HEAD", nil); return err },
		"VerifyRange": func() error { _, err := u.VerifyRange("a", "b", mcpserver.RangeVerifyRequest{}); return err },
		"Doctor":      func() error { _, err := u.Doctor("main"); return err },
		"Audit":       func() error { _, err := u.Audit("main"); return err },
		"Status":      func() error { _, err := u.Status(); return err },
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			err := call()
			if err == nil {
				t.Fatal("returned success while warden has no repository to work in")
			}
			// The directory must be named. "not a git repository" without
			// saying which one leaves the user guessing which cwd warden saw.
			if !strings.Contains(err.Error(), "/some/dir") {
				t.Errorf("message does not name the directory: %v", err)
			}
			// It must survive as the sentinel for callers matching on it.
			if !errors.Is(err, git.ErrNotARepository) {
				t.Errorf("cause was flattened away: %v", err)
			}
			// And it must be marked visible, or the dispatcher replaces it with
			// a bare "internal error" and the explanation never leaves the
			// process — which is the whole failure being fixed.
			var vis *mcpclient.ToolInputError
			if !errors.As(err, &vis) {
				t.Errorf("not marked visible; the client would see only \"internal error\": %v", err)
			}
			if !strings.Contains(vis.Message, "/some/dir") {
				t.Errorf("visible message lost the directory: %s", vis.Message)
			}
		})
	}
}

// Open must return the sentinel, or cmdMCP cannot tell "you pointed me
// somewhere without a repo" from any other startup failure and would degrade
// on both.
func TestOpen_NonRepoReturnsSentinel(t *testing.T) {
	if _, err := git.Open(t.TempDir()); !errors.Is(err, git.ErrNotARepository) {
		t.Fatalf("git.Open on a non-repo = %v, want it to wrap ErrNotARepository", err)
	}
}
