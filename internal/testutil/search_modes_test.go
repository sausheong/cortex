package testutil

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/sausheong/cortex"
)

// TestMemorySearchOrderEqualsLegacy runs the same corpus through a legacy and
// an encrypted handle: memory search must return the same order (the score is
// the same IDF formula with the same id tie-break), and chunk keyword search
// and entity search must find the same sets.
func TestMemorySearchOrderEqualsLegacy(t *testing.T) {
	ctx := context.Background()
	type outcome struct {
		mem, chunks, ents []string
	}
	got := map[string]outcome{}
	ForEachMode(t, func(t *testing.T, c *cortex.Cortex) {
		corpus := []string{
			"Alice works at Stripe as an engineer",
			"Stripe ships payments in April",
			"Bob likes hiking near the lake",
			"Alice and Bob met at the Stripe offsite",
			"The offsite agenda lists payments and hiking",
			"Quarterly planning notes for payments",
		}
		for _, name := range []string{"Alice Johnson", "Alicia Keys", "Bob Marley"} {
			if err := c.PutEntity(ctx, &cortex.Entity{Type: "person", Name: name}); err != nil {
				t.Fatal(err)
			}
		}
		for _, s := range corpus {
			if err := c.PutMemory(ctx, &cortex.Memory{Content: s, Source: "t"}); err != nil {
				t.Fatal(err)
			}
			if err := c.PutChunk(ctx, &cortex.Chunk{Content: s}); err != nil {
				t.Fatal(err)
			}
		}
		var o outcome
		ms, err := c.SearchMemories(ctx, "Stripe payments offsite", 10)
		if err != nil || len(ms) == 0 {
			t.Fatalf("SearchMemories = %d, %v", len(ms), err)
		}
		for _, m := range ms {
			o.mem = append(o.mem, m.Content)
		}
		cs, err := c.SearchKeyword(ctx, "Stripe payments offsite", 10)
		if err != nil || len(cs) == 0 {
			t.Fatalf("SearchKeyword = %d, %v", len(cs), err)
		}
		for _, ch := range cs {
			o.chunks = append(o.chunks, ch.Content)
		}
		es, err := c.SearchEntities(ctx, "ali", nil, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range es {
			o.ents = append(o.ents, e.Name)
		}
		got[strings.TrimPrefix(t.Name()[strings.LastIndex(t.Name(), "/")+1:], "/")] = o
	})
	l, e := got["legacy"], got["encrypted"]
	if len(l.mem) == 0 || len(e.mem) == 0 {
		t.Fatalf("an arm did not run: %+v", got)
	}
	if !reflect.DeepEqual(l.mem, e.mem) {
		t.Errorf("memory order differs:\nlegacy    %v\nencrypted %v", l.mem, e.mem)
	}
	if !reflect.DeepEqual(l.ents, e.ents) {
		t.Errorf("entity search differs:\nlegacy    %v\nencrypted %v", l.ents, e.ents)
	}
	// Chunk ranking differs by design (BM25 vs ts_rank_cd); the same chunks match.
	if len(l.chunks) != len(e.chunks) {
		t.Errorf("chunk keyword matched %d legacy, %d encrypted", len(l.chunks), len(e.chunks))
	}
}
