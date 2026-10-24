package integration_test

import (
	"context"
	. "github.com/sausheong/cortex"
	"testing"
)

func TestRemember_MapsOntoVocabulary(t *testing.T) { forEachMode(t, testRemember_MapsOntoVocabulary) }

func testRemember_MapsOntoVocabulary(t *testing.T) {
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

func TestRemember_ConceptNamesakeIsReused(t *testing.T) {
	forEachMode(t, testRemember_ConceptNamesakeIsReused)
}

func testRemember_ConceptNamesakeIsReused(t *testing.T) {
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
