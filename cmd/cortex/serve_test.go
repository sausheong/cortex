package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

func TestEnrollmentFlagDefaultsOn(t *testing.T) {
	for v, want := range map[string]bool{"": true, "1": true, "on": true, "0": false, "false": false, "off": false, "garbage": true} {
		if got := envBoolDefault(v, true); got != want {
			t.Errorf("envBoolDefault(%q, true) = %v, want %v", v, got, want)
		}
	}
}

// The static owner token is retired: serve starts without it and the token
// opens nothing, whatever the environment says.
func TestServeStartsAndRejectsStaticToken(t *testing.T) {
	dsn := pgtest.DSN(t)
	env := map[string]string{
		"CORTEX_DATABASE_URL": dsn,
		"CORTEX_AUTH_TOKEN":   "tunnel-tok", "OPENAI_API_KEY": "k", "OPENAI_BASE_URL": stubOpenAI(t),
		"CORTEX_ALLOW_PRIVATE_PROVIDERS": "1",
	}
	app, err := runServe(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	for _, tok := range []string{"wrong", "tunnel-tok"} {
		if rec, _ := mcpCall(t, app.Handler, tok, `{}`, ""); rec.Code != 401 {
			t.Fatalf("token %q: %d, want 401", tok, rec.Code)
		}
	}
}
