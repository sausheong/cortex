package jobs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
)

func (e *env) backdate(t *testing.T, uid string, days int) {
	t.Helper()
	if _, err := e.db.ExecContext(t.Context(),
		`UPDATE users SET deleted_at = now() - make_interval(days => $2) WHERE id = $1`, uid, days); err != nil {
		t.Fatal(err)
	}
}

// bareUser is an encrypted user with no settings.
func (e *env) bareUser(t *testing.T, sub string) string {
	t.Helper()
	id, _ := e.bareEnc(t, sub)
	return id
}

// resolver returns a Resolve func that counts calls per user and fails for
// the users in failFor.
func (e *env) resolver(calls *sync.Map, failFor map[string]bool) func(context.Context, string) (*cortex.Cortex, error) {
	return func(_ context.Context, id string) (*cortex.Cortex, error) {
		v, _ := calls.LoadOrStore(id, new(atomic.Int64))
		v.(*atomic.Int64).Add(1)
		if failFor[id] {
			return nil, errors.New("boom https://secret.example/key=abc")
		}
		return e.root.ForUser(id), nil
	}
}

func callsOf(m *sync.Map, id string) int64 {
	if v, ok := m.Load(id); ok {
		return v.(*atomic.Int64).Load()
	}
	return 0
}

// The server-wide nightly never opens an account: deleted users are skipped,
// encrypted ones only get numeric usage counters, nobody is resolved.
func TestNightlyIsKeyFree(t *testing.T) {
	e := newEnv(t)
	live := e.user(t, "live")
	deleted := e.user(t, "gone")
	if err := e.st.DeleteAccount(t.Context(), deleted); err != nil {
		t.Fatal(err)
	}
	var calls sync.Map
	rep, err := Nightly(t.Context(), e.root, e.st, NightlyOptions{Resolve: e.resolver(&calls, nil)})
	if err != nil {
		t.Fatal(err)
	}
	if callsOf(&calls, live) != 0 || callsOf(&calls, deleted) != 0 || rep.Maintained != 0 || rep.Failed != 0 {
		t.Fatalf("calls live=%d deleted=%d rep=%+v", callsOf(&calls, live), callsOf(&calls, deleted), rep)
	}
}

func seedRows(t *testing.T, e *env, uid string) {
	t.Helper()
	cx := e.handle(uid, nil, 0)
	if err := cx.PutMemory(t.Context(), &cortex.Memory{Content: "remember " + uid}); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutEntity(t.Context(), &cortex.Entity{Type: "thing", Name: "N" + uid}); err != nil {
		t.Fatal(err)
	}
}

func TestNightlyPurgesSettingslessDeletedUser(t *testing.T) {
	e := newEnv(t)
	old, recent := e.bareUser(t, "old"), e.bareUser(t, "recent")
	seedRows(t, e, old)
	seedRows(t, e, recent)
	e.backdate(t, old, 31)
	e.backdate(t, recent, 29)
	rep, err := Nightly(t.Context(), e.root, e.st, NightlyOptions{Resolve: e.resolver(new(sync.Map), nil)})
	if err != nil {
		t.Fatal(err)
	}
	if rep.UsersPurged != 1 {
		t.Fatalf("purged %d", rep.UsersPurged)
	}
	for _, tbl := range []string{"users WHERE id", "memories WHERE user_id", "entities WHERE user_id"} {
		if n := e.count(t, `SELECT count(*) FROM `+tbl+` = $1`, old); n != 0 {
			t.Fatalf("%s: old user rows remain: %d", tbl, n)
		}
		if n := e.count(t, `SELECT count(*) FROM `+tbl+` = $1`, recent); n == 0 {
			t.Fatalf("%s: 29-day user wrongly purged", tbl)
		}
	}
}

