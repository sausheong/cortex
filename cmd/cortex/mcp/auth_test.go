package mcp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsLoopback(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8080", true},
		{"localhost:8080", true},
		{"[::1]:8080", true},
		{"0.0.0.0:8080", false},
		{":8080", false},
		{"192.168.1.5:8080", false},
		{"example.com:8080", false},
	}
	for _, c := range cases {
		got := isLoopback(c.addr)
		if got != c.want {
			t.Errorf("isLoopback(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}

func TestBearerAuthMiddleware_NoToken(t *testing.T) {
	// When token is empty we should not be calling the middleware at all,
	// but verify defensive behavior: empty token + empty header still rejects.
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	h := bearerAuthMiddleware(next, "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if called {
		t.Fatal("downstream handler should not be called")
	}
}

func TestBearerAuthMiddleware_ValidToken(t *testing.T) {
	const tok = "s3cret"
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := bearerAuthMiddleware(next, tok)
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestBearerAuthMiddleware_MissingHeader(t *testing.T) {
	const tok = "s3cret"
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("downstream handler should not be called")
	})
	h := bearerAuthMiddleware(next, tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if string(body) != `{"error":"unauthorized"}` {
		t.Fatalf("unexpected body: %q", string(body))
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("expected JSON content-type, got %q", rec.Header().Get("Content-Type"))
	}
}

func TestBearerAuthMiddleware_WrongToken(t *testing.T) {
	const tok = "s3cret"
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("downstream handler should not be called")
	})
	h := bearerAuthMiddleware(next, tok)
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestBearerAuthMiddleware_MalformedHeader(t *testing.T) {
	const tok = "s3cret"
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("downstream handler should not be called")
	})
	h := bearerAuthMiddleware(next, tok)
	cases := []string{
		"Bearer" + tok, // no space between scheme and token
		"Basic " + tok, // wrong scheme
		tok,            // raw token, no scheme
	}
	for _, hv := range cases {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Authorization", hv)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("header %q: expected 401, got %d", hv, rec.Code)
		}
	}
}
