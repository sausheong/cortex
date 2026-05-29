// Package mcp implements the MCP server for cortex. It is invoked from the
// cortex CLI as `cortex mcp [--transport stdio|http] [--addr ...]`.
package mcp

import (
	"fmt"
	"os"

	"github.com/mark3labs/mcp-go/server"
	"github.com/sausheong/cortex"
)

// Config holds runtime options for the MCP server. Token is used only when
// Transport == "http"; when empty the /mcp endpoint is unauthenticated.
type Config struct {
	Transport string // "stdio" (default) or "http"
	Addr      string // host:port for http transport
	Token     string // optional bearer token for http transport
}

// Serve wires the cortex tool set into a new MCP server and runs it on the
// requested transport. Blocks until the transport returns. Returns an error
// for unknown transports or underlying serve errors.
func Serve(cx *cortex.Cortex, cfg Config) error {
	s := server.NewMCPServer("cortex", "1.0.0", server.WithToolCapabilities(false))
	RegisterTools(s, cx)

	switch cfg.Transport {
	case "", "stdio":
		return serveStdio(s)
	case "http":
		if cfg.Addr == "" {
			cfg.Addr = "127.0.0.1:8080"
		}
		return serveHTTP(s, cfg.Addr, cfg.Token)
	default:
		return fmt.Errorf("invalid transport %q (must be stdio or http)", cfg.Transport)
	}
}

// EnvOr returns the value of env key when set and non-empty, otherwise fallback.
// Exposed for callers that want to honor CORTEX_TRANSPORT / CORTEX_ADDR /
// CORTEX_AUTH_TOKEN with the same precedence as the CLI flags.
func EnvOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
