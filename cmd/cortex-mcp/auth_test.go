package main

import "testing"

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
