package cortex

import (
	"context"
	"sort"
	"testing"
)

func TestParseExtractionJSON_AboutAndAttributes(t *testing.T) {
	raw := "```json\n" + `{
	  "entities": [{"name": "Alice", "type": "Person", "attributes": {"role": "engineer"}, "confidence": 0.9}],
	  "relationships": [{"source": "Alice", "target": "Acme", "type": "works_at", "attributes": {"since": "2024"}}],
	  "memories": [{"content": "Alice is an engineer", "about": ["Alice"]}, "a bare string memory"]
	}` + "\n```"
	ex, err := ParseExtractionJSON(raw)
	if err != nil {
		t.Fatalf("ParseExtractionJSON: %v", err)
	}
	if ex.Entities[0].Attributes["role"] != "engineer" {
		t.Errorf("entity attributes dropped: %v", ex.Entities[0].Attributes)
	}
	if ex.Relationships[0].Attributes["since"] != "2024" {
		t.Errorf("relationship attributes dropped: %v", ex.Relationships[0].Attributes)
	}
	if len(ex.Memories) != 2 || len(ex.Memories[0].About) != 1 || ex.Memories[0].About[0] != "Alice" {
		t.Errorf("memories = %+v, want about=[Alice] plus a bare-string memory", ex.Memories)
	}
}

func TestPutRelationship_UpsertsAndCountsEvidence(t *testing.T) {
	forEachMode(t, testPutRelationship_UpsertsAndCountsEvidence)
}

