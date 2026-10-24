package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sausheong/cortex/internal/accounts"
)

// rememberProvider answers chat with one extracted memory and embeddings with
// 512-dim vectors, so a remember job runs to completion.
func rememberProvider(t *testing.T, failChat *atomic.Bool) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/embeddings") {
			var in struct {
				Input []string `json:"input"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			data := make([]map[string]any, len(in.Input))
			for i, s := range in.Input {
				data[i] = map[string]any{"object": "embedding", "index": i, "embedding": vecFor(s, 512)}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data, "model": "m"})
			return
		}
		if failChat != nil && failChat.Load() {
			http.Error(w, "down", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "x", "object": "chat.completion", "choices": []map[string]any{
			{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": `{"memories":[{"content":"Maria Lopez leads the billing team at Acme Corp","confidence":0.9}]}`}}}})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func rememberEnv(t *testing.T, url string) (*env, string, *accounts.Connection) {
	t.Helper()
	e := newEnv(t)
	e.st.AllowPrivate = true
	u, err := e.root.CreateUser(t.Context(), "r", "r@x.test", "r", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.PutSettings(t.Context(), u.ID, accounts.Settings{BaseURL: url, APIKey: "sk-test", ChatModel: "m", EmbedModel: "e", EmbedDims: 512}); err != nil {
		t.Fatal(err)
	}
	conn, err := e.st.CreateConnection(t.Context(), u.ID, "agent", "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	return e, u.ID, conn
}

func TestRememberJobStoresWithConnectionProvenance(t *testing.T) {
	e, uid, conn := rememberEnv(t, rememberProvider(t, nil))
	pool := NewPool(2, func(ctx context.Context, j Job) error {
		return RememberJob(ctx, e.root, e.st, j.UserID, j.Payload)
	}).WithDB(e.db)
	text := strings.Repeat("Maria Lopez leads the billing team at Acme Corp. ", 300)
	id, err := EnqueueRemember(t.Context(), pool, uid, RememberPayload{Content: text, Source: "doc.md", Speaker: "maria", ConnID: conn.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pool.Start(t.Context())
	defer pool.Stop()
	waitStatus(t, e, id, "done")

	if n := e.count(t, `SELECT count(*) FROM chunks WHERE user_id=$1 AND written_by_connection_id=$2`, uid, conn.ID); n < 2 {
		t.Fatalf("chunks written by the connection = %d, want the document split into several", n)
	}
	if n := e.count(t, `SELECT count(*) FROM chunks WHERE user_id=$1 AND written_by_connection_id IS DISTINCT FROM $2`, uid, conn.ID); n != 0 {
		t.Fatalf("%d chunks lack the connection", n)
	}
	if n := e.count(t, `SELECT count(*) FROM memories WHERE user_id=$1 AND written_by_connection_id IS DISTINCT FROM $2`, uid, conn.ID); n != 0 {
		t.Fatalf("%d memories lack the connection", n)
	}
	if n := e.count(t, `SELECT count(*) FROM memories WHERE user_id=$1 AND written_by_connection_id=$2 AND speaker='maria' AND source='doc.md'`, uid, conn.ID); n == 0 {
		t.Fatal("no memory carries the connection, source and speaker")
	}
	if n := e.count(t, `SELECT count(*) FROM jobs WHERE id=$1 AND payload=''`, id); n != 1 {
		t.Fatal("payload kept after the job finished")
	}
}

func TestRememberJobRetriesAndResumesAfterRestart(t *testing.T) {
	var down atomic.Bool
	down.Store(true)
	e, uid, conn := rememberEnv(t, rememberProvider(t, &down))
	run := func(ctx context.Context, j Job) error {
		return RememberJob(ctx, e.root, e.st, j.UserID, j.Payload)
	}
	pool := NewPool(1, run).WithDB(e.db)
	text := strings.Repeat("Maria Lopez leads the billing team at Acme Corp. ", 300)
	id, err := EnqueueRemember(t.Context(), pool, uid, RememberPayload{Content: text, ConnID: conn.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	pool.Start(t.Context())
	// A transient failure puts the job back in the queue for later.
	deadline := func() (status, msg string, attempts int, later bool) {
		e.db.QueryRow(`SELECT status, error, attempts, run_after > now() FROM jobs WHERE id=$1`, id).Scan(&status, &msg, &attempts, &later)
		return
	}
	for i := 0; i < 500; i++ {
		if _, _, a, _ := deadline(); a == 1 {
			break
		}
		sleep10()
	}
	pool.Stop()
	status, msg, attempts, later := deadline()
	if status != "queued" || attempts != 1 || !later || msg == "" {
		t.Fatalf("after failure: status=%q attempts=%d later=%v err=%q", status, attempts, later, msg)
	}

	// The provider recovers and the process restarts: the queued job resumes.
	down.Store(false)
	e.db.Exec(`UPDATE jobs SET run_after = now() WHERE id=$1`, id) // the backoff has passed
	pool2 := NewPool(1, run).WithDB(e.db)
	pool2.Start(t.Context())
	defer pool2.Stop()
	waitStatus(t, e, id, "done")
	if n := e.count(t, `SELECT count(*) FROM chunks WHERE user_id=$1`, uid); n < 2 {
		t.Fatalf("chunks = %d after resume", n)
	}
}

func TestRememberJobGivesUpAfterMaxAttempts(t *testing.T) {
	e := newEnv(t)
	u := e.user(t, "g")
	pool := NewPool(1, func(context.Context, Job) error { return errors.New("boom") }).WithDB(e.db)
	id, err := pool.EnqueueWith(t.Context(), u, RememberKind, `{"content":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	e.db.Exec(`UPDATE jobs SET attempts=$2 WHERE id=$1`, id, maxAttempts-1)
	pool.Start(t.Context())
	defer pool.Stop()
	waitStatus(t, e, id, "failed")
}

func sleep10() { time.Sleep(10 * time.Millisecond) }
