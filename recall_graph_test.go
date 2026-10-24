package cortex

import (
	"context"
	"reflect"
	"testing"

	"github.com/sausheong/cortex/internal/model"
)

// graphFixture: Carol works at WYNNES; Dan is unrelated.
//
//	m1 "She designs packaging"      -> Carol     (no keyword overlap with the query)
//	m2 "WYNNES moved offices"       -> WYNNES    (one hop from Carol)
//	m3 "Dan asked about Carol Seah" -> Dan       (keyword hit, not linked to Carol)
func graphFixture(t *testing.T) (*Cortex, map[string]string) {
	t.Helper()
	c := openTestDB(t)
	ctx := context.Background()
	ids := map[string]string{}
	for _, e := range []*Entity{{Name: "Carol Seah", Type: "person"}, {Name: "WYNNES", Type: "organization"}, {Name: "Dan", Type: "person"}} {
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
		ids[e.Name] = e.ID
	}
	if err := c.PutRelationship(ctx, &Relationship{SourceID: ids["Carol Seah"], TargetID: ids["WYNNES"], Type: RelWorksAt}); err != nil {
		t.Fatal(err)
	}
	for content, ent := range map[string]string{
		"She designs packaging":      "Carol Seah",
		"WYNNES moved offices":       "WYNNES",
		"Dan asked about Carol Seah": "Dan",
	} {
		m := &Memory{Content: content, EntityIDs: []string{ids[ent]}}
		if err := c.PutMemory(ctx, m); err != nil {
			t.Fatal(err)
		}
		ids[content] = m.ID
	}
	c.SetLLM(&mockLLM{decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
		return []StructuredQuery{{Type: "memory_lookup", Params: map[string]any{"query": q}}}, nil
	}})
	return c, ids
}

func TestRecall_GraphSeedsAndBoost(t *testing.T) {
	c, ids := graphFixture(t)
	ctx := context.Background()

	off, err := c.Recall(ctx, "Carol Seah", WithGraph(false))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range off {
		if r.ID == ids["She designs packaging"] {
			t.Fatal("without the graph, the memory with no keyword overlap should not be found")
		}
	}

	on, err := c.Recall(ctx, "Carol Seah")
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	for _, r := range on {
		order = append(order, r.Content)
	}
	if len(on) == 0 || on[0].ID != ids["She designs packaging"] {
		t.Fatalf("with the graph, Carol's own memory should rank first; got %v", order)
	}
	pos := map[string]int{}
	for i, r := range on {
		pos[r.ID] = i
	}
	if _, ok := pos[ids["Dan asked about Carol Seah"]]; !ok {
		t.Errorf("keyword hit dropped: %v", order)
	}
}

func TestNeighbourhood_DoesNotWalkThroughOwner(t *testing.T) {
	c, ownerID := ownerCortex(t, "Sau Sheong", "s@x.com")
	ctx := context.Background()
	a := &Entity{Name: "Alice", Type: "person"}
	b := &Entity{Name: "Bob", Type: "person"}
	for _, e := range []*Entity{a, b} {
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []*Relationship{
		{SourceID: ownerID, TargetID: a.ID, Type: RelKnows},
		{SourceID: ownerID, TargetID: b.ID, Type: RelKnows},
	} {
		if err := c.PutRelationship(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := c.neighbourhood(ctx, []string{a.ID}, 2, model.TemporalMode{})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{a.ID: 0, ownerID: 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("neighbourhood = %v, want %v (Bob is only reachable through the owner)", got, want)
	}
}
