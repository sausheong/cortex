package cortex

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sausheong/cortex/internal/pgtest"
)

// limitEmbedder rejects inputs over max characters, like a real model with
// an input token limit, and can be switched to fail everything.
type limitEmbedder struct {
	max     int
	down    bool
	longest int
}

func (e *limitEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		if len(t) > e.longest {
			e.longest = len(t)
		}
		if e.down || len(t) > e.max {
			return nil, errors.New("input too long")
		}
		out[i] = make([]float32, 8)
	}
	return out, nil
}
func (e *limitEmbedder) Dimensions() int { return 8 }

func rememberDoc(t *testing.T, emb *limitEmbedder) (*Cortex, error) {
	t.Helper()
	c, err := Open(pgtest.DSN(t), WithEmbedder(emb))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	c.SetExtractor(&mockExtractor{
		extractFn: func(_ context.Context, _, _ string) (*Extraction, error) {
			return &Extraction{Memories: []Memory{{Content: "the doc says hello"}}}, nil
		},
	})
	para := strings.Repeat("word ", 400)   // ~2 KB paragraph
	doc := strings.Repeat(para+"\n\n", 70) // ~140 KB, like a long Markdown file
	return c, c.Remember(context.Background(), doc, WithSource("doc.md"))
}

// A long document is split so every chunk fits the embedder, and nothing
// is left without a vector.
func TestRemember_LongDocumentIsSplitAndEmbedded(t *testing.T) {
	emb := &limitEmbedder{max: 8000}
	c, err := rememberDoc(t, emb)
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}
	if emb.longest > DefaultMaxChunkChars+200 {
		t.Fatalf("longest embedded text %d chars, want about %d or less", emb.longest, DefaultMaxChunkChars)
	}
	gen, _ := c.ActiveGeneration(context.Background())
	pending, err := c.PendingEmbeddings(context.Background(), gen, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("%d rows without an embedding", len(pending))
	}
}

// An embedding outage must not lose the save: memories are still stored,
// the caller learns embeddings are pending, and the backfill fixes it.
func TestRemember_EmbedOutageStoresEverythingAndBackfills(t *testing.T) {
	emb := &limitEmbedder{max: 8000, down: true}
	c, err := rememberDoc(t, emb)
	if !IsEmbedPartial(err) {
		t.Fatalf("Remember err = %v, want an EmbedPartialError", err)
	}
	ctx := context.Background()
	mems, _, err := c.ListMemories(ctx, MemoryListFilter{}, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(mems) != 1 {
		t.Fatalf("memories stored = %d, want 1", len(mems))
	}

	emb.down = false
	n, err := c.BackfillEmbeddings(ctx, 1000)
	if err != nil || n == 0 {
		t.Fatalf("backfill: n=%d err=%v", n, err)
	}
	gen, _ := c.ActiveGeneration(ctx)
	pending, _ := c.PendingEmbeddings(ctx, gen, 1000)
	if len(pending) != 0 {
		t.Fatalf("%d rows still without an embedding after backfill", len(pending))
	}
}
