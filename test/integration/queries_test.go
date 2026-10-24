package integration_test

import (
	"context"
	"testing"
	"time"

	. "github.com/sausheong/cortex"
)

func TestWhoIs_ResolvesAliasAndDescribes(t *testing.T) {
	forEachMode(t, testWhoIs_ResolvesAliasAndDescribes)
}

func testWhoIs_ResolvesAliasAndDescribes(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	carol := &Entity{Name: "Carol Seah", Type: "person"}
	acme := &Entity{Name: "WYNNES", Type: "organization"}
	for _, e := range []*Entity{carol, acme} {
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.AddAlias(ctx, carol.ID, "", "carol@wynnes.example"); err != nil {
		t.Fatal(err)
	}
	if err := c.PutRelationship(ctx, &Relationship{SourceID: carol.ID, TargetID: acme.ID, Type: RelWorksAt}); err != nil {
		t.Fatal(err)
	}
	if err := c.PutMemory(ctx, &Memory{Content: "Carol prefers mornings", EntityIDs: []string{carol.ID}, Static: true}); err != nil {
		t.Fatal(err)
	}
	card, err := c.WhoIs(ctx, "Carol@Wynnes.example")
	if err != nil {
		t.Fatal(err)
	}
	if card.Entity.ID != carol.ID || len(card.Aliases) != 1 || len(card.Memories) != 1 {
		t.Fatalf("card = %+v", card)
	}
	if len(card.Relationships) != 1 || card.Relationships[0].String() != "Carol Seah works_at WYNNES" {
		t.Errorf("relationships = %+v", card.Relationships)
	}
	miss, err := c.WhoIs(ctx, "Carl Seah")
	if err != nil {
		t.Fatal(err)
	}
	if miss.Entity.ID != "" && miss.Entity.ID != carol.ID {
		t.Errorf("unexpected match %+v", miss.Entity)
	}
}

func TestPath_ShortestChainAvoidingOwner(t *testing.T) {
	c, ownerID := ownerCortex(t, "Sau Sheong", "s@x.com")
	ctx := context.Background()
	ids := map[string]string{}
	for _, n := range []string{"Ann", "Bob", "Cat", "Dan"} {
		e := &Entity{Name: n, Type: "person"}
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
		ids[n] = e.ID
	}
	link := func(a, b string) {
		if err := c.PutRelationship(ctx, &Relationship{SourceID: a, TargetID: b, Type: RelKnows}); err != nil {
			t.Fatal(err)
		}
	}
	link(ids["Ann"], ids["Bob"])
	link(ids["Cat"], ids["Bob"]) // reversed direction still connects
	link(ownerID, ids["Ann"])
	link(ownerID, ids["Dan"])

	path, err := c.Path(ctx, ids["Ann"], ids["Cat"], 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(path) != 2 || path[0].String() != "Ann knows Bob" || path[1].String() != "Cat knows Bob" {
		t.Fatalf("path = %v", path)
	}
	// Ann and Dan only connect through the owner, which is not a stepping stone.
	if p, err := c.Path(ctx, ids["Ann"], ids["Dan"], 0); err != nil || p != nil {
		t.Errorf("Ann→Dan = %v %v, want not connected", p, err)
	}
	// The owner as an end is fine.
	if p, err := c.Path(ctx, ownerID, ids["Bob"], 0); err != nil || len(p) != 2 {
		t.Errorf("owner→Bob = %v %v, want 2 hops", p, err)
	}
}

func TestTimelineAndWhatChanged(t *testing.T) { forEachMode(t, testTimelineAndWhatChanged) }

func testTimelineAndWhatChanged(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	start := time.Now().UTC().Add(-time.Second)
	ann := &Entity{Name: "Ann", Type: "person"}
	ldn := &Entity{Name: "London", Type: "place"}
	sg := &Entity{Name: "Singapore", Type: "place"}
	for _, e := range []*Entity{ann, ldn, sg} {
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	jan := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	jun := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	if err := c.PutRelationship(ctx, &Relationship{SourceID: ann.ID, TargetID: ldn.ID, Type: RelLivesIn, ValidAt: &jan}); err != nil {
		t.Fatal(err)
	}
	if err := c.PutRelationship(ctx, &Relationship{SourceID: ann.ID, TargetID: sg.ID, Type: RelLivesIn, ValidAt: &jun}); err != nil {
		t.Fatal(err)
	}
	if err := c.PutMemory(ctx, &Memory{Content: "Ann started a new job", EntityIDs: []string{ann.ID}, ValidAt: &jun}); err != nil {
		t.Fatal(err)
	}

	tl, err := c.Timeline(ctx, ann.ID, time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	var kinds []string
	for _, ev := range tl {
		kinds = append(kinds, ev.Kind+":"+ev.Text)
	}
	want := []string{
		"relationship_started:Ann lives_in London",
		"relationship_ended:Ann lives_in London",
	}
	if len(tl) != 4 || kinds[0] != want[0] || kinds[1] != want[1] {
		t.Fatalf("timeline = %v", kinds)
	}

	// End Singapore now; London ended in June 2025, before start.
	cur, _ := c.GetRelationships(ctx, ann.ID)
	if err := c.InvalidateRelationship(ctx, cur[0].ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	ch, err := c.WhatChanged(ctx, start, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.NewEntities) != 3 || len(ch.NewMemories) != 1 || len(ch.NewRelationships) != 2 || len(ch.EndedRelationships) != 1 {
		t.Errorf("changes = %d entities, %d memories, %d new rels, %d ended",
			len(ch.NewEntities), len(ch.NewMemories), len(ch.NewRelationships), len(ch.EndedRelationships))
	}
}

func TestLink_ResolvesOrCreatesEnds(t *testing.T) { forEachMode(t, testLink_ResolvesOrCreatesEnds) }

func testLink_ResolvesOrCreatesEnds(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	acme := &Entity{Name: "Acme Corp", Type: "organization"}
	if err := c.PutEntity(ctx, acme); err != nil {
		t.Fatal(err)
	}
	e, err := c.Link(ctx, LinkRequest{Source: "Acme Corp", Type: "employs", Target: "ann@acme.example", Provenance: "gmail"})
	if err != nil {
		t.Fatal(err)
	}
	if e.Type != RelWorksAt || e.TargetID != acme.ID || e.SourceName != "ann@acme.example" {
		t.Fatalf("edge = %+v, want ann works_at Acme", e)
	}
	// The email became an alias of the new person, so it resolves.
	res, err := c.ResolveEntity(ctx, "ann@acme.example", "")
	if err != nil || res.Entity == nil || res.Entity.ID != e.SourceID {
		t.Errorf("resolve email = %+v %v", res, err)
	}
	// Linking again strengthens the same edge.
	again, err := c.Link(ctx, LinkRequest{Source: e.SourceID, Type: "works_at", Target: "acme corp"})
	if err != nil || again.ID != e.ID || again.EvidenceCount != 2 {
		t.Errorf("second link = %+v %v", again, err)
	}
}
