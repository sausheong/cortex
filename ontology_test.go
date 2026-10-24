package cortex

import (
	"context"
	"testing"
)

func TestRemember_MapsOntoVocabulary(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	c.SetExtractor(&mockExtractor{
		extractFn: func(_ context.Context, _, _ string) (*Extraction, error) {
			return &Extraction{
				Entities: []Entity{
					{Name: "Alice", Type: "Human"},
					{Name: "Acme", Type: "company"},
					{Name: "Quantum Foam", Type: "phenomenon"},
				},
				Relationships: []Relationship{
					{SourceID: "Acme", TargetID: "Alice", Type: "employs"},
					{SourceID: "Alice", TargetID: "Quantum Foam", Type: "obsessed_with"},
				},
			}, nil
		},
	})
	if err := c.Remember(ctx, "x"); err != nil {
		t.Fatal(err)
	}
	alice, _ := c.FindEntities(ctx, EntityFilter{NameLike: "Alice"})
	acme, _ := c.FindEntities(ctx, EntityFilter{NameLike: "Acme"})
	foam, _ := c.FindEntities(ctx, EntityFilter{NameLike: "Quantum Foam"})
	if alice[0].Type != TypePerson || acme[0].Type != TypeOrganization || foam[0].Type != TypeConcept {
		t.Fatalf("types = %s/%s/%s", alice[0].Type, acme[0].Type, foam[0].Type)
	}
	if foam[0].Attributes["type_label"] != "phenomenon" {
		t.Errorf("type_label = %v", foam[0].Attributes["type_label"])
	}
	rels, _ := c.GetRelationships(ctx, alice[0].ID)
	byType := map[string]Relationship{}
	for _, r := range rels {
		byType[r.Type] = r
	}
	if w, ok := byType[RelWorksAt]; !ok || w.SourceID != alice[0].ID || w.TargetID != acme[0].ID {
		t.Errorf("employs not stored as Alice works_at Acme: %+v", rels)
	}
	if r, ok := byType[RelRelatedTo]; !ok || r.Attributes["label"] != "obsessed_with" {
		t.Errorf("unknown verb not kept as related_to with label: %+v", rels)
	}
}

func TestNormalizeGraph_RetypesMergesAndUndoes(t *testing.T) {
	c, ownerID := ownerCortex(t, "Sau Sheong", "sausheong@gmail.com")
	ctx := context.Background()
	put := func(name, typ string) string {
		e := &Entity{Name: name, Type: "concept"}
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
		// Write the legacy type label directly, as pre-normalisation rows have.
		if _, err := c.h.DB.ExecContext(ctx, `UPDATE entities SET type = ? WHERE id = ?`, typ, e.ID); err != nil {
			t.Fatal(err)
		}
		return e.ID
	}
	dupOwner := put("Sau Sheong", "Person")
	merchant := put("FairPrice", "merchant")
	org := put("fairprice", "organization")
	group := put("SNDG + JT", "whatsapp group")
	odd := put("Gizmo", "widget")
	for _, r := range []Relationship{
		{SourceID: dupOwner, TargetID: group, Type: "belongs_to"},
		{SourceID: dupOwner, TargetID: merchant, Type: "spent_at"},
		{SourceID: org, TargetID: dupOwner, Type: "sent_notice_about"},
	} {
		r := r
		if err := c.PutRelationship(ctx, &r); err != nil {
			t.Fatal(err)
		}
	}
	m := &Memory{Content: "Sau Sheong shops at FairPrice", EntityIDs: []string{dupOwner, merchant}}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}

	dry, err := c.NormalizeGraph(ctx, WithNormalizeDryRun())
	if err != nil {
		t.Fatal(err)
	}
	if len(dry.Merged) != 2 {
		t.Fatalf("dry run merged %d, want 2 (owner twin, FairPrice pair): %+v", len(dry.Merged), dry.Merged)
	}
	if e, _ := c.GetEntity(ctx, merchant); e == nil || e.Type != "merchant" {
		t.Fatalf("dry run wrote changes: %+v", e)
	}

	rep, err := c.NormalizeGraph(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetEntity(ctx, dupOwner); err == nil {
		t.Error("owner twin still live")
	}
	mems, _ := c.GetMemoriesByEntity(ctx, ownerID)
	if len(mems) != 1 {
		t.Errorf("owner has %d memories, want the twin's 1", len(mems))
	}
	g, _ := c.GetEntity(ctx, group)
	o, _ := c.GetEntity(ctx, odd)
	if g.Type != TypeGroup || o.Type != TypeConcept || o.Attributes["type_label"] != "widget" {
		t.Errorf("retype: group=%s odd=%s/%v", g.Type, o.Type, o.Attributes["type_label"])
	}
	rels, _ := c.GetRelationships(ctx, ownerID)
	types := map[string]bool{}
	for _, r := range rels {
		types[r.Type] = true
	}
	if !types[RelMemberOf] || !types[RelPaid] {
		t.Errorf("owner's edges after normalise = %+v", rels)
	}

	if err := c.RestoreBatch(ctx, rep.BatchID); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if e, err := c.GetEntity(ctx, merchant); err != nil || e.Type != "merchant" {
		t.Errorf("after undo merchant = %+v %v", e, err)
	}
	if e, err := c.GetEntity(ctx, dupOwner); err != nil || e.Type != "Person" {
		t.Errorf("after undo owner twin = %+v %v", e, err)
	}
	if rels, _ := c.GetRelationships(ctx, dupOwner); len(rels) != 3 {
		t.Errorf("after undo twin has %d edges, want 3: %+v", len(rels), rels)
	}
}

