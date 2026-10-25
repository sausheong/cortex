package vaultart_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sausheong/cortex/internal/enctest"
	"github.com/sausheong/cortex/internal/graph"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
	"github.com/sausheong/cortex/internal/vaultart"
	"github.com/sausheong/cortex/internal/workset"
)

func buildIx(t *testing.T, env *enctest.Env, owner string, s vaultart.Settings, now time.Time) *vaultart.Index {
	t.Helper()
	ix, err := vaultart.BuildIndex(bg, env.H, getSet(t, env), owner, s, now)
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func entries(ix *vaultart.Index) []vaultart.IndexEntry {
	var all []vaultart.IndexEntry
	all = append(all, ix.People...)
	all = append(all, ix.Projects...)
	all = append(all, ix.Events...)
	all = append(all, ix.Topics...)
	return all
}

func entryFor(ix *vaultart.Index, id string) *vaultart.IndexEntry {
	for _, e := range entries(ix) {
		if e.Subject.ID == id {
			e := e
			return &e
		}
	}
	return nil
}

// writeRow stores an article row whose fingerprint matches the current data.
func writeRow(t *testing.T, env *enctest.Env, sub vaultart.Subject, s vaultart.Settings) {
	t.Helper()
	fp := vaultart.Fingerprint(env.H, getSet(t, env), sub, "", s)
	slug := vaultart.ArticleSlug(sub.Name, sub.ID)
	kind := "entity"
	if sub.Kind == "topic" {
		kind = "topic"
		slug = "topic-" + vaultart.ShortID(sub.ID)
	}
	err := env.H.PutArticle(bg, store.ArticleRow{SubjectID: sub.ID, Kind: kind, ShortID: vaultart.ShortID(sub.ID),
		Slug: slug, Fingerprint: fp, WrittenAt: time.Now(), Article: model.Article{Title: sub.Name, Lead: "x"}})
	if err != nil {
		t.Fatal(err)
	}
}

func TestBuildIndexGroups(t *testing.T) {
	env := open(t)
	p := ent(t, env.H, "person", "Ada", map[string]any{"role": "Engineer"})
	o := ent(t, env.H, "organization", "Acme", nil)
	pr := ent(t, env.H, "project", "Apollo", nil)
	for _, id := range []string{p, o, pr} {
		mems(t, env.H, id, 3)
	}
	c1 := ent(t, env.H, "concept", "Tea", nil)
	c2 := ent(t, env.H, "concept", "Coffee", nil)
	putMem(t, env.H, "tea and coffee", "t", 0.9, false, c1, c2)
	putMem(t, env.H, "more tea and coffee", "t", 0.9, false, c1, c2)
	putMem(t, env.H, "tea again", "t", 0.9, false, c1, c2)
	ix := buildIx(t, env, "", vaultart.DefaultSettings(), time.Now())
	if len(ix.People) != 1 || ix.People[0].Name != "Ada" || ix.People[0].Descriptor != "Engineer" {
		t.Fatalf("people %+v", ix.People)
	}
	if len(ix.Projects) != 2 || len(ix.Events) != 0 || len(ix.Topics) != 1 {
		t.Fatalf("projects %d events %d topics %d", len(ix.Projects), len(ix.Events), len(ix.Topics))
	}
	e := ix.People[0]
	if e.Group != vaultart.GroupPeople || e.Kind != "person" || e.WrittenAt != nil || e.Path != vaultart.Path(vaultart.GroupPeople, vaultart.ArticleSlug("Ada", p)) {
		t.Fatalf("%+v", e)
	}
	tp := ix.Topics[0]
	if tp.Group != vaultart.GroupTopics || tp.Subject.Kind != "topic" || len(tp.Subject.Members) != 2 || !strings.HasPrefix(tp.Path, "/vault/topics/") {
		t.Fatalf("%+v", tp)
	}
}

func TestIndexDoesNotShowNonQualifying(t *testing.T) {
	env := open(t)
	low := ent(t, env.H, "person", "Low", nil)
	acct := ent(t, env.H, "email address", "a@b.c", nil)
	solo := ent(t, env.H, "concept", "Lonely", nil)
	mems(t, env.H, low, 2)
	mems(t, env.H, acct, 9)
	mems(t, env.H, solo, 9)
	ix := buildIx(t, env, "", vaultart.DefaultSettings(), time.Now())
	if n := len(entries(ix)); n != 0 {
		t.Fatalf("%+v", entries(ix))
	}
	if len(ix.Recent) != 0 {
		t.Fatal("recent")
	}
}

func TestIndexEventsFoldOrSeparate(t *testing.T) {
	env := open(t)
	pr := ent(t, env.H, "project", "Apollo", nil)
	mems(t, env.H, pr, 3)
	for i := 0; i < 12; i++ {
		mems(t, env.H, ent(t, env.H, "event", fmt.Sprintf("Event %d", i), nil), 3)
	}
	ix := buildIx(t, env, "", vaultart.DefaultSettings(), time.Now())
	if len(ix.Projects) != 13 || len(ix.Events) != 0 {
		t.Fatalf("fold: projects %d events %d", len(ix.Projects), len(ix.Events))
	}
	ev := entryFor(ix, "")
	_ = ev
	mems(t, env.H, ent(t, env.H, "event", "Event 12", nil), 3)
	ix = buildIx(t, env, "", vaultart.DefaultSettings(), time.Now())
	if len(ix.Projects) != 1 || len(ix.Events) != 13 {
		t.Fatalf("split: projects %d events %d", len(ix.Projects), len(ix.Events))
	}
	if ix.Events[0].Group != vaultart.GroupProjects || ix.Events[0].Kind != "event" {
		t.Fatalf("%+v", ix.Events[0])
	}
}

func TestIndexChangedAndStale(t *testing.T) {
	env := open(t)
	s := vaultart.DefaultSettings()
	id := ent(t, env.H, "person", "Ada", nil)
	other := ent(t, env.H, "person", "Bo", nil)
	mems(t, env.H, id, 3)
	mems(t, env.H, other, 3)
	now := time.Now()
	writeRow(t, env, personSub(id, "Ada"), s)
	writeRow(t, env, personSub(other, "Bo"), s)
	ix := buildIx(t, env, "", s, now)
	if e := entryFor(ix, id); e.Changed || e.WrittenAt == nil || e.NewlyQualified {
		t.Fatalf("fresh %+v", e)
	}
	if len(ix.Recent) != 0 {
		t.Fatalf("recent %+v", ix.Recent)
	}
	mems(t, env.H, id, 1) // data changed since the write
	if err := env.H.MarkArticleStale(bg, other); err != nil {
		t.Fatal(err)
	}
	ix = buildIx(t, env, "", s, now)
	if e := entryFor(ix, id); !e.Changed {
		t.Fatalf("fingerprint differs: %+v", e)
	}
	if e := entryFor(ix, other); !e.Changed {
		t.Fatalf("stale: %+v", e)
	}
	if len(ix.Recent) != 2 {
		t.Fatalf("recent %+v", ix.Recent)
	}
}

func TestIndexNewlyQualified(t *testing.T) {
	env := open(t)
	s := vaultart.DefaultSettings()
	fresh := ent(t, env.H, "person", "Fresh", nil)
	old := ent(t, env.H, "person", "Old", nil)
	mems(t, env.H, fresh, 3)
	mems(t, env.H, old, 3)
	env.Exec(`UPDATE memories SET created_at = now() - interval '30 days'
		WHERE id IN (SELECT m.id FROM memories m JOIN memory_entities me ON me.memory_id = m.id WHERE me.entity_id = $1)`, old)
	ix := buildIx(t, env, "", s, time.Now())
	if e := entryFor(ix, fresh); !e.NewlyQualified {
		t.Fatalf("fresh %+v", e)
	}
	if e := entryFor(ix, old); e.NewlyQualified || e.Changed {
		t.Fatalf("old %+v", e)
	}
	if len(ix.Recent) != 1 || ix.Recent[0].Subject.ID != fresh {
		t.Fatalf("%+v", ix.Recent)
	}
	// Once written it is no longer newly qualified.
	writeRow(t, env, personSub(fresh, "Fresh"), s)
	ix = buildIx(t, env, "", s, time.Now())
	if e := entryFor(ix, fresh); e.NewlyQualified || e.Changed {
		t.Fatalf("written %+v", e)
	}
}

func TestIndexRecentNewestFirstCapped(t *testing.T) {
	env := open(t)
	s := vaultart.DefaultSettings()
	var ids []string
	for i := 0; i < 14; i++ {
		id := ent(t, env.H, "person", fmt.Sprintf("P%02d", i), nil)
		mems(t, env.H, id, 3)
		env.Exec(`UPDATE memories SET created_at = now() - make_interval(hours => $2)
			WHERE id IN (SELECT memory_id FROM memory_entities WHERE entity_id = $1)`, id, i+1)
		ids = append(ids, id)
	}
	ix := buildIx(t, env, "", s, time.Now())
	if len(ix.Recent) != 10 {
		t.Fatalf("len %d", len(ix.Recent))
	}
	for i, e := range ix.Recent {
		if e.Subject.ID != ids[i] {
			t.Fatalf("pos %d is %s want %s", i, e.Name, ids[i])
		}
	}
}

func TestIndexIgnoresOrphanRows(t *testing.T) {
	env := open(t)
	s := vaultart.DefaultSettings()
	id := ent(t, env.H, "person", "Ada", nil)
	mems(t, env.H, id, 3)
	c1 := ent(t, env.H, "concept", "Tea", nil)
	c2 := ent(t, env.H, "concept", "Coffee", nil)
	for i := 0; i < 3; i++ {
		putMem(t, env.H, fmt.Sprintf("tea %d", i), "t", 0.9, false, c1, c2)
	}
	ix := buildIx(t, env, "", s, time.Now())
	topic := ix.Topics[0].Subject
	writeRow(t, env, topic, s)
	writeRow(t, env, personSub(id, "Ada"), s)
	// Rows for an entity that is gone and a topic id that was never persisted.
	for _, sub := range []vaultart.Subject{
		{ID: "gone-entity-000001", Group: vaultart.GroupPeople, Kind: "entity", Name: "Gone"},
		{ID: "vanished-topic-00002", Group: vaultart.GroupTopics, Kind: "topic", Name: "Vanished", Members: []string{c1}},
	} {
		err := env.H.PutArticle(bg, store.ArticleRow{SubjectID: sub.ID, Kind: map[string]string{"entity": "entity", "topic": "topic"}[sub.Kind],
			ShortID: vaultart.ShortID(sub.ID), Slug: vaultart.ArticleSlug(sub.Name, sub.ID), Fingerprint: []byte("x"),
			WrittenAt: time.Now(), Article: model.Article{Title: "x", Lead: "x"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	ix = buildIx(t, env, "", s, time.Now())
	if len(entries(ix)) != 2 || len(ix.Recent) != 0 {
		t.Fatalf("%+v", entries(ix))
	}
	if _, _, ok := ix.Lookup(vaultart.GroupPeople, vaultart.ArticleSlug("Gone", "gone-entity-000001")); ok {
		t.Fatal("orphan entity resolvable")
	}
	if _, _, ok := ix.Lookup(vaultart.GroupTopics, vaultart.ArticleSlug("Vanished", "vanished-topic-00002")); ok {
		t.Fatal("orphan topic resolvable")
	}
}

func TestIndexCacheByWorksetVersion(t *testing.T) {
	env := open(t)
	s := vaultart.DefaultSettings()
	id := ent(t, env.H, "person", "Ada", nil)
	mems(t, env.H, id, 3)
	now := time.Now()
	set := getSet(t, env)
	a, err := vaultart.BuildIndex(bg, env.H, set, "", s, now)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := vaultart.BuildIndex(bg, env.H, set, "", s, now)
	if a != b {
		t.Fatal("same version must reuse the index")
	}
	s2 := s
	s2.Threshold = 4
	if c, _ := vaultart.BuildIndex(bg, env.H, set, "", s2, now); c == a || len(c.People) != 0 {
		t.Fatal("settings change must rebuild")
	}
	mems(t, env.H, id, 1)
	d := buildIx(t, env, "", s, now)
	if d == a {
		t.Fatal("set change must rebuild")
	}
	// An article row change (stale flag) also rebuilds.
	writeRow(t, env, personSub(id, "Ada"), s)
	e := buildIx(t, env, "", s, now)
	if err := env.H.MarkArticleStale(bg, id); err != nil {
		t.Fatal(err)
	}
	set2, _ := workset.For(bg, env.H)
	f, _ := vaultart.BuildIndex(bg, env.H, set2, "", s, now)
	if f == e || !entryFor(f, id).Changed {
		t.Fatal("article rows change must rebuild")
	}
	vaultart.DropIndex(env.UserID)
	if g, _ := vaultart.BuildIndex(bg, env.H, set2, "", s, now); g == f {
		t.Fatal("DropIndex must clear the cache")
	}
}

func TestPathForEntityAndTopic(t *testing.T) {
	env := open(t)
	s := vaultart.DefaultSettings()
	id := ent(t, env.H, "person", "Ada", nil)
	low := ent(t, env.H, "person", "Low", nil)
	mems(t, env.H, id, 3)
	mems(t, env.H, low, 1)
	c1 := ent(t, env.H, "concept", "Tea", nil)
	c2 := ent(t, env.H, "concept", "Coffee", nil)
	for i := 0; i < 3; i++ {
		putMem(t, env.H, fmt.Sprintf("tea %d", i), "t", 0.9, false, c1, c2)
	}
	ix := buildIx(t, env, "", s, time.Now())
	if p, ok := ix.PathFor(id); !ok || p != ix.People[0].Path {
		t.Fatalf("entity %q %v", p, ok)
	}
	for _, c := range []string{c1, c2} {
		if p, ok := ix.PathFor(c); !ok || p != ix.Topics[0].Path {
			t.Fatalf("member %q %v", p, ok)
		}
	}
	if _, ok := ix.PathFor(low); ok {
		t.Fatal("non-qualifying has a path")
	}
	if _, ok := ix.PathFor("nope"); ok {
		t.Fatal("unknown has a path")
	}
}

func TestLookupWrongNamePartReturnsCanonical(t *testing.T) {
	env := open(t)
	s := vaultart.DefaultSettings()
	id := ent(t, env.H, "person", "Ada", nil)
	mems(t, env.H, id, 3)
	sub := personSub(id, "Ada")
	// A row written under an older name keeps its stored slug as canonical.
	err := env.H.PutArticle(bg, store.ArticleRow{SubjectID: id, Kind: "entity", ShortID: vaultart.ShortID(id),
		Slug: "old-name-" + vaultart.ShortID(id), Fingerprint: []byte("x"), WrittenAt: time.Now(),
		Article: model.Article{Title: "x", Lead: "x"}})
	if err != nil {
		t.Fatal(err)
	}
	ix := buildIx(t, env, "", s, time.Now())
	got, canon, ok := ix.Lookup(vaultart.GroupPeople, "whatever-"+vaultart.ShortID(id))
	if !ok || got.ID != sub.ID || canon != "old-name-"+vaultart.ShortID(id) {
		t.Fatalf("%v %q %v", got, canon, ok)
	}
	if ix.People[0].Path != "/vault/people/old-name-"+vaultart.ShortID(id) {
		t.Fatalf("path %q", ix.People[0].Path)
	}
	// Wrong group, bad slug and unknown id are not found.
	if _, _, ok := ix.Lookup(vaultart.GroupProjects, canon); ok {
		t.Fatal("wrong group")
	}
	for _, bad := range []string{"", "nodash", "ada-xyz", "ada-ZZZZZZZZ"} {
		if _, _, ok := ix.Lookup(vaultart.GroupPeople, bad); ok {
			t.Fatalf("%q resolved", bad)
		}
	}
	// No row: canonical is derived from the current name.
	id2 := ent(t, env.H, "person", "Bo Li", nil)
	mems(t, env.H, id2, 3)
	ix = buildIx(t, env, "", s, time.Now())
	if _, canon, ok := ix.Lookup(vaultart.GroupPeople, "x-"+vaultart.ShortID(id2)); !ok || canon != vaultart.ArticleSlug("Bo Li", id2) {
		t.Fatalf("%q %v", canon, ok)
	}
}

func TestAlsoMentionedBelowThreshold(t *testing.T) {
	env := open(t)
	s := vaultart.DefaultSettings()
	ada := ent(t, env.H, "person", "Ada", nil)
	big := ent(t, env.H, "person", "Big", nil)
	small := ent(t, env.H, "person", "Small", nil)
	viaRel := ent(t, env.H, "organization", "RelOrg", nil)
	acct := ent(t, env.H, "email address", "ada@x.test", nil)
	unrelated := ent(t, env.H, "person", "Unrelated", nil)
	mems(t, env.H, ada, 3)
	mems(t, env.H, big, 3)
	mems(t, env.H, unrelated, 1)
	putMem(t, env.H, "Ada met Small and Big", "t", 0.9, false, ada, small, big)
	putMem(t, env.H, "Ada mail", "t", 0.9, false, ada, acct)
	if err := graph.New(env.H).PutRelationship(bg, &model.Relationship{SourceID: ada, TargetID: viaRel, Type: "works_at"}); err != nil {
		t.Fatal(err)
	}
	ix := buildIx(t, env, "", s, time.Now())
	if entryFor(ix, small) != nil || entryFor(ix, viaRel) != nil {
		t.Fatal("test needs below-threshold entities")
	}
	got := ix.AlsoMentioned(personSub(ada, "Ada"))
	want := map[string]bool{"Small": true, "RelOrg": true}
	if len(got) != 2 || !want[got[0]] || !want[got[1]] {
		t.Fatalf("got %v", got)
	}
}

func TestIndexCanWriteFlag(t *testing.T) {
	env := open(t)
	id := ent(t, env.H, "person", "Ada", nil)
	mems(t, env.H, id, 3)
	ix := buildIx(t, env, "", vaultart.DefaultSettings(), time.Now())
	if ix.CanWrite {
		t.Fatal("default must be false")
	}
	on := ix.WithCanWrite(true)
	if !on.CanWrite || ix.CanWrite {
		t.Fatal("copy must carry the flag without touching the cached index")
	}
	if len(on.People) != 1 {
		t.Fatal("copy lost data")
	}
	if p, ok := on.PathFor(id); !ok || p == "" {
		t.Fatal("copy lost lookups")
	}
	if got := on.AlsoMentioned(personSub(id, "Ada")); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestIndexOwnerLeadsPeopleWithYou(t *testing.T) {
	env := open(t)
	a := ent(t, env.H, "person", "Aaron", nil)
	me := ent(t, env.H, "person", "Zed", nil)
	mems(t, env.H, a, 5)
	mems(t, env.H, me, 1)
	ix := buildIx(t, env, me, vaultart.DefaultSettings(), time.Now())
	if len(ix.People) != 2 || ix.People[0].Subject.ID != me || ix.People[0].Descriptor != "You" {
		t.Fatalf("%+v", ix.People)
	}
}

func TestIndexWarmUnder300msOwnerSized(t *testing.T) {
	if testing.Short() {
		t.Skip("owner-sized fixture")
	}
	env := open(t)
	s := vaultart.DefaultSettings()
	var people, orgs, concepts []string
	for i := 0; i < 800; i++ {
		people = append(people, ent(t, env.H, "person", fmt.Sprintf("Person %d", i), nil))
	}
	for i := 0; i < 400; i++ {
		orgs = append(orgs, ent(t, env.H, "organization", fmt.Sprintf("Org %d", i), nil))
	}
	for i := 0; i < 400; i++ {
		concepts = append(concepts, ent(t, env.H, "concept", fmt.Sprintf("Concept %d", i), nil))
	}
	subjects := append(append([]string{}, people...), orgs...)
	for i := 0; i < 4000; i++ {
		ids := []string{subjects[i%len(subjects)]}
		if j := (i*7 + 3) % len(subjects); j != i%len(subjects) {
			ids = append(ids, subjects[j])
		}
		if i%2 == 0 {
			ids = append(ids, concepts[i%len(concepts)])
			if j := (i / 3) % len(concepts); j != i%len(concepts) {
				ids = append(ids, concepts[j])
			}
		}
		putMem(t, env.H, fmt.Sprintf("memory %d", i), "t", 0.8, false, ids...)
	}
	set := getSet(t, env)
	now := time.Now()
	start := time.Now()
	ix, err := vaultart.BuildIndex(bg, env.H, set, "", s, now)
	if err != nil {
		t.Fatal(err)
	}
	cold := time.Since(start)
	// Write a handful of articles so the warm path also sees row metadata.
	for _, e := range ix.People[:20] {
		writeRow(t, env, e.Subject, s)
	}
	set = getSet(t, env)
	if _, err := vaultart.BuildIndex(bg, env.H, set, "", s, now); err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	for i := 0; i < 5; i++ {
		if _, err := vaultart.BuildIndex(bg, env.H, set, "", s, now); err != nil {
			t.Fatal(err)
		}
	}
	warm := time.Since(start) / 5
	t.Logf("people %d projects %d topics %d; cold %v; warm %v", len(ix.People), len(ix.Projects), len(ix.Topics), cold, warm)
	if warm > 300*time.Millisecond {
		t.Fatalf("warm %v over 300ms", warm)
	}
}