func TestNightlyPurgeRunsEvenIfMaintenanceFails(t *testing.T) {
	e := newEnv(t)
	a := e.user(t, "a")
	gone := e.bareUser(t, "gone")
	e.backdate(t, gone, 40)
	if _, err := Nightly(t.Context(), e.root, e.st, NightlyOptions{Resolve: e.resolver(new(sync.Map), map[string]bool{a: true})}); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM users WHERE id=$1`, gone); n != 0 {
		t.Fatal("purge skipped after a maintenance failure")
	}
}

func TestNightlyPurges(t *testing.T) {
	e := newEnv(t)
	uid := e.user(t, "p")
	cx := e.handle(uid, nil, 0)
	ctx := t.Context()
	m := &cortex.Memory{Content: "old fact"}
	if err := cx.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	if _, err := cx.SoftDeleteEntity(ctx, mustEntity(t, cx), "test"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := cx.SoftDeleteMemories(ctx, cortex.DeleteFilter{IDs: []string{m.ID}}, "test"); err != nil {
		t.Fatal(err)
	}
	e.mustExec(t, `UPDATE memories SET deleted_at = now() - interval '31 days' WHERE user_id=$1`, uid)
	e.mustExec(t, `UPDATE entities SET deleted_at = now() - interval '31 days' WHERE user_id=$1 AND deleted_at IS NOT NULL`, uid)
	e.mustExec(t, `INSERT INTO oauth_clients (id, kind, created_at, expires_at) VALUES ('old','dcr', now()-interval '2 days', now()-interval '1 day')`)
	e.mustExec(t, `INSERT INTO oauth_clients (id, kind, created_at, expires_at) VALUES ('fresh','dcr', now(), now()+interval '1 day')`)
	e.mustExec(t, `INSERT INTO oauth_clients (id, kind, created_at, authorized_at) VALUES ('authd','dcr', now()-interval '5 days', now()-interval '5 days')`)
	e.mustExec(t, `INSERT INTO sessions (id_hash, user_id, csrf, expires_at) VALUES ('s1',$1,'c', now()-interval '1 hour')`, uid)
	e.mustExec(t, `INSERT INTO sessions (id_hash, user_id, csrf, expires_at) VALUES ('s2',$1,'c', now()+interval '1 hour')`, uid)
	e.mustExec(t, `INSERT INTO login_attempts (id_hash, state, nonce, expires_at) VALUES ('l1','s','n', now()-interval '1 hour')`)
	e.mustExec(t, `INSERT INTO authorize_requests (id, user_id, client_id, redirect_uri, code_challenge, expires_at) VALUES ('ar1',$1,'c','https://x','ch', now()-interval '1 hour')`, uid)

	if _, err := Nightly(ctx, e.root, e.st, NightlyOptions{Resolve: e.resolver(new(sync.Map), nil)}); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM memories WHERE id=$1`, m.ID); n != 0 {
		t.Fatal("old soft-deleted memory not purged")
	}
	if n := e.count(t, `SELECT count(*) FROM oauth_clients WHERE id='old'`); n != 0 {
		t.Fatal("expired unauthorized DCR client kept")
	}
	if n := e.count(t, `SELECT count(*) FROM oauth_clients WHERE id IN ('fresh','authd')`); n != 2 {
		t.Fatal("live or authorized client deleted")
	}
	if n := e.count(t, `SELECT count(*) FROM sessions WHERE id_hash='s1'`) + e.count(t, `SELECT count(*) FROM login_attempts`) + e.count(t, `SELECT count(*) FROM authorize_requests`); n != 0 {
		t.Fatal("expired rows kept")
	}
	if n := e.count(t, `SELECT count(*) FROM sessions WHERE id_hash='s2'`); n != 1 {
		t.Fatal("live session deleted")
	}
}

func mustEntity(t *testing.T, cx *cortex.Cortex) string {
	t.Helper()
	en := &cortex.Entity{Type: "thing", Name: "Doomed"}
	if err := cx.PutEntity(t.Context(), en); err != nil {
		t.Fatal(err)
	}
	return en.ID
}

func (e *env) mustExec(t *testing.T, q string, args ...any) {
	t.Helper()
	if _, err := e.db.ExecContext(t.Context(), q, args...); err != nil {
		t.Fatal(err)
	}
}

func TestAccountPurgedAfter30Days(t *testing.T) {
	e := newEnv(t)
	u31, u29 := e.user(t, "d31"), e.user(t, "d29")
	seedRows(t, e, u31)
	seedRows(t, e, u29)
	for _, u := range []string{u31, u29} {
		if err := e.st.DeleteAccount(t.Context(), u); err != nil {
			t.Fatal(err)
		}
	}
	e.backdate(t, u31, 31)
	e.backdate(t, u29, 29)
	if _, err := Nightly(t.Context(), e.root, e.st, NightlyOptions{Resolve: e.resolver(new(sync.Map), nil)}); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"users WHERE id", "memories WHERE user_id", "entities WHERE user_id", "user_settings WHERE user_id", "connections WHERE user_id"} {
		if n := e.count(t, `SELECT count(*) FROM `+tbl+` = $1`, u31); n != 0 {
			t.Fatalf("%s: rows remain for the 31-day account: %d", tbl, n)
		}
	}
	if err := e.st.RestoreAccount(t.Context(), u29); err != nil {
		t.Fatalf("29-day account not restorable: %v", err)
	}
	if n := e.count(t, `SELECT count(*) FROM memories WHERE user_id=$1`, u29); n == 0 {
		t.Fatal("29-day account lost data")
	}
}

