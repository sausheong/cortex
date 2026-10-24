package cortex

import (
	"context"
	"testing"
)

func provExtractor() Extractor {
	return &mockExtractor{
		extractFn: func(_ context.Context, _, _ string) (*Extraction, error) {
			return &Extraction{
				Entities:      []Entity{{Type: "person", Name: "Alice"}, {Type: "organization", Name: "Stripe"}},
				Relationships: []Relationship{{SourceID: "Alice", TargetID: "Stripe", Type: "works_at"}},
				Memories:      []Memory{{Content: "Alice works at Stripe"}},
			}, nil
		},
	}
}

// writtenByCounts returns, per table, rows whose written_by_connection_id equals want ("" = NULL).
func writtenByCounts(t *testing.T, c *Cortex, want string) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, tbl := range []string{"memories", "entities", "chunks", "relationships"} {
		var n int
		q := `SELECT count(*) FROM ` + tbl + ` WHERE user_id = ? AND written_by_connection_id IS NOT DISTINCT FROM ?`
		var arg any = want
		if want == "" {
			arg = nil
		}
		if err := c.h.DB.QueryRowContext(context.Background(), q, c.h.UserID, arg).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[tbl] = n
	}
	return out
}

func TestRememberRecordsConnection(t *testing.T) {
	a, _ := twoUsers(t)
	a.SetExtractor(provExtractor())
	ctx := context.Background()
	if err := a.WithConnection("c1").Remember(ctx, "Alice works at Stripe"); err != nil {
		t.Fatal(err)
	}
	for tbl, n := range writtenByCounts(t, a, "c1") {
		if n < 1 {
			t.Errorf("%s: no rows with connection c1", tbl)
		}
	}
	// Everything written through the connection carries it.
	if n := writtenByCounts(t, a, ""); n["memories"]+n["chunks"]+n["relationships"]+n["entities"] != 0 {
		t.Errorf("unexpected NULL rows: %v", n)
	}
	ents, _ := a.FindEntities(ctx, EntityFilter{})
	for _, e := range ents {
		if e.Source == "owner" {
			continue
		}
		if e.WrittenBy != "c1" {
			t.Errorf("entity %s WrittenBy=%q", e.Name, e.WrittenBy)
		}
	}
	mems, err := a.SearchMemories(ctx, "Alice", 10)
	if err != nil || len(mems) == 0 {
		t.Fatalf("search: %v %d", err, len(mems))
	}
	for _, m := range mems {
		if m.WrittenBy != "c1" {
			t.Errorf("memory WrittenBy=%q", m.WrittenBy)
		}
	}
}

func TestPlainWriteHasNoConnection(t *testing.T) {
	a, _ := twoUsers(t)
	a.SetExtractor(provExtractor())
	ctx := context.Background()
	if err := a.Remember(ctx, "Alice works at Stripe"); err != nil {
		t.Fatal(err)
	}
	if n := writtenByCounts(t, a, "c1"); n["memories"]+n["entities"]+n["chunks"]+n["relationships"] != 0 {
		t.Errorf("rows tagged c1: %v", n)
	}
	mems, _ := a.SearchMemories(ctx, "Alice", 10)
	for _, m := range mems {
		if m.WrittenBy != "" {
			t.Errorf("WrittenBy=%q", m.WrittenBy)
		}
	}
}

func TestDeleteByWrittenByScopedToUser(t *testing.T) {
	a, b := twoUsers(t)
	a.SetExtractor(provExtractor())
	b.SetExtractor(provExtractor())
	ctx := context.Background()
	if err := a.WithConnection("c1").Remember(ctx, "Alice works at Stripe"); err != nil {
		t.Fatal(err)
	}
	if err := b.WithConnection("c1").Remember(ctx, "Alice works at Stripe"); err != nil {
		t.Fatal(err)
	}
	_, n, err := a.SoftDeleteMemories(ctx, DeleteFilter{WrittenBy: "c1"}, "test")
	if err != nil || n == 0 {
		t.Fatalf("delete: n=%d err=%v", n, err)
	}
	if mems, _ := a.SearchMemories(ctx, "Alice", 10); len(mems) != 0 {
		t.Errorf("a still has %d", len(mems))
	}
	if mems, _ := b.SearchMemories(ctx, "Alice", 10); len(mems) == 0 {
		t.Errorf("b's memories were deleted")
	}
}
