package vaultart_test

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	"github.com/sausheong/cortex/internal/enctest"
	"github.com/sausheong/cortex/internal/graph"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/memory"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
	"github.com/sausheong/cortex/internal/vaultart"
)

func graphRel(h *store.Handle, a, b string) error {
	return graph.New(h).PutRelationship(bg, &model.Relationship{SourceID: a, TargetID: b, Type: "works_at"})
}

func addAlias(h *store.Handle, id, v string) error {
	return graph.New(h).AddAlias(bg, id, "name", v)
}

func fp(t *testing.T, env *enctest.Env, sub vaultart.Subject, s vaultart.Settings) []byte {
	t.Helper()
	b := vaultart.Fingerprint(env.H, getSet(t, env), sub, "Sam", s)
	if len(b) == 0 {
		t.Fatal("empty fingerprint")
	}
	return b
}

func TestFingerprintStableAcrossRuns(t *testing.T) {
	env := open(t)
	id := ent(t, env.H, "person", "Ada", map[string]any{"b": 1, "a": 2})
	mems(t, env.H, id, 4)
	s := vaultart.DefaultSettings()
	sub := personSub(id, "Ada")
	a := fp(t, env, sub, s)
	for i := 0; i < 5; i++ {
		if !bytes.Equal(a, fp(t, env, sub, s)) {
			t.Fatal("fingerprint moved with no change")
		}
	}
}

func TestFingerprintChangesOnNewMemoryUpdateRetireRenameAliasLength(t *testing.T) {
	env := open(t)
	id := ent(t, env.H, "person", "Ada", nil)
	other := ent(t, env.H, "organization", "Acme", nil)
	ms := mems(t, env.H, id, 3)
	s := vaultart.DefaultSettings()
	sub := personSub(id, "Ada")
	prev := fp(t, env, sub, s)
	step := func(name string, next []byte) {
		t.Helper()
		if bytes.Equal(prev, next) {
			t.Fatalf("%s did not change the fingerprint", name)
		}
		prev = next
	}

	mems(t, env.H, id, 1)
	step("new memory", fp(t, env, sub, s))

	env.Exec(`UPDATE memories SET updated_at = updated_at + interval '1 second' WHERE id = $1`, ms[0])
	step("memory update", fp(t, env, sub, s))

	if err := memory.New(env.H).InvalidateMemory(bg, ms[1], nil); err != nil {
		t.Fatal(err)
	}
	step("retire", fp(t, env, sub, s))

	if err := graph.New(env.H).RenameEntity(bg, id, "Ada L"); err != nil {
		t.Fatal(err)
	}
	step("rename", fp(t, env, sub, s))

	if err := addAlias(env.H, id, "Countess"); err != nil {
		t.Fatal(err)
	}
	step("alias", fp(t, env, sub, s))

	s2 := s
	s2.Length = vaultart.LengthLong
	step("length", fp(t, env, sub, s2))

	r := &model.Relationship{SourceID: id, TargetID: other, Type: "works_at"}
	if err := graph.New(env.H).PutRelationship(bg, r); err != nil {
		t.Fatal(err)
	}
	step("relationship", fp(t, env, sub, s2))
	now := time.Now().UTC()
	env.Exec(`UPDATE relationships SET invalid_at = $1 WHERE id = $2`, now, r.ID)
	step("relationship closed", fp(t, env, sub, s2))

	other2 := vaultart.Fingerprint(env.H, getSet(t, env), sub, "Someone Else", s2)
	if bytes.Equal(prev, other2) {
		t.Fatal("owner name must feed the fingerprint")
	}
}

func TestFingerprintCoversMemoriesBeyondBudget(t *testing.T) {
	env := open(t)
	id := ent(t, env.H, "person", "Ada", nil)
	bulk(t, env, id, 300)
	s := vaultart.DefaultSettings()
	sub := personSub(id, "Ada")
	set := getSet(t, env)
	_, used, _ := vaultart.BuildInput(set, sub, "Sam", nil, s, 200)
	if len(used) >= 300 {
		t.Fatal("test needs memories beyond the budget")
	}
	in := map[string]bool{}
	for _, u := range used {
		in[u] = true
	}
	var left string
	for _, m := range set.Memories() {
		if !in[m.ID] {
			left = m.ID
			break
		}
	}
	before := vaultart.Fingerprint(env.H, set, sub, "Sam", s)
	env.Exec(`UPDATE memories SET updated_at = updated_at + interval '1 second' WHERE id = $1`, left)
	after := vaultart.Fingerprint(env.H, getSet(t, env), sub, "Sam", s)
	if bytes.Equal(before, after) {
		t.Fatal("a memory beyond the prompt budget must still move the fingerprint")
	}
}

