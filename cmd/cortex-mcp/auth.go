package main

import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)

// isLoopback reports whether the given host:port address binds only the
// loopback interface. Empty host (":8080") binds all interfaces in Go's
// http.ListenAndServe, so it's treated as non-loopback.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// bearerAuthMiddleware wraps next with a constant-time Bearer-token check
// against expected. Returns 401 with a JSON {"error":"unauthorized"} body
// on any mismatch (missing header, wrong scheme, wrong token).
func bearerAuthMiddleware(next http.Handler, expected string) http.Handler {
	want := []byte(expected)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(h, prefix) ||
			subtle.ConstantTimeCompare([]byte(h[len(prefix):]), want) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
