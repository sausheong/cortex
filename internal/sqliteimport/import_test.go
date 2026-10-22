package sqliteimport

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/pgtest"
)

type fakeEmb struct {
	calls  atomic.Int32
	failAt int32 // fail on the Nth call (1-based); 0 = never
}

func (f *fakeEmb) Dimensions() int { return 512 }
func (f *fakeEmb) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	n := f.calls.Add(1)
	if f.failAt > 0 && n >= f.failAt {
		return nil, errors.New("embedder down")
	}
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, 512)
		v[i%512] = 1
		out[i] = v
	}
	return out, nil
}

func makeSQLite(t *testing.T, createdAt string) string {
	t.Helper()
	schema, err := os.ReadFile("testdata/v040_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	stmts := []string{
		string(schema),
		`INSERT INTO entities (id,type,name,attributes,source,created_at,updated_at,confidence) VALUES
			('e1','person','Alice','{"k":"v"}','s','` + createdAt + `','2026-03-01T08:00:00Z',0.9),
			('e2','org','Acme',NULL,NULL,'2026-03-01 08:00:00','2026-03-01 08:00:00',1.0)`,
		`INSERT INTO relationships (id,source_id,target_id,type,created_at) VALUES ('r1','e1','e2','works_at','2026-03-01 08:00:00')`,
		`INSERT INTO memories (id,content,source,created_at,updated_at,valid_at,static,speaker) VALUES
			('m1','Alice joined Acme','s','2026-03-01 08:00:00','2026-03-01 08:00:00','2026-03-01 08:00:00',0,'bob'),
			('m2','Alice likes tea','s','2026-03-01 08:00:00','2026-03-01 08:00:00',NULL,1,NULL)`,
		`INSERT INTO memory_entities (memory_id,entity_id) VALUES ('m1','e1'),('m1','e2')`,
		`INSERT INTO memory_edges (id,source_id,target_id,type,created_at) VALUES ('x1','m1','m2','extends','2026-03-01 08:00:00')`,
		`INSERT INTO chunks (id,entity_id,content,created_at) VALUES ('c1','e1','hello world','2026-03-01 08:00:00')`,
		`INSERT INTO sync_state (connector,state,updated_at) VALUES ('files','{}','2026-03-01 08:00:00')`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("fixture: %v\n%s", err, s)
		}
	}
	return path
}

func openTarget(t *testing.T, emb cortex.Embedder) *cortex.Cortex {
	t.Helper()
	cx, err := cortex.Open(pgtest.DSN(t), cortex.WithEmbedder(emb))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cx.Close() })
	return cx
}

func TestImportCopiesAll(t *testing.T) {
	path := makeSQLite(t, "2026-03-01 08:00:00")
	cx := openTarget(t, &fakeEmb{})
	rep, err := Import(context.Background(), path, cx, cortex.BootstrapUserID, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for tbl, p := range rep.Counts {
		if p[0] != p[1] || p[0] == 0 {
			t.Errorf("%s: source %d target %d", tbl, p[0], p[1])
		}
	}
	var valid time.Time
	var static bool
	if err := cx.DB().QueryRow(`SELECT valid_at, static FROM memories WHERE id='m1'`).Scan(&valid, &static); err != nil {
		t.Fatal(err)
	}
	if !valid.Equal(time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC)) || static {
		t.Errorf("m1 valid_at=%v static=%v", valid, static)
	}
	if err := cx.DB().QueryRow(`SELECT static FROM memories WHERE id='m2'`).Scan(&static); err != nil || !static {
		t.Errorf("m2 static=%v err=%v", static, err)
	}
	var name string
	if err := cx.DB().QueryRow(`SELECT name FROM entities WHERE id='e1'`).Scan(&name); err != nil || name != "Alice" {
		t.Errorf("entity id not preserved: %q %v", name, err)
	}
	if rep.Embedded != 5 {
		t.Errorf("embedded %d, want 5", rep.Embedded)
	}
}

