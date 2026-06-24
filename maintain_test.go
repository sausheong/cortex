package cortex

import (
	"context"
	"testing"
	"time"
)

func TestMaintain_RunsAllThreePasses(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	// Two contradicting memories on an entity (reconcile fodder) + an aging one.
	e := &Entity{Name: "Proj", Type: "project"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	older := &Memory{Content: "Proj budget is 5000", EntityIDs: []string{e.ID}}
	newer := &Memory{Content: "Proj budget is 9000", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, older); err != nil {
		t.Fatal(err)
	}
	if err := c.PutMemory(ctx, newer); err != nil {
		t.Fatal(err)
	}
	// Backdate older so newer is strictly newer (reconcile gate) and so decay has age.
	if _, err := c.db.ExecContext(ctx,
		`UPDATE memories SET created_at = ? WHERE id = ?`,
		newer.CreatedAt.Add(-48*time.Hour), older.ID); err != nil {
		t.Fatal(err)
	}

	c.SetLLM(&mockLLM{
		detectConflictsFn: func(_ context.Context, mems []Memory) ([]ConflictPair, error) {
			return []ConflictPair{{StaleID: older.ID, SupersededByID: newer.ID, Reason: "budget changed"}}, nil
		},
		detectRelationsFn: func(_ context.Context, mems []Memory) ([]MemoryRelation, error) {
			return nil, nil // no relations for this test; just prove the pass ran
		},
	})

	report, err := c.Maintain(ctx, WithMaintainDecayOptions(WithHalfLife(30*24*time.Hour), WithFloor(0.05)))
	if err != nil {
		t.Fatalf("Maintain: %v", err)
	}
	if report.Reconcile == nil || report.Relate == nil || report.Decay == nil {
		t.Fatalf("expected all three sub-reports present, got %+v", report)
	}
	if report.DryRun {
		t.Fatal("expected DryRun false")
	}
	// Reconcile should have applied the supersession.
	if len(report.Reconcile.Proposed) != 1 {
		t.Fatalf("expected 1 reconciled supersession, got %d", len(report.Reconcile.Proposed))
	}
	// The stale memory is now invalidated → a supersedes edge exists.
	edges, err := c.GetMemoryEdgesByType(ctx, newer.ID, EdgeSupersedes)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected supersedes edge from reconcile pass, got %d", len(edges))
	}
}

func TestMaintain_DryRunWritesNothing(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Name: "P", Type: "project"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	older := &Memory{Content: "P is red", EntityIDs: []string{e.ID}}
	newer := &Memory{Content: "P is blue", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, older); err != nil {
		t.Fatal(err)
	}
	if err := c.PutMemory(ctx, newer); err != nil {
		t.Fatal(err)
	}
	if _, err := c.db.ExecContext(ctx,
		`UPDATE memories SET created_at = ? WHERE id = ?`,
		newer.CreatedAt.Add(-48*time.Hour), older.ID); err != nil {
		t.Fatal(err)
	}

	c.SetLLM(&mockLLM{
		detectConflictsFn: func(_ context.Context, mems []Memory) ([]ConflictPair, error) {
			return []ConflictPair{{StaleID: older.ID, SupersededByID: newer.ID, Reason: "color changed"}}, nil
		},
	})

	report, err := c.Maintain(ctx, WithMaintainDryRun())
	if err != nil {
		t.Fatalf("Maintain dry-run: %v", err)
	}
	if !report.DryRun {
		t.Fatal("expected DryRun true")
	}
	// Relate is skipped under dry-run.
	if report.Relate == nil || !report.Relate.Skipped {
		t.Fatalf("expected relate Skipped under dry-run, got %+v", report.Relate)
	}
	// Reconcile dry-run proposed but DID NOT apply → no supersedes edge, older still valid.
	edges, err := c.GetMemoryEdgesByType(ctx, newer.ID, EdgeSupersedes)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 0 {
		t.Fatalf("dry-run must not record edges, got %d", len(edges))
	}
	got, err := c.getMemoryByID(ctx, older.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExpiredAt != nil || got.InvalidAt != nil {
		t.Fatal("dry-run must not invalidate the stale memory")
	}
}

func TestMaintain_SkipToggles(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	report, err := c.Maintain(ctx, WithoutReconcile(), WithoutRelate(), WithoutDecay())
	if err != nil {
		t.Fatalf("Maintain: %v", err)
	}
	if report.Reconcile != nil || report.Relate != nil || report.Decay != nil {
		t.Fatalf("expected all sub-reports nil when all skipped, got %+v", report)
	}
}