func TestNightlyMaintainUsesActiveEmbedder(t *testing.T) {
	var mu sync.Mutex
	models := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, 1<<20)
		n, _ := r.Body.Read(body)
		b := string(body[:n])
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			model := "?"
			for _, m := range []string{"model-active", "model-new"} {
				if strings.Contains(b, `"`+m+`"`) {
					model = m
				}
			}
			mu.Lock()
			models[model]++
			mu.Unlock()
			// one vector per input (count "input" array elements by commas is fragile; send 3 and trim)
			vec := "[" + strings.TrimRight(strings.Repeat("0.5,", 256), ",") + "]"
			w.Write([]byte(`{"object":"list","model":"m","data":[{"object":"embedding","index":0,"embedding":` + vec + `}]}`))
			return
		}
		w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"{\"entities\":[],\"relationships\":[]}"},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	e := newEnv(t)
	e.st.AllowPrivate = true
	uidA, uk := e.bareEnc(t, "act")
	u := struct{ ID string }{uidA}
	ctx := t.Context()
	s := accounts.Settings{BaseURL: srv.URL, APIKey: "sk-test-1234", ChatModel: "m", EmbedModel: "model-active", EmbedDims: 256}
	if _, err := e.st.PutSettings(ctx, u.ID, s, uk); err != nil {
		t.Fatal(err)
	}
	// An existing embedding makes the settings change non-promoting (re-embed pending).
	m := &cortex.Memory{Content: "seed"}
	cx0 := e.handle(u.ID, nil, 0)
	if err := cx0.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := cx0.PutEmbeddingGen(ctx, m.ID, "memory", 1, vecFor("seed", 256)); err != nil {
		t.Fatal(err)
	}
	s.EmbedModel = "model-new"
	needs, err := e.st.PutSettings(ctx, u.ID, s, uk)
	if err != nil || !needs {
		t.Fatalf("expected pending re-embed: %v %v", needs, err)
	}

	var enq []string
	o := NightlyOptions{Enqueue: func(_ context.Context, uid, kind string) (string, error) {
		enq = append(enq, uid+":"+kind)
		return "j", nil
	}}
	o.beforeMaintain = func(ctx context.Context, uid string, cx *cortex.Cortex) {
		if err := cx.Remember(ctx, "the active embedder embeds this"); err != nil {
			t.Errorf("remember: %v", err)
		}
		if g, _ := cx.ActiveGeneration(ctx); g != 1 {
			t.Errorf("active generation %d", g)
		}
	}
	// Maintenance of an encrypted user runs only under its key.
	if err := maintainUser(ctx, e.root, e.st, u.ID, &o, uk); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	active, fresh := models["model-active"], models["model-new"]
	mu.Unlock()
	if active == 0 || fresh != 0 {
		t.Fatalf("embed calls by model: active=%d new=%d", active, fresh)
	}
	if len(enq) != 1 || enq[0] != u.ID+":reembed" {
		t.Fatalf("enqueue: %v", enq)
	}
	// A queued reembed suppresses a second enqueue.
	e.mustExec(t, `INSERT INTO jobs (id, user_id, kind, status) VALUES ('jq',$1,'reembed','queued')`, u.ID)
	if err := maintainUser(ctx, e.root, e.st, u.ID, &o, uk); err != nil {
		t.Fatal(err)
	}
	if len(enq) != 1 {
		t.Fatalf("re-enqueued while one is queued: %v", enq)
	}
}

func TestNightlyPurgesExpiredSessionWraps(t *testing.T) {
	e := newEnv(t)
	uid := e.user(t, "w")
	e.mustExec(t, `INSERT INTO sessions (id_hash, user_id, csrf, expires_at) VALUES ('live',$1,'c', now()+interval '1 day')`, uid)
	e.mustExec(t, `INSERT INTO user_key_wraps (user_id, kind, ref_id, wrapped_uk, expires_at) VALUES ($1,'session','live','x', now()-interval '1 minute')`, uid)
	e.mustExec(t, `INSERT INTO user_key_wraps (user_id, kind, ref_id, wrapped_uk, expires_at) VALUES ($1,'session','kept','x', now()+interval '1 hour')`, uid)
	e.mustExec(t, `INSERT INTO sessions (id_hash, user_id, csrf, expires_at) VALUES ('kept',$1,'c', now()+interval '1 day')`, uid)
	e.mustExec(t, `INSERT INTO user_key_wraps (user_id, kind, ref_id, wrapped_uk, expires_at) VALUES ($1,'session','orphan','x', now()+interval '1 hour')`, uid)
	if _, err := Nightly(t.Context(), e.root, e.st, NightlyOptions{Resolve: e.resolver(new(sync.Map), nil)}); err != nil {
		t.Fatal(err)
	}
	if n := e.count(t, `SELECT count(*) FROM user_key_wraps WHERE kind='session'`); n != 1 {
		t.Fatalf("%d session wraps left, want only the live one", n)
	}
	if n := e.count(t, `SELECT count(*) FROM user_key_wraps WHERE ref_id='kept'`); n != 1 {
		t.Fatal("live wrap deleted")
	}
}

