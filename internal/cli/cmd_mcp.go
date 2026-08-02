package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"go.klarlabs.de/warden/internal/infrastructure/git"
	mcpserver "go.klarlabs.de/warden/internal/mcp"
)

// cmdMCP handles `warden mcp serve`, exposing Warden's operation set as MCP
// tools over stdio (§4.6).
func cmdMCP(args []string, _, stderr io.Writer) int {
	if len(args) < 1 || args[0] != "serve" {
		_, _ = fmt.Fprintln(stderr, "usage: warden mcp serve")
		return 2
	}
	var f mcpserver.Facade
	built, err := newFacade()
	switch {
	case err == nil:
		f = built
	case errors.Is(err, git.ErrNotARepository):
		// Serve anyway. An MCP client launches this as a subprocess and reads
		// the protocol on stdout; exiting during startup shows up as "server
		// exited" with the reason on a stderr stream most clients discard. The
		// working directory is the client's choice, not the user's, so landing
		// outside a repository is a likely first run. Every tool now answers
		// with what is wrong and how to fix it — nothing is pretended to work.
		_, _ = fmt.Fprintf(stderr, "warden: %v\n", err)
		_, _ = fmt.Fprintln(stderr, "warden: serving MCP anyway; every tool will explain this until the server is restarted inside a repository.")
		f = &unavailableFacade{dir: mustCwd(), cause: err}
	default:
		// A repository that exists but cannot be read is not user-fixable by
		// relaunching elsewhere, so it still fails at startup.
		return fail(stderr, err)
	}
	// An MCP client cannot pass CLI flags, so run_trigger's trust opt-in for
	// this surface is the WARDEN_MCP_ALLOW_RUN env var alone; the gate is
	// evaluated per call so the read-only tools stay available regardless.
	gate := mcpserver.RunGate(func() error {
		if mcpRunTrusted(false) {
			return nil
		}
		return errUntrustedMCPRun()
	})
	if err := mcpserver.Serve(context.Background(), f, Version, gate); err != nil {
		return fail(stderr, err)
	}
	return 0
}
