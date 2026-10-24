package cortex

import (
	"context"
	"testing"
)

func TestOwnerProtectionUsesIsOwner(t *testing.T) {
	c, ownerID := ownerCortex(t, "Flag Owner", "flag@owner.test")
	ctx := context.Background()
	if err := c.PutEntity(ctx, &Entity{ID: ownerID, Type: "person", Name: "Flag Owner", Source: "ingest"}); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetEntity(ctx, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "owner" {
		t.Fatalf("owner source overwritten: %q", got.Source)
	}
	// Upsert by (name,type) must also not overwrite.
	if err := c.PutEntity(ctx, &Entity{Type: "person", Name: "Flag Owner", Source: "ingest"}); err != nil {
		t.Fatal(err)
	}
	got, _ = c.GetEntity(ctx, ownerID)
	if got.Source != "owner" {
		t.Fatalf("owner source overwritten by upsert: %q", got.Source)
	}
	// A non-owner entity with source 'owner' text is NOT protected.
	e := &Entity{Type: "person", Name: "Fake", Source: "owner"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	e2 := &Entity{ID: e.ID, Type: "person", Name: "Fake", Source: "ingest"}
	if err := c.PutEntity(ctx, e2); err != nil {
		t.Fatal(err)
	}
	got, _ = c.GetEntity(ctx, e.ID)
	if got.Source != "ingest" {
		t.Fatalf("non-owner protected by source text: %q", got.Source)
	}
}

func TestOwnerLocatedViaOwnerEntityIDNotSource(t *testing.T) {
	c, ownerID := ownerCortex(t, "Locate Me", "loc@owner.test")
	ctx := context.Background()
	got, err := c.OwnerEntityID(ctx)
	if err != nil || got != ownerID {
		t.Fatalf("OwnerEntityID = %q, %v", got, err)
	}
	// Owner-name resolution still works with a non-'owner' source.
	res, err := c.ResolveEntity(ctx, "Locate Me", "person")
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || res.Entity == nil || res.Entity.ID != ownerID {
		t.Fatalf("resolve = %+v", res)
	}
}

func TestNewUserOwnerEntityIsOwner(t *testing.T) {
	c, ownerID := ownerCortex(t, "New User", "new@owner.test")
	var isOwner bool
	if err := c.h.DB.QueryRowContext(context.Background(), `SELECT is_owner FROM entities WHERE id = ?`, ownerID).Scan(&isOwner); err != nil {
		t.Fatal(err)
	}
	if !isOwner {
		t.Fatal("owner entity not flagged is_owner")
	}
}

func TestProfileEligibleIncludesOwnerByFlag(t *testing.T) {
	c, ownerID := ownerCortex(t, "Profile Owner", "prof@owner.test")
	ctx := context.Background()
	other := &Entity{Type: "person", Name: "Textual", Source: "owner"}
	if err := c.PutEntity(ctx, other); err != nil {
		t.Fatal(err)
	}
	ids, err := c.profileEligibleIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	if !set[ownerID] {
		t.Fatalf("owner by flag missing: %v", ids)
	}
	if set[other.ID] {
		t.Fatalf("source-text owner wrongly eligible: %v", ids)
	}
}
