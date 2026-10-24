package mcp

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	mcpgo "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/extractor/deterministic"
	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/pgtest"
)

const testPublicURL = "https://cortex.test"

// stubProvider is an OpenAI-compatible endpoint counting its calls.
func stubProvider(t *testing.T) (url string, calls *atomic.Int64) {
	t.Helper()
	calls = &atomic.Int64{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
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
				v[i%512] = 1
				data[i] = map[string]any{"object": "embedding", "index": i, "embedding": v}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data, "model": "m"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "x", "object": "chat.completion", "choices": []map[string]any{
			{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": "{}"}}}})
	}))
	t.Cleanup(srv.Close)
	return srv.URL, calls
}

type muEnv struct {
	root    *cortex.Cortex
	st      *accounts.Store
	handler http.Handler
	srv     *httptest.Server
	limiter *accounts.Limiter
	keys    map[string]*keys.UK
}

func newMUEnv(t *testing.T) *muEnv { return newMUEnvOpts(t) }

func newMUEnvOpts(t *testing.T, opts ...ToolOption) *muEnv {
	t.Helper()
	dsn := pgtest.DSN(t)
	root, err := cortex.Open(dsn, cortex.WithExtractor(deterministic.New()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := &accounts.Store{DB: db, AllowPrivate: true}
	e := &muEnv{root: root, st: st, limiter: accounts.NewLimiter(30, 30, nil), keys: map[string]*keys.UK{}}
	resolve := func(ctx context.Context, bearer string) (*cortex.Cortex, error) {
		uid, cid, uk, err := st.ResolveBearer(ctx, bearer)
		if err != nil {
			return nil, err
		}
		defer uk.Zero() // the handle keeps its own clone
		return st.ResolveUser(ctx, root, uid, cid, uk)
	}
	s := server.NewMCPServer("cortex-test", "0.0.0", server.WithToolCapabilities(false))
	RegisterToolsFor(s, nil, testPublicURL, opts...)
	e.handler = NewHTTPHandler(s, resolve, testPublicURL, e.limiter)
	e.srv = httptest.NewServer(e.handler)
	t.Cleanup(e.srv.Close)
	return e
}

// user creates a user, optionally with provider settings, and an agent token.
func (e *muEnv) user(t *testing.T, name, providerURL string) (id, token string) {
	t.Helper()
	u, err := e.root.CreateUser(t.Context(), name, name+"@x.test", name, true)
	if err != nil {
		t.Fatal(err)
	}
	// An enrolled account: encrypted state and a user key. Agent tokens carry
	// a wrap of it.
	if _, err := e.st.DB.ExecContext(t.Context(), `UPDATE users SET encryption_state = 'encrypted' WHERE id = $1`, u.ID); err != nil {
		t.Fatal(err)
	}
	uk, err := keys.NewUK()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(uk.Zero)
	e.keys[u.ID] = uk
	if providerURL != "" {
		if _, err := e.st.PutSettings(t.Context(), u.ID, accounts.Settings{BaseURL: providerURL, APIKey: "sk-" + name, ChatModel: "m", EmbedModel: "e", EmbedDims: 512}, uk); err != nil {
			t.Fatal(err)
		}
	}
	tok, _, err := e.st.CreateAgentToken(t.Context(), u.ID, "test", uk)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID, tok
}

// cx is a handle for the user's own data, holding its key.
func (e *muEnv) cx(t *testing.T, id string) *cortex.Cortex {
	t.Helper()
	cx := e.root.ForUser(id).WithKey(e.keys[id], "encrypted")
	t.Cleanup(cx.Release)
	return cx
}

func (e *muEnv) client(t *testing.T, token string) *client.Client {
	t.Helper()
	tr, err := transport.NewStreamableHTTP(e.srv.URL+"/mcp", transport.WithHTTPHeaders(map[string]string{"Authorization": "Bearer " + token}))
	if err != nil {
		t.Fatal(err)
	}
	c := client.NewClient(tr)
	if err := c.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	req := mcpgo.InitializeRequest{}
	req.Params.ProtocolVersion = mcpgo.LATEST_PROTOCOL_VERSION
	req.Params.ClientInfo = mcpgo.Implementation{Name: "t", Version: "0"}
	if _, err := c.Initialize(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	return c
}

func call(t *testing.T, c *client.Client, tool string, args map[string]any) (string, bool) {
	t.Helper()
	req := mcpgo.CallToolRequest{}
	req.Params.Name = tool
	req.Params.Arguments = args
	res, err := c.CallTool(t.Context(), req)
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(mcpgo.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String(), res.IsError
}

func rawPost(t *testing.T, e *muEnv, auth, xff string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/mcp", strings.NewReader(`{}`))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func TestMCPUnauthorizedHasResourceMetadata(t *testing.T) {
	e := newMUEnv(t)
	for _, auth := range []string{"", "Bearer nope"} {
		resp := rawPost(t, e, auth, "")
		if resp.StatusCode != 401 {
			t.Fatalf("auth %q: status %d, want 401", auth, resp.StatusCode)
		}
		want := `Bearer resource_metadata="` + testPublicURL + `/.well-known/oauth-protected-resource"`
		if got := resp.Header.Get("WWW-Authenticate"); got != want {
			t.Fatalf("WWW-Authenticate = %q, want %q", got, want)
		}
	}
}

func TestMCPFailedLookupsRateLimited(t *testing.T) {
	e := newMUEnv(t)
	_, good := e.user(t, "u1", "")
	// Peer is loopback (httptest), so the client IP is the last XFF entry.
	for i := 0; i < 30; i++ {
		if resp := rawPost(t, e, "Bearer bad", "203.0.113.9"); resp.StatusCode != 401 {
			t.Fatalf("attempt %d: status %d, want 401", i+1, resp.StatusCode)
		}
	}
	if resp := rawPost(t, e, "Bearer bad", "203.0.113.9"); resp.StatusCode != 429 {
		t.Fatalf("31st bad token: status %d, want 429", resp.StatusCode)
	}
	if resp := rawPost(t, e, "Bearer "+good, "198.51.100.7"); resp.StatusCode == 401 || resp.StatusCode == 429 {
		t.Fatalf("good token from another IP: status %d", resp.StatusCode)
	}
}

func TestMCPAgentTokenScopesToUser(t *testing.T) {
	e := newMUEnv(t)
	purl, _ := stubProvider(t)
	ida, toka := e.user(t, "a", purl)
	idb, tokb := e.user(t, "b", purl)
	ctx := t.Context()
	if err := e.cx(t, ida).Remember(ctx, "Alice works at Stripe."); err != nil {
		t.Fatal(err)
	}
	if err := e.cx(t, idb).Remember(ctx, "Bob works at Initech."); err != nil {
		t.Fatal(err)
	}
	ca, cb := e.client(t, toka), e.client(t, tokb)
	ta, _ := call(t, ca, "recall", map[string]any{"query": "works"})
	tb, _ := call(t, cb, "recall", map[string]any{"query": "works"})
	if !strings.Contains(ta, "Stripe") || strings.Contains(ta, "Initech") {
		t.Fatalf("user A recall leaked or missed: %s", ta)
	}
	if !strings.Contains(tb, "Initech") || strings.Contains(tb, "Stripe") {
		t.Fatalf("user B recall leaked or missed: %s", tb)
	}
}

func TestMCPNoSettingsNeverUsesOperatorKey(t *testing.T) {
	e := newMUEnv(t)
	opURL, opCalls := stubProvider(t)
	t.Setenv("OPENAI_API_KEY", "operator-key")
	t.Setenv("OPENAI_BASE_URL", opURL)
	_, tok := e.user(t, "nosettings", "")
	c := e.client(t, tok)
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{
		{"recall", map[string]any{"query": "x"}},
		{"remember", map[string]any{"content": "I like tea"}},
		{"context", map[string]any{"question": "x"}},
	} {
		text, isErr := call(t, c, tc.tool, tc.args)
		if !isErr || text != "set up your AI provider at "+testPublicURL+"/settings" {
			t.Fatalf("%s: isErr=%v text=%q", tc.tool, isErr, text)
		}
	}
	if n := opCalls.Load(); n != 0 {
		t.Fatalf("operator provider received %d calls, want 0", n)
	}
}

func TestMCPDeletedUserToken401(t *testing.T) {
	e := newMUEnv(t)
	purl, _ := stubProvider(t)
	id, tok := e.user(t, "gone", purl)
	if resp := rawPost(t, e, "Bearer "+tok, ""); resp.StatusCode == 401 {
		t.Fatalf("live token got 401")
	}
	if err := e.st.DeleteAccount(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if resp := rawPost(t, e, "Bearer "+tok, ""); resp.StatusCode != 401 {
		t.Fatalf("deleted user's token: status %d, want 401", resp.StatusCode)
	}
}

func TestContextToolShape(t *testing.T) {
	e := newMUEnv(t)
	purl, _ := stubProvider(t)
	id, tok := e.user(t, "ctx", purl)
	if err := e.cx(t, id).Remember(t.Context(), "Ctx works at Acme."); err != nil {
		t.Fatal(err)
	}
	c := e.client(t, tok)
	text, isErr := call(t, c, "context", map[string]any{"question": "Acme"})
	if isErr {
		t.Fatalf("context error: %s", text)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("not JSON: %v: %s", err, text)
	}
	for _, k := range []string{"profile", "memories", "entities", "abstain"} {
		if _, ok := out[k]; !ok {
			t.Fatalf("missing key %q in %s", k, text)
		}
	}
	var prof map[string]json.RawMessage
	if err := json.Unmarshal(out["profile"], &prof); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"static", "dynamic"} {
		if _, ok := prof[k]; !ok {
			t.Fatalf("profile missing %q: %s", k, out["profile"])
		}
	}
	if !strings.Contains(string(out["memories"]), "Acme") {
		t.Fatalf("memories lack Acme: %s", out["memories"])
	}
}

func TestRememberViaMCPRecordsConnection(t *testing.T) {
	e := newMUEnv(t)
	purl, _ := stubProvider(t)
	id, tok := e.user(t, "rem", purl)
	c := e.client(t, tok)
	if text, isErr := call(t, c, "remember", map[string]any{"content": "Rem likes green tea."}); isErr {
		t.Fatalf("remember: %s", text)
	}
	conns, err := e.st.ListConnections(t.Context(), id)
	if err != nil || len(conns) != 1 {
		t.Fatalf("connections: %v %v", conns, err)
	}
	var n int
	if err := e.st.DB.QueryRowContext(t.Context(), `SELECT count(*) FROM chunks WHERE user_id = $1 AND written_by_connection_id = $2`,
		id, conns[0].ID).Scan(&n); err != nil || n < 1 {
		t.Fatalf("rows stamped with the connection = %d (%v), want >= 1", n, err)
	}
}

func TestMCPIsolationForeignIDs(t *testing.T) {
	e := newMUEnv(t)
	purl, _ := stubProvider(t)
	_, toka := e.user(t, "iso-a", purl)
	idb, _ := e.user(t, "iso-b", purl)
	ctx := t.Context()
	b := e.cx(t, idb)
	e1 := &cortex.Entity{Type: "person", Name: "Zelda Secret"}
	e2 := &cortex.Entity{Type: "person", Name: "Zoltan Secret"}
	if err := b.PutEntity(ctx, e1); err != nil {
		t.Fatal(err)
	}
	if err := b.PutEntity(ctx, e2); err != nil {
		t.Fatal(err)
	}
	if err := b.Remember(ctx, "Zelda Secret owns a lighthouse.", cortex.WithSource("bsrc")); err != nil {
		t.Fatal(err)
	}
	// An unenrolled-then-keyed test user has no owner entity; the leak checks
	// below look for this ID, so keep a value that cannot match.
	owner, err := b.OwnerEntityID(ctx)
	if err != nil {
		owner = "no-owner-entity"
	}
	snapshot := func() string {
		var n1, n2, n3 int
		for q, p := range map[string]*int{
			`SELECT count(*) FROM entities WHERE user_id = $1 AND deleted_at IS NULL`: &n1,
			`SELECT count(*) FROM memories WHERE user_id = $1 AND deleted_at IS NULL`: &n2,
			`SELECT count(*) FROM memory_edges WHERE user_id = $1`:                    &n3,
		} {
			if err := e.st.DB.QueryRowContext(ctx, q, idb).Scan(p); err != nil {
				t.Fatal(err)
			}
		}
		return strings.Join([]string{string(rune('0' + n1)), string(rune('0' + n2)), string(rune('0' + n3))}, ",")
	}
	before := snapshot()

	absent1, absent2 := "01ZZZZZZZZZZZZZZZZZZZZZZZZ", "01YYYYYYYYYYYYYYYYYYYYYYYY"
	ca := e.client(t, toka)
	norm := func(s string, ids ...string) string {
		for _, id := range ids {
			s = strings.ReplaceAll(s, id, "<ID>")
		}
		return s
	}
	cases := []struct {
		name string
		tool string
		mk   func(id1, id2 string) map[string]any
	}{
		{"get_entity", "get_entity", func(a, _ string) map[string]any { return map[string]any{"id": a} }},
		{"get_relationships", "get_relationships", func(a, _ string) map[string]any { return map[string]any{"entity_id": a} }},
		{"traverse", "traverse", func(a, _ string) map[string]any { return map[string]any{"start_id": a, "depth": 2} }},
		{"forget", "forget", func(a, _ string) map[string]any { return map[string]any{"entity_id": a} }},
		{"forget-source", "forget", func(a, _ string) map[string]any { return map[string]any{"source": "bsrc"} }},
		{"merge", "merge", func(a, b string) map[string]any { return map[string]any{"keep_id": a, "drop_id": b} }},
		{"merge-dry", "merge", func(a, b string) map[string]any { return map[string]any{"keep_id": a, "drop_id": b, "dry_run": true} }},
		{"profile", "profile", func(a, _ string) map[string]any { return map[string]any{"entity_id": a} }},
		{"find_entities", "find_entities", func(_, _ string) map[string]any { return map[string]any{"name": "%Secret%"} }},
		{"search-keyword", "search", func(_, _ string) map[string]any { return map[string]any{"query": "lighthouse", "mode": "keyword"} }},
		{"search-memory", "search", func(_, _ string) map[string]any { return map[string]any{"query": "lighthouse", "mode": "memory"} }},
		{"lint", "lint", func(_, _ string) map[string]any { return map[string]any{} }},
		{"who_is", "who_is", func(a, _ string) map[string]any { return map[string]any{"name": a} }},
		{"connections", "connections", func(a, b string) map[string]any { return map[string]any{"from": a, "to": b} }},
		{"timeline", "timeline", func(a, _ string) map[string]any { return map[string]any{"entity": a} }},
		{"add_alias", "add_alias", func(a, _ string) map[string]any { return map[string]any{"entity": a, "value": "zelda@b.example"} }},
	}
	for _, tc := range cases {
		foreign, fErr := call(t, ca, tc.tool, tc.mk(e1.ID, e2.ID))
		absent, aErr := call(t, ca, tc.tool, tc.mk(absent1, absent2))
		// The searches and find use a query rather than an ID: the "absent"
		// side is the same query as user B's empty-handed neighbour would see,
		// so compare against a query that matches nothing.
		if tc.name == "find_entities" {
			absent, aErr = call(t, ca, tc.tool, map[string]any{"name": "%Nonexistent%"})
		}
		if strings.HasPrefix(tc.name, "search") {
			absent, aErr = call(t, ca, tc.tool, map[string]any{"query": "nonexistentword", "mode": tc.mk("", "")["mode"]})
		}
		if tc.name == "lint" {
			if strings.Contains(foreign, "Zelda") || strings.Contains(foreign, "Zoltan") || strings.Contains(foreign, owner) {
				t.Fatalf("lint leaked B's data: %s", foreign)
			}
			continue
		}
		if fErr != aErr || norm(foreign, e1.ID, e2.ID) != norm(absent, absent1, absent2) {
			t.Errorf("%s: foreign result differs from absent\nforeign(err=%v): %s\nabsent(err=%v): %s", tc.name, fErr, foreign, aErr, absent)
		}
		if strings.Contains(foreign, "Zelda") || strings.Contains(foreign, "lighthouse") {
			t.Errorf("%s leaked B's data: %s", tc.name, foreign)
		}
	}
	// B's owner entity ID via profile (no entity_id) must be A's own profile.
	if text, _ := call(t, ca, "profile", map[string]any{}); strings.Contains(text, owner) {
		t.Errorf("profile without entity_id leaked B's owner: %s", text)
	}
	if after := snapshot(); after != before {
		t.Fatalf("B's rows changed: before %s after %s", before, after)
	}
	_ = time.Now
}

func TestLegacyHTTPRequiresToken(t *testing.T) {
	cx := newTestCortex(t)
	for _, addr := range []string{"127.0.0.1:0", "0.0.0.0:0"} {
		err := Serve(cx, Config{Transport: "http", Addr: addr})
		if err == nil || !strings.Contains(err.Error(), "CORTEX_AUTH_TOKEN") {
			t.Fatalf("addr %s with empty token: err = %v, want a CORTEX_AUTH_TOKEN error", addr, err)
		}
	}
	s := server.NewMCPServer("t", "0", server.WithToolCapabilities(false))
	rec := httptest.NewRecorder()
	buildHTTPMux(s, "tok").ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("/mcp without token: %d, want 401", rec.Code)
	}
}

func TestStaticTokenRejectedForEncryptedUserHTTP(t *testing.T) {
	e := newMUEnv(t)
	e.st.SetStaticToken("tunnel-secret")
	if _, err := e.st.DB.ExecContext(t.Context(), `UPDATE users SET encryption_state='encrypted' WHERE id=$1`, cortex.BootstrapUserID); err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, e.srv.URL+"/mcp", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer tunnel-secret")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("status %d ct %q", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	var body struct{ Error string }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Error != model.ErrStaticTokenDisabled.Error() {
		t.Fatalf("error text %q", body.Error)
	}
}