func TestNightlyDoesNotDecryptAndStillPurges(t *testing.T) {
	e := newEnv(t)
	uid, _ := e.encUser(t, "np")
	var calls sync.Map
	m := &cortex.Memory{Content: "old fact"}
	if err := e.handle(uid, nil, 0).PutMemory(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	e.mustExec(t, `UPDATE memories SET deleted_at = now() - interval '31 days' WHERE user_id=$1`, uid)
	e.mustExec(t, `INSERT INTO sessions (id_hash, user_id, csrf, expires_at) VALUES ('s1',$1,'c', now()-interval '1 hour')`, uid)
	e.mustExec(t, `INSERT INTO user_key_wraps (user_id, kind, ref_id, wrapped_uk, expires_at) VALUES ($1,'session','s1','x', now()-interval '1 minute')`, uid)
	rep, err := Nightly(t.Context(), e.root, e.st, NightlyOptions{Resolve: e.resolver(&calls, nil)})
	if err != nil {
		t.Fatal(err)
	}
	if callsOf(&calls, uid) != 0 || rep.Failed != 0 || rep.Maintained != 0 {
		t.Fatalf("nightly touched an encrypted user: calls=%d rep=%+v", callsOf(&calls, uid), rep)
	}
	if e.count(t, `SELECT count(*) FROM memories WHERE id=$1`, m.ID) != 0 {
		t.Fatal("old soft-deleted row not purged")
	}
	if e.count(t, `SELECT count(*) FROM sessions WHERE id_hash='s1'`)+e.count(t, `SELECT count(*) FROM user_key_wraps WHERE ref_id='s1'`) != 0 {
		t.Fatal("expired session or wrap kept")
	}
	if e.count(t, `SELECT count(*) FROM users WHERE id=$1 AND last_maintenance_error IS NOT NULL`, uid) != 0 {
		t.Fatal("encrypted user recorded a nightly failure")
	}
}

// The key-free purge phase removes article rows whose subject is gone, and
// only those: an existing entity, a soft-deleted (restorable) entity and a
// persisted topic keep theirs.
func TestNightlyPurgesOrphanArticlesWithoutKey(t *testing.T) {
	e := newEnv(t)
	uid, _ := e.encUser(t, "orph")
	cx := e.handle(uid, nil, 0)
	h := cx.StoreHandle()
	live := &cortex.Entity{Type: "person", Name: "Live Person"}
	soft := &cortex.Entity{Type: "person", Name: "Soft Person"}
	for _, en := range []*cortex.Entity{live, soft} {
		if err := cx.PutEntity(t.Context(), en); err != nil {
			t.Fatal(err)
		}
	}
	e.mustExec(t, `UPDATE entities SET deleted_at = now() WHERE id = $1`, soft.ID)
	if err := h.PutTopics(t.Context(), []store.TopicRow{{TopicID: "topic-live", ShortID: "tlive001", Slug: "t", Title: "T", Members: []string{live.ID}}}); err != nil {
		t.Fatal(err)
	}
	put := func(subject, kind string) {
		t.Helper()
		err := h.PutArticle(t.Context(), store.ArticleRow{SubjectID: subject, Kind: kind, ShortID: "s" + subject,
			Slug: "x", Fingerprint: []byte{1}, Article: model.Article{Lead: "x"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	put(live.ID, "entity")
	put(soft.ID, "entity")
	put("gone-entity", "entity")
	put("topic-live", "topic")
	put("topic-gone", "topic")

	var calls sync.Map
	rep, err := Nightly(t.Context(), e.root, e.st, NightlyOptions{Resolve: e.resolver(&calls, nil)})
	if err != nil {
		t.Fatal(err)
	}
	if callsOf(&calls, uid) != 0 {
		t.Fatal("nightly resolved the encrypted user")
	}
	if rep.ArticlesPurged != 2 {
		t.Fatalf("purged %d, want 2", rep.ArticlesPurged)
	}
	left := map[string]bool{}
	rows, err := e.db.QueryContext(t.Context(), `SELECT subject_id FROM vault_articles WHERE user_id = $1`, uid)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		left[id] = true
	}
	if len(left) != 3 || !left[live.ID] || !left[soft.ID] || !left["topic-live"] {
		t.Fatalf("remaining %v", left)
	}
}
