package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/pgtest"
)

const testKeyHex = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f"

// vecFor is a deterministic bag-of-words embedding.
func vecFor(text string, dims int) []float32 {
	v := make([]float32, dims)
	for _, w := range strings.Fields(strings.ToLower(text)) {
		h := fnv.New32a()
		h.Write([]byte(strings.Trim(w, ".,:")))
		v[int(h.Sum32())%dims]++
	}
	return v
}

// stub is a counting embedder.
type stub struct {
	dims  int
	calls atomic.Int64
	// after, if set, runs after each Embed call (1-based call number).
	after func(call int64)
	fail  func(call int64) error
}

func (s *stub) Embed(_ context.Context, texts []string) ([][]float32, error) {
	n := s.calls.Add(1)
	if s.fail != nil {
		if err := s.fail(n); err != nil {
			return nil, err
		}
	}
	out := make([][]float32, len(texts))
	for i, t := range texts {
		out[i] = vecFor(t, s.dims)
	}
	if s.after != nil {
		s.after(n)
	}
	return out, nil
}
func (s *stub) Dimensions() int { return s.dims }

type env struct {
	dsn  string
	root *cortex.Cortex
	db   *sql.DB
	st   *accounts.Store
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dsn := pgtest.DSN(t)
	root, err := cortex.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	sl, err := accounts.NewSealer(testKeyHex)
	if err != nil {
		t.Fatal(err)
	}
	return &env{dsn: dsn, root: root, db: db, st: &accounts.Store{DB: db, Sealer: sl}}
}

