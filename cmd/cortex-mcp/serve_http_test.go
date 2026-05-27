package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"
)

func TestBuildHTTPMux_HealthzNeverAuthenticated(t *testing.T) {
	s := server.NewMCPServer("test", "0.0.0", server.WithToolCapabilities(false))
	mux := buildHTTPMux(s, "any-token")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("healthz with no auth header should return 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Errorf("unexpected healthz body: %q", rec.Body.String())
	}
}

func TestBuildHTTPMux_MCPAuthGated(t *testing.T) {
	s := server.NewMCPServer("test", "0.0.0", server.WithToolCapabilities(false))
	mux := buildHTTPMux(s, "s3cret")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("/mcp without auth should be 401, got %d", rec.Code)
	}
}

func TestBuildHTTPMux_NoTokenSkipsAuth(t *testing.T) {
	s := server.NewMCPServer("test", "0.0.0", server.WithToolCapabilities(false))
	mux := buildHTTPMux(s, "")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`)))
	// Without a token, the request reaches the streamable HTTP handler and gets
	// whatever mcp-go decides (likely a 4xx/5xx for malformed input), but
	// crucially NOT a 401 from our middleware.
	if rec.Code == http.StatusUnauthorized {
		t.Errorf("/mcp without configured token should not return 401, got %d", rec.Code)
	}
}

func TestMaybeWarnNonLoopback(t *testing.T) {
	cases := []struct {
		name    string
		addr    string
		token   string
		warnSub string // substring expected in output; "" = expect no warning
	}{
		{"non-loopback no token warns", "0.0.0.0:8080", "", "non-loopback"},
		{"non-loopback with token silent", "0.0.0.0:8080", "tok", ""},
		{"loopback no token silent", "127.0.0.1:8080", "", ""},
		{"loopback with token silent", "127.0.0.1:8080", "tok", ""},
		{"empty host treated as non-loopback warns", ":8080", "", "non-loopback"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			maybeWarnNonLoopback(&buf, c.addr, c.token)
			out := buf.String()
			if c.warnSub == "" {
				if out != "" {
					t.Errorf("expected no warning, got %q", out)
				}
			} else if !strings.Contains(out, c.warnSub) {
				t.Errorf("expected warning containing %q, got %q", c.warnSub, out)
			}
		})
	}
}
