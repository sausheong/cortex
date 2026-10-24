// Package oauth is the OAuth 2.1 authorization server that lets assistants
// (ChatGPT, claude.ai, MCP Inspector) connect to a user's cortex /mcp.
package oauth

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/web"
)

const (
	codeTTL        = 60 * time.Second
	accessTTL      = time.Hour
	refreshTTL     = 90 * 24 * time.Hour
	requestTTL     = 10 * time.Minute
	dcrTTL         = 24 * time.Hour
	cimdCacheTTL   = 24 * time.Hour
	registerCap    = 64 << 10
	rateLimitPerIP = 30
)

// Options tune the server; the zero value is production.
type Options struct {
	// CIMDClient overrides the hardened client (tests only).
	CIMDClient *http.Client
	// CIMDTimeout overrides the 5 s fetch timeout (tests only).
	CIMDTimeout time.Duration
	// FailAfterRewrap, when set, runs after a refresh rewrap and before the
	// commit; a non-nil error aborts the rotation (tests only).
	FailAfterRewrap func() error
}

type handler struct {
	d   web.Deps
	st  *accounts.Store
	o   Options
	now func() time.Time

	regLimit, authLimit, tokLimit *accounts.Limiter
}

// Mount returns the route-mounting function for web.Options.Mounts.
func Mount(o Options) func(mux *http.ServeMux, d web.Deps) {
	return func(mux *http.ServeMux, d web.Deps) {
		now := d.Now
		if now == nil {
			now = time.Now
		}
		h := &handler{d: d, st: d.Accounts, o: o, now: now,
			regLimit:  accounts.NewLimiter(rateLimitPerIP, rateLimitPerIP, now),
			authLimit: accounts.NewLimiter(rateLimitPerIP, rateLimitPerIP, now),
			tokLimit:  accounts.NewLimiter(rateLimitPerIP, rateLimitPerIP, now),
		}
		mux.HandleFunc("GET /.well-known/oauth-protected-resource", h.protectedResource)
		mux.HandleFunc("GET /.well-known/oauth-protected-resource/mcp", h.protectedResource)
		mux.HandleFunc("GET /.well-known/oauth-authorization-server", h.serverMetadata)
		mux.HandleFunc("OPTIONS /.well-known/", cors)
		mux.HandleFunc("OPTIONS /oauth/register", cors)
		mux.HandleFunc("OPTIONS /oauth/token", cors)
		mux.HandleFunc("POST /oauth/register", h.limited(h.regLimit, h.register))
		mux.HandleFunc("GET /oauth/authorize", h.limited(h.authLimit, h.d.Auth(h.gate(h.authorizeGet))))
		mux.HandleFunc("POST /oauth/authorize", h.limited(h.authLimit, h.d.Auth(h.gate(h.authorizePost))))
		mux.HandleFunc("POST /oauth/token", h.limited(h.tokLimit, h.token))
	}
}

// gate requires an unlocked session for an encrypted account: the consent
// grant must carry the user's key into the code wrap. GETs go to /unlock and
// back; POSTs answer 423 before the authorize request is consumed.
func (h *handler) gate(next func(http.ResponseWriter, *http.Request, web.SessionUser)) func(http.ResponseWriter, *http.Request, web.SessionUser) {
	return func(w http.ResponseWriter, r *http.Request, u web.SessionUser) {
		st, err := h.st.EncState(r.Context(), u.UserID)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if st != "none" && (!u.Unlocked || u.Key == nil) {
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				http.Redirect(w, r, "/unlock?next="+url.QueryEscape(r.URL.RequestURI()), http.StatusSeeOther)
				return
			}
			http.Error(w, "locked: unlock with your passkey", http.StatusLocked)
			return
		}
		next(w, r, u)
	}
}

func (h *handler) public() string { return strings.TrimRight(h.d.Cfg.PublicURL, "/") }

func (h *handler) limited(l *accounts.Limiter, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !l.Allow(accounts.ClientIP(r)) {
			w.Header().Set("Retry-After", "60")
			http.Error(w, "too many requests", http.StatusTooManyRequests)
			return
		}
		next(w, r)
	}
}

func cors(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, MCP-Protocol-Version")
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func oauthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

func randToken(prefix string) string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b)
}

func (h *handler) protectedResource(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":              h.public() + "/mcp",
		"authorization_servers": []string{h.public()},
		"scopes_supported":      []string{"cortex", "offline_access"},
	})
}

func (h *handler) serverMetadata(w http.ResponseWriter, r *http.Request) {
	p := h.public()
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                p,
		"authorization_endpoint":                p + "/oauth/authorize",
		"token_endpoint":                        p + "/oauth/token",
		"registration_endpoint":                 p + "/oauth/register",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{"cortex", "offline_access"},
		"client_id_metadata_document_supported": true,
	})
}

// validRedirect: https, or http to a loopback host; no userinfo or fragment.
func validRedirect(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	switch u.Scheme {
	case "https":
		return true
	case "http":
		switch u.Hostname() {
		case "localhost", "127.0.0.1", "::1":
			return true
		}
	}
	return false
}

func redirectHost(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return u.Host
	}
	return ""
}