func (e *env) user(t *testing.T, sub string) string {
	t.Helper()
	u, err := e.root.CreateUser(t.Context(), sub, sub+"@x.test", sub, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.PutSettings(t.Context(), u.ID, accounts.Settings{
		BaseURL: "https://api.example.com/v1", APIKey: "sk-secret-3f9a", ChatModel: "m", EmbedModel: "e", EmbedDims: 256,
	}); err != nil {
		t.Fatal(err)
	}
	return u.ID
}

func (e *env) handle(uid string, emb cortex.Embedder, gen int) *cortex.Cortex {
	cx := e.root.ForUser(uid, cortex.WithEmbedder(emb))
	if gen > 0 {
		cx = cx.WithGeneration(gen)
	}
	return cx
}

// addMemory writes a memory and its embedding at the handle's active gen.
func addMemory(t *testing.T, cx *cortex.Cortex, emb cortex.Embedder, text string) string {
	t.Helper()
	ctx := t.Context()
	m := &cortex.Memory{Content: text}
	if err := cx.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	g, err := cx.ActiveGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	vecs, _ := emb.Embed(ctx, []string{text})
	if err := cx.PutEmbeddingGen(ctx, m.ID, "memory", g, vecs[0]); err != nil {
		t.Fatal(err)
	}
	return m.ID
}

func addChunk(t *testing.T, cx *cortex.Cortex, emb cortex.Embedder, text string) string {
	t.Helper()
	ctx := t.Context()
	ch := &cortex.Chunk{Content: text}
	if err := cx.PutChunk(ctx, ch); err != nil {
		t.Fatal(err)
	}
	g, _ := cx.ActiveGeneration(ctx)
	vecs, _ := emb.Embed(ctx, []string{text})
	if err := cx.PutEmbeddingGen(ctx, ch.ID, "chunk", g, vecs[0]); err != nil {
		t.Fatal(err)
	}
	return ch.ID
}

func (e *env) count(t *testing.T, q string, args ...any) int {
	t.Helper()
	var n int
	if err := e.db.QueryRowContext(t.Context(), q, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func hasMemory(ms []cortex.Memory, id string) bool {
	for _, m := range ms {
		if m.ID == id {
			return true
		}
	}
	return false
}

type extractStub struct{}

func (extractStub) Extract(_ context.Context, content, _ string) (*cortex.Extraction, error) {
	return &cortex.Extraction{Memories: []cortex.Memory{{Content: content}}}, nil
}

// pausing makes a target embedder stop after its n-th call until resume.
func pausing(dims int, n int64) (s *stub, reached chan struct{}, resume chan struct{}) {
	reached, resume = make(chan struct{}), make(chan struct{})
	s = &stub{dims: dims}
	s.after = func(call int64) {
		if call == n {
			close(reached)
			<-resume
		}
	}
	return
}

func seedMemories(t *testing.T, cx *cortex.Cortex, emb cortex.Embedder, n int) []string {
	t.Helper()
	ids := make([]string, n)
	for i := range ids {
		ids[i] = addMemory(t, cx, emb, fmt.Sprintf("seeded fact number%d word%d", i, i*7))
	}
	return ids
}

func TestReEmbedSwitchesGeneration(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	uid := e.user(t, "a")
	oldE, newE := &stub{dims: 8}, &stub{dims: 16}
	cx := e.handle(uid, oldE, 0)
	addChunk(t, cx, oldE, "zebra crossing")
	addChunk(t, cx, oldE, "quokka island")

	before, err := e.handle(uid, newE, 0).SearchVector(ctx, "zebra", 5)
	if err != nil || len(before) != 0 {
		t.Fatalf("new-model search before re-embed = %v err=%v, want none", before, err)
	}
	var progress []int
	if err := ReEmbed(ctx, cx, e.st, newE, func(d, tot int) { progress = append(progress, d) }); err != nil {
		t.Fatal(err)
	}
	if len(progress) == 0 {
		t.Fatal("no progress reported")
	}
	got, err := e.handle(uid, newE, 0).SearchVector(ctx, "zebra", 5)
	if err != nil || len(got) == 0 || !strings.Contains(got[0].Content, "zebra") {
		t.Fatalf("new-model search after = %v err=%v", got, err)
	}
	if g, _ := cx.ActiveGeneration(ctx); g != 2 {
		t.Fatalf("active generation = %d, want 2", g)
	}
	if n := e.count(t, `SELECT count(*) FROM embeddings WHERE user_id=$1 AND generation<>2`, uid); n != 0 {
		t.Fatalf("%d old-generation rows remain", n)
	}
}

func TestReEmbedResumes(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	uid := e.user(t, "a")
	oldE := &stub{dims: 8}
	cx := e.handle(uid, oldE, 0)
	seedMemories(t, cx, oldE, 130)

	bad := &stub{dims: 16, fail: func(c int64) error {
		if c == 2 {
			return errors.New("boom")
		}
		return nil
	}}
	if err := ReEmbed(ctx, cx, e.st, bad, nil); err == nil {
		t.Fatal("first run should fail")
	}
	if g, _ := cx.ActiveGeneration(ctx); g != 1 {
		t.Fatalf("active generation after failure = %d, want 1", g)
	}
	partial := e.count(t, `SELECT count(*) FROM embeddings WHERE user_id=$1 AND generation=2`, uid)
	if partial == 0 {
		t.Fatal("expected partial new-generation rows")
	}
	good := &stub{dims: 16}
	if err := ReEmbed(ctx, cx, e.st, good, nil); err != nil {
		t.Fatal(err)
	}
	rows := e.count(t, `SELECT (SELECT count(*) FROM entities WHERE user_id=$1)+(SELECT count(*) FROM memories WHERE user_id=$1)`, uid)
	if n := e.count(t, `SELECT count(*) FROM embeddings WHERE user_id=$1 AND generation=2`, uid); n != rows {
		t.Fatalf("gen-2 rows = %d, want %d", n, rows)
	}
	if n := e.count(t, `SELECT count(*) FROM (SELECT 1 FROM embeddings WHERE user_id=$1 GROUP BY ref_type, ref_id, generation HAVING count(*)>1) x`, uid); n != 0 {
		t.Fatalf("%d duplicated rows", n)
	}
	// The resumed run must not redo the rows the failed run stored.
	if int(good.calls.Load()) > 3 {
		t.Fatalf("resume made %d embed calls", good.calls.Load())
	}
}

// embedServer is an OpenAI-compatible embeddings endpoint whose vector length
// follows the model name ("old" = 256, "new" = 384).
type embedServer struct {
	*httptest.Server
	mu    sync.Mutex
	calls map[string]int
}

func newEmbedServer(t *testing.T) *embedServer {
	t.Helper()
	es := &embedServer{calls: map[string]int{}}
	es.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !strings.HasSuffix(r.URL.Path, "/embeddings") {
			http.NotFound(w, r)
			return
		}
		dims := 256
		if req.Model == "new" {
			dims = 384
		}
		es.mu.Lock()
		es.calls[req.Model]++
		es.mu.Unlock()
		type item struct {
			Object    string    `json:"object"`
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}
		var data []item
		for i, in := range req.Input {
			data = append(data, item{"embedding", i, vecFor(in, dims)})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data, "model": req.Model})
	}))
	t.Cleanup(es.Close)
	return es
}

