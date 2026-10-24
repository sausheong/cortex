package integration_test

import (
	"context"
	"errors"
	"fmt"
	. "github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/testutil"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sausheong/cortex/internal/ingest"
	"github.com/sausheong/cortex/internal/pgtest"
)

// batchEmbedder records the size of every Embed call.
type batchEmbedder struct {
	mu      sync.Mutex
	sizes   []int
	failNth int // 1-based call number that fails (0 = never)
	down    bool
}

func (e *batchEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.mu.Lock()
	e.sizes = append(e.sizes, len(texts))
	n := len(e.sizes)
	e.mu.Unlock()
	if e.down || n == e.failNth {
		return nil, errors.New("provider down")
	}
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = make([]float32, 8)
	}
	return out, nil
}
func (e *batchEmbedder) Dimensions() int { return 8 }

func (e *batchEmbedder) calls() []int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]int(nil), e.sizes...)
}

func (e *batchEmbedder) reset() {
	e.mu.Lock()
	e.sizes = nil
	e.mu.Unlock()
}

// manyParagraphs builds n paragraphs that each become their own chunk at
// WithMaxChunkChars(100).
func manyParagraphs(n int) string {
	paras := make([]string, n)
	for i := range paras {
		paras[i] = fmt.Sprintf("P%03d %s", i, strings.Repeat("x", 60))
	}
	return strings.Join(paras, "\n\n")
}

func openBatchCortex(t *testing.T, emb *batchEmbedder) *Cortex {
	t.Helper()
	c, err := Open(pgtest.DSN(t), WithEmbedder(emb))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return modeHandle(t, c)
}

func TestRemember_EmbedsInBatches(t *testing.T) { forEachMode(t, testRemember_EmbedsInBatches) }

func testRemember_EmbedsInBatches(t *testing.T) {
	emb := &batchEmbedder{}
	c := openBatchCortex(t, emb)
	c.SetExtractor(&mockExtractor{})
	if err := c.Remember(context.Background(), manyParagraphs(150), WithMaxChunkChars(100)); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	got := emb.calls()
	if len(got) != 3 || got[0] != 64 || got[1] != 64 || got[2] != 22 {
		t.Fatalf("embed call sizes = %v, want [64 64 22]", got)
	}
}

func TestRemember_FailedBatchCountsItsItemsAsPending(t *testing.T) {
	forEachMode(t, testRemember_FailedBatchCountsItsItemsAsPending)
}

func testRemember_FailedBatchCountsItsItemsAsPending(t *testing.T) {
	emb := &batchEmbedder{failNth: 2}
	c := openBatchCortex(t, emb)
	c.SetExtractor(&mockExtractor{})
	err := c.Remember(context.Background(), manyParagraphs(150), WithMaxChunkChars(100))
	var p *EmbedPartialError
	if !errors.As(err, &p) || p.Count != 64 {
		t.Fatalf("err = %v, want EmbedPartialError with Count 64", err)
	}
	if len(emb.calls()) != 3 {
		t.Fatalf("later batches should still be attempted, calls = %v", emb.calls())
	}
}

func TestBackfillEmbeddings_Batches(t *testing.T) { forEachMode(t, testBackfillEmbeddings_Batches) }

func testBackfillEmbeddings_Batches(t *testing.T) {
	emb := &batchEmbedder{down: true}
	c := openBatchCortex(t, emb)
	c.SetExtractor(&mockExtractor{})
	ctx := context.Background()
	if err := c.Remember(ctx, manyParagraphs(150), WithMaxChunkChars(100)); !IsEmbedPartial(err) {
		t.Fatalf("Remember err = %v, want EmbedPartialError", err)
	}
	emb.down = false
	emb.reset()
	n, err := c.BackfillEmbeddings(ctx, 1000)
	if err != nil || n != 150 {
		t.Fatalf("backfill n=%d err=%v, want 150", n, err)
	}
	got := emb.calls()
	if len(got) != 3 || got[0] != 64 || got[1] != 64 || got[2] != 22 {
		t.Fatalf("backfill embed call sizes = %v, want [64 64 22]", got)
	}
}

// chunkedExtractor answers per paragraph marker (P1, P2, P3).
func chunkedExtractor(failMarker string, calls *atomic.Int32) *mockExtractor {
	return &mockExtractor{extractFn: func(_ context.Context, content, _ string) (*Extraction, error) {
		calls.Add(1)
		if failMarker != "" && strings.Contains(content, failMarker) {
			return nil, errors.New("llm overloaded")
		}
		switch {
		case strings.Contains(content, "P1"):
			return &Extraction{
				Entities: []Entity{
					{Type: "person", Name: "Alice", Attributes: map[string]any{"a": 1}},
					{Type: "organization", Name: "Stripe"},
				},
				Memories: []Memory{{Content: "Alice works at Stripe"}},
			}, nil
		case strings.Contains(content, "P2"):
			// Stripe is only defined in chunk 1: the relationship must
			// resolve through the merged name map.
			return &Extraction{
				Entities:      []Entity{{Type: "Person", Name: "alice", Attributes: map[string]any{"a": 9, "b": 2}}},
				Relationships: []Relationship{{SourceID: "alice", TargetID: "STRIPE", Type: "works_at"}},
				Memories:      []Memory{{Content: "  alice works   at STRIPE "}},
			}, nil
		default:
			return &Extraction{
				Entities: []Entity{{Type: "person", Name: "Bob"}},
				Relationships: []Relationship{
					{SourceID: "Alice", TargetID: "Stripe", Type: "works_at"},
					{SourceID: "Bob", TargetID: "stripe", Type: "uses"},
				},
				Memories: []Memory{{Content: "Bob knows Stripe"}},
			}, nil
		}
	}}
}