func testPutRelationship_UpsertsAndCountsEvidence(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	a := &Entity{Name: "Alice", Type: "person"}
	b := &Entity{Name: "Acme", Type: "organization"}
	for _, e := range []*Entity{a, b} {
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	r1 := &Relationship{SourceID: a.ID, TargetID: b.ID, Type: "Works At", Confidence: 0.6, Attributes: map[string]any{"since": "2024"}}
	if err := c.PutRelationship(ctx, r1); err != nil {
		t.Fatal(err)
	}
	r2 := &Relationship{SourceID: a.ID, TargetID: b.ID, Type: "works_at", Confidence: 0.9, Attributes: map[string]any{"title": "eng"}}
	if err := c.PutRelationship(ctx, r2); err != nil {
		t.Fatal(err)
	}
	if r1.ID != r2.ID {
		t.Fatalf("repeat edge got a new id: %s vs %s", r1.ID, r2.ID)
	}

	rels, err := c.GetRelationships(ctx, a.ID, RelTypeFilter("WORKS AT"))
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 {
		t.Fatalf("got %d relationships, want 1", len(rels))
	}
	got := rels[0]
	if got.Type != "works_at" || got.EvidenceCount != 2 || got.Confidence != 0.9 {
		t.Errorf("got type=%q evidence=%d conf=%v, want works_at/2/0.9", got.Type, got.EvidenceCount, got.Confidence)
	}
	if got.Attributes["since"] != "2024" || got.Attributes["title"] != "eng" {
		t.Errorf("attributes not merged: %v", got.Attributes)
	}
}

func TestRestoreBatch_FoldsRelationshipTwin(t *testing.T) {
	forEachMode(t, testRestoreBatch_FoldsRelationshipTwin)
}

func testRestoreBatch_FoldsRelationshipTwin(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	a := &Entity{Name: "Alice", Type: "person"}
	b := &Entity{Name: "Acme", Type: "organization"}
	for _, e := range []*Entity{a, b} {
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.PutRelationship(ctx, &Relationship{SourceID: a.ID, TargetID: b.ID, Type: "works_at"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.h.DB.ExecContext(ctx,
		`INSERT INTO delete_batches (id, user_id, kind) VALUES ('b1', ?, 'forget')`, c.h.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := c.h.DB.ExecContext(ctx,
		`UPDATE relationships SET deleted_at = now(), delete_batch_id = 'b1' WHERE user_id = ?`, c.h.UserID); err != nil {
		t.Fatal(err)
	}
	// Re-assert the edge while its twin sits in the batch.
	if err := c.PutRelationship(ctx, &Relationship{SourceID: a.ID, TargetID: b.ID, Type: "works_at"}); err != nil {
		t.Fatal(err)
	}
	if err := c.RestoreBatch(ctx, "b1"); err != nil {
		t.Fatalf("RestoreBatch: %v", err)
	}
	rels, err := c.GetRelationships(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 || rels[0].EvidenceCount != 2 {
		t.Fatalf("got %+v, want one live edge with evidence 2", rels)
	}
}

func TestRemember_LinksMemoriesToTheirOwnEntities(t *testing.T) {
	forEachMode(t, testRemember_LinksMemoriesToTheirOwnEntities)
}

func testRemember_LinksMemoriesToTheirOwnEntities(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	c.SetExtractor(&mockExtractor{
		extractFn: func(_ context.Context, _, _ string) (*Extraction, error) {
			return &Extraction{
				Entities: []Entity{
					{Name: "Carol Seah", Type: "Person"},
					{Name: "WYNNES", Type: "organization"},
					{Name: "Collin Ng", Type: "person"},
				},
				Memories: []Memory{
					{Content: "Carol Seah is associated with WYNNES", About: []string{"carol seah", "WYNNES"}},
					{Content: "Collin Ng sent a connection invitation", About: []string{"Collin Ng"}},
					{Content: "Three invitations arrived"}, // no about: falls back to all
				},
			}, nil
		},
	})
	if err := c.Remember(ctx, "inbox digest"); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	carol, err := c.FindEntities(ctx, EntityFilter{NameLike: "Carol Seah", Type: "PERSON"})
	if err != nil || len(carol) != 1 {
		t.Fatalf("find Carol: %v %v", carol, err)
	}
	if carol[0].Type != "person" {
		t.Errorf("type stored as %q, want person", carol[0].Type)
	}
	mems, err := c.GetMemoriesByEntity(ctx, carol[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range mems {
		got = append(got, m.Content)
	}
	sort.Strings(got)
	want := []string{"Carol Seah is associated with WYNNES", "Three invitations arrived"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Carol's memories = %v, want %v", got, want)
	}
}

func TestRemember_ReusesEntityAcrossCaseAndKeepsAttributes(t *testing.T) {
	forEachMode(t, testRemember_ReusesEntityAcrossCaseAndKeepsAttributes)
}

func testRemember_ReusesEntityAcrossCaseAndKeepsAttributes(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	stored := &Entity{Name: "Sau Sheong", Type: "Person", Attributes: map[string]any{"profile_cache": "x"}}
	if err := c.PutEntity(ctx, stored); err != nil {
		t.Fatal(err)
	}
	c.SetExtractor(&mockExtractor{
		extractFn: func(_ context.Context, _, _ string) (*Extraction, error) {
			return &Extraction{
				Entities: []Entity{{Name: "sau sheong", Type: "person", Attributes: map[string]any{"city": "Singapore", "profile_cache": "clobbered"}}},
				Memories: []Memory{{Content: "Sau Sheong lives in Singapore", About: []string{"sau sheong"}}},
			}, nil
		},
	})
	if err := c.Remember(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	all, err := c.FindEntities(ctx, EntityFilter{Type: "person"})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].ID != stored.ID {
		t.Fatalf("got %d person entities (%v), want the one stored entity reused", len(all), all)
	}
	if all[0].Attributes["profile_cache"] != "x" || all[0].Attributes["city"] != "Singapore" {
		t.Errorf("attributes = %v, want stored value kept and city added", all[0].Attributes)
	}
	mems, err := c.GetMemoriesByEntity(ctx, stored.ID)
	if err != nil || len(mems) != 1 {
		t.Fatalf("memories on reused entity: %v %v", mems, err)
	}
}
