package cortex

import (
	"context"
	"testing"
	"time"
)

func TestDecayConfidence_DecaysOldMemory(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	m := &Memory{Content: "aging fact", Confidence: 1.0}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	// Backdate created_at to ~one 30-day half-life ago and leave last_decay_at NULL.
	anchor := time.Now().UTC().Add(-30 * 24 * time.Hour)
	if _, err := c.db.ExecContext(ctx,
		`UPDATE memories SET created_at = ? WHERE id = ?`, anchor, m.ID); err != nil {
		t.Fatal(err)
	}

	report, err := c.DecayConfidence(ctx, WithHalfLife(30*24*time.Hour), WithFloor(0.05))
	if err != nil {
		t.Fatalf("DecayConfidence: %v", err)
	}
	if report.Decayed != 1 {
		t.Fatalf("expected 1 decayed, got %d (%+v)", report.Decayed, report)
	}

	got, err := c.getMemoryByID(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	// ~one half-life → ~0.5. Allow tolerance for the exact elapsed.
	if got.Confidence < 0.45 || got.Confidence > 0.55 {
		t.Fatalf("expected ~0.5 after one half-life, got %v", got.Confidence)
	}
}

func TestDecayConfidence_AutoPrunesBelowFloor(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	m := &Memory{Content: "ancient fact", Confidence: 1.0}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	// Backdate far past so decayed confidence falls well below the floor.
	anchor := time.Now().UTC().Add(-3650 * 24 * time.Hour) // ~10 years
	if _, err := c.db.ExecContext(ctx,
		`UPDATE memories SET created_at = ? WHERE id = ?`, anchor, m.ID); err != nil {
		t.Fatal(err)
	}

	report, err := c.DecayConfidence(ctx, WithHalfLife(30*24*time.Hour), WithFloor(0.05))
	if err != nil {
		t.Fatal(err)
	}
	if report.Pruned != 1 {
		t.Fatalf("expected 1 pruned, got %d (%+v)", report.Pruned, report)
	}

	// Pruned memory is soft-invalidated: hidden from default recall, but
	// reachable unfiltered. expired_at set, invalid_at NULL.
	got, err := c.getMemoryByID(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExpiredAt == nil {
		t.Fatal("expected expired_at set on pruned memory")
	}
	if got.InvalidAt != nil {
		t.Fatal("expected invalid_at NULL (decay-prune is system-retirement, not event-invalidation)")
	}

	// It must NOT appear in a default (currently-valid) search.
	found, err := c.SearchMemories(ctx, "ancient fact", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Fatalf("pruned memory should be hidden from default recall, got %d", len(found))
	}
}

func TestDecayConfidence_DryRunWritesNothing(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	m := &Memory{Content: "dry run fact", Confidence: 1.0}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	anchor := time.Now().UTC().Add(-60 * 24 * time.Hour)
	if _, err := c.db.ExecContext(ctx,
		`UPDATE memories SET created_at = ? WHERE id = ?`, anchor, m.ID); err != nil {
		t.Fatal(err)
	}

	report, err := c.DecayConfidence(ctx, WithHalfLife(30*24*time.Hour), WithDecayDryRun())
	if err != nil {
		t.Fatal(err)
	}
	if !report.DryRun || len(report.Changes) != 1 {
		t.Fatalf("expected dry-run with 1 change, got %+v", report)
	}

	// Nothing written: confidence unchanged.
	got, err := c.getMemoryByID(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Confidence != 1.0 {
		t.Fatalf("dry-run must not write; confidence changed to %v", got.Confidence)
	}
}

func TestDecay_SkipsStaticMemories(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "Alice"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	// A static memory and a non-static memory, both old and low-confidence.
	stat := &Memory{Content: "Alice is from Seattle", EntityIDs: []string{e.ID}, Confidence: 0.2, Static: true}
	if err := c.PutMemory(ctx, stat); err != nil {
		t.Fatal(err)
	}
	dyn := &Memory{Content: "Alice has a meeting", EntityIDs: []string{e.ID}, Confidence: 0.2, Static: false}
	if err := c.PutMemory(ctx, dyn); err != nil {
		t.Fatal(err)
	}
	// Age both far back so decay would fire.
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{stat.ID, dyn.ID} {
		if _, err := c.db.ExecContext(ctx, `UPDATE memories SET created_at = ?, last_decay_at = NULL WHERE id = ?`, old, id); err != nil {
			t.Fatal(err)
		}
	}

	rep, err := c.DecayConfidence(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Only the dynamic memory should appear in the decay changes.
	for _, ch := range rep.Changes {
		if ch.ID == stat.ID {
			t.Errorf("static memory %s should not be decayed", stat.ID)
		}
	}
	// Sanity: the dynamic memory WAS decayed (proves the test setup triggers decay).
	sawDyn := false
	for _, ch := range rep.Changes {
		if ch.ID == dyn.ID {
			sawDyn = true
		}
	}
	if !sawDyn {
		t.Error("expected the non-static memory to be decayed")
	}
}
