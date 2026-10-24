package cortex

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/pgtest"
	"github.com/sausheong/cortex/internal/store"
)

// openEncryptedOnly is openTestDBWithEmbedder for a user in state 'encrypted'.
func openEncryptedOnly(t *testing.T) *Cortex {
	t.Helper()
	testMode = "encrypted"
	t.Cleanup(func() { testMode = "" })
	return openTestDBWithEmbedder(t)
}

// recordSQL captures every statement (text and args) issued until the
// returned stop function is called.
func recordSQL(t *testing.T) (stop func() []recordedSQL) {
	t.Helper()
	var mu sync.Mutex
	var got []recordedSQL
	store.OnQuery = func(q string, args []any) {
		mu.Lock()
		got = append(got, recordedSQL{q, append([]any(nil), args...)})
		mu.Unlock()
	}
	t.Cleanup(func() { store.OnQuery = nil })
	return func() []recordedSQL {
		store.OnQuery = nil
		mu.Lock()
		defer mu.Unlock()
		return got
	}
}

type recordedSQL struct {
	q    string
	args []any
}

func seedSearchData(t *testing.T, c *Cortex) (alice, bob *Entity) {
	t.Helper()
	ctx := context.Background()
	alice = &Entity{Type: "person", Name: "Alice Johnson", Source: "seed"}
	bob = &Entity{Type: "organization", Name: "Stripe", Source: "seed"}
	for _, e := range []*Entity{alice, bob} {
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.AddAlias(ctx, alice.ID, AliasEmail, "alice@x.test"); err != nil {
		t.Fatal(err)
	}
	if err := c.PutRelationship(ctx, &Relationship{SourceID: alice.ID, TargetID: bob.ID, Type: "works_at"}); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"Alice Johnson works at Stripe as an engineer", "Stripe ships payments in April", "Unrelated note about gardening"} {
		m := &Memory{Content: content, EntityIDs: []string{alice.ID}, Source: "seed"}
		if err := c.PutMemory(ctx, m); err != nil {
			t.Fatal(err)
		}
		sdEmbed(t, c, m.ID, "memory", content)
	}
	ch := &Chunk{Content: "Alice Johnson works at Stripe as an engineer", EntityID: alice.ID, Metadata: map[string]any{"source": "seed"}}
	if err := c.PutChunk(ctx, ch); err != nil {
		t.Fatal(err)
	}
	sdEmbed(t, c, ch.ID, "chunk", ch.Content)
	return alice, bob
}

var sqlTextSearch = regexp.MustCompile(`(?i)\btsv\b|ILIKE|ts_rank|to_tsquery|plainto_tsquery|LIKE ANY`)

