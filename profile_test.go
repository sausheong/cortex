package cortex

import (
	"context"
	"testing"
)

func TestTrackProfile_SetsMarker(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "Alice"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := c.TrackProfile(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetEntity(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := got.Attributes[attrProfiled].(bool); !v {
		t.Errorf("expected _profiled=true, attrs=%v", got.Attributes)
	}
}

func TestUntrackProfile_RemovesMarkerAndCache(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "Bob", Attributes: map[string]any{
		attrProfiled: true,
		attrProfile:  map[string]any{"static": []string{"x"}},
	}}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := c.UntrackProfile(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetEntity(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Attributes[attrProfiled]; ok {
		t.Error("expected _profiled removed")
	}
	if _, ok := got.Attributes[attrProfile]; ok {
		t.Error("expected _profile cache removed")
	}
}

func TestProfileEligibleIDs_OwnerAndTracked(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	owner := &Entity{Type: "person", Name: "Me", Source: "owner"}
	if err := c.PutEntity(ctx, owner); err != nil {
		t.Fatal(err)
	}
	tracked := &Entity{Type: "person", Name: "Alice"}
	if err := c.PutEntity(ctx, tracked); err != nil {
		t.Fatal(err)
	}
	if err := c.TrackProfile(ctx, tracked.ID); err != nil {
		t.Fatal(err)
	}
	untracked := &Entity{Type: "person", Name: "Carol"}
	if err := c.PutEntity(ctx, untracked); err != nil {
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
	if !set[owner.ID] || !set[tracked.ID] {
		t.Errorf("expected owner+tracked eligible, got %v", ids)
	}
	if set[untracked.ID] {
		t.Errorf("untracked entity should not be eligible, got %v", ids)
	}
}
