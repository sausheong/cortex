package cortex

import (
	"context"
	"testing"
)

func TestPutAndGetMemoryEdge(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	newer := &Memory{Content: "budget is 10000"}
	older := &Memory{Content: "budget is 5000"}
	if err := c.PutMemory(ctx, newer); err != nil {
		t.Fatal(err)
	}
	if err := c.PutMemory(ctx, older); err != nil {
		t.Fatal(err)
	}

	e := &MemoryEdge{SourceID: newer.ID, TargetID: older.ID, Type: EdgeSupersedes}
	if err := c.PutMemoryEdge(ctx, e); err != nil {
		t.Fatalf("PutMemoryEdge: %v", err)
	}
	if e.ID == "" {
		t.Fatal("expected edge ID to be set")
	}

	// Idempotent: re-inserting the same (source,target,type) does not error or duplicate.
	dup := &MemoryEdge{SourceID: newer.ID, TargetID: older.ID, Type: EdgeSupersedes}
	if err := c.PutMemoryEdge(ctx, dup); err != nil {
		t.Fatalf("PutMemoryEdge (dup): %v", err)
	}

	edges, err := c.GetMemoryEdges(ctx, newer.ID)
	if err != nil {
		t.Fatalf("GetMemoryEdges: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge (idempotent), got %d", len(edges))
	}
	if edges[0].SourceID != newer.ID || edges[0].TargetID != older.ID || edges[0].Type != EdgeSupersedes {
		t.Fatalf("edge mismatch: %+v", edges[0])
	}

	// The older memory sees the same edge (as target).
	asTarget, err := c.GetMemoryEdges(ctx, older.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(asTarget) != 1 {
		t.Fatalf("expected older memory to see 1 edge as target, got %d", len(asTarget))
	}

	// Type filter.
	byType, err := c.GetMemoryEdgesByType(ctx, newer.ID, EdgeSupersedes)
	if err != nil {
		t.Fatal(err)
	}
	if len(byType) != 1 {
		t.Fatalf("expected 1 supersedes edge, got %d", len(byType))
	}
	none, err := c.GetMemoryEdgesByType(ctx, newer.ID, EdgeDerives)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("expected 0 derives edges, got %d", len(none))
	}
}
