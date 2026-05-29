package mcp

import (
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/mark3labs/mcp-go/server"
)

// buildHTTPMux composes the streamable-HTTP MCP handler with optional bearer-
// token auth and an unauthenticated /healthz endpoint. Factored out of
// serveHTTP so it can be tested without binding a port.
func buildHTTPMux(s *server.MCPServer, token string) *http.ServeMux {
	streamable := server.NewStreamableHTTPServer(s)

	mux := http.NewServeMux()
	var mcpHandler http.Handler = streamable
	if token != "" {
		mcpHandler = bearerAuthMiddleware(streamable, token)
	}
	mux.Handle("/mcp", mcpHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	return mux
}

// maybeWarnNonLoopback writes a warning to w when binding to a non-loopback
// address without an auth token. Extracted for testability.
func maybeWarnNonLoopback(w io.Writer, addr, token string) {
	if !isLoopback(addr) && token == "" {
		fmt.Fprintln(w, "warning: HTTP transport bound to non-loopback address without CORTEX_AUTH_TOKEN — anyone on the network can call destructive tools")
	}
}

// serveHTTP runs the MCP server on the streamable-HTTP transport at /mcp on
// the given address. If token is non-empty, /mcp requires a matching
// Authorization: Bearer header. /healthz is always unauthenticated.
func serveHTTP(s *server.MCPServer, addr, token string) error {
	mux := buildHTTPMux(s, token)
	maybeWarnNonLoopback(os.Stderr, addr, token)
	fmt.Fprintf(os.Stderr, "cortex mcp: streamable-http transport ready on %s/mcp\n", addr)
	return http.ListenAndServe(addr, mux)
}
