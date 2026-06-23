package cortex

import (
	"context"
	"testing"
	"time"
)

func TestReconcile_DryRun_ProposesSupersession(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "Alice"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	older := &Memory{Content: "Alice's budget is 5000", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, older); err != nil {
		t.Fatal(err)
	}
	newer := &Memory{Content: "Alice's budget is 10000", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, newer); err != nil {
		t.Fatal(err)
	}
	// Force a created_at ordering so newer is strictly newer (PutMemory uses now();
	// two inserts in the same test may share a timestamp).
	older.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := c.db.ExecContext(ctx, `UPDATE memories SET created_at = ? WHERE id = ?`, older.CreatedAt, older.ID); err != nil {
		t.Fatal(err)
	}
	newer.CreatedAt = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if _, err := c.db.ExecContext(ctx, `UPDATE memories SET created_at = ? WHERE id = ?`, newer.CreatedAt, newer.ID); err != nil {
		t.Fatal(err)
	}

	c.SetLLM(&mockLLM{
		detectConflictsFn: func(_ context.Context, mems []Memory) ([]ConflictPair, error) {
			// Flag: newer supersedes older.
			return []ConflictPair{{StaleID: older.ID, SupersededByID: newer.ID, Reason: "budget changed"}}, nil
		},
	})

	rep, err := c.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if rep.Skipped {
		t.Fatalf("expected not skipped, got skip reason %q", rep.SkipReason)
	}
	if len(rep.Proposed) != 1 {
		t.Fatalf("expected 1 proposed supersession, got %d (rejected=%d)", len(rep.Proposed), len(rep.Rejected))
	}
	p := rep.Proposed[0]
	if p.StaleID != older.ID || p.SupersededByID != newer.ID {
		t.Fatalf("wrong supersession: %+v", p)
	}
	if !p.InvalidAt.Equal(newer.CreatedAt) {
		t.Fatalf("expected InvalidAt = newer.CreatedAt %v, got %v", newer.CreatedAt, p.InvalidAt)
	}

	// Dry-run changed nothing: both memories still currently-valid.
	got, err := c.SearchMemories(ctx, "budget", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("dry-run must not invalidate; expected 2 valid memories, got %d", len(got))
	}
}

func TestReconcile_GateRejectsWrongDirection(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "Bob"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	older := &Memory{Content: "Bob lives in Paris", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, older); err != nil {
		t.Fatal(err)
	}
	newer := &Memory{Content: "Bob lives in Berlin", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, newer); err != nil {
		t.Fatal(err)
	}
	older.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c.db.ExecContext(ctx, `UPDATE memories SET created_at = ? WHERE id = ?`, older.CreatedAt, older.ID)
	newer.CreatedAt = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	c.db.ExecContext(ctx, `UPDATE memories SET created_at = ? WHERE id = ?`, newer.CreatedAt, newer.ID)

	c.SetLLM(&mockLLM{
		detectConflictsFn: func(_ context.Context, _ []Memory) ([]ConflictPair, error) {
			// WRONG direction: claims the OLDER supersedes the NEWER.
			return []ConflictPair{{StaleID: newer.ID, SupersededByID: older.ID, Reason: "backwards"}}, nil
		},
	})

	rep, err := c.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Proposed) != 0 {
		t.Fatalf("gate must reject older-supersedes-newer, got %d proposed", len(rep.Proposed))
	}
	if len(rep.Rejected) != 1 {
		t.Fatalf("expected 1 rejected pair, got %d", len(rep.Rejected))
	}
}

func TestReconcile_SkipsWithoutReconciler(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	// Default: no LLM set, so no Reconciler.
	rep, err := c.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile should not error when no reconciler: %v", err)
	}
	if !rep.Skipped {
		t.Fatal("expected Skipped=true when no Reconciler is configured")
	}
}
