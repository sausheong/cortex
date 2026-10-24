package graph

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sausheong/cortex/internal/enctest"
	"github.com/sausheong/cortex/internal/model"
)

var bg = context.Background()

func TestBlindNameNormalisation(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	g := New(env.H)
	a := &model.Entity{Type: "person", Name: "Zoë"}
	if err := g.PutEntity(bg, a); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"ZOË", " zoë "} {
		b := &model.Entity{Type: "person", Name: n}
		if err := g.PutEntity(bg, b); err != nil {
			t.Fatal(err)
		}
		if b.ID != a.ID {
			t.Fatalf("%q forked a second entity", n)
		}
	}
	c := &model.Entity{Type: "organization", Name: "Zoë"}
	if err := g.PutEntity(bg, c); err != nil {
		t.Fatal(err)
	}
	if c.ID == a.ID {
		t.Fatal("a different type must not collide")
	}
}

func TestEntityRoundTripEncrypted(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	g := New(env.H)
	e := &model.Entity{Type: "Person", Name: "Ada Lovelace", Source: "notes", Confidence: 0.8,
		Attributes: map[string]any{"role": "analyst", "n": float64(3)}}
	if err := g.PutEntity(bg, e); err != nil {
		t.Fatal(err)
	}
	got, err := g.GetEntity(bg, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Ada Lovelace" || got.Type != "person" || got.Source != "notes" ||
		got.Attributes["role"] != "analyst" || got.Attributes["n"] != float64(3) || got.Confidence != 0.8 {
		t.Fatalf("round trip lost data: %+v", got)
	}
	if err := g.AddAlias(bg, e.ID, "", "ada@x.test"); err != nil {
		t.Fatal(err)
	}
	al, err := g.Aliases(bg, e.ID)
	if err != nil || len(al) != 1 || al[0].Value != "ada@x.test" || al[0].Kind != model.AliasEmail || al[0].Source != "user" {
		t.Fatalf("aliases = %+v, %v", al, err)
	}
	// FindEntities filters decrypted values in memory.
	fs, err := g.FindEntities(bg, model.EntityFilter{Type: "person", Source: "notes", NameLike: "ada%"})
	if err != nil || len(fs) != 1 || fs[0].ID != e.ID {
		t.Fatalf("FindEntities = %+v, %v", fs, err)
	}
	// Update in place by ID keeps one row.
	got.Attributes["role"] = "mathematician"
	if err := g.PutEntity(bg, got); err != nil {
		t.Fatal(err)
	}
	again, _ := g.GetEntity(bg, e.ID)
	if again.Attributes["role"] != "mathematician" {
		t.Fatal("update lost")
	}
	if n := env.Count(`SELECT count(*) FROM entities WHERE user_id=$1 AND NOT is_owner`, env.UserID); n != 1 {
		t.Fatalf("rows = %d", n)
	}
}

func TestEntityDedupViaBlindIndex(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	g := New(env.H)
	a := &model.Entity{Type: "person", Name: "Grace", Attributes: map[string]any{"v": "1"}}
	b := &model.Entity{Type: "person", Name: "grace", Attributes: map[string]any{"v": "2"}}
	if err := g.PutEntity(bg, a); err != nil {
		t.Fatal(err)
	}
	if err := g.PutEntity(bg, b); err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID {
		t.Fatal("duplicate entity")
	}
	got, _ := g.GetEntity(bg, a.ID)
	if got.Attributes["v"] != "2" {
		t.Fatalf("upsert did not update attributes: %v", got.Attributes)
	}
}

func TestRelationshipDedupEncrypted(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	g := New(env.H)
	x := &model.Entity{Type: "person", Name: "X"}
	y := &model.Entity{Type: "organization", Name: "Y"}
	if err := g.PutEntity(bg, x); err != nil {
		t.Fatal(err)
	}
	if err := g.PutEntity(bg, y); err != nil {
		t.Fatal(err)
	}
	r1 := &model.Relationship{SourceID: x.ID, TargetID: y.ID, Type: "Works At", Source: "s1", Confidence: 0.6,
		Attributes: map[string]any{"a": "1"}}
	if err := g.PutRelationship(bg, r1); err != nil {
		t.Fatal(err)
	}
	r2 := &model.Relationship{SourceID: x.ID, TargetID: y.ID, Type: "works_at", Source: "s2", Confidence: 0.9,
		Attributes: map[string]any{"b": "2"}}
	if err := g.PutRelationship(bg, r2); err != nil {
		t.Fatal(err)
	}
	if r2.ID != r1.ID || r2.EvidenceCount != 2 {
		t.Fatalf("want one edge with evidence 2, got id %s/%s count %d", r1.ID, r2.ID, r2.EvidenceCount)
	}
	if n := env.Count(`SELECT count(*) FROM relationships WHERE user_id=$1 AND deleted_at IS NULL`, env.UserID); n != 1 {
		t.Fatalf("live edges = %d", n)
	}
	rels, err := g.GetRelationships(bg, x.ID)
	if err != nil || len(rels) != 1 {
		t.Fatalf("%v %v", rels, err)
	}
	r := rels[0]
	if r.Type != "works_at" || r.Source != "s1" || r.Confidence != 0.9 || r.EvidenceCount != 2 ||
		r.Attributes["a"] != "1" || r.Attributes["b"] != "2" {
		t.Fatalf("merged edge wrong: %+v", r)
	}
	// Type filter works through the blind index.
	rels, _ = g.GetRelationships(bg, x.ID, model.RelTypeFilter("Works At"))
	if len(rels) != 1 {
		t.Fatal("type filter lost the edge")
	}
}

func TestAliasLookupViaBlind(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	g := New(env.H)
	e := &model.Entity{Type: "person", Name: "Linus"}
	if err := g.PutEntity(bg, e); err != nil {
		t.Fatal(err)
	}
	if err := g.AddAlias(bg, e.ID, model.AliasEmail, "Linus@Kernel.ORG"); err != nil {
		t.Fatal(err)
	}
	res, err := g.ResolveEntity(bg, "linus@kernel.org", "")
	if err != nil || res.Entity == nil || res.Entity.ID != e.ID || res.Method != model.ResolvedByAlias {
		t.Fatalf("resolve = %+v, %v", res, err)
	}
	// Exact name resolution (blind) ignores case.
	res, err = g.ResolveEntity(bg, "LINUS", "person")
	if err != nil || res.Entity == nil || res.Entity.ID != e.ID {
		t.Fatalf("name resolve = %+v, %v", res, err)
	}
	if err := g.RemoveAlias(bg, model.AliasEmail, "linus@kernel.org"); err != nil {
		t.Fatal(err)
	}
	if al, _ := g.Aliases(bg, e.ID); len(al) != 0 {
		t.Fatal("alias not removed")
	}
}

func TestAliasCrossEntityUniquenessEncrypted(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	g := New(env.H)
	a := &model.Entity{Type: "person", Name: "A"}
	b := &model.Entity{Type: "person", Name: "B"}
	g.PutEntity(bg, a)
	g.PutEntity(bg, b)
	if err := g.AddAlias(bg, a.ID, model.AliasName, "Shared Nick"); err != nil {
		t.Fatal(err)
	}
	if err := g.AddAlias(bg, a.ID, model.AliasName, "shared  nick"); err != nil {
		t.Fatalf("re-adding to the same entity must be a no-op: %v", err)
	}
	if err := g.AddAlias(bg, b.ID, model.AliasName, "SHARED NICK"); !errors.Is(err, model.ErrAliasTaken) {
		t.Fatalf("want ErrAliasTaken, got %v", err)
	}
	if n := env.Count(`SELECT count(*) FROM entity_aliases WHERE user_id=$1`, env.UserID); n != 1 {
		t.Fatalf("alias rows = %d", n)
	}
}

func TestAliasSurvivesMergeRetarget(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	g := New(env.H)
	keep := &model.Entity{Type: "person", Name: "Keep Me"}
	drop := &model.Entity{Type: "person", Name: "Drop Me"}
	g.PutEntity(bg, keep)
	g.PutEntity(bg, drop)
	if err := g.AddAlias(bg, drop.ID, model.AliasEmail, "drop@x.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.MergeEntities(bg, keep.ID, drop.ID); err != nil {
		t.Fatal(err)
	}
	al, err := g.Aliases(bg, keep.ID)
	if err != nil {
		t.Fatalf("aliases must decrypt after entity_id changes: %v", err)
	}
	vals := map[string]bool{}
	for _, a := range al {
		vals[a.Value] = true
	}
	if !vals["drop@x.test"] || !vals["Drop Me"] {
		t.Fatalf("aliases after merge = %+v", al)
	}
	if k, err := g.GetEntity(bg, keep.ID); err != nil || k.Attributes["merged_from"] == nil {
		t.Fatalf("keep = %+v, %v", k, err)
	}
}

func TestMergeDedupsEncryptedRelationships(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	g := New(env.H)
	keep := &model.Entity{Type: "person", Name: "K"}
	drop := &model.Entity{Type: "person", Name: "D"}
	tgt := &model.Entity{Type: "organization", Name: "T"}
	g.PutEntity(bg, keep)
	g.PutEntity(bg, drop)
	g.PutEntity(bg, tgt)
	for _, s := range []string{keep.ID, drop.ID} {
		if err := g.PutRelationship(bg, &model.Relationship{SourceID: s, TargetID: tgt.ID, Type: "member_of"}); err != nil {
			t.Fatal(err)
		}
	}
	st, err := g.MergeEntities(bg, keep.ID, drop.ID)
	if err != nil {
		t.Fatal(err)
	}
	if st.DupesDropped < 1 {
		t.Fatalf("stats = %+v", st)
	}
}

func TestEmptyCiphertextFailsDecrypt(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	g := New(env.H)
	e := &model.Entity{Type: "person", Name: "Victim"}
	g.PutEntity(bg, e)
	env.Exec(`UPDATE entities SET name_ct = '\x' WHERE id = $1`, e.ID)
	if _, err := g.GetEntity(bg, e.ID); err == nil {
		t.Fatal("NULL ciphertext on an enc row must fail")
	}
	env.Exec(`UPDATE entities SET name_ct = '\x', type_ct = '\x', attributes_ct = NULL WHERE id = $1`, e.ID)
	if _, err := g.GetEntity(bg, e.ID); err == nil {
		t.Fatal("NULL ciphertext on an enc row must fail")
	}
}

func TestBlindCollisionAcrossUsersNeverReads(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	g := New(env.H)
	e := &model.Entity{Type: "person", Name: "Mallory Target"}
	g.PutEntity(bg, e)
	// Another user, same key by construction (worst case), with identical
	// blind bytes copied onto their own row.
	other := env.NewUser("other", "encrypted")
	oh := env.HandleFor(other, "encrypted", env.UK)
	env.Exec(`INSERT INTO entities (id, user_id, name_ct, type_ct, source_ct, attributes_ct, name_blind, type_blind)
	          SELECT 'collide', $2, name_ct, type_ct, source_ct, attributes_ct, name_blind, type_blind FROM entities WHERE id = $1`, e.ID, other)
	// User A's lookups only ever see A's row.
	res, err := g.ResolveEntity(bg, "mallory target", "person")
	if err != nil || res.Entity == nil || res.Entity.ID != e.ID {
		t.Fatalf("A resolve = %+v, %v", res, err)
	}
	dup := &model.Entity{Type: "person", Name: "Mallory Target"}
	if err := g.PutEntity(bg, dup); err != nil || dup.ID != e.ID {
		t.Fatalf("A put forked or hit other user's row: %s %v", dup.ID, err)
	}
	// And B's handle cannot open A's ciphertext (AAD binds user_id), nor does
	// A's handle read B's row by id.
	if _, err := g.GetEntity(bg, "collide"); err == nil {
		t.Fatal("A read B's row")
	}
	if _, err := New(oh).GetEntity(bg, "collide"); err == nil {
		t.Fatal("copied ciphertext must fail AAD under a different user")
	}
}

func TestKeylessHandleCannotReadEncryptedRow(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	g := New(env.H)
	e := &model.Entity{Type: "person", Name: "Locked Out"}
	g.PutEntity(bg, e)
	g.AddAlias(bg, e.ID, model.AliasName, "nick")
	k := New(env.Keyless)
	if _, err := k.GetEntity(bg, e.ID); !errors.Is(err, model.ErrLocked) {
		t.Fatalf("read: want ErrLocked, got %v", err)
	}
	if _, err := k.Aliases(bg, e.ID); !errors.Is(err, model.ErrLocked) {
		t.Fatalf("aliases: want ErrLocked, got %v", err)
	}
	if err := k.PutEntity(bg, &model.Entity{Type: "person", Name: "New"}); !errors.Is(err, model.ErrLocked) {
		t.Fatalf("write: want ErrLocked, got %v", err)
	}
	if err := k.AddAlias(bg, e.ID, model.AliasName, "x"); !errors.Is(err, model.ErrLocked) {
		t.Fatalf("alias write: want ErrLocked, got %v", err)
	}
	if err := k.PutRelationship(bg, &model.Relationship{SourceID: e.ID, TargetID: e.ID, Type: "t"}); !errors.Is(err, model.ErrLocked) {
		t.Fatalf("rel write: want ErrLocked, got %v", err)
	}
}

func TestRawRowsHoldNoPlaintext(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	g := New(env.H)
	a := &model.Entity{Type: "person", Name: "Quillfeather Brightwater", Source: "zebra-source-xq",
		Attributes: map[string]any{"secret": "marmalade-attr-xq"}}
	b := &model.Entity{Type: "organization", Name: "Gloomhaven Holdings"}
	g.PutEntity(bg, a)
	g.PutEntity(bg, b)
	g.AddAlias(bg, a.ID, model.AliasEmail, "quill@brightwater-xq.test")
	g.AddAliasQuiet(bg, a.ID, model.AliasName, "Quilly Nickname", "ingest-source-xq")
	g.PutRelationship(bg, &model.Relationship{SourceID: a.ID, TargetID: b.ID, Type: "employed_by_xq", Source: "rel-source-xq",
		Attributes: map[string]any{"evidence": "overheard-evidence-xq"}})
	for _, tbl := range []string{"entities", "relationships", "entity_aliases"} {
		rows, err := env.DB.Query(`SELECT row_to_json(t)::text FROM `+tbl+` t WHERE user_id = $1 AND NOT COALESCE((to_jsonb(t)->>'is_owner')::boolean, false)`, env.UserID)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for rows.Next() {
			var s string
			rows.Scan(&s)
			n++
			low := strings.ToLower(s)
			for _, bad := range []string{"quillfeather", "brightwater", "zebra-source", "marmalade", "gloomhaven", "quill@", "nickname", "employed_by", "rel-source", "overheard", "ingest-source"} {
				if strings.Contains(low, bad) {
					t.Fatalf("%s row leaks %q: %s", tbl, bad, s)
				}
			}
		}
		rows.Close()
		if n == 0 {
			t.Fatalf("no rows in %s", tbl)
		}
	}
}

func TestEntityCiphertextMovedToOtherRowFailsAAD(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	g := New(env.H)
	a := &model.Entity{Type: "person", Name: "Alpha One"}
	b := &model.Entity{Type: "person", Name: "Beta Two"}
	g.PutEntity(bg, a)
	g.PutEntity(bg, b)
	env.Exec(`UPDATE entities SET name_ct = (SELECT name_ct FROM entities WHERE id=$1) WHERE id=$2`, a.ID, b.ID)
	if got, err := g.GetEntity(bg, b.ID); err == nil {
		t.Fatalf("moved ciphertext decrypted: %+v", got)
	}
}

func TestAliasCiphertextMovedToOtherRowFailsAAD(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	g := New(env.H)
	a := &model.Entity{Type: "person", Name: "Alpha"}
	g.PutEntity(bg, a)
	g.AddAlias(bg, a.ID, model.AliasName, "first alias")
	g.AddAlias(bg, a.ID, model.AliasName, "second alias")
	env.Exec(`UPDATE entity_aliases SET value_ct = (SELECT value_ct FROM entity_aliases WHERE user_id=$1 ORDER BY id LIMIT 1)
	          WHERE id = (SELECT id FROM entity_aliases WHERE user_id=$1 ORDER BY id DESC LIMIT 1)`, env.UserID)
	if _, err := g.Aliases(bg, a.ID); err == nil {
		t.Fatal("alias ciphertext moved between rows must fail")
	}
}
