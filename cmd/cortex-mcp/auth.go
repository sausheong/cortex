package main

import (
	"net"
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
