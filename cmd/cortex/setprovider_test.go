package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sausheong/cortex/internal/accounts"
)

const spKey = "sk-test-SECRET-1234"

func fakeProvider(t *testing.T, fail bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			v := make([]float32, 512)
			v[0] = 1
			json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []map[string]any{{"object": "embedding", "index": 0, "embedding": v}}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": "x", "object": "chat.completion", "choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": "pong"}, "finish_reason": "stop"}}})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func spStore(t *testing.T, dsn string) *accounts.Store {
	t.Helper()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &accounts.Store{DB: db, AllowPrivate: true}
}

func TestSetProviderAmbiguousEmailRefused(t *testing.T) {
	root, dsn := adminRoot(t)
	st := spStore(t, dsn)
	ctx := t.Context()
	a, _ := root.CreateUser(ctx, "s1", "dup@x.test", "A", true)
	b, _ := root.CreateUser(ctx, "s2", "dup@x.test", "B", true)
	srv := fakeProvider(t, false)
	err := runSetProvider(ctx, &bytes.Buffer{}, st, root, "dup@x.test", []string{"--base-url", srv.URL + "/v1"}, strings.NewReader(spKey), map[string]string{}, nil)
	if err == nil || !strings.Contains(err.Error(), a.ID) || !strings.Contains(err.Error(), b.ID) {
		t.Fatalf("want ambiguity error: %v", err)
	}
	for _, id := range []string{a.ID, b.ID} {
		if _, err := st.GetSettings(ctx, id, nil); err != accounts.ErrNoSettings {
			t.Fatalf("settings written for %s", id)
		}
	}
}

func TestSetProviderCLIRefusesEncryptedUser(t *testing.T) {
	root, dsn := adminRoot(t)
	st := spStore(t, dsn)
	ctx := t.Context()
	u, _ := root.CreateUser(ctx, "s1", "enc@x.test", "E", true)
	if _, err := st.DB.ExecContext(ctx, `UPDATE users SET encryption_state = 'encrypted' WHERE id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
	srv := fakeProvider(t, false)
	err := runSetProvider(ctx, &bytes.Buffer{}, st, root, u.ID, []string{"--base-url", srv.URL + "/v1"}, strings.NewReader(spKey), map[string]string{}, nil)
	const want = "this user is encrypted; change provider settings from Settings while signed in"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v", err)
	}
	if _, err := st.GetSettings(ctx, u.ID, nil); err != accounts.ErrNoSettings {
		t.Fatalf("settings written: %v", err)
	}
}

// Provider settings are sealed under the user's key, so the command only says
// where to change them.
func TestSetProviderRefusesWithReason(t *testing.T) {
	root, dsn := adminRoot(t)
	st := spStore(t, dsn)
	ctx := t.Context()
	u, _ := root.CreateUser(ctx, "s1", "u@x.test", "U", true)
	run := func() error {
		return runSetProvider(ctx, &bytes.Buffer{}, st, root, u.ID, nil, strings.NewReader(""), map[string]string{}, nil)
	}
	if err := run(); err == nil || !strings.Contains(err.Error(), "has not enrolled") {
		t.Fatalf("unenrolled: %v", err)
	}
	if _, err := st.DB.ExecContext(ctx, `UPDATE users SET encryption_state = 'encrypted' WHERE id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
	if err := run(); err == nil || !strings.Contains(err.Error(), "from Settings while signed in") {
		t.Fatalf("encrypted: %v", err)
	}
	if err := runSetProvider(ctx, &bytes.Buffer{}, st, root, "", nil, nil, nil, nil); err == nil {
		t.Fatal("--user is required")
	}
}
