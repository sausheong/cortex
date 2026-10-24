package memory

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/sausheong/cortex/internal/enctest"
	"github.com/sausheong/cortex/internal/model"
)

var bg = context.Background()

func TestMemoryChunkRoundTripEncrypted(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	m := New(env.H)
	ent := seedEntity(t, env, "Ent One")
	mem := &model.Memory{Content: "The quick brown fox", Source: "diary", Speaker: "ada", EntityIDs: []string{ent}, Confidence: 0.7}
	if err := m.PutMemory(bg, mem); err != nil {
		t.Fatal(err)
	}
	got, err := m.GetMemoryByID(bg, mem.ID)
	if err != nil || got.Content != "The quick brown fox" || got.Source != "diary" || got.Speaker != "ada" || got.Confidence != 0.7 {
		t.Fatalf("got %+v, %v", got, err)
	}
	// No speaker stays empty.
	mem2 := &model.Memory{Content: "no speaker", Source: "s", EntityIDs: []string{ent}}
	if err := m.PutMemory(bg, mem2); err != nil {
		t.Fatal(err)
	}
	g2, err := m.GetMemoryByID(bg, mem2.ID)
	if err != nil || g2.Speaker != "" {
		t.Fatalf("%+v %v", g2, err)
	}
	byEnt, err := m.GetMemoriesByEntity(bg, ent)
	if err != nil || len(byEnt) != 2 {
		t.Fatalf("by entity = %d, %v", len(byEnt), err)
	}
	all, err := m.MemoriesByEntityAll(bg)
	if err != nil || len(all[ent]) != 2 {
		t.Fatalf("bulk = %v, %v", all, err)
	}
	found := false
	for _, x := range all[ent] {
		if x.Content == "The quick brown fox" && x.Speaker == "ada" {
			found = true
		}
	}
	if !found {
		t.Fatal("bulk did not decrypt")
	}

	ch := &model.Chunk{Content: "chunk body text", EntityID: ent, Metadata: map[string]any{"source": "Doc A", "page": float64(2)}}
	if err := m.PutChunk(bg, ch); err != nil {
		t.Fatal(err)
	}
	if n := env.Count(`SELECT count(*) FROM chunks WHERE id=$1 AND enc AND content IS NULL AND metadata IS NULL AND content_ct IS NOT NULL AND metadata_ct IS NOT NULL AND source_blind IS NOT NULL`, ch.ID); n != 1 {
		t.Fatal("chunk not stored in encrypted form")
	}
	gc, err := m.GetChunk(bg, ch.ID)
	if err != nil || gc.Content != "chunk body text" || gc.Metadata["source"] != "Doc A" || gc.Metadata["page"] != float64(2) || gc.EntityID != ent {
		t.Fatalf("chunk = %+v, %v", gc, err)
	}

	// Memory edge: type plain, source sealed.
	mem3 := &model.Memory{Content: "third", Source: "s"}
	m.PutMemory(bg, mem3)
	edge := &model.MemoryEdge{SourceID: mem3.ID, TargetID: mem.ID, Type: model.EdgeExtends, Source: "edge-origin"}
	if err := m.PutMemoryEdge(bg, edge); err != nil {
		t.Fatal(err)
	}
	if n := env.Count(`SELECT count(*) FROM memory_edges WHERE id=$1 AND enc AND source IS NULL AND source_ct IS NOT NULL AND type = 'extends'`, edge.ID); n != 1 {
		t.Fatal("edge source not sealed")
	}
	es, err := m.GetMemoryEdges(bg, mem.ID)
	if err != nil || len(es) != 1 || es[0].Source != "edge-origin" {
		t.Fatalf("edges = %+v, %v", es, err)
	}
	// Edge with an empty source stays NULL and decodes to "".
	e2 := &model.MemoryEdge{SourceID: mem2.ID, TargetID: mem.ID, Type: model.EdgeDerives}
	if err := m.PutMemoryEdge(bg, e2); err != nil {
		t.Fatal(err)
	}
	es, _ = m.GetMemoryEdgesByType(bg, mem2.ID, model.EdgeDerives)
	if len(es) != 1 || es[0].Source != "" {
		t.Fatalf("edges = %+v", es)
	}
}