func (es *embedServer) count(model string) int {
	es.mu.Lock()
	defer es.mu.Unlock()
	return es.calls[model]
}

func TestRecallDuringReEmbedUsesOldModel(t *testing.T) {
	e := newEnv(t)
	e.st.AllowPrivate = true
	es := newEmbedServer(t)
	ctx := t.Context()
	u, err := e.root.CreateUser(ctx, "a", "a@x.test", "a", true)
	if err != nil {
		t.Fatal(err)
	}
	uid := u.ID
	s := accounts.Settings{BaseURL: es.URL + "/v1", APIKey: "sk-test", ChatModel: "chat", EmbedModel: "old", EmbedDims: 256}
	if _, err := e.st.PutSettings(ctx, uid, s); err != nil {
		t.Fatal(err)
	}
	cx, err := e.st.ResolveUser(ctx, e.root, uid, "")
	if err != nil {
		t.Fatal(err)
	}
	ids := seedMemories(t, cx, cx.Embedder(), 70)

	s.EmbedModel, s.EmbedDims = "new", 384
	needs, err := e.st.PutSettings(ctx, uid, s)
	if err != nil || !needs {
		t.Fatalf("PutSettings needsReembed=%v err=%v", needs, err)
	}
	newEmb := mustProviders(t, e, uid)

	pause, reached, resume := pausing(384, 1)
	pause.fail = nil
	wrapped := &wrapEmb{inner: newEmb, after: pause.after, dims: 384}
	done := make(chan error, 1)
	go func() { done <- ReEmbed(ctx, cx, e.st, wrapped, nil) }()
	<-reached

	newBefore := es.count("new")
	h, err := e.st.ResolveUser(ctx, e.root, uid, "")
	if err != nil {
		t.Fatal(err)
	}
	res, err := h.Recall(ctx, "number3 word21")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range res {
		if strings.Contains(r.Content, "number3 word21") {
			found = true
		}
	}
	if !found {
		t.Fatalf("recall during re-embed missed the memory: %+v", res)
	}
	if d := es.count("new") - newBefore; d != 0 {
		t.Fatalf("recall used the new model %d times", d)
	}
	close(resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	h2, err := e.st.ResolveUser(ctx, e.root, uid, "")
	if err != nil {
		t.Fatal(err)
	}
	before := es.count("old")
	res, err = h2.Recall(ctx, "number3 word21")
	if err != nil || len(res) == 0 {
		t.Fatalf("recall after flip: %v %v", res, err)
	}
	if es.count("old") != before {
		t.Fatal("recall after the flip still used the old model")
	}
	if n, err := h2.SearchMemoryVector(ctx, "number3 word21", 3); err != nil || !hasMemory(n, ids[3]) {
		t.Fatalf("new-model vector search = %v err=%v", n, err)
	}
}

type wrapEmb struct {
	inner cortex.Embedder
	after func(int64)
	dims  int
	n     atomic.Int64
}

func (w *wrapEmb) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out, err := w.inner.Embed(ctx, texts)
	if err != nil {
		return nil, err
	}
	n := w.n.Add(1)
	if w.after != nil {
		w.after(n)
	}
	return out, nil
}
func (w *wrapEmb) Dimensions() int { return w.dims }

// mustProviders builds the embedder for the user's desired (new) settings.
func mustProviders(t *testing.T, e *env, uid string) cortex.Embedder {
	t.Helper()
	s, err := e.st.GetSettings(t.Context(), uid)
	if err != nil {
		t.Fatal(err)
	}
	hc := accounts.SafeClient(true, accounts.ChatBodyCap)
	_, emb, _, err := accounts.Providers(s, s, hc)
	if err != nil {
		t.Fatal(err)
	}
	return emb
}

