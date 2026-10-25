package oauth

import "testing"

// RFC 8252 section 7.3: native clients (Claude Code) register a loopback
// redirect without a port and use an ephemeral one at runtime.
func TestHasRedirectLoopbackAnyPort(t *testing.T) {
	c := &client{RedirectURIs: []string{"http://localhost/callback", "http://127.0.0.1/callback", "https://chatgpt.com/cb"}}
	for _, tc := range []struct {
		uri  string
		want bool
	}{
		{"http://localhost/callback", true},
		{"http://localhost:59724/callback", true},
		{"http://127.0.0.1:8080/callback", true},
		{"https://chatgpt.com/cb", true},
		{"http://localhost:59724/other", false},        // path must match
		{"http://localhost:59724/callback?x=1", false}, // query must match
		{"http://127.0.0.1:59724/callback/", false},    // trailing slash differs
		{"http://evil.example:59724/callback", false},  // not loopback
		{"https://localhost:59724/callback", false},    // scheme must match
		{"https://chatgpt.com:8443/cb", false},         // non-loopback port is exact
		{"http://[::1]:59724/callback", false},         // ::1 not registered
	} {
		if got := c.hasRedirect(tc.uri); got != tc.want {
			t.Errorf("hasRedirect(%q) = %v, want %v", tc.uri, got, tc.want)
		}
	}
}
