package cli

import (
	"context"
	"fmt"
	"io"

	mcpserver "go.klarlabs.de/warden/internal/mcp"
)

// cmdMCP handles `warden mcp serve`, exposing Warden's operation set as MCP
// tools over stdio (§4.6).
func cmdMCP(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 || args[0] != "serve" {
		fmt.Fprintln(stderr, "usage: warden mcp serve")
		return 2
	}
	f, err := newFacade()
	if err != nil {
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