func TestEncryptedSearchNeverTouchesSQLText(t *testing.T) {
	c := openEncryptedOnly(t)
	ctx := context.Background()
	alice, _ := seedSearchData(t, c)
	c.SetLLM(&mockLLM{decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
		return []StructuredQuery{
			{Type: "memory_lookup", Params: map[string]any{"query": q}},
			{Type: "keyword_search", Params: map[string]any{"query": q}},
			{Type: "vector_search", Params: map[string]any{"query": q}},
			{Type: "graph_traverse", Params: map[string]any{"query": q}},
		}, nil
	}})
	stop := recordSQL(t)

	if got, err := c.SearchMemories(ctx, "Stripe engineer", 5); err != nil || len(got) == 0 {
		t.Fatalf("SearchMemories = %d, %v", len(got), err)
	}
	if got, err := c.SearchKeyword(ctx, "Stripe engineer", 5); err != nil || len(got) == 0 {
		t.Fatalf("SearchKeyword = %d, %v", len(got), err)
	}
	if got, err := c.SearchEntities(ctx, "alic", nil, "", 10); err != nil || len(got) != 1 {
		t.Fatalf("SearchEntities = %d, %v", len(got), err)
	}
	if got, err := c.FindEntities(ctx, EntityFilter{NameLike: "ali%"}); err != nil || len(got) != 1 {
		t.Fatalf("FindEntities = %d, %v", len(got), err)
	}
	if _, total, err := c.ListMemories(ctx, MemoryListFilter{Query: "gardening"}, 10, 0); err != nil || total != 1 {
		t.Fatalf("ListMemories total = %d, %v", total, err)
	}
	if n, err := c.CountMemories(ctx, DeleteFilter{Query: "stripe"}); err != nil || n != 2 {
		t.Fatalf("CountMemories = %d, %v", n, err)
	}
	if got, err := c.SampleMemories(ctx, DeleteFilter{Query: "stripe"}, 5); err != nil || len(got) != 2 || got[0].Content == "" {
		t.Fatalf("SampleMemories = %+v, %v", got, err)
	}
	if res, err := c.Recall(ctx, "Alice Johnson Stripe", WithLimit(10)); err != nil || len(res) == 0 {
		t.Fatalf("Recall = %d, %v", len(res), err)
	}
	if res, err := c.ResolveEntity(ctx, "Alice Jonson", "person"); err != nil {
		t.Fatalf("ResolveEntity: %v", err)
	} else if res.Entity == nil && len(res.Candidates) == 0 {
		t.Fatal("fuzzy resolve found nothing")
	}
	if _, err := c.Lint(ctx); err != nil {
		t.Fatal(err)
	}
	_ = alice

	stmts := stop()
	if len(stmts) == 0 {
		t.Fatal("recorder saw no statements")
	}
	for _, s := range stmts {
		if sqlTextSearch.MatchString(s.q) {
			t.Fatalf("encrypted search issued text-search SQL: %s", strings.Join(strings.Fields(s.q), " "))
		}
	}
}

func TestSearchEntitiesILikeEscapesWildcards(t *testing.T) {
	forEachMode(t, testSearchEntitiesILikeEscapesWildcards)
}

