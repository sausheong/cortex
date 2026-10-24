package integration_test

import (
	"context"
	. "github.com/sausheong/cortex"
	"testing"
)

// The bulk loaders must return what the per-entity calls return, so the
// vault build can switch between them without changing its output.
func TestBulkLoadersMatchPerEntity(t *testing.T) { forEachMode(t, testBulkLoadersMatchPerEntity) }

func testBulkLoadersMatchPerEntity(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	alice := &Entity{Type: "person", Name: "Alice"}
	bob := &Entity{Type: "person", Name: "Bob"}
	acme := &Entity{Type: "organization", Name: "Acme"}
	for _, e := range []*Entity{alice, bob, acme} {
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []*Memory{
		{Content: "Alice and Bob met", EntityIDs: []string{alice.ID, bob.ID}},
		{Content: "Alice works at Acme", EntityIDs: []string{alice.ID, acme.ID}},
		{Content: "Bob likes tea", EntityIDs: []string{bob.ID}},
	} {
		if err := c.PutMemory(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []*Relationship{
		{SourceID: alice.ID, TargetID: acme.ID, Type: "works_at"},
		{SourceID: alice.ID, TargetID: bob.ID, Type: "knows"},
	} {
		if err := c.PutRelationship(ctx, r); err != nil {
			t.Fatal(err)
		}
	}

	byEntity, err := c.MemoriesByEntityAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	allRels, err := c.AllRelationships(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(allRels) != 2 {
		t.Fatalf("AllRelationships = %d, want 2", len(allRels))
	}

	for _, e := range []*Entity{alice, bob, acme} {
		want, err := c.GetMemoriesByEntity(ctx, e.ID)
		if err != nil {
			t.Fatal(err)
		}
		got := byEntity[e.ID]
		if ids(got) != ids(want) {
			t.Errorf("%s memories: bulk %s, per-entity %s", e.Name, ids(got), ids(want))
		}
		for _, m := range got {
			if len(m.EntityIDs) == 0 {
				t.Errorf("%s: memory %q has no EntityIDs", e.Name, m.Content)
			}
		}

		wantRels, err := c.GetRelationships(ctx, e.ID)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, r := range allRels {
			if r.SourceID == e.ID || r.TargetID == e.ID {
				n++
			}
		}
		if n != len(wantRels) {
			t.Errorf("%s relationships: bulk %d, per-entity %d", e.Name, n, len(wantRels))
		}
	}
}
