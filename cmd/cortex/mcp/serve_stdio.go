package mcp

import (
	"fmt"
	"os"

	"github.com/mark3labs/mcp-go/server"
)

// serveStdio runs the MCP server on stdin/stdout. Blocks until the connection
// closes or errors.
func serveStdio(s *server.MCPServer) error {
	fmt.Fprintln(os.Stderr, "cortex mcp: stdio transport ready")
	return server.ServeStdio(s)
}
