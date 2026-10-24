package cortex

import (
	"context"
	"errors"
	"testing"

	"github.com/sausheong/cortex/internal/tenancy"
)

func TestAddAlias_ConflictAndResolve(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	a := &Entity{Name: "Sau Sheong", Type: "person"}
	b := &Entity{Name: "Bob", Type: "person"}
	for _, e := range []*Entity{a, b} {
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.AddAlias(ctx, a.ID, "", "SauSheong@gmail.com"); err != nil {
		t.Fatal(err)
	}
	if err := c.AddAlias(ctx, a.ID, "", "sausheong@gmail.com"); err != nil {
		t.Errorf("re-adding own alias: %v", err)
	}
	if err := c.AddAlias(ctx, b.ID, "", "sausheong@gmail.com"); !errors.Is(err, ErrAliasTaken) {
		t.Errorf("alias on second entity: err = %v, want ErrAliasTaken", err)
	}
	res, err := c.ResolveEntity(ctx, "SAUSHEONG@gmail.com", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Entity == nil || res.Entity.ID != a.ID || res.Method != ResolvedByAlias {
		t.Errorf("resolve email = %+v, want alias match on %s", res, a.ID)
	}
	al, err := c.Aliases(ctx, a.ID)
	if err != nil || len(al) != 1 || al[0].Kind != AliasEmail {
		t.Errorf("Aliases = %v %v", al, err)
	}
}

// ownerCortex returns a handle scoped to a fresh user whose owner entity is
// named name.
func ownerCortex(t *testing.T, name, email string) (*Cortex, string) {
	t.Helper()
	base := openTestDB(t)
	u, err := base.CreateUser(context.Background(), "", email, name, true)
	if err != nil {
		t.Fatal(err)
	}
	c := base.ForUser(u.ID)
	ownerID, err := tenancy.New(c.h).OwnerEntityID(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return c, ownerID
}

func TestRemember_AnchorsOwnerAndLearnsAliases(t *testing.T) {
	c, ownerID := ownerCortex(t, "Sau Sheong", "sausheong@gmail.com")
	ctx := context.Background()
	c.SetExtractor(&mockExtractor{
		extractFn: func(_ context.Context, _, _ string) (*Extraction, error) {
			return &Extraction{
				Entities: []Entity{
					{Name: "Sau Sheong", Type: "Person"},
					{Name: "me", Type: "person"},
					{Name: "Chang Sau Sheong", Type: "person", Attributes: map[string]any{"phone": "+65 9123 4567"}},
				},
				Memories: []Memory{{Content: "I like tea", About: []string{"me"}}},
			}, nil
		},
	})
	if err := c.Remember(ctx, "I like tea", WithSpeaker("user")); err != nil {
		t.Fatal(err)
	}
	people, err := c.FindEntities(ctx, EntityFilter{Type: "person"})
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 1 || people[0].ID != ownerID {
		t.Fatalf("people = %+v, want only the owner %s", people, ownerID)
	}
	if people[0].Source != "owner" {
		t.Errorf("owner source overwritten: %q", people[0].Source)
	}
	mems, err := c.GetMemoriesByEntity(ctx, ownerID)
	if err != nil || len(mems) != 1 {
		t.Fatalf("owner memories = %v %v", mems, err)
	}
	// The fuzzy-matched full name and the phone number are now aliases.
	for _, m := range []string{"chang sau sheong", "+6591234567"} {
		res, err := c.ResolveEntity(ctx, m, "person")
		if err != nil || res.Entity == nil || res.Entity.ID != ownerID || res.Method != ResolvedByAlias {
			t.Errorf("resolve %q = %+v %v, want alias match on owner", m, res, err)
		}
	}
}

// stubDisambiguator is a mockLLM that also implements EntityDisambiguator.
type stubDisambiguator struct {
	mockLLM
	choose func(EntityMention, []Entity) EntityChoice
	calls  int
}

func (s *stubDisambiguator) DisambiguateEntity(_ context.Context, m EntityMention, cands []Entity) (EntityChoice, error) {
	s.calls++
	return s.choose(m, cands), nil
}

func TestRemember_AsksDisambiguatorWhenUnsure(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	stored := &Entity{Name: "Apollo Platform", Type: "project"}
	if err := c.PutEntity(ctx, stored); err != nil {
		t.Fatal(err)
	}
	dis := &stubDisambiguator{choose: func(m EntityMention, cands []Entity) EntityChoice {
		if m.Context == "" {
			t.Error("disambiguator got no context")
		}
		return EntityChoice{ID: cands[0].ID, Confidence: 0.9}
	}}
	c.SetLLM(dis)
	c.SetExtractor(&mockExtractor{
		extractFn: func(_ context.Context, _, _ string) (*Extraction, error) {
			return &Extraction{Entities: []Entity{{Name: "Apollo", Type: "project"}}}, nil
		},
	})
	if err := c.Remember(ctx, "Apollo shipped"); err != nil {
		t.Fatal(err)
	}
	projects, _ := c.FindEntities(ctx, EntityFilter{Type: "project"})
	if dis.calls != 1 || len(projects) != 1 {
		t.Fatalf("calls=%d projects=%d, want the disambiguator asked once and no new project", dis.calls, len(projects))
	}
}

func TestMerge_MovesAliasesAndUndoRestoresThem(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	keep := &Entity{Name: "Sau Sheong", Type: "person"}
	drop := &Entity{Name: "S. S. Chang", Type: "person"}
	for _, e := range []*Entity{keep, drop} {
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.AddAlias(ctx, drop.ID, AliasEmail, "ss@x.com"); err != nil {
		t.Fatal(err)
	}
	stats, err := c.MergeEntities(ctx, keep.ID, drop.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Aliases != 1 {
		t.Errorf("stats.Aliases = %d, want 1", stats.Aliases)
	}
	for _, m := range []string{"ss@x.com", "s. s. chang"} {
		res, _ := c.ResolveEntity(ctx, m, "person")
		if res.Entity == nil || res.Entity.ID != keep.ID {
			t.Errorf("after merge %q resolves to %+v, want keep", m, res.Entity)
		}
	}
	if err := c.RestoreBatch(ctx, stats.BatchID); err != nil {
		t.Fatal(err)
	}
	res, _ := c.ResolveEntity(ctx, "ss@x.com", "")
	if res.Entity == nil || res.Entity.ID != drop.ID {
		t.Errorf("after undo email resolves to %+v, want drop", res.Entity)
	}
	if al, _ := c.Aliases(ctx, keep.ID); len(al) != 0 {
		t.Errorf("keep still has aliases after undo: %v", al)
	}
}

func TestRemember_FirstPersonIsNotOwnerInSomeoneElsesText(t *testing.T) {
	c, ownerID := ownerCortex(t, "Sau Sheong", "sausheong@gmail.com")
	ctx := context.Background()
	c.SetExtractor(&mockExtractor{
		extractFn: func(_ context.Context, _, _ string) (*Extraction, error) {
			return &Extraction{
				Entities: []Entity{{Name: "me", Type: "person"}, {Name: "the user", Type: "person"}},
				Memories: []Memory{{Content: "The sender wants to connect", About: []string{"me"}}},
			}, nil
		},
	})
	// An email ingested by an agent: no owner speaker.
	if err := c.Remember(ctx, "Hi, I'd like to connect", WithSource("gmail")); err != nil {
		t.Fatal(err)
	}
	mems, err := c.GetMemoriesByEntity(ctx, ownerID)
	if err != nil {
		t.Fatal(err)
	}
	if len(mems) != 0 {
		t.Errorf("owner picked up %d memories from someone else's first person", len(mems))
	}
	res, _ := c.ResolveEntity(ctx, "the user", "person")
	if res.Entity == nil || res.Entity.ID != ownerID {
		t.Errorf("\"the user\" should still resolve to the owner, got %+v", res.Entity)
	}
}