func TestNormalizeGraph_FoldsConceptIntoSpecificNamesake(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	put := func(name, typ string) string {
		e := &Entity{Name: name, Type: "placeholder_" + typ} // distinct rows per type
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
		if _, err := c.h.DB.ExecContext(ctx, `UPDATE entities SET type = ? WHERE id = ?`, typ, e.ID); err != nil {
			t.Fatal(err)
		}
		return e.ID
	}
	loose := put("FileSG", "concept")
	sys := put("FileSG", "System")
	// Two specific namesakes keep a concept namesake apart.
	put("Mercury", "concept")
	put("Mercury", "place")
	put("Mercury", "product")
	if err := c.PutMemory(ctx, &Memory{Content: "FileSG stores documents", EntityIDs: []string{loose}}); err != nil {
		t.Fatal(err)
	}

	rep, err := c.NormalizeGraph(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Merged) != 1 {
		t.Fatalf("merged = %+v, want only the FileSG pair", rep.Merged)
	}
	survivor := rep.Merged[0].KeepID
	e, err := c.GetEntity(ctx, survivor)
	if err != nil || e.Type != TypeProduct {
		t.Fatalf("FileSG survivor = %+v %v, want typed product", e, err)
	}
	if survivor != loose && survivor != sys {
		t.Fatalf("unexpected survivor %s", survivor)
	}
	for _, r := range rep.Retyped {
		if r.ID == survivor && r.To != TypeProduct {
			t.Errorf("retyped report says %s → %s", r.From, r.To)
		}
	}
	mercury, _ := c.FindEntities(ctx, EntityFilter{NameLike: "Mercury"})
	if len(mercury) != 3 {
		t.Errorf("Mercury entities = %d, want 3 kept apart", len(mercury))
	}
}

func TestRemember_ConceptNamesakeIsReused(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	loose := &Entity{Name: "FileSG", Type: "concept"}
	if err := c.PutEntity(ctx, loose); err != nil {
		t.Fatal(err)
	}
	c.SetExtractor(&mockExtractor{extractFn: func(context.Context, string, string) (*Extraction, error) {
		return &Extraction{Entities: []Entity{{Name: "FileSG", Type: "software"}}}, nil
	}})
	if err := c.Remember(ctx, "FileSG"); err != nil {
		t.Fatal(err)
	}
	all, _ := c.FindEntities(ctx, EntityFilter{NameLike: "FileSG"})
	if len(all) != 1 || all[0].ID != loose.ID {
		t.Errorf("FileSG entities = %+v, want the concept one reused", all)
	}
}
