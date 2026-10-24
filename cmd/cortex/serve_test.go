package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/extractor/deterministic"
	"github.com/sausheong/cortex/internal/pgtest"
)

const serveSecret = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

func stubOpenAI(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			var in struct {
				Input any `json:"input"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			n := 1
			if arr, ok := in.Input.([]any); ok {
				n = len(arr)
			}
			data := make([]map[string]any, n)
			for i := range data {
				v := make([]float32, 512)
				v[0] = 1
				data[i] = map[string]any{"object": "embedding", "index": i, "embedding": v}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data, "model": "m"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "x", "object": "chat.completion", "choices": []map[string]any{
			{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": "{}"}}}})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func mcpCall(t *testing.T, h http.Handler, token string, body string, session string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	b, _ := io.ReadAll(rec.Body)
	return rec, string(b)
}

func TestServeStartupOrder(t *testing.T) {
	dsn := pgtest.DSN(t)
	ctx := context.Background()
	// Seed the "legacy" brain before serve starts: bootstrap user data.
	seed, err := cortex.Open(dsn, cortex.WithExtractor(deterministic.New()))
	if err != nil {
		t.Fatal(err)
	}
	if err := seed.Remember(ctx, "Quentin works at Wonka Industries."); err != nil {
		t.Fatal(err)
	}
	seed.Close()

	env := map[string]string{
		"CORTEX_DATABASE_URL": dsn, "CORTEX_SECRET_KEY": serveSecret,
		"CORTEX_AUTH_TOKEN": "tunnel-tok", "OPENAI_API_KEY": "k", "OPENAI_BASE_URL": stubOpenAI(t),
		"CORTEX_ALLOW_PRIVATE_PROVIDERS": "1",
		// The bootstrap user here is created after the migrations, so with
		// enrollment on it would count as a new account that must enroll
		// first; a real legacy deployment's owner predates the marker.
		"CORTEX_ENCRYPTION_ENROLL": "0",
	}
	app, err := runServe(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	rec, _ := mcpCall(t, app.Handler, "wrong", `{}`, "")
	if rec.Code != 401 {
		t.Fatalf("wrong token: %d, want 401", rec.Code)
	}
	rec, _ = mcpCall(t, app.Handler, "tunnel-tok", `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`, "")
	if rec.Code != 200 {
		t.Fatalf("tunnel token initialize: %d, want 200", rec.Code)
	}
	sess := rec.Header().Get("Mcp-Session-Id")
	rec, body := mcpCall(t, app.Handler, "tunnel-tok",
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"recall","arguments":{"query":"Quentin"}}}`, sess)
	if rec.Code != 200 || !strings.Contains(body, "Wonka") {
		t.Fatalf("recall as bootstrap: %d %s", rec.Code, body)
	}
}

func TestServeFailsWithoutSecretKey(t *testing.T) {
	_, err := runServe(context.Background(), map[string]string{"CORTEX_DATABASE_URL": "postgres://nowhere.invalid/x"})
	if err == nil || !strings.Contains(err.Error(), "CORTEX_SECRET_KEY") {
		t.Fatalf("err = %v, want CORTEX_SECRET_KEY error", err)
	}
}

func TestServeBootstrapRunsOnce(t *testing.T) {
	dsn := pgtest.DSN(t)
	env := map[string]string{
		"CORTEX_DATABASE_URL": dsn, "CORTEX_SECRET_KEY": serveSecret,
		"CORTEX_AUTH_TOKEN": "tunnel-tok", "OPENAI_API_KEY": "k", "OPENAI_BASE_URL": "https://api.example.com/v1",
	}
	ctx := context.Background()
	app, err := runServe(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	app.Close()

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE connections SET revoked_at = now()`); err != nil {
		t.Fatal(err)
	}
	app, err = runServe(ctx, env)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	var live, total int
	if err := db.QueryRow(`SELECT count(*) FILTER (WHERE revoked_at IS NULL), count(*) FROM connections`).Scan(&live, &total); err != nil {
		t.Fatal(err)
	}
	if live != 0 || total != 1 {
		t.Fatalf("after restart: %d live of %d connections, want 0 of 1", live, total)
	}
	rec, _ := mcpCall(t, app.Handler, "tunnel-tok", `{}`, "")
	if rec.Code != 401 {
		t.Fatalf("revoked tunnel token: %d, want 401", rec.Code)
	}
}

func TestEnrollmentFlagDefaultsOn(t *testing.T) {
	for v, want := range map[string]bool{"": true, "1": true, "on": true, "0": false, "false": false, "off": false, "garbage": true} {
		if got := envBoolDefault(v, true); got != want {
			t.Errorf("envBoolDefault(%q, true) = %v, want %v", v, got, want)
		}
	}
}