func TestWriteBetweenScanAndFlipSurvives(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	uid := e.user(t, "a")
	oldE := &stub{dims: 8}
	cx := e.handle(uid, oldE, 0)
	seedMemories(t, cx, oldE, 70)

	target, reached, resume := pausing(16, 1)
	done := make(chan error, 1)
	go func() { done <- ReEmbed(ctx, cx, e.st, target, nil) }()
	<-reached
	late := addMemory(t, e.handle(uid, oldE, 1), oldE, "late arrival platypus")
	close(resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got, err := e.handle(uid, &stub{dims: 16}, 0).SearchMemoryVector(ctx, "platypus", 3)
	if err != nil || !hasMemory(got, late) {
		t.Fatalf("late write not found after flip: %v err=%v", got, err)
	}
}

func TestWriteAfterFlipWithStaleHandleRejected(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	uid := e.user(t, "a")
	oldE := &stub{dims: 8}
	cx := e.handle(uid, oldE, 0)
	addMemory(t, cx, oldE, "something old")
	stale := e.root.ForUser(uid, cortex.WithEmbedder(oldE), cortex.WithExtractor(extractStub{})).WithGeneration(1)
	if err := ReEmbed(ctx, cx, e.st, &stub{dims: 16}, nil); err != nil {
		t.Fatal(err)
	}
	before := e.count(t, `SELECT count(*) FROM embeddings WHERE user_id=$1`, uid)
	err := stale.Remember(ctx, "written by a stale handle")
	if !errors.Is(err, cortex.ErrStaleGeneration) {
		t.Fatalf("Remember = %v, want ErrStaleGeneration", err)
	}
	if n := e.count(t, `SELECT count(*) FROM embeddings WHERE user_id=$1`, uid); n != before {
		t.Fatalf("embeddings %d -> %d, want no new rows", before, n)
	}
}

func TestReEmbedIncludesSoftDeleted(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	uid := e.user(t, "a")
	oldE, newE := &stub{dims: 8}, &stub{dims: 16}
	cx := e.handle(uid, oldE, 0)
	id := addMemory(t, cx, oldE, "narwhal tusk")
	batch, n, err := cx.SoftDeleteMemories(ctx, cortex.DeleteFilter{IDs: []string{id}}, "test")
	if err != nil || n != 1 {
		t.Fatalf("soft delete: n=%d err=%v", n, err)
	}
	if err := ReEmbed(ctx, cx, e.st, newE, nil); err != nil {
		t.Fatal(err)
	}
	if err := cx.RestoreBatch(ctx, batch); err != nil {
		t.Fatal(err)
	}
	got, err := e.handle(uid, newE, 0).SearchMemoryVector(ctx, "narwhal", 3)
	if err != nil || !hasMemory(got, id) {
		t.Fatalf("restored memory not found with new model: %v err=%v", got, err)
	}
}

func TestReEmbedFlipExcludesWriters(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	uid := e.user(t, "a")
	oldE := &stub{dims: 8}
	cx := e.handle(uid, oldE, 0)
	addMemory(t, cx, oldE, "baseline fact")

	// The only embed call is the tail, made while the flip holds the lock.
	target, inFlip, resume := pausing(16, 1)
	done := make(chan error, 1)
	go func() { done <- ReEmbed(ctx, cx, e.st, target, nil) }()
	<-inFlip

	writer := e.root.ForUser(uid, cortex.WithEmbedder(oldE), cortex.WithExtractor(extractStub{})).WithGeneration(1)
	wdone := make(chan error, 1)
	go func() { wdone <- writer.Remember(ctx, "writer racing the flip") }()
	select {
	case err := <-wdone:
		t.Fatalf("writer did not block on the flip lock (err=%v)", err)
	case <-time.After(300 * time.Millisecond):
	}
	close(resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	werr := <-wdone
	if werr != nil && !errors.Is(werr, cortex.ErrStaleGeneration) {
		t.Fatalf("writer error = %v", werr)
	}
	if n := e.count(t, `SELECT count(*) FROM embeddings WHERE user_id=$1 AND generation=2 AND dims<>16`, uid); n != 0 {
		t.Fatalf("%d old-model vectors landed in the new generation", n)
	}
}

func TestIndexJobBuildsForActiveDims(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	uid := e.user(t, "a")
	plain, err := e.st.Sealer.Seal(`{"url":"https://api.example.com/v1","key":"k","model":"m","dims":16}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.ExecContext(ctx, `UPDATE user_settings SET active_embed_sealed=$2 WHERE user_id=$1`, uid, plain); err != nil {
		t.Fatal(err)
	}
	pool := NewPool(1, func(ctx context.Context, j Job) error {
		if j.Kind != "index" {
			return fmt.Errorf("unexpected kind %s", j.Kind)
		}
		return IndexJob(ctx, e.root, e.st, j.UserID)
	}).WithDB(e.db)
	pool.Start(ctx)
	defer pool.Stop()
	id, err := pool.Enqueue(ctx, uid, "index")
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, id, "done")
	for _, rt := range []string{"entity", "memory", "chunk"} {
		if n := e.count(t, `SELECT count(*) FROM pg_indexes WHERE schemaname=current_schema() AND indexname=$1`, "emb_hnsw_"+rt+"_16"); n != 1 {
			t.Fatalf("index emb_hnsw_%s_16 missing", rt)
		}
	}
}

func waitStatus(t *testing.T, e *env, id, want string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		e.db.QueryRowContext(t.Context(), `SELECT status FROM jobs WHERE id=$1`, id).Scan(&got)
		if got == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	var msg string
	e.db.QueryRowContext(t.Context(), `SELECT error FROM jobs WHERE id=$1`, id).Scan(&msg)
	t.Fatalf("job %s status = %q (%s), want %q", id, got, msg, want)
}

func TestPoolRunsTwoConcurrent(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	uid := e.user(t, "a")
	var inflight, peak atomic.Int64
	release := make(chan struct{})
	pool := NewPool(2, func(ctx context.Context, j Job) error {
		n := inflight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		defer inflight.Add(-1)
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}).WithDB(e.db)
	pool.Start(ctx)
	defer pool.Stop()
	var ids []string
	for i := 0; i < 3; i++ {
		id, err := pool.Enqueue(ctx, uid, "x")
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	deadline := time.Now().Add(5 * time.Second)
	for inflight.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if inflight.Load() != 2 {
		t.Fatalf("in flight = %d, want exactly 2 with 3 queued", inflight.Load())
	}
	close(release)
	for _, id := range ids {
		waitStatus(t, e, id, "done")
	}
	if peak.Load() != 2 {
		t.Fatalf("peak concurrency = %d, want 2", peak.Load())
	}
}

func TestPoolSkipsDeletedUser(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	uid := e.user(t, "a")
	if _, err := e.db.ExecContext(ctx, `UPDATE users SET deleted_at = now() WHERE id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	var ran atomic.Int64
	pool := NewPool(2, func(ctx context.Context, j Job) error { ran.Add(1); return nil }).WithDB(e.db)
	pool.Start(ctx)
	defer pool.Stop()
	id, err := pool.Enqueue(ctx, uid, "reembed")
	if err != nil {
		t.Fatal(err)
	}
	waitStatus(t, e, id, "skipped")
	if ran.Load() != 0 {
		t.Fatal("job ran for a deleted user")
	}
}

func TestEnsureIndexesCreatesForDims(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	cx := e.root.ForUser(cortex.BootstrapUserID)
	for i := 0; i < 2; i++ { // idempotent
		if err := EnsureIndexes(ctx, cx, 24); err != nil {
			t.Fatal(err)
		}
	}
	for _, rt := range []string{"entity", "memory", "chunk"} {
		if n := e.count(t, `SELECT count(*) FROM pg_indexes WHERE schemaname=current_schema() AND indexname=$1`, "emb_hnsw_"+rt+"_24"); n != 1 {
			t.Fatalf("index emb_hnsw_%s_24 missing", rt)
		}
	}
}

func TestIsolationReEmbed(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	a, b := e.user(t, "a"), e.user(t, "b")
	oldE, newE := &stub{dims: 8}, &stub{dims: 16}
	ca, cb := e.handle(a, oldE, 0), e.handle(b, oldE, 0)
	seedMemories(t, ca, oldE, 3)
	seedMemories(t, cb, oldE, 3)
	snap := func() string {
		return fmt.Sprint(e.count(t, `SELECT count(*) FROM embeddings WHERE user_id=$1 AND generation=1 AND dims=8`, b),
			e.count(t, `SELECT count(*) FROM embeddings WHERE user_id=$1 AND generation<>1`, b),
			e.count(t, `SELECT active_embedding_generation FROM users WHERE id=$1`, b))
	}
	before := snap()
	if err := ReEmbed(ctx, ca, e.st, newE, nil); err != nil {
		t.Fatal(err)
	}
	if after := snap(); after != before {
		t.Fatalf("user b changed: %s -> %s", before, after)
	}
	if g, _ := ca.ActiveGeneration(ctx); g != 2 {
		t.Fatalf("a generation = %d", g)
	}
}
