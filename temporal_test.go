package cortex

import (
	"context"
	"testing"
	"time"
)

func TestValidAtNonUTCRoundTrip(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	loc := time.FixedZone("UTC+8", 8*3600)
	validAt := time.Date(2026, 3, 1, 8, 0, 0, 0, loc) // == 2026-03-01T00:00:00Z
	m := &Memory{Content: "Alice budget kickoff", ValidAt: &validAt}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	// Backdate created_at so only valid_at gates the as-of window.
	if _, err := c.db.ExecContext(ctx, `UPDATE memories SET created_at = ? WHERE id = ?`,
		time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), m.ID); err != nil {
		t.Fatal(err)
	}

	got, err := c.getMemoryByID(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ValidAt == nil || !got.ValidAt.Equal(validAt) {
		t.Fatalf("ValidAt = %v, want instant %v", got.ValidAt, validAt)
	}

	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
			return []StructuredQuery{{Type: "memory_lookup", Params: map[string]any{"query": q}}}, nil
		},
	})
	in, err := c.Recall(ctx, "budget", WithLimit(10), WithAsOf(time.Date(2026, 3, 1, 0, 30, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	if len(in) != 1 {
		t.Fatalf("as_of 00:30Z should include the memory, got %d", len(in))
	}
	out, err := c.Recall(ctx, "budget", WithLimit(10), WithAsOf(time.Date(2026, 2, 28, 23, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 0 {
		t.Fatalf("as_of 2026-02-28T23:00Z should exclude the memory, got %d", len(out))
	}
}
