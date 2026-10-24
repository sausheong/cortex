package graph

import (
	"strings"
	"testing"

	"github.com/sausheong/cortex/internal/enctest"
	"github.com/sausheong/cortex/internal/model"
)

func TestUndoSnapshotEncryptedAndRestores(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	g := New(env.H)
	keep := &model.Entity{Type: "person", Name: "Keeper Zebra", Attributes: map[string]any{"note": "keepsecret"}}
	drop := &model.Entity{Type: "person", Name: "Dropped Walrus", Attributes: map[string]any{"note": "dropsecret"}}
	tgt := &model.Entity{Type: "organization", Name: "Target Org"}
	for _, e := range []*model.Entity{keep, drop, tgt} {
		if err := g.PutEntity(bg, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := g.PutRelationship(bg, &model.Relationship{SourceID: drop.ID, TargetID: tgt.ID, Type: "member_of"}); err != nil {
		t.Fatal(err)
	}
	if err := g.AddAlias(bg, drop.ID, model.AliasEmail, "walrus@x.test"); err != nil {
		t.Fatal(err)
	}
	st, err := g.MergeEntities(bg, keep.ID, drop.ID)
	if err != nil {
		t.Fatal(err)
	}
	n := env.Count(`SELECT count(*) FROM delete_batch_changes WHERE batch_id = $1`, st.BatchID)
	if n == 0 {
		t.Fatal("no change records written")
	}
	if bad := env.Count(`SELECT count(*) FROM delete_batch_changes WHERE batch_id = $1 AND (row_json IS NOT NULL OR row_ct IS NULL OR NOT enc)`, st.BatchID); bad != 0 {
		t.Fatalf("%d snapshot rows are not sealed", bad)
	}
	var dump string
	env.DB.QueryRow(`SELECT coalesce(string_agg(to_jsonb(t)::text, ' '), '') FROM delete_batch_changes t WHERE user_id = $1`, env.UserID).Scan(&dump)
	for _, s := range []string{"Dropped", "Walrus", "walrus@x.test", "dropsecret", "keepsecret", "member_of"} {
		if strings.Contains(dump, s) {
			t.Fatalf("snapshot rows leak %q", s)
		}
	}
	if _, err := g.GetEntity(bg, drop.ID); err == nil {
		t.Fatal("drop entity should be soft-deleted")
	}
	// Keyless handle cannot undo.
	if err := New(env.Keyless).RestoreBatch(bg, st.BatchID); err == nil {
		t.Fatal("keyless undo must fail")
	}
	if err := g.RestoreBatch(bg, st.BatchID); err != nil {
		t.Fatal(err)
	}
	got, err := g.GetEntity(bg, drop.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "Dropped Walrus" || got.Type != "person" || got.Attributes["note"] != "dropsecret" {
		t.Fatalf("restored entity differs: %+v", got)
	}
	k, err := g.GetEntity(bg, keep.ID)
	if err != nil || k.Name != "Keeper Zebra" || k.Attributes["note"] != "keepsecret" {
		t.Fatalf("keep not restored: %+v %v", k, err)
	}
	if k.Attributes["merged_from"] != nil {
		t.Fatalf("merge provenance not undone: %+v", k.Attributes)
	}
}

func TestProfileEncryptedAtRest(t *testing.T) {
	env := enctest.Open(t, "encrypted")
	g := New(env.H)
	e := &model.Entity{Type: "person", Name: "Owner Person", Attributes: map[string]any{"_profile": "PROFILESECRET digest"}}
	if err := g.PutEntity(bg, e); err != nil {
		t.Fatal(err)
	}
	var raw string
	env.DB.QueryRow(`SELECT to_jsonb(t)::text FROM entities t WHERE id = $1`, e.ID).Scan(&raw)
	if strings.Contains(raw, "PROFILESECRET") || strings.Contains(raw, "Owner Person") {
		t.Fatal("profile leaks in entity row")
	}
}
