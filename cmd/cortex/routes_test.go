package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/pgtest"
	"github.com/sausheong/cortex/internal/web"
)

// TestServeRouteInventory drives the handler exactly as `cortex serve` builds
// it (runServe -> buildHandler) and checks every route family is reachable.
func TestServeRouteInventory(t *testing.T) {
	dsn := pgtest.DSN(t)
	ctx := context.Background()
	const pub = "https://cortex.route.test"
	app, err := runServe(ctx, map[string]string{
		"CORTEX_DATABASE_URL": dsn, "CORTEX_SECRET_KEY": serveSecret,
		"CORTEX_PUBLIC_URL": pub, "CORTEX_BOOTSTRAP_EMAIL": "owner@route.test",
		"GOOGLE_OAUTH_CLIENT_ID": "id", "GOOGLE_OAUTH_CLIENT_SECRET": "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	h := app.Handler

	// A real session for the bootstrap user.
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sealer, _ := accounts.NewSealer(serveSecret)
	st := &accounts.Store{DB: db, Sealer: sealer}
	uid := cortex.BootstrapUserID
	sid, _, err := web.NewSessions(st, nil).Create(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	cid := "c_routetest"
	if _, err := db.Exec(`INSERT INTO compactions (id, user_id, status) VALUES ($1, $2, 'proposing') ON CONFLICT DO NOTHING`, cid, uid); err != nil {
		t.Fatal(err)
	}

	do := func(method, path string, body string, session bool, hdr map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.RemoteAddr = "203.0.113.9:1234"
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		if session {
			req.AddCookie(&http.Cookie{Name: "cortex_session", Value: sid})
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	secHeaders := func(t *testing.T, path string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if rec.Header().Get("X-Frame-Options") != "DENY" || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "frame-ancestors") {
			t.Errorf("%s: missing security headers: %v", path, rec.Header())
		}
	}

	t.Run("well-known", func(t *testing.T) {
		rec := do("GET", "/.well-known/oauth-authorization-server", "", false, nil)
		var m map[string]any
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &m) != nil || m["issuer"] != pub {
			t.Fatalf("as metadata: %d %s", rec.Code, rec.Body)
		}
		for _, p := range []string{"/.well-known/oauth-protected-resource", "/.well-known/oauth-protected-resource/mcp"} {
			rec = do("GET", p, "", false, nil)
			m = nil
			if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &m) != nil || m["resource"] != pub+"/mcp" {
				t.Fatalf("%s: %d %s", p, rec.Code, rec.Body)
			}
		}
	})

	var clientID string
	t.Run("register", func(t *testing.T) {
		rec := do("POST", "/oauth/register", `{"client_name":"t","redirect_uris":["https://client.test/cb"]}`, false,
			map[string]string{"Content-Type": "application/json"})
		var m map[string]any
		if rec.Code != 201 && rec.Code != 200 {
			t.Fatalf("register: %d %s", rec.Code, rec.Body)
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		clientID, _ = m["client_id"].(string)
		if clientID == "" {
			t.Fatalf("no client_id: %s", rec.Body)
		}
	})

	t.Run("authorize signed out", func(t *testing.T) {
		q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {"https://client.test/cb"},
			"code_challenge": {"E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"}, "code_challenge_method": {"S256"}, "state": {"s"}}
		rec := do("GET", "/oauth/authorize?"+q.Encode(), "", false, nil)
		if rec.Code != 302 || !strings.HasPrefix(rec.Header().Get("Location"), "/auth/google/start") {
			t.Fatalf("authorize: %d %s", rec.Code, rec.Header().Get("Location"))
		}
		// And that redirect target really goes to Google.
		rec = do("GET", rec.Header().Get("Location"), "", false, nil)
		if rec.Code != 302 || !strings.HasPrefix(rec.Header().Get("Location"), "https://accounts.google.com/") {
			t.Fatalf("sign-in: %d %s", rec.Code, rec.Header().Get("Location"))
		}
	})

	t.Run("token bad code", func(t *testing.T) {
		rec := do("POST", "/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {"nope"},
			"client_id": {clientID}, "redirect_uri": {"https://client.test/cb"}, "code_verifier": {"x"}}.Encode(), false,
			map[string]string{"Content-Type": "application/x-www-form-urlencoded"})
		var m map[string]string
		if rec.Code != 400 || json.Unmarshal(rec.Body.Bytes(), &m) != nil || m["error"] != "invalid_grant" {
			t.Fatalf("token: %d %s", rec.Code, rec.Body)
		}
	})

	t.Run("mcp", func(t *testing.T) {
		rec := do("POST", "/mcp", `{}`, false, nil)
		want := `Bearer resource_metadata="` + pub + `/.well-known/oauth-protected-resource"`
		if rec.Code != 401 || rec.Header().Get("WWW-Authenticate") != want {
			t.Fatalf("mcp: %d %q", rec.Code, rec.Header().Get("WWW-Authenticate"))
		}
	})

	t.Run("healthz", func(t *testing.T) {
		if rec := do("GET", "/healthz", "", false, nil); rec.Code != 200 {
			t.Fatalf("healthz: %d", rec.Code)
		}
	})

	t.Run("pages", func(t *testing.T) {
		// Signed out: / renders sign-in (200); the rest redirect to sign-in.
		rec := do("GET", "/", "", false, nil)
		if rec.Code != 200 {
			t.Fatalf("/ signed out: %d", rec.Code)
		}
		secHeaders(t, "/", rec)
		for _, p := range []string{"/settings", "/connections", "/memories", "/memories/deleted", "/account", "/compaction/" + cid} {
			rec = do("GET", p, "", false, nil)
			if rec.Code != 302 || !strings.HasPrefix(rec.Header().Get("Location"), "/auth/google/start") {
				t.Errorf("%s signed out: %d %s", p, rec.Code, rec.Header().Get("Location"))
			}
			secHeaders(t, p, rec)
			rec = do("GET", p, "", true, nil)
			if rec.Code != 200 {
				t.Errorf("%s signed in: %d %.120s", p, rec.Code, rec.Body)
			}
			secHeaders(t, p, rec)
		}
		// Signed in, / is the Home dashboard.
		if rec = do("GET", "/", "", true, nil); rec.Code != 200 || !strings.Contains(rec.Body.String(), "<h1>Home</h1>") {
			t.Errorf("/ signed in: %d %.120s", rec.Code, rec.Body)
		}
		// Graph and Vault: signed out they go to sign-in (the data endpoint
		// answers 401 JSON); signed in they render.
		for _, p := range []string{"/graph", "/vault", "/vault/download", "/vault/index.md"} {
			rec = do("GET", p, "", false, nil)
			if rec.Code != 302 || !strings.HasPrefix(rec.Header().Get("Location"), "/auth/google/start") {
				t.Errorf("%s signed out: %d %s", p, rec.Code, rec.Header().Get("Location"))
			}
			secHeaders(t, p, rec)
		}
		if rec = do("GET", "/graph/data", "", false, nil); rec.Code != 401 {
			t.Errorf("/graph/data signed out: %d, want 401", rec.Code)
		}
		for _, p := range []string{"/graph", "/vault", "/vault/index.md"} {
			rec = do("GET", p, "", true, nil)
			if rec.Code != 200 {
				t.Errorf("%s signed in: %d %.120s", p, rec.Code, rec.Body)
			}
			secHeaders(t, p, rec)
		}
		rec = do("GET", "/graph/data", "", true, nil)
		var gd map[string]any
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &gd) != nil || gd["nodes"] == nil {
			t.Errorf("/graph/data signed in: %d %.120s", rec.Code, rec.Body)
		}
		rec = do("GET", "/vault/download", "", true, nil)
		if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Type"), "zip") {
			t.Errorf("/vault/download signed in: %d %q", rec.Code, rec.Header().Get("Content-Type"))
		}
		if rec = do("GET", "/vault/no-such-page.md", "", true, nil); rec.Code != 404 {
			t.Errorf("/vault/no-such-page.md signed in: %d, want 404", rec.Code)
		}
		// POST-only routes exist and demand a session (401, not 404/405).
		for _, p := range []string{"/memories/compact", "/compaction/" + cid + "/apply", "/compaction/" + cid + "/undo",
			"/connections/tokens", "/connections/x/revoke", "/memories/delete", "/settings"} {
			if rec = do("POST", p, "", false, nil); rec.Code != 401 {
				t.Errorf("POST %s signed out: %d, want 401", p, rec.Code)
			}
		}
	})
}
