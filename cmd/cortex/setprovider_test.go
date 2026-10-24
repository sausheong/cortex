package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sausheong/cortex"
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
	sealer, err := accounts.NewSealer(strings.Repeat("ab", 32))
	if err != nil {
		t.Fatal(err)
	}
	return &accounts.Store{DB: db, Sealer: sealer, AllowPrivate: true}
}

func TestSetProviderRightUserOnlyKeyNotLeaked(t *testing.T) {
	root, dsn := adminRoot(t)
	st := spStore(t, dsn)
	ctx := t.Context()
	a, _ := root.CreateUser(ctx, "sa", "a@x.test", "A", true)
	b, _ := root.CreateUser(ctx, "sb", "b@x.test", "B", true)
	srv := fakeProvider(t, false)
	var out bytes.Buffer
	args := []string{"--base-url", srv.URL + "/v1", "--chat-model", "gpt-x"}
	if err := runSetProvider(ctx, &out, st, root, "a@x.test", args, strings.NewReader(spKey+"\n"), map[string]string{}, nil); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetSettings(ctx, a.ID, nil)
	if err != nil || got.APIKey != spKey || got.ChatModel != "gpt-x" {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := st.GetSettings(ctx, b.ID, nil); err != accounts.ErrNoSettings {
		t.Fatalf("other user touched: %v", err)
	}
	if strings.Contains(out.String(), spKey) || strings.Contains(strings.Join(args, " "), spKey) {
		t.Fatal("key leaked")
	}
	// env key, keeps others unchanged
	out.Reset()
	if err := runSetProvider(ctx, &out, st, root, a.ID, []string{"--chat-model", "gpt-y"}, nil, map[string]string{"CORTEX_PROVIDER_API_KEY": "sk-env-9999"}, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetSettings(ctx, a.ID, nil)
	if got.APIKey != "sk-env-9999" || got.ChatModel != "gpt-y" || got.BaseURL != srv.URL+"/v1" || strings.Contains(out.String(), "sk-env-9999") {
		t.Fatalf("%+v %q", got, out.String())
	}
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

func TestSetProviderTestFailureLeavesSettingsUnchanged(t *testing.T) {
	root, dsn := adminRoot(t)
	st := spStore(t, dsn)
	ctx := t.Context()
	u, _ := root.CreateUser(ctx, "s1", "u@x.test", "U", true)
	good := fakeProvider(t, false)
	if err := runSetProvider(ctx, &bytes.Buffer{}, st, root, u.ID, []string{"--base-url", good.URL + "/v1"}, strings.NewReader("sk-old"), map[string]string{}, nil); err != nil {
		t.Fatal(err)
	}
	bad := fakeProvider(t, true)
	var out bytes.Buffer
	err := runSetProvider(ctx, &out, st, root, u.ID, []string{"--base-url", bad.URL + "/v1"}, strings.NewReader(spKey), map[string]string{}, nil)
	if err == nil || strings.Contains(err.Error()+out.String(), spKey) {
		t.Fatalf("want failure without key: %v", err)
	}
	got, _ := st.GetSettings(ctx, u.ID, nil)
	if got.APIKey != "sk-old" || got.BaseURL != good.URL+"/v1" {
		t.Fatalf("settings changed: %+v", got)
	}
	// --skip-test bypasses the test
	if err := runSetProvider(ctx, &out, st, root, u.ID, []string{"--skip-test", "--base-url", bad.URL + "/v1"}, strings.NewReader(spKey), map[string]string{}, nil); err != nil {
		t.Fatal(err)
	}
	got, _ = st.GetSettings(ctx, u.ID, nil)
	if got.APIKey != spKey {
		t.Fatal("skip-test did not save")
	}
}

func TestSetProviderNeedsKeyAndNoKeyArgv(t *testing.T) {
	root, dsn := adminRoot(t)
	st := spStore(t, dsn)
	ctx := t.Context()
	u, _ := root.CreateUser(ctx, "s1", "u@x.test", "U", true)
	if err := runSetProvider(ctx, &bytes.Buffer{}, st, root, u.ID, nil, strings.NewReader(""), map[string]string{}, nil); err == nil || !strings.Contains(err.Error(), "API key") {
		t.Fatalf("want key error: %v", err)
	}
	if err := runSetProvider(ctx, &bytes.Buffer{}, st, root, u.ID, []string{spKey}, nil, map[string]string{}, nil); err == nil || strings.Contains(err.Error(), spKey) || !strings.Contains(err.Error(), "positional") {
		t.Fatalf("positional key must be refused: %v", err)
	}
}

func TestProfileDefaultUserIsDistilledWithBootstrapProviders(t *testing.T) {
	root, dsn := adminRoot(t)
	st := spStore(t, dsn)
	ctx := t.Context()
	srv := fakeProvider(t, false)
	var out bytes.Buffer
	if err := runSetProvider(ctx, &out, st, root, "u_bootstrap", []string{"--base-url", srv.URL + "/v1", "--skip-test"}, strings.NewReader(spKey+"\n"), map[string]string{}, nil); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"CORTEX_SECRET_KEY": strings.Repeat("ab", 32), "CORTEX_ALLOW_PRIVATE_PROVIDERS": "1"}
	cx, ok, err := openDefaultUserCortex(ctx, dsn, env)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	defer cx.Close()
	e := &cortex.Entity{Type: "person", Name: "Me", Source: "owner"}
	if err := cx.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutMemory(ctx, &cortex.Memory{Content: "Me is an engineer", EntityIDs: []string{e.ID}, Static: true, Confidence: 1}); err != nil {
		t.Fatal(err)
	}
	p, err := cx.Profile(ctx, e.ID)
	if err != nil || !p.Distilled {
		t.Fatalf("want distilled profile: %+v %v", p, err)
	}
	if _, ok, _ := openDefaultUserCortex(ctx, dsn, map[string]string{}); ok {
		t.Fatal("without CORTEX_SECRET_KEY the default handle must not be used")
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