func seedEntity(t *testing.T, env *enctest.Env, name string) string {
	t.Helper()
	// An entity row in encrypted form, written through the graph-free path:
	// plain ids are all that memory tables reference.
	id := "ent-" + strings.ReplaceAll(strings.ToLower(name), " ", "-")
	env.Exec(`INSERT INTO entities (id, user_id, enc, name_ct, type_ct, name_blind, type_blind) VALUES ($1,$2,true,'\x00','\x00','\x01','\x02')`, id, env.UserID)
	return id
}

func TestFirstChunkBySourceUsesBlind(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	m := New(env.H)
	for _, c := range []struct{ body, src string }{{"first body", "Source One"}, {"second body", "Source One"}, {"other body", "Other"}} {
		if err := m.PutChunk(bg, &model.Chunk{Content: c.body, Metadata: map[string]any{"source": c.src}}); err != nil {
			t.Fatal(err)
		}
	}
	if got := m.firstChunkBySource(bg, " source one "); got != "first body" {
		t.Fatalf("got %q", got)
	}
	if got := m.firstChunkBySource(bg, "missing"); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestMigratingFirstChunkBySourceFindsLegacyChunk(t *testing.T) {
	env := enctest.Open(t, "migrating")
	m := New(env.H)
	env.Exec(`INSERT INTO chunks (id, user_id, content, metadata) VALUES ('legacy-chunk',$1,'legacy body','{"source":"Old Doc"}'::jsonb)`, env.UserID)
	if got := m.firstChunkBySource(bg, "Old Doc"); got != "legacy body" {
		t.Fatalf("got %q", got)
	}
	// The hit was rewritten as encrypted.
	if n := env.Count(`SELECT count(*) FROM chunks WHERE id='legacy-chunk' AND enc AND content IS NULL AND metadata IS NULL AND content_ct IS NOT NULL AND source_blind IS NOT NULL`); n != 1 {
		t.Fatal("legacy chunk not rewritten as encrypted")
	}
	if got := m.firstChunkBySource(bg, "old doc"); got != "legacy body" {
		t.Fatalf("second lookup (blind) got %q", got)
	}
	// Encrypted state does not look at legacy chunks.
	env2 := enctest.Open(t, "encrypted")
	env2.Exec(`INSERT INTO chunks (id, user_id, content, metadata) VALUES ('p',$1,'plain body','{"source":"Doc"}'::jsonb)`, env2.UserID)
	if got := New(env2.H).firstChunkBySource(bg, "Doc"); got != "" {
		t.Fatalf("encrypted state read a plaintext chunk: %q", got)
	}
}

func TestEmbeddingRoundTripEncrypted(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	m := New(env.H)
	v := []float32{0.25, -0.5, 0.125, 1}
	if err := m.PutEmbedding(bg, "ref-1", "memory", v); err != nil {
		t.Fatal(err)
	}
	got, err := m.GetEmbedding(bg, "ref-1", "memory")
	if err != nil || len(got) != 4 {
		t.Fatalf("%v %v", got, err)
	}
	for i := range v {
		if got[i] != v[i] {
			t.Fatalf("elem %d: %v != %v", i, got[i], v[i])
		}
	}
	if n := env.Count(`SELECT count(*) FROM embeddings WHERE user_id=$1 AND enc AND embedding IS NULL AND embedding_ct IS NOT NULL AND dims = 4`, env.UserID); n != 1 {
		t.Fatal("embedding not sealed")
	}
	// Overwrite keeps one row and still decrypts (AAD uses the row id).
	v2 := []float32{1, 1, 1, 1}
	if err := m.PutEmbedding(bg, "ref-1", "memory", v2); err != nil {
		t.Fatal(err)
	}
	got, err = m.GetEmbedding(bg, "ref-1", "memory")
	if err != nil || got[0] != 1 {
		t.Fatalf("after overwrite %v %v", got, err)
	}
	if n := env.Count(`SELECT count(*) FROM embeddings WHERE user_id=$1`, env.UserID); n != 1 {
		t.Fatalf("rows = %d", n)
	}
	// Flip path writes encrypted too.
	if err := m.FlipGeneration(bg, 2, func(ctx context.Context, tx *sql.Tx, put func(refID, refType string, v []float32) error) error {
		return put("ref-2", "memory", v)
	}, nil); err != nil {
		t.Fatal(err)
	}
	if n := env.Count(`SELECT count(*) FROM embeddings WHERE user_id=$1 AND enc AND generation=2 AND embedding IS NULL`, env.UserID); n != 1 {
		t.Fatal("flip put not sealed")
	}
	// Keyless cannot read.
	if _, err := New(env.Keyless).GetEmbedding(bg, "ref-2", "memory"); !errors.Is(err, model.ErrLocked) {
		t.Fatalf("want ErrLocked, got %v", err)
	}
	// Legacy form is read in none/migrating and rejected when encrypted.
	env.Exec(`INSERT INTO embeddings (id, user_id, ref_id, ref_type, embedding, dims, generation) VALUES ('plain-emb',$1,'ref-3','memory','[1,2,3,4]'::halfvec,4,2)`, env.UserID)
	if _, err := m.GetEmbedding(bg, "ref-3", "memory"); !errors.Is(err, model.ErrPlaintextRow) {
		t.Fatalf("want ErrPlaintextRow, got %v", err)
	}
}

func TestRawRowsHoldNoPlaintext(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	m := New(env.H)
	ent := seedEntity(t, env, "Holder")
	mem := &model.Memory{Content: "xylophone-secret-content", Source: "pelican-source", Speaker: "walrus-speaker", EntityIDs: []string{ent}}
	if err := m.PutMemory(bg, mem); err != nil {
		t.Fatal(err)
	}
	m2 := &model.Memory{Content: "other", Source: "s"}
	m.PutMemory(bg, m2)
	m.PutMemoryEdge(bg, &model.MemoryEdge{SourceID: m2.ID, TargetID: mem.ID, Type: model.EdgeExtends, Source: "toucan-edge-source"})
	m.PutChunk(bg, &model.Chunk{Content: "kumquat-chunk-body", Metadata: map[string]any{"source": "ocelot-doc-source", "x": "narwhal-meta"}})
	m.PutEmbedding(bg, mem.ID, "memory", []float32{0.1, 0.2, 0.3})
	for _, tbl := range []string{"memories", "memory_edges", "chunks", "embeddings"} {
		rows, err := env.DB.Query(`SELECT row_to_json(t)::text FROM `+tbl+` t WHERE user_id = $1`, env.UserID)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for rows.Next() {
			var s string
			rows.Scan(&s)
			n++
			low := strings.ToLower(s)
			for _, bad := range []string{"xylophone", "pelican", "walrus", "toucan", "kumquat", "ocelot", "narwhal", "0.1,0.2"} {
				if strings.Contains(low, bad) {
					t.Fatalf("%s row leaks %q: %s", tbl, bad, s)
				}
			}
		}
		rows.Close()
		if n == 0 {
			t.Fatalf("no rows in %s", tbl)
		}
	}
}

func TestMemoryCiphertextMovedToOtherRowFailsAAD(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	m := New(env.H)
	a := &model.Memory{Content: "memory A", Source: "s"}
	b := &model.Memory{Content: "memory B", Source: "s"}
	m.PutMemory(bg, a)
	m.PutMemory(bg, b)
	env.Exec(`UPDATE memories SET content_ct = (SELECT content_ct FROM memories WHERE id=$1) WHERE id=$2`, a.ID, b.ID)
	if got, err := m.GetMemoryByID(bg, b.ID); err == nil {
		t.Fatalf("moved ciphertext decrypted: %+v", got)
	}
	// Chunk and embedding too.
	c1 := &model.Chunk{Content: "c1", Metadata: map[string]any{}}
	c2 := &model.Chunk{Content: "c2", Metadata: map[string]any{}}
	m.PutChunk(bg, c1)
	m.PutChunk(bg, c2)
	env.Exec(`UPDATE chunks SET content_ct = (SELECT content_ct FROM chunks WHERE id=$1) WHERE id=$2`, c1.ID, c2.ID)
	if _, err := m.GetChunk(bg, c2.ID); err == nil {
		t.Fatal("chunk ciphertext moved between rows must fail")
	}
	m.PutEmbedding(bg, "r1", "memory", []float32{1, 2})
	m.PutEmbedding(bg, "r2", "memory", []float32{3, 4})
	env.Exec(`UPDATE embeddings SET embedding_ct = (SELECT embedding_ct FROM embeddings WHERE ref_id='r1') WHERE ref_id='r2'`)
	if _, err := m.GetEmbedding(bg, "r2", "memory"); err == nil {
		t.Fatal("embedding ciphertext moved between rows must fail")
	}
}

func TestMemoryCiphertextMovedToOtherUserFails(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	m := New(env.H)
	a := &model.Memory{Content: "victim secret", Source: "s"}
	m.PutMemory(bg, a)
	other := env.NewUser("other", "encrypted")
	env.Exec(`INSERT INTO memories (id, user_id, enc, content_ct, source_ct) SELECT 'stolen', $2, true, content_ct, source_ct FROM memories WHERE id=$1`, a.ID, other)
	oh := New(env.HandleFor(other, "encrypted", env.UK))
	if got, err := oh.GetMemoryByID(bg, "stolen"); err == nil {
		t.Fatalf("ciphertext copied to another user decrypted: %+v", got)
	}
}

func TestEncryptedUserRejectsPlaintextRow(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	m := New(env.H)
	env.Exec(`INSERT INTO memories (id, user_id, content, source) VALUES ('plain-mem',$1,'plain text','s')`, env.UserID)
	if _, err := m.GetMemoryByID(bg, "plain-mem"); !errors.Is(err, model.ErrPlaintextRow) {
		t.Fatalf("want ErrPlaintextRow, got %v", err)
	}
	// Migrating accepts it.
	envM := enctest.Open(t, "migrating")
	envM.Exec(`INSERT INTO memories (id, user_id, content, source) VALUES ('plain-mem',$1,'plain text','s')`, envM.UserID)
	got, err := New(envM.H).GetMemoryByID(bg, "plain-mem")
	if err != nil || got.Content != "plain text" {
		t.Fatalf("migrating read: %+v, %v", got, err)
	}
}

func TestEncTrueWithNullCiphertextFailsDecrypt(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	m := New(env.H)
	a := &model.Memory{Content: "x", Source: "s"}
	m.PutMemory(bg, a)
	env.Exec(`UPDATE memories SET content_ct = NULL WHERE id = $1`, a.ID)
	if _, err := m.GetMemoryByID(bg, a.ID); err == nil {
		t.Fatal("NULL ciphertext must fail")
	}
}

func TestKeylessHandleCannotReadEncryptedRow(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	m := New(env.H)
	a := &model.Memory{Content: "x", Source: "s"}
	m.PutMemory(bg, a)
	k := New(env.Keyless)
	if _, err := k.GetMemoryByID(bg, a.ID); !errors.Is(err, model.ErrLocked) {
		t.Fatalf("want ErrLocked, got %v", err)
	}
	if err := k.PutMemory(bg, &model.Memory{Content: "y", Source: "s"}); !errors.Is(err, model.ErrLocked) {
		t.Fatalf("write: want ErrLocked, got %v", err)
	}
	if err := k.PutChunk(bg, &model.Chunk{Content: "y"}); !errors.Is(err, model.ErrLocked) {
		t.Fatalf("chunk write: want ErrLocked, got %v", err)
	}
	if err := k.PutEmbedding(bg, "r", "memory", []float32{1}); !errors.Is(err, model.ErrLocked) {
		t.Fatalf("embedding write: want ErrLocked, got %v", err)
	}
}