func TestImportRefusesNonEmpty(t *testing.T) {
	path := makeSQLite(t, "2026-03-01 08:00:00")
	cx := openTarget(t, &fakeEmb{})
	if _, err := Import(context.Background(), path, cx, cortex.BootstrapUserID, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := Import(context.Background(), path, cx, cortex.BootstrapUserID, Options{}); err == nil {
		t.Fatal("second import without Force should error")
	}
}

func TestImportBadTimestamp(t *testing.T) {
	path := makeSQLite(t, "garbage")
	cx := openTarget(t, &fakeEmb{})
	_, err := Import(context.Background(), path, cx, cortex.BootstrapUserID, Options{})
	if err == nil || !strings.Contains(err.Error(), "table entities row e1") || !strings.Contains(err.Error(), "garbage") {
		t.Fatalf("err = %v", err)
	}
	var n int
	if err := cx.DB().QueryRow(`SELECT count(*) FROM entities`).Scan(&n); err != nil || n != 0 {
		t.Errorf("entities = %d (%v), want 0 after rollback", n, err)
	}
}

func TestImportEmbedResumes(t *testing.T) {
	path := makeSQLite(t, "2026-03-01 08:00:00")
	bad := &fakeEmb{failAt: 2}
	cx := openTarget(t, bad)
	if _, err := Import(context.Background(), path, cx, cortex.BootstrapUserID, Options{BatchSize: 1}); err == nil {
		t.Fatal("expected embed error")
	}
	cx.SetEmbedder(&fakeEmb{})
	if _, err := Import(context.Background(), path, cx, cortex.BootstrapUserID, Options{EmbedOnly: true, BatchSize: 1}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`SELECT count(*) FROM memories m WHERE (SELECT count(*) FROM embeddings e WHERE e.ref_id=m.id AND e.ref_type='memory') <> 1`,
		`SELECT count(*) FROM chunks m WHERE (SELECT count(*) FROM embeddings e WHERE e.ref_id=m.id AND e.ref_type='chunk') <> 1`,
		`SELECT count(*) FROM entities m WHERE (SELECT count(*) FROM embeddings e WHERE e.ref_id=m.id AND e.ref_type='entity') <> 1`,
	} {
		var bad int
		if err := cx.DB().QueryRow(q).Scan(&bad); err != nil || bad != 0 {
			t.Errorf("%s: bad=%d err=%v", q, bad, err)
		}
	}
}

// TestImportOlderSchema covers brains written by cortex versions before
// v0.4.0: no static/forget_after columns and no memory_edges table.
func TestImportOlderSchema(t *testing.T) {
	path := makeSQLite(t, "2026-03-01 08:00:00")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		`ALTER TABLE memories DROP COLUMN static`,
		`ALTER TABLE memories DROP COLUMN forget_after`,
		`DROP TABLE memory_edges`,
	} {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	db.Close()

	cx := openTarget(t, &fakeEmb{})
	rep, err := Import(context.Background(), path, cx, cortex.BootstrapUserID, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if p := rep.Counts["memories"]; p[0] != 2 || p[1] != 2 {
		t.Errorf("memories: source %d target %d", p[0], p[1])
	}
	if p := rep.Counts["memory_edges"]; p[0] != 0 || p[1] != 0 {
		t.Errorf("memory_edges: source %d target %d", p[0], p[1])
	}
	var static bool
	if err := cx.DB().QueryRow(`SELECT static FROM memories WHERE id='m2'`).Scan(&static); err != nil || static {
		t.Errorf("m2 static=%v err=%v, want default false", static, err)
	}
}

// TestImportBatchBoundaries copies with a batch size smaller than a table so
// both a full batch and a partial remainder are flushed.
func TestImportBatchBoundaries(t *testing.T) {
	old := copyBatchRows
	copyBatchRows = 3
	t.Cleanup(func() { copyBatchRows = old })

	path := makeSQLite(t, "2026-03-01 08:00:00")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO memory_entities (memory_id,entity_id) VALUES ('m2','e1'),('m2','e2')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	cx := openTarget(t, &fakeEmb{})
	rep, err := Import(context.Background(), path, cx, cortex.BootstrapUserID, Options{})
	if err != nil {
		t.Fatal(err)
	}
	for tbl, p := range rep.Counts {
		if p[0] != p[1] {
			t.Errorf("%s: source %d target %d", tbl, p[0], p[1])
		}
	}
	if p := rep.Counts["memory_entities"]; p[1] != 4 {
		t.Errorf("memory_entities target %d, want 4", p[1])
	}
}

// lenEmb records the longest text it was asked to embed.
type lenEmb struct {
	fakeEmb
	longest atomic.Int64
}

func (l *lenEmb) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	for _, s := range texts {
		if n := int64(len(s)); n > l.longest.Load() {
			l.longest.Store(n)
		}
	}
	return l.fakeEmb.Embed(ctx, texts)
}

// TestImportTruncatesLongEmbedInput: old brains hold chunks far past the
// embedding model's token limit; the importer embeds a prefix instead of
// failing, and the stored chunk keeps its full text.
func TestImportTruncatesLongEmbedInput(t *testing.T) {
	path := makeSQLite(t, "2026-03-01 08:00:00")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("word ", 40000) // 200K chars
	if _, err := db.Exec(`UPDATE chunks SET content = ? WHERE id = 'c1'`, long); err != nil {
		t.Fatal(err)
	}
	db.Close()

	emb := &lenEmb{}
	cx := openTarget(t, emb)
	if _, err := Import(context.Background(), path, cx, cortex.BootstrapUserID, Options{}); err != nil {
		t.Fatal(err)
	}
	if got := emb.longest.Load(); got > maxEmbedChars {
		t.Errorf("embedder got %d chars, want <= %d", got, maxEmbedChars)
	}
	var n int
	if err := cx.DB().QueryRow(`SELECT length(content) FROM chunks WHERE id='c1'`).Scan(&n); err != nil || n != len(long) {
		t.Errorf("stored chunk length %d (err %v), want %d", n, err, len(long))
	}
}

func TestImportSqliteFreshDB(t *testing.T) {
	path := makeSQLite(t, "2026-03-01 08:00:00")
	cx := openTarget(t, &fakeEmb{})
	// A fresh Postgres has no cortex_meta embedding_dims row; the source
	// carries none either, so dims come from the vectors themselves.
	var n int
	if err := cx.DB().QueryRow(`SELECT count(*) FROM cortex_meta WHERE key = 'embedding_dims'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("fresh DB has embedding_dims row (n=%d err=%v)", n, err)
	}
	rep, err := Import(context.Background(), path, cx, cortex.BootstrapUserID, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Embedded == 0 {
		t.Fatal("nothing embedded")
	}
	var bad int
	if err := cx.DB().QueryRow(`SELECT count(*) FROM embeddings WHERE dims <> 512 OR generation <> 1 OR user_id <> 'u_bootstrap'`).Scan(&bad); err != nil || bad != 0 {
		t.Fatalf("bad embedding rows = %d err=%v", bad, err)
	}
}