const threeChunkDoc = "P1 " + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" +
	"\n\nP2 " + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" +
	"\n\nP3 " + "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

func TestRemember_ChunkedExtractionMergesAndDedupes(t *testing.T) {
	forEachMode(t, testRemember_ChunkedExtractionMergesAndDedupes)
}

func testRemember_ChunkedExtractionMergesAndDedupes(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	var calls atomic.Int32
	c.SetExtractor(chunkedExtractor("", &calls))
	if err := c.Remember(ctx, threeChunkDoc, WithMaxChunkChars(100)); err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("extractor calls = %d, want 3 (one per chunk)", calls.Load())
	}
	ents, _ := c.FindEntities(ctx, EntityFilter{})
	if len(ents) != 3 {
		t.Fatalf("entities = %d (%v), want 3 after case-insensitive dedupe", len(ents), ents)
	}
	var alice *Entity
	for i := range ents {
		if ents[i].Name == "Alice" {
			alice = &ents[i]
		}
	}
	if alice == nil {
		t.Fatalf("Alice missing: %v", ents)
	}
	if alice.Attributes["b"] == nil || fmt.Sprint(alice.Attributes["a"]) != "1" {
		t.Fatalf("attributes not merged with first value kept: %v", alice.Attributes)
	}
	rels, _ := c.GetRelationships(ctx, alice.ID)
	if len(rels) != 1 || rels[0].Type != "works_at" {
		t.Fatalf("Alice relationships = %v, want exactly one works_at (cross-chunk resolved, deduped)", rels)
	}
	var bob *Entity
	for i := range ents {
		if ents[i].Name == "Bob" {
			bob = &ents[i]
		}
	}
	brels, _ := c.GetRelationships(ctx, bob.ID)
	if len(brels) != 1 || brels[0].Type != "uses" {
		t.Fatalf("Bob relationships = %v, want one uses", brels)
	}
	mems, _, _ := c.ListMemories(ctx, MemoryListFilter{}, 100, 0)
	if len(mems) != 2 {
		t.Fatalf("memories = %d, want 2 after normalised-content dedupe", len(mems))
	}
}

func TestRemember_ChunkExtractionFailureKeepsTheRest(t *testing.T) {
	forEachMode(t, testRemember_ChunkExtractionFailureKeepsTheRest)
}

func testRemember_ChunkExtractionFailureKeepsTheRest(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	var calls atomic.Int32
	c.SetExtractor(chunkedExtractor("P2", &calls))
	err := c.Remember(ctx, threeChunkDoc, WithMaxChunkChars(100))
	var p *ExtractPartialError
	if !errors.As(err, &p) || p.Failed != 1 || p.Total != 3 {
		t.Fatalf("err = %v, want ExtractPartialError 1 of 3", err)
	}
	ents, _ := c.FindEntities(ctx, EntityFilter{})
	if len(ents) != 3 {
		t.Fatalf("entities = %d, want 3 from the surviving chunks", len(ents))
	}
	var n int
	if err := testutil.H(c).DB.QueryRowContext(ctx, `SELECT count(*) FROM chunks WHERE user_id = ?`, testutil.H(c).UserID).Scan(&n); err != nil || n != 3 {
		t.Fatalf("chunks stored = %d err=%v, want 3 (raw text kept for failed chunk too)", n, err)
	}
}

func TestRemember_AllChunkExtractionsFailIsFatal(t *testing.T) {
	forEachMode(t, testRemember_AllChunkExtractionsFailIsFatal)
}

func testRemember_AllChunkExtractionsFailIsFatal(t *testing.T) {
	c := openTestDB(t)
	c.SetExtractor(&mockExtractor{extractFn: func(context.Context, string, string) (*Extraction, error) {
		return nil, errors.New("llm down")
	}})
	err := c.Remember(context.Background(), threeChunkDoc, WithMaxChunkChars(100))
	if err == nil || IsExtractPartial(err) {
		t.Fatalf("err = %v, want a fatal extract error", err)
	}
}

func TestRemember_ShortContentExtractsOnce(t *testing.T) {
	forEachMode(t, testRemember_ShortContentExtractsOnce)
}

func testRemember_ShortContentExtractsOnce(t *testing.T) {
	c := openTestDB(t)
	var seen []string
	c.SetExtractor(&mockExtractor{extractFn: func(_ context.Context, content, _ string) (*Extraction, error) {
		seen = append(seen, content)
		return &Extraction{}, nil
	}})
	if err := c.Remember(context.Background(), "short note"); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != "short note" {
		t.Fatalf("extractor saw %q, want the whole content once", seen)
	}
}

func TestRemember_ExtractionConcurrencyIsBounded(t *testing.T) {
	forEachMode(t, testRemember_ExtractionConcurrencyIsBounded)
}

func testRemember_ExtractionConcurrencyIsBounded(t *testing.T) {
	c := openTestDB(t)
	var cur, peak atomic.Int32
	c.SetExtractor(&mockExtractor{extractFn: func(context.Context, string, string) (*Extraction, error) {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		cur.Add(-1)
		return &Extraction{}, nil
	}})
	if err := c.Remember(context.Background(), manyParagraphs(12), WithMaxChunkChars(100)); err != nil {
		t.Fatal(err)
	}
	if p := peak.Load(); p > ingest.ExtractConcurrency || p < 2 {
		t.Fatalf("peak concurrency = %d, want between 2 and %d", p, ingest.ExtractConcurrency)
	}
}
