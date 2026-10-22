package mcp

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// Behind a reverse proxy the bind is loopback; the token must still be
// enforced whenever it is set, on any bind address.
func TestServeHTTP_LoopbackBindEnforcesToken(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	if !isLoopback(addr) {
		t.Fatalf("test addr %q should be loopback", addr)
	}

	s := server.NewMCPServer("test", "0.0.0", server.WithToolCapabilities(false))
	const tok = "s3cret"
	go func() { _ = serveHTTP(s, addr, tok) }()

	base := "http://" + addr
	for i := 0; i < 50; i++ {
		if resp, err := http.Get(base + "/healthz"); err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	const initBody = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`
	post := func(auth string) int {
		req, _ := http.NewRequest(http.MethodPost, base+"/mcp", strings.NewReader(initBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if got := post(""); got != http.StatusUnauthorized {
		t.Errorf("no token: got %d, want 401", got)
	}
	if got := post("Bearer wrong"); got != http.StatusUnauthorized {
		t.Errorf("wrong token: got %d, want 401", got)
	}
	if got := post("Bearer " + tok); got != http.StatusOK {
		t.Errorf("correct token: got %d, want 200", got)
	}
	resp, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("healthz unauthenticated: got %d, want 200", resp.StatusCode)
	}
}
