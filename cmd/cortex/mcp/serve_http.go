package mcp

import (
	"context"
	"strings"

	"errors"
	"fmt"
	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
	"io"
	"net/http"
	"os"

	"github.com/mark3labs/mcp-go/server"
)

// buildHTTPMux composes the streamable-HTTP MCP handler with bearer-token auth
// that is always enforced (an empty token rejects every request; serveHTTP
// refuses to start without one) and an unauthenticated /healthz endpoint. Factored out of
// serveHTTP so it can be tested without binding a port.
func buildHTTPMux(s *server.MCPServer, token string) *http.ServeMux {
	streamable := server.NewStreamableHTTPServer(s)

	mux := http.NewServeMux()
	mux.Handle("/mcp", bearerAuthMiddleware(streamable, token))
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
	if token == "" {
		return errors.New("cortex mcp: --transport http requires CORTEX_AUTH_TOKEN; refusing to start without it")
	}
	mux := buildHTTPMux(s, token)
	maybeWarnNonLoopback(os.Stderr, addr, token)
	fmt.Fprintf(os.Stderr, "cortex mcp: streamable-http transport ready on %s/mcp\n", addr)
	return http.ListenAndServe(addr, mux)
}

// Resolver maps a bearer token to a per-user cortex handle. It returns
// accounts.ErrInvalidToken for an unknown, revoked or expired token and
// accounts.ErrNoProvider when the user has no usable AI provider.
type Resolver func(ctx context.Context, bearer string) (cx *cortex.Cortex, err error)

// NewHTTPHandler serves the MCP server over streamable HTTP, resolving each
// request's bearer token to a user handle. A missing or unknown token gets
// 401 with the protected-resource metadata pointer and counts against the
// per-IP failed-lookup limit (429 once exceeded).
func NewHTTPHandler(s *server.MCPServer, resolve Resolver, publicURL string, failLimiter *accounts.Limiter) http.Handler {
	streamable := server.NewStreamableHTTPServer(s, server.WithHTTPContextFunc(
		func(ctx context.Context, r *http.Request) context.Context {
			if v, ok := r.Context().Value(handleKey{}).(handleVal); ok {
				return context.WithValue(ctx, handleKey{}, v)
			}
			return ctx
		}))
	meta := strings.TrimRight(publicURL, "/") + "/.well-known/oauth-protected-resource"
	deny := func(w http.ResponseWriter, r *http.Request) {
		if failLimiter != nil && !failLimiter.Allow(accounts.ClientIP(r)) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":"rate_limited"}`))
			return
		}
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+meta+`"`)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if len(h) <= len(prefix) || !strings.EqualFold(h[:len(prefix)], prefix) {
			deny(w, r)
			return
		}
		cx, err := resolve(r.Context(), strings.TrimSpace(h[len(prefix):]))
		switch {
		case err == nil:
		case errors.Is(err, accounts.ErrInvalidToken):
			deny(w, r)
			return
		case errors.Is(err, accounts.ErrNoProvider):
			cx = nil
		default:
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		streamable.ServeHTTP(w, r.WithContext(withHandle(r.Context(), cx, err)))
	})
}