func testSearchEntitiesILikeEscapesWildcards(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	for _, n := range []string{"100% Done", "100x Done", "a_b", "axb", `back\slash`, "Zebra Crossing"} {
		if err := c.PutEntity(ctx, &Entity{Type: "concept", Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	names := func(q string) []string {
		got, err := c.SearchEntities(ctx, q, nil, "", 20)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range got {
			out = append(out, e.Name)
		}
		return out
	}
	for q, want := range map[string]string{"100%": "100% Done", "a_b": "a_b", `k\s`: `back\slash`, "ZEBRA": "Zebra Crossing"} {
		if got := names(q); len(got) != 1 || got[0] != want {
			t.Errorf("SearchEntities(%q) = %v, want [%s]", q, got, want)
		}
	}
	if got := names("%"); len(got) != 1 || got[0] != "100% Done" {
		t.Errorf("a bare %% must be literal, got %v", got)
	}
	if got := names("_"); len(got) != 1 || got[0] != "a_b" {
		t.Errorf("a bare _ must be literal, got %v", got)
	}
	// Names starting with the query come first.
	for _, n := range []string{"Crossing Guard", "Guard Crossing"} {
		if err := c.PutEntity(ctx, &Entity{Type: "concept", Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	if got := names("crossing"); len(got) != 3 || got[0] != "Crossing Guard" {
		t.Errorf("prefix match should lead: %v", got)
	}
}

func TestFuzzyIdentityResolutionInMemory(t *testing.T) {
	forEachMode(t, testFuzzyIdentityResolutionInMemory)
}

func testFuzzyIdentityResolutionInMemory(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	for _, e := range []*Entity{
		{Type: "person", Name: "Alexander Hamilton"},
		{Type: "person", Name: "Alexandra Hamilton-Reyes"},
		{Type: "organization", Name: "Hamilton Holdings"},
		{Type: "person", Name: "Bob Unrelated"},
	} {
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	res, err := c.ResolveEntity(ctx, "Alexander Hamiltn", "person")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	if res.Entity != nil {
		names = append(names, res.Entity.Name)
	}
	for _, cand := range res.Candidates {
		names = append(names, cand.Entity.Name)
	}
	joined := strings.Join(names, "|")
	if !strings.Contains(joined, "Alexander Hamilton") {
		t.Fatalf("fuzzy resolve missed the near match: entity=%v candidates=%v", res.Entity, res.Candidates)
	}
	if strings.Contains(joined, "Bob Unrelated") || strings.Contains(joined, "Hamilton Holdings") {
		t.Fatalf("fuzzy resolve returned a wrong-type or unrelated entity: %v", names)
	}
}

func TestEncryptedUserLockedReturnsErrNotEmpty(t *testing.T) {
	c := openEncryptedOnly(t)
	ctx := context.Background()
	seedSearchData(t, c)
	// Same user, same state, no key: a locked session.
	locked := c.WithKey(nil, "encrypted")
	check := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, model.ErrLocked) {
			t.Errorf("%s on a locked handle: err = %v, want ErrLocked (never an empty result)", name, err)
		}
	}
	_, err := locked.SearchMemories(ctx, "Stripe", 5)
	check("SearchMemories", err)
	_, err = locked.SearchKeyword(ctx, "Stripe", 5)
	check("SearchKeyword", err)
	_, err = locked.SearchEntities(ctx, "Alice", nil, "", 5)
	check("SearchEntities", err)
	_, err = locked.FindEntities(ctx, EntityFilter{NameLike: "%"})
	check("FindEntities", err)
	_, _, err = locked.ListMemories(ctx, MemoryListFilter{Query: "Stripe"}, 5, 0)
	check("ListMemories", err)
	_, err = locked.CountMemories(ctx, DeleteFilter{Query: "Stripe"})
	check("CountMemories", err)
	_, err = locked.Recall(ctx, "Alice Stripe")
	check("Recall", err)
	_, err = locked.Lint(ctx)
	check("Lint", err)
	_, err = locked.EntityTypes(ctx)
	check("EntityTypes", err)
	_, err = locked.ResolveEntity(ctx, "Alise Johnsen", "person")
	check("ResolveEntity", err)
	// A cached set is never served to a different key: still locked.
	if _, err := c.SearchMemories(ctx, "Stripe", 5); err != nil {
		t.Fatal(err)
	}
	other, kerr := keys.NewUK()
	if kerr != nil {
		t.Fatal(kerr)
	}
	defer other.Zero()
	wrong := c.WithKey(other, "encrypted")
	defer wrong.Release()
	_, err = wrong.SearchMemories(ctx, "Stripe", 5)
	check("SearchMemories with the wrong key", err)
}

func TestLegacyQueriesUsePartialIndexes(t *testing.T) {
	ctx := context.Background()
	c, err := Open(pgtest.DSN(t), WithEmbedder(&dimsEmbedder{8}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	for i := 0; i < 40; i++ {
		m := &Memory{Content: fmt.Sprintf("memory number %d about payments and invoices", i), Source: "seed"}
		if err := c.PutMemory(ctx, m); err != nil {
			t.Fatal(err)
		}
		v := make([]float32, 8)
		v[i%8] = 1
		if err := c.putEmbedding(ctx, m.ID, "memory", v); err != nil {
			t.Fatal(err)
		}
		ch := &Chunk{Content: fmt.Sprintf("chunk %d discussing invoices and payments", i)}
		if err := c.PutChunk(ctx, ch); err != nil {
			t.Fatal(err)
		}
		if err := c.putEmbedding(ctx, ch.ID, "chunk", v); err != nil {
			t.Fatal(err)
		}
	}
	for _, rt := range []string{"memory", "chunk"} {
		if err := c.EnsureVectorIndex(ctx, 8, rt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.h.DB.ExecContext(ctx, `ANALYZE`); err != nil {
		t.Fatal(err)
	}
	stop := recordSQL(t)
	if _, err := c.SearchMemories(ctx, "payments invoices", 5); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SearchKeyword(ctx, "payments invoices", 5); err != nil {
		t.Fatal(err)
	}
	if _, err := c.searchVectorRaw(ctx, make([]float32, 8), "memory", 5); err != nil {
		t.Fatal(err)
	}
	stmts := stop()

	// The indexes really are partial.
	for _, idx := range []string{"idx_memories_tsv", "idx_chunks_tsv", "emb_hnsw_memory_8", "emb_hnsw_chunk_8"} {
		var def string
		if err := c.h.DB.QueryRowContext(ctx,
			`SELECT indexdef FROM pg_indexes WHERE schemaname = current_schema() AND indexname = ?`, idx).Scan(&def); err != nil {
			t.Fatalf("%s: %v", idx, err)
		}
		if !strings.Contains(def, "enc IS NOT TRUE") {
			t.Errorf("%s is not partial on enc IS NOT TRUE: %s", idx, def)
		}
	}

	// Inside a transaction that is rolled back: drop every other index on the
	// searched tables and forbid sequential scans, so a plan can only use a
	// partial index if its predicate is implied by the query.
	conn, err := c.h.DB.DB.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `BEGIN`); err != nil {
		t.Fatal(err)
	}
	defer conn.ExecContext(ctx, `ROLLBACK`)
	irows, err := conn.QueryContext(ctx,
		`SELECT indexname FROM pg_indexes WHERE schemaname = current_schema() AND tablename IN ('memories','chunks','embeddings')
		   AND indexname NOT LIKE '%_pkey' AND indexname NOT IN ('idx_memories_tsv','idx_chunks_tsv') AND indexname NOT LIKE 'emb_hnsw_%'`)
	if err != nil {
		t.Fatal(err)
	}
	var drop []string
	for irows.Next() {
		var n string
		if err := irows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		drop = append(drop, n)
	}
	irows.Close()
	for _, n := range drop {
		if _, err := conn.ExecContext(ctx, `DROP INDEX `+n); err != nil {
			t.Fatalf("drop %s: %v", n, err)
		}
	}
	if _, err := conn.ExecContext(ctx, `SET LOCAL enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	plan := func(s recordedSQL) string {
		rows, err := conn.QueryContext(ctx, `EXPLAIN `+store.Rebind(s.q), s.args...)
		if err != nil {
			t.Fatalf("explain %q: %v", s.q, err)
		}
		defer rows.Close()
		var b strings.Builder
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			b.WriteString(line + "\n")
		}
		return b.String()
	}
	want := map[string]string{
		"FROM memories m JOIN d":     "idx_memories_tsv",
		"c.tsv @@":                   "idx_chunks_tsv",
		"FROM embeddings WHERE user": "emb_hnsw_memory_8",
	}
	found := map[string]bool{}
	for _, s := range stmts {
		for frag, idx := range want {
			if !strings.Contains(s.q, frag) {
				continue
			}
			found[frag] = true
			if p := plan(s); !strings.Contains(p, idx) {
				t.Errorf("plan for %q does not use partial index %s:\n%s", frag, idx, p)
			}
		}
	}
	for frag := range want {
		if !found[frag] {
			t.Errorf("no recorded statement matched %q", frag)
		}
	}
}

func BenchmarkRecallWarm(b *testing.B) {
	ctx := context.Background()
	testMode = "encrypted"
	defer func() { testMode = "" }()
	dsn := pgtest.DSN(b)
	root, err := Open(dsn, WithEmbedder(&testEmbedder{}))
	if err != nil {
		b.Fatal(err)
	}
	defer root.Close()
	c := modeHandleTB(b, root)
	c.SetLLM(&mockLLM{decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
		return []StructuredQuery{
			{Type: "memory_lookup", Params: map[string]any{"query": q}},
			{Type: "keyword_search", Params: map[string]any{"query": q}},
		}, nil
	}})
	e := &Entity{Type: "person", Name: "Alice"}
	if err := c.PutEntity(ctx, e); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 500; i++ {
		m := &Memory{Content: fmt.Sprintf("note %d: Alice reviewed invoice %d for the Stripe payments project", i, i*7), EntityIDs: []string{e.ID}, Source: "bench"}
		if err := c.PutMemory(ctx, m); err != nil {
			b.Fatal(err)
		}
		if i%5 == 0 {
			if err := c.PutChunk(ctx, &Chunk{Content: m.Content}); err != nil {
				b.Fatal(err)
			}
		}
	}
	if _, err := c.Recall(ctx, "Alice Stripe invoice", WithLimit(10)); err != nil { // warm the working set
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Recall(ctx, "Alice Stripe invoice", WithLimit(10)); err != nil {
			b.Fatal(err)
		}
	}
}
