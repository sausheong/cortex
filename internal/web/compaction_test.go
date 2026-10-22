package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/sausheong/cortex"
)

type compactEmb struct{}

func (compactEmb) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, 8)
		v[i%8] = 1
		out[i] = v
	}
	return out, nil
}
func (compactEmb) Dimensions() int { return 8 }

// compactEnv is newEnv plus the compaction routes.
func compactEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.ts.Close()
	srv := newServer(Config{PublicURL: "https://cortex.test", BootstrapEmail: "owner@x.test", Identity: e.idp},
		e.root, e.st, Options{Now: func() time.Time { return e.now },
			Mounts: []func(*http.ServeMux, Deps){compactionMount(
				func(ctx context.Context, userID, kind string) (string, error) {
					e.enq = append(e.enq, kind)
					return "j_stub", nil
				},
				func(ctx context.Context, userID string) (*cortex.Cortex, error) {
					return e.root.ForUser(userID, cortex.WithEmbedder(compactEmb{})), nil
				})}})
	srv.testProvider = e.srv.testProvider
	e.srv = srv
	e.ts = httptest.NewUnstartedServer(srv.handler())
	e.ts.StartTLS()
	t.Cleanup(e.ts.Close)
	e.newClient()
	return e
}

func (e *env) seedMemory(userID, content string) string {
	e.t.Helper()
	m := &cortex.Memory{Content: content}
	if err := e.root.ForUser(userID).PutMemory(e.t.Context(), m); err != nil {
		e.t.Fatal(err)
	}
	return m.ID
}

func (e *env) seedCompaction(id, userID string, changes []cortex.Change) {
	e.t.Helper()
	b, _ := json.Marshal(changes)
	if _, err := e.st.DB.Exec(`INSERT INTO compactions (id, user_id, status, proposal_json) VALUES ($1,$2,'ready',$3::jsonb)`,
		id, userID, string(b)); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) liveContents(userID string) map[string]bool {
	e.t.Helper()
	rows, err := e.st.DB.Query(`SELECT content FROM memories WHERE user_id=$1 AND deleted_at IS NULL`, userID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var s string
		rows.Scan(&s)
		out[s] = true
	}
	return out
}

func TestCompactStartEnqueues(t *testing.T) {
	e := compactEnv(t)
	e.signIn(alice(), "")
	csrf := e.csrf()
	r, _ := e.post("/memories/compact", url.Values{"csrf": {csrf}})
	if r.StatusCode != 303 || !strings.HasPrefix(r.Header.Get("Location"), "/compaction/c_") {
		t.Fatalf("%d %q", r.StatusCode, r.Header.Get("Location"))
	}
	if len(e.enq) != 1 || !strings.HasPrefix(e.enq[0], "compact:c_") {
		t.Fatalf("enq = %v", e.enq)
	}
	if r, body := e.get(r.Header.Get("Location")); r.StatusCode != 200 || !strings.Contains(body, "refreshes") {
		t.Fatalf("%d %.200s", r.StatusCode, body)
	}
}

func TestApplyOnlySelected(t *testing.T) {
	e := compactEnv(t)
	e.signIn(alice(), "")
	a := e.userBySub("g-alice").ID
	m1, m2 := e.seedMemory(a, "one"), e.seedMemory(a, "two")
	m3, m4 := e.seedMemory(a, "three"), e.seedMemory(a, "four")
	e.seedCompaction("c_1", a, []cortex.Change{
		{Kind: "merge", SourceIDs: []string{m1, m2}, Result: "one-two", Sources: []string{"one", "two"}},
		{Kind: "merge", SourceIDs: []string{m3, m4}, Result: "three-four", Sources: []string{"three", "four"}},
	})
	csrf := e.csrf()
	if r, body := e.get("/compaction/c_1"); r.StatusCode != 200 || !strings.Contains(body, "one-two") {
		t.Fatalf("%d %.300s", r.StatusCode, body)
	}
	if r, body := e.post("/compaction/c_1/apply", url.Values{"csrf": {csrf}, "idx": {"1"}}); r.StatusCode != 303 {
		t.Fatalf("%d %s", r.StatusCode, body)
	}
	live := e.liveContents(a)
	if !live["one"] || !live["two"] || live["three"] || live["four"] || !live["three-four"] {
		t.Fatalf("live = %v", live)
	}
	// Undo restores.
	if r, body := e.post("/compaction/c_1/undo", url.Values{"csrf": {csrf}}); r.StatusCode != 303 {
		t.Fatalf("%d %s", r.StatusCode, body)
	}
	live = e.liveContents(a)
	if !live["three"] || !live["four"] || live["three-four"] {
		t.Fatalf("after undo live = %v", live)
	}
}

func TestApplyIgnoresPostedChangeBodies(t *testing.T) {
	e := compactEnv(t)
	e.signIn(alice(), "")
	a := e.userBySub("g-alice").ID
	b, _ := e.root.CreateUser(t.Context(), "g-bob", "bob@x.test", "Bob", true)
	am1, am2 := e.seedMemory(a, "one"), e.seedMemory(a, "two")
	bm := e.seedMemory(b.ID, "bob private")
	e.seedCompaction("c_1", a, []cortex.Change{{Kind: "merge", SourceIDs: []string{am1, am2}, Result: "stored result"}})
	csrf := e.csrf()
	r, _ := e.post("/compaction/c_1/apply", url.Values{
		"csrf": {csrf}, "idx": {"0"},
		"source_ids": {bm}, "result": {"evil"}, "changes": {`[{"source_ids":["` + bm + `"],"result":"evil"}]`},
	})
	if r.StatusCode != 303 {
		t.Fatalf("status %d", r.StatusCode)
	}
	if live := e.liveContents(a); !live["stored result"] || live["evil"] || live["one"] {
		t.Fatalf("alice live = %v", live)
	}
	if live := e.liveContents(b.ID); !live["bob private"] || len(live) != 1 {
		t.Fatalf("bob live = %v", live)
	}
}

func TestIsolationCompaction(t *testing.T) {
	e := compactEnv(t)
	e.signIn(alice(), "")
	b, _ := e.root.CreateUser(t.Context(), "g-bob", "bob@x.test", "Bob", true)
	bm := e.seedMemory(b.ID, "bob private")
	e.seedCompaction("c_bob", b.ID, []cortex.Change{{Kind: "merge", SourceIDs: []string{bm}, Result: "x"}})
	csrf := e.csrf()
	r1, b1 := e.get("/compaction/c_bob")
	r2, b2 := e.get("/compaction/c_absent")
	if r1.StatusCode != 404 || r2.StatusCode != 404 || b1 != b2 {
		t.Fatalf("get: %d %q vs %d %q", r1.StatusCode, b1, r2.StatusCode, b2)
	}
	for _, act := range []string{"apply", "undo"} {
		f := url.Values{"csrf": {csrf}, "idx": {"0"}}
		r1, b1 := e.post("/compaction/c_bob/"+act, f)
		r2, b2 := e.post("/compaction/c_absent/"+act, f)
		if r1.StatusCode != 404 || r2.StatusCode != 404 || b1 != b2 {
			t.Fatalf("%s: %d %q vs %d %q", act, r1.StatusCode, b1, r2.StatusCode, b2)
		}
	}
	if live := e.liveContents(b.ID); !live["bob private"] {
		t.Fatal("bob's memory changed")
	}
}
