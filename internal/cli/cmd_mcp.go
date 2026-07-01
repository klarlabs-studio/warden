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
	if err := mcpserver.Serve(context.Background(), f, Version); err != nil {
		return fail(stderr, err)
	}
	return 0
}