func TestFingerprintUnchangedByUnrelatedEntityOrNewQualifier(t *testing.T) {
	env := open(t)
	id := ent(t, env.H, "person", "Ada", nil)
	mems(t, env.H, id, 3)
	s := vaultart.DefaultSettings()
	sub := personSub(id, "Ada")
	before := fp(t, env, sub, s)

	stranger := ent(t, env.H, "person", "Grace", nil)
	mems(t, env.H, stranger, 5) // a new qualifier elsewhere
	if err := addAlias(env.H, stranger, "Amazing Grace"); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, fp(t, env, sub, s)) {
		t.Fatal("unrelated entity or new qualifier changed the fingerprint")
	}
}

func TestFingerprintDiffersBetweenUsers(t *testing.T) {
	env := open(t)
	id := ent(t, env.H, "person", "Ada", nil)
	mems(t, env.H, id, 3)
	s := vaultart.DefaultSettings()
	a := fp(t, env, personSub(id, "Ada"), s)

	uk, err := keys.NewUK()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(uk.Zero)
	h2 := env.HandleFor(env.NewUser("second", "encrypted"), "encrypted", uk)
	set, err := workSetFor(h2)
	if err != nil {
		t.Fatal(err)
	}
	b := vaultart.Fingerprint(h2, set, personSub(id, "Ada"), "Sam", s)
	if len(b) == 0 || bytes.Equal(a, b) {
		t.Fatal("fingerprints must be keyed per user")
	}
}

func TestFingerprintAllMatchesFingerprint(t *testing.T) {
	env := open(t)
	a := ent(t, env.H, "person", "Ada", map[string]any{"role": "x"})
	b := ent(t, env.H, "person", "Bob", nil)
	c := ent(t, env.H, "concept", "Tea", nil)
	d := ent(t, env.H, "concept", "Kettle", nil)
	mems(t, env.H, a, 3)
	mems(t, env.H, b, 2)
	linkMem(t, env.H, 3, c, d)
	if err := graphRel(env.H, a, b); err != nil {
		t.Fatal(err)
	}
	subs := []vaultart.Subject{
		personSub(a, "Ada"), personSub(b, "Bob"),
		{ID: "tp", Group: vaultart.GroupTopics, Kind: "topic", Name: "Tea", Members: []string{c, d}},
	}
	set := getSet(t, env)
	s := vaultart.DefaultSettings()
	all := vaultart.FingerprintAll(env.H, set, subs, "Sam", s)
	if len(all) != 3 {
		t.Fatalf("%d results", len(all))
	}
	for _, sub := range subs {
		if !bytes.Equal(all[sub.ID], vaultart.Fingerprint(env.H, set, sub, "Sam", s)) {
			t.Fatalf("mismatch for %s", sub.ID)
		}
	}
}

func TestFingerprintAllFast(t *testing.T) {
	env := open(t)
	var subs []vaultart.Subject
	var ids []string
	for i := 0; i < 1600; i++ {
		id := ent(t, env.H, "person", fmt.Sprintf("Person %04d", i), nil)
		ids = append(ids, id)
		subs = append(subs, personSub(id, fmt.Sprintf("Person %04d", i)))
	}
	for i := 0; i < 4000; i++ {
		putMem(t, env.H, fmt.Sprintf("fact %d", i), "t", 0.5, false, ids[i%1600])
	}
	set := getSet(t, env)
	s := vaultart.DefaultSettings()
	start := time.Now()
	all := vaultart.FingerprintAll(env.H, set, subs, "Sam", s)
	took := time.Since(start)
	if len(all) != 1600 {
		t.Fatal("missing results")
	}
	if took > 150*time.Millisecond {
		t.Fatalf("FingerprintAll took %v, want under 150ms", took)
	}
}
