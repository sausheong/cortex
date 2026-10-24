package graph

import (
	"errors"
	"strings"
	"testing"

	"github.com/sausheong/cortex/internal/enctest"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
	"github.com/sausheong/cortex/internal/workset"
)

// TestIdentityOwnerNameEncrypted: the owner's names come from the working set
// (the entity name is ciphertext), so a mention of the encrypted owner name,
// or of the account name, still resolves to the owner, and no SQL reads
// entities.name.
func TestIdentityOwnerNameEncrypted(t *testing.T) {
	workset.Reset()
	t.Cleanup(workset.Reset)
	env := enctest.Open(t, "encrypted")
	g := New(env.H)
	owner := &model.Entity{Type: "person", Name: "Sau Sheong Chang"}
	other := &model.Entity{Type: "person", Name: "Priya Raman"}
	for _, e := range []*model.Entity{owner, other} {
		if err := g.PutEntity(bg, e); err != nil {
			t.Fatal(err)
		}
	}
	env.Exec(`UPDATE entities SET is_owner = TRUE WHERE id = $1`, owner.ID)
	env.Exec(`UPDATE users SET owner_entity_id = $1 WHERE id = $2`, owner.ID, env.UserID)

	var stmts []string
	store.OnQuery = func(q string, _ []any) { stmts = append(stmts, q) }
	defer func() { store.OnQuery = nil }()

	for _, mention := range []string{"Sau Sheong Chang", "sau sheong chang", "enc-sub-encrypted"} {
		res, err := g.Resolve(bg, mention, "person", nil, &ResolveContext{}, false)
		if err != nil {
			t.Fatal(err)
		}
		if res.Entity == nil || res.Entity.ID != owner.ID || res.Method != model.ResolvedByOwner {
			t.Fatalf("%q resolved to %+v (method %q), want the owner", mention, res.Entity, res.Method)
		}
	}
	res, err := g.Resolve(bg, "Priya Raman", "person", nil, &ResolveContext{}, false)
	if err != nil || res.Entity == nil || res.Entity.ID != other.ID || res.Method == model.ResolvedByOwner {
		t.Fatalf("other person resolved to %+v (%q), %v", res.Entity, res.Method, err)
	}
	store.OnQuery = nil
	for _, q := range stmts {
		if strings.Contains(q, "SELECT e.name, u.name") {
			t.Fatalf("owner lookup read entities.name in SQL: %s", q)
		}
	}
	// A locked handle cannot resolve through the owner path.
	if _, err := New(env.Keyless).Resolve(bg, "Sau Sheong Chang", "person", nil, &ResolveContext{}, false); !errors.Is(err, model.ErrLocked) {
		t.Fatalf("keyless resolve err = %v, want ErrLocked", err)
	}
}

func TestSearchEntitiesEncryptedMatchesLegacySemantics(t *testing.T) {
	workset.Reset()
	t.Cleanup(workset.Reset)
	env := enctest.Open(t, "encrypted")
	g := New(env.H)
	for _, n := range []string{"100% Done", "100x Done", "a_b", "axb", "Crossing Guard", "Guard Crossing"} {
		if err := g.PutEntity(bg, &model.Entity{Type: "concept", Name: n}); err != nil {
			t.Fatal(err)
		}
	}
	names := func(q string) []string {
		got, err := g.SearchEntities(bg, q, nil, "", 20)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range got {
			out = append(out, e.Name)
		}
		return out
	}
	if got := names("100%"); len(got) != 1 || got[0] != "100% Done" {
		t.Errorf("%% must be literal: %v", got)
	}
	if got := names("a_b"); len(got) != 1 || got[0] != "a_b" {
		t.Errorf("_ must be literal: %v", got)
	}
	if got := names("crossing"); len(got) != 2 || got[0] != "Crossing Guard" {
		t.Errorf("prefix first: %v", got)
	}
	if _, err := New(env.Keyless).SearchEntities(bg, "x", nil, "", 5); !errors.Is(err, model.ErrLocked) {
		t.Errorf("keyless err = %v", err)
	}
}
