package cortex

import (
	"context"
	"testing"
)

func TestBuildMemoryEdges_RecordsExtends(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Name: "Project Y", Type: "project"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	base := &Memory{Content: "Project Y uses Postgres", EntityIDs: []string{e.ID}}
	detail := &Memory{Content: "Project Y uses Postgres 16 with pgvector", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, base); err != nil {
		t.Fatal(err)
	}
	if err := c.PutMemory(ctx, detail); err != nil {
		t.Fatal(err)
	}

	c.SetLLM(&mockLLM{
		detectRelationsFn: func(_ context.Context, mems []Memory) ([]MemoryRelation, error) {
			return []MemoryRelation{
				{SourceID: detail.ID, TargetID: base.ID, Type: EdgeExtends, Reason: "adds version detail"},
			}, nil
		},
	})

	report, err := c.BuildMemoryEdges(ctx)
	if err != nil {
		t.Fatalf("BuildMemoryEdges: %v", err)
	}
	if report.Skipped {
		t.Fatalf("unexpected skip: %s", report.SkipReason)
	}
	if len(report.Proposed) != 1 {
		t.Fatalf("expected 1 proposed relation, got %d (%+v)", len(report.Proposed), report.Proposed)
	}

	edges, err := c.GetMemoryEdgesByType(ctx, detail.ID, EdgeExtends)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 || edges[0].SourceID != detail.ID || edges[0].TargetID != base.ID {
		t.Fatalf("expected extends edge detail->base, got %+v", edges)
	}
}

func TestBuildMemoryEdges_GateRejectsSelfLoopAndBadType(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Name: "Z", Type: "thing"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	m1 := &Memory{Content: "fact one", EntityIDs: []string{e.ID}}
	m2 := &Memory{Content: "fact two", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, m1); err != nil {
		t.Fatal(err)
	}
	if err := c.PutMemory(ctx, m2); err != nil {
		t.Fatal(err)
	}

	c.SetLLM(&mockLLM{
		detectRelationsFn: func(_ context.Context, mems []Memory) ([]MemoryRelation, error) {
			return []MemoryRelation{
				{SourceID: m1.ID, TargetID: m1.ID, Type: EdgeExtends, Reason: "self"},        // self-loop
				{SourceID: m1.ID, TargetID: m2.ID, Type: "supersedes", Reason: "wrong type"}, // unsupported type
				{SourceID: m1.ID, TargetID: "ghost", Type: EdgeDerives, Reason: "unknown id"}, // not in set
			}, nil
		},
	})

	report, err := c.BuildMemoryEdges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Proposed) != 0 {
		t.Fatalf("expected 0 proposed (all rejected), got %+v", report.Proposed)
	}
	if len(report.Rejected) != 3 {
		t.Fatalf("expected 3 rejected, got %d (%+v)", len(report.Rejected), report.Rejected)
	}
}

func TestBuildMemoryEdges_SkipsWithoutDetector(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	// No LLM set → no RelationDetector → Skipped.
	report, err := c.BuildMemoryEdges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Skipped {
		t.Fatalf("expected Skipped report, got %+v", report)
	}
}
