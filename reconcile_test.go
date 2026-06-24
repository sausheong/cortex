package cortex

import (
	"context"
	"encoding/json"
	"strings"
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

func TestApplyReconcile_InvalidatesStale(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "Carol"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	older := &Memory{Content: "Carol's title is Engineer", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, older); err != nil {
		t.Fatal(err)
	}
	newer := &Memory{Content: "Carol's title is Director", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, newer); err != nil {
		t.Fatal(err)
	}
	older.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c.db.ExecContext(ctx, `UPDATE memories SET created_at = ? WHERE id = ?`, older.CreatedAt, older.ID)
	newer.CreatedAt = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	c.db.ExecContext(ctx, `UPDATE memories SET created_at = ? WHERE id = ?`, newer.CreatedAt, newer.ID)

	c.SetLLM(&mockLLM{
		detectConflictsFn: func(_ context.Context, _ []Memory) ([]ConflictPair, error) {
			return []ConflictPair{{StaleID: older.ID, SupersededByID: newer.ID, Reason: "promoted"}}, nil
		},
	})

	rep, err := c.ApplyReconcile(ctx)
	if err != nil {
		t.Fatalf("ApplyReconcile: %v", err)
	}
	if len(rep.Proposed) != 1 {
		t.Fatalf("expected 1 applied supersession, got %d", len(rep.Proposed))
	}

	// After apply: only the newer memory is currently-valid.
	got, err := c.SearchMemories(ctx, "Carol title", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != newer.ID {
		t.Fatalf("expected only newer memory valid after apply, got %d", len(got))
	}

	// The stale one is still retrievable with history.
	all, err := c.Recall(ctx, "Carol title", WithLimit(10), WithIncludeInvalid())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("expected both memories via WithIncludeInvalid, got %d", len(all))
	}
}

func TestReconcile_DedupsSharedMemoryAcrossEntities(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e1 := &Entity{Type: "person", Name: "Dave"}
	if err := c.PutEntity(ctx, e1); err != nil {
		t.Fatal(err)
	}
	e2 := &Entity{Type: "org", Name: "Acme"}
	if err := c.PutEntity(ctx, e2); err != nil {
		t.Fatal(err)
	}

	// Both memories are linked to BOTH entities, so each is evaluated once per
	// entity pass. Without dedup the same supersession is appended twice.
	older := &Memory{Content: "Dave's role at Acme is Engineer", EntityIDs: []string{e1.ID, e2.ID}}
	if err := c.PutMemory(ctx, older); err != nil {
		t.Fatal(err)
	}
	newer := &Memory{Content: "Dave's role at Acme is Director", EntityIDs: []string{e1.ID, e2.ID}}
	if err := c.PutMemory(ctx, newer); err != nil {
		t.Fatal(err)
	}
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
			// Flag the same pair on every entity pass that sees both memories.
			if len(mems) >= 2 {
				return []ConflictPair{{StaleID: older.ID, SupersededByID: newer.ID, Reason: "promoted"}}, nil
			}
			return nil, nil
		},
	})

	rep, err := c.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(rep.Proposed) != 1 {
		t.Fatalf("expected 1 deduped proposal across entity passes, got %d", len(rep.Proposed))
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

func TestReconcileReport_JSONRoundTrip(t *testing.T) {
	orig := ReconcileReport{
		EntitiesScanned: 2,
		MemoriesScanned: 5,
		Proposed: []Supersession{{
			StaleID: "s1", StaleContent: "budget 5000",
			SupersededByID: "n1", SupersededByContent: "budget 10000",
			Reason: "changed", InvalidAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		}},
		Rejected: []RejectedPair{{StaleID: "s2", SupersededByID: "n2", Reason: "not newer"}},
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Snake_case tags must be present in the wire form.
	for _, want := range []string{`"stale_id"`, `"superseded_by_id"`, `"invalid_at"`, `"proposed"`, `"entities_scanned"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("expected JSON to contain %s, got: %s", want, data)
		}
	}

	var back ReconcileReport
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back.Proposed) != 1 || back.Proposed[0].StaleID != "s1" ||
		!back.Proposed[0].InvalidAt.Equal(orig.Proposed[0].InvalidAt) {
		t.Fatalf("round-trip mismatch: %+v", back)
	}
	if back.EntitiesScanned != 2 || len(back.Rejected) != 1 {
		t.Fatalf("round-trip lost fields: %+v", back)
	}
}

func TestApplyReconcileReport_AppliesReviewed(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "Dana"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	older := &Memory{Content: "Dana's role is IC", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, older); err != nil {
		t.Fatal(err)
	}
	newer := &Memory{Content: "Dana's role is Manager", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, newer); err != nil {
		t.Fatal(err)
	}
	older.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c.db.ExecContext(ctx, `UPDATE memories SET created_at = ? WHERE id = ?`, older.CreatedAt, older.ID)
	newer.CreatedAt = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	c.db.ExecContext(ctx, `UPDATE memories SET created_at = ? WHERE id = ?`, newer.CreatedAt, newer.ID)

	// A report as if produced by an earlier dry-run.
	report := ReconcileReport{
		Proposed: []Supersession{{
			StaleID: older.ID, SupersededByID: newer.ID,
			Reason: "promoted", InvalidAt: newer.CreatedAt,
		}},
	}

	applied, err := c.ApplyReconcileReport(ctx, report)
	if err != nil {
		t.Fatalf("ApplyReconcileReport: %v", err)
	}
	if len(applied.Proposed) != 1 {
		t.Fatalf("expected 1 applied, got %d (rejected=%d)", len(applied.Proposed), len(applied.Rejected))
	}
	// older now hidden from default recall; newer remains.
	got, err := c.SearchMemories(ctx, "Dana role", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != newer.ID {
		t.Fatalf("expected only newer valid, got %d", len(got))
	}
}

func TestApplyReconcileReport_SkipsStaleProposal(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "Eli"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	older := &Memory{Content: "Eli's city is Rome", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, older); err != nil {
		t.Fatal(err)
	}
	newer := &Memory{Content: "Eli's city is Oslo", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, newer); err != nil {
		t.Fatal(err)
	}
	older.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c.db.ExecContext(ctx, `UPDATE memories SET created_at = ? WHERE id = ?`, older.CreatedAt, older.ID)
	newer.CreatedAt = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	c.db.ExecContext(ctx, `UPDATE memories SET created_at = ? WHERE id = ?`, newer.CreatedAt, newer.ID)

	// The graph changed since the dry-run: the stale memory was ALREADY invalidated.
	cut := newer.CreatedAt
	if err := c.InvalidateMemory(ctx, older.ID, &cut); err != nil {
		t.Fatal(err)
	}

	report := ReconcileReport{
		Proposed: []Supersession{{
			StaleID: older.ID, SupersededByID: newer.ID,
			Reason: "stale", InvalidAt: newer.CreatedAt,
		}},
	}

	applied, err := c.ApplyReconcileReport(ctx, report)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.Proposed) != 0 {
		t.Fatalf("expected 0 applied (stale already invalid), got %d", len(applied.Proposed))
	}
	if len(applied.Rejected) != 1 {
		t.Fatalf("expected 1 rejected by re-validation, got %d", len(applied.Rejected))
	}
}

func TestApplyReconcileReport_SkippedInputUnchanged(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	in := ReconcileReport{Skipped: true, SkipReason: "no reconciler"}
	out, err := c.ApplyReconcileReport(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Skipped || len(out.Proposed) != 0 {
		t.Fatalf("skipped input should pass through unchanged, got %+v", out)
	}
}

func TestApplyReconcile_RecordsSupersedesEdge(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	// Two contradicting memories on the same entity, newer supersedes older.
	e := &Entity{Name: "Project X", Type: "project"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	older := &Memory{Content: "Project X budget is 5000", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, older); err != nil {
		t.Fatal(err)
	}
	// Ensure the newer memory has a strictly-later created_at.
	newer := &Memory{Content: "Project X budget is 10000", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, newer); err != nil {
		t.Fatal(err)
	}
	// Backdate the older one so newer.CreatedAt is strictly after.
	if _, err := c.db.ExecContext(ctx,
		`UPDATE memories SET created_at = ? WHERE id = ?`,
		older.CreatedAt.Add(-1*time.Hour), older.ID); err != nil {
		t.Fatal(err)
	}

	c.SetLLM(&mockLLM{
		detectConflictsFn: func(_ context.Context, mems []Memory) ([]ConflictPair, error) {
			return []ConflictPair{{StaleID: older.ID, SupersededByID: newer.ID, Reason: "budget changed"}}, nil
		},
	})

	report, err := c.ApplyReconcile(ctx)
	if err != nil {
		t.Fatalf("ApplyReconcile: %v", err)
	}
	if len(report.Proposed) != 1 {
		t.Fatalf("expected 1 applied supersession, got %d", len(report.Proposed))
	}

	// A supersedes edge must exist: newer -> older.
	edges, err := c.GetMemoryEdgesByType(ctx, newer.ID, EdgeSupersedes)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 supersedes edge, got %d", len(edges))
	}
	if edges[0].SourceID != newer.ID || edges[0].TargetID != older.ID {
		t.Fatalf("edge direction wrong: source=%s target=%s (want source=newer %s, target=older %s)",
			edges[0].SourceID, edges[0].TargetID, newer.ID, older.ID)
	}
}
