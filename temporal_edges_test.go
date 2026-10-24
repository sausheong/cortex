package cortex

import (
	"context"
	"testing"
	"time"
)

func TestPutRelationship_SingleValuedVerbClosesPrevious(t *testing.T) {
	forEachMode(t, testPutRelationship_SingleValuedVerbClosesPrevious)
}

func testPutRelationship_SingleValuedVerbClosesPrevious(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	ids := map[string]string{}
	for _, e := range []*Entity{{Name: "Ann", Type: "person"}, {Name: "London", Type: "place"}, {Name: "Singapore", Type: "place"}} {
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
		ids[e.Name] = e.ID
	}
	if err := c.PutRelationship(ctx, &Relationship{SourceID: ids["Ann"], TargetID: ids["London"], Type: RelLivesIn}); err != nil {
		t.Fatal(err)
	}
	before := time.Now().UTC()
	time.Sleep(10 * time.Millisecond)
	if err := c.PutRelationship(ctx, &Relationship{SourceID: ids["Ann"], TargetID: ids["Singapore"], Type: RelLivesIn}); err != nil {
		t.Fatal(err)
	}

	now, err := c.GetRelationships(ctx, ids["Ann"])
	if err != nil {
		t.Fatal(err)
	}
	if len(now) != 1 || now[0].TargetID != ids["Singapore"] {
		t.Fatalf("current lives_in = %+v, want only Singapore", now)
	}
	then, err := c.GetRelationships(ctx, ids["Ann"], RelAsOf(before))
	if err != nil {
		t.Fatal(err)
	}
	if len(then) != 1 || then[0].TargetID != ids["London"] {
		t.Fatalf("as-of lives_in = %+v, want London", then)
	}
	all, _ := c.GetRelationships(ctx, ids["Ann"], RelIncludeInvalid())
	if len(all) != 2 {
		t.Fatalf("history has %d edges, want 2", len(all))
	}

	// Moving back reopens the London edge and closes Singapore.
	if err := c.PutRelationship(ctx, &Relationship{SourceID: ids["Ann"], TargetID: ids["London"], Type: RelLivesIn}); err != nil {
		t.Fatal(err)
	}
	now, _ = c.GetRelationships(ctx, ids["Ann"])
	if len(now) != 1 || now[0].TargetID != ids["London"] || now[0].InvalidAt != nil {
		t.Fatalf("after moving back = %+v, want London reopened", now)
	}
}

func TestInvalidateRelationship_HidesFromTraverse(t *testing.T) {
	forEachMode(t, testInvalidateRelationship_HidesFromTraverse)
}

func testInvalidateRelationship_HidesFromTraverse(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	a := &Entity{Name: "Ann", Type: "person"}
	b := &Entity{Name: "Acme", Type: "organization"}
	for _, e := range []*Entity{a, b} {
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	r := &Relationship{SourceID: a.ID, TargetID: b.ID, Type: RelWorksAt}
	if err := c.PutRelationship(ctx, r); err != nil {
		t.Fatal(err)
	}
	if err := c.InvalidateRelationship(ctx, r.ID, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	g, err := c.Traverse(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Relationships) != 0 {
		t.Errorf("traverse saw an ended edge: %+v", g.Relationships)
	}
	g, _ = c.Traverse(ctx, a.ID, WithTraverseIncludeInvalid())
	if len(g.Relationships) != 1 {
		t.Errorf("traverse with history saw %d edges, want 1", len(g.Relationships))
	}
}

func TestRecall_TrackAccessAndDecayClock(t *testing.T) {
	forEachMode(t, testRecall_TrackAccessAndDecayClock)
}

func testRecall_TrackAccessAndDecayClock(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	m := &Memory{Content: "Ann drinks oolong"}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	c.SetLLM(&mockLLM{decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
		return []StructuredQuery{{Type: "memory_lookup", Params: map[string]any{"query": q}}}, nil
	}})
	// Untracked recall writes nothing.
	if _, err := c.Recall(ctx, "oolong"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := c.h.DB.QueryRowContext(ctx, `SELECT access_count FROM memories WHERE id = ?`, m.ID).Scan(&n); err != nil || n != 0 {
		t.Fatalf("access_count after untracked recall = %d %v", n, err)
	}
	if _, err := c.Recall(ctx, "oolong", WithTrackAccess()); err != nil {
		t.Fatal(err)
	}
	if err := c.h.DB.QueryRowContext(ctx, `SELECT access_count FROM memories WHERE id = ?`, m.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("access_count after tracked recall = %d %v", n, err)
	}

	// Pretend the memory is a year old but was used just now: decay should
	// barely touch it.
	if _, err := c.h.DB.ExecContext(ctx,
		`UPDATE memories SET created_at = now() - interval '365 days' WHERE id = ?`, m.ID); err != nil {
		t.Fatal(err)
	}
	rep, err := c.DecayConfidence(ctx, WithDecayDryRun())
	if err != nil {
		t.Fatal(err)
	}
	for _, ch := range rep.Changes {
		if ch.ID == m.ID && ch.NewConfidence < 0.99 {
			t.Errorf("used memory decayed to %.3f; use should restart the clock", ch.NewConfidence)
		}
	}
}
