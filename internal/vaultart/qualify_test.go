package vaultart_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/enctest"
	"github.com/sausheong/cortex/internal/graph"
	"github.com/sausheong/cortex/internal/memory"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
	"github.com/sausheong/cortex/internal/vaultart"
	"github.com/sausheong/cortex/internal/workset"
)

var bg = context.Background()

func open(t *testing.T) *enctest.Env {
	t.Helper()
	workset.Reset()
	t.Cleanup(workset.Reset)
	return enctest.Open(t, "encrypted")
}

func ent(t *testing.T, h *store.Handle, typ, name string, attrs map[string]any) string {
	t.Helper()
	e := &model.Entity{Type: typ, Name: name, Attributes: attrs}
	if err := graph.New(h).PutEntity(bg, e); err != nil {
		t.Fatal(err)
	}
	return e.ID
}

func mems(t *testing.T, h *store.Handle, id string, n int) []string {
	t.Helper()
	var ids []string
	for i := 0; i < n; i++ {
		m := &model.Memory{Content: fmt.Sprintf("fact %d about %s", i, id), Source: "t", EntityIDs: []string{id}}
		if err := memory.New(h).PutMemory(bg, m); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
	}
	return ids
}

func qualify(t *testing.T, h *store.Handle, owner string, s vaultart.Settings) []vaultart.Candidate {
	t.Helper()
	set, err := workset.For(bg, h)
	if err != nil {
		t.Fatal(err)
	}
	return vaultart.Qualify(set, owner, s, time.Now())
}

func find(cs []vaultart.Candidate, id string) *vaultart.Candidate {
	for i := range cs {
		if cs[i].ID == id {
			return &cs[i]
		}
	}
	return nil
}

func TestQualifyThreshold(t *testing.T) {
	env := open(t)
	three := ent(t, env.H, "person", "Three", nil)
	two := ent(t, env.H, "person", "Two", nil)
	inv := ent(t, env.H, "person", "Invalidated", nil)
	mems(t, env.H, three, 3)
	mems(t, env.H, two, 2)
	ids := mems(t, env.H, inv, 3)
	env.Exec(`UPDATE memories SET invalid_at = now() - interval '1 hour' WHERE id = $1`, ids[0])
	workset.Invalidate(env.UserID)
	cs := qualify(t, env.H, "", vaultart.DefaultSettings())
	if find(cs, three) == nil || find(cs, two) != nil || find(cs, inv) != nil {
		t.Fatalf("%+v", cs)
	}
	if c := find(cs, three); c.Memories != 3 || c.Group != vaultart.GroupPeople || c.Descriptor != "Person" {
		t.Fatalf("%+v", c)
	}
}

func TestQualifyConnectedToOwner(t *testing.T) {
	env := open(t)
	owner := ent(t, env.H, "person", "Owner", nil)
	friend := ent(t, env.H, "person", "Friend", nil)
	stranger := ent(t, env.H, "person", "Stranger", nil)
	mems(t, env.H, friend, 2)
	mems(t, env.H, stranger, 2)
	if err := graph.New(env.H).PutRelationship(bg, &model.Relationship{SourceID: owner, TargetID: friend, Type: "knows"}); err != nil {
		t.Fatal(err)
	}
	s := vaultart.DefaultSettings()
	cs := qualify(t, env.H, owner, s)
	if c := find(cs, friend); c == nil || !c.ConnectedToOwner {
		t.Fatalf("%+v", cs)
	}
	if find(cs, stranger) != nil {
		t.Fatal("stranger qualified")
	}
	s.IncludeConnected = false
	if find(qualify(t, env.H, owner, s), friend) != nil {
		t.Fatal("connected qualified with setting off")
	}
}

func TestQualifyExcludesAccountLikeAndConcepts(t *testing.T) {
	env := open(t)
	// Type strings seen in real data and the ontology: free-text labels map
	// through the ontology; none of these gets a standalone article.
	excluded := []string{
		"account", "email address", "email_address", "phone number", "username", "handle", "identifier",
		"concept", "topic", "skill", "document", "email", "article", "product", "financial_products",
		"financial product", "payment card", "place", "city", "group", "team", "mystery thing",
	}
	var ids []string
	for i, typ := range excluded {
		id := ent(t, env.H, typ, fmt.Sprintf("X%d", i), nil)
		mems(t, env.H, id, 5)
		ids = append(ids, id)
	}
	cs := qualify(t, env.H, "", vaultart.DefaultSettings())
	for i, id := range ids {
		if find(cs, id) != nil {
			t.Errorf("%q qualified", excluded[i])
		}
	}
	for _, typ := range []string{"email address", "email addresss", "phone number", "username"} {
		if !vaultart.IsAccountLike(typ) || vaultart.TopicEligible(typ) {
			t.Errorf("account-like %q", typ)
		}
	}
	for _, typ := range []string{"concept", "document", "product", "place", "group", "financial_products", "team"} {
		if !vaultart.TopicEligible(typ) || vaultart.IsAccountLike(typ) {
			t.Errorf("topic-eligible %q", typ)
		}
	}
	for _, typ := range []string{"person", "organization", "project", "event", "merchant"} {
		if vaultart.TopicEligible(typ) || vaultart.IsAccountLike(typ) {
			t.Errorf("article type %q", typ)
		}
	}
}

func TestQualifyGroupsOrgsAndEventsWithProjects(t *testing.T) {
	env := open(t)
	org := ent(t, env.H, "organization", "Acme", map[string]any{"description": "Maker \u2014 of anvils"})
	merchant := ent(t, env.H, "merchant", "Corner Shop", nil)
	proj := ent(t, env.H, "project", "Apollo", nil)
	evt := ent(t, env.H, "event", "Launch", map[string]any{"role": "  "})
	per := ent(t, env.H, "person", "Ada", map[string]any{"role": "Engineer"})
	for _, id := range []string{org, merchant, proj, evt, per} {
		mems(t, env.H, id, 3)
	}
	cs := qualify(t, env.H, "", vaultart.DefaultSettings())
	for _, id := range []string{org, merchant, proj, evt} {
		if c := find(cs, id); c == nil || c.Group != vaultart.GroupProjects {
			t.Fatalf("%s: %+v", id, c)
		}
	}
	if c := find(cs, evt); c.Kind != "event" || c.Descriptor != "Event" {
		t.Fatalf("%+v", c)
	}
	if c := find(cs, org); c.Descriptor != "Maker - of anvils" || c.Kind != "organization" {
		t.Fatalf("%+v", c)
	}
	if c := find(cs, per); c.Group != vaultart.GroupPeople || c.Descriptor != "Engineer" {
		t.Fatalf("%+v", c)
	}
}

func TestQualifyOwnerAlwaysQualifies(t *testing.T) {
	env := open(t)
	owner := ent(t, env.H, "person", "Me", nil)
	cs := qualify(t, env.H, owner, vaultart.Settings{Threshold: 50})
	c := find(cs, owner)
	if c == nil || !c.IsOwner || c.Descriptor != "You" || c.Group != vaultart.GroupPeople {
		t.Fatalf("%+v", cs)
	}
}

func TestSettingsNormalize(t *testing.T) {
	cases := []struct {
		in   vaultart.Settings
		want vaultart.Settings
	}{
		{vaultart.Settings{Threshold: 0, Length: ""}, vaultart.Settings{Threshold: 1, Length: "standard"}},
		{vaultart.Settings{Threshold: 99, Length: "long"}, vaultart.Settings{Threshold: 50, Length: "long"}},
		{vaultart.Settings{Threshold: 7, Length: "epic", IncludeConnected: true}, vaultart.Settings{Threshold: 7, Length: "standard", IncludeConnected: true}},
		{vaultart.Settings{Threshold: -4, Length: "short"}, vaultart.Settings{Threshold: 1, Length: "short"}},
	}
	for _, c := range cases {
		if got := c.in.Normalize(); got != c.want {
			t.Errorf("%+v -> %+v want %+v", c.in, got, c.want)
		}
	}
	d := vaultart.DefaultSettings()
	if d.Threshold != 3 || !d.IncludeConnected || d.Length != "standard" {
		t.Fatalf("%+v", d)
	}
}

func TestSettingsLoadSave(t *testing.T) {
	env := open(t)
	got, err := vaultart.LoadSettings(bg, env.H)
	if err != nil || got != vaultart.DefaultSettings() {
		t.Fatal(got, err)
	}
	want := vaultart.Settings{Threshold: 9, IncludeConnected: false, Length: "long"}
	if err := vaultart.SaveSettings(bg, env.H, want); err != nil {
		t.Fatal(err)
	}
	if got, err := vaultart.LoadSettings(bg, env.H); err != nil || got != want {
		t.Fatal(got, err)
	}
	if _, err := vaultart.LoadSettings(bg, env.Keyless); err == nil {
		t.Fatal("keyless load succeeded")
	}
}

func TestSettingsSurviveProviderSave(t *testing.T) {
	env := open(t)
	want := vaultart.Settings{Threshold: 12, IncludeConnected: false, Length: "short"}
	if err := vaultart.SaveSettings(bg, env.H, want); err != nil {
		t.Fatal(err)
	}
	st := &accounts.Store{DB: env.DB, Now: time.Now}
	ps := accounts.Settings{Provider: accounts.ProviderOpenAI, BaseURL: "https://llm.example/v1", APIKey: "sk-test-1234",
		ChatModel: "chat", EmbedModel: "embed", EmbedDims: 512}
	if _, err := st.PutSettings(bg, env.UserID, ps, env.UK); err != nil {
		t.Fatal(err)
	}
	if got, err := vaultart.LoadSettings(bg, env.H); err != nil || got != want {
		t.Fatal(got, err)
	}
}

func TestThresholdChangeNeverDeletesArticles(t *testing.T) {
	env := open(t)
	id := ent(t, env.H, "person", "Ada", nil)
	mems(t, env.H, id, 3)
	row := store.ArticleRow{SubjectID: id, Kind: "entity", ShortID: "abcd1234", Slug: "ada-abcd1234",
		Fingerprint: []byte{1}, Article: model.Article{Title: "Ada", Lead: "x", LeadSources: []string{"m"}}}
	if err := env.H.PutArticle(bg, row); err != nil {
		t.Fatal(err)
	}
	if find(qualify(t, env.H, "", vaultart.DefaultSettings()), id) == nil {
		t.Fatal("should qualify")
	}
	if find(qualify(t, env.H, "", vaultart.Settings{Threshold: 50}), id) != nil {
		t.Fatal("should not qualify at 50")
	}
	meta, err := env.H.ListArticleMeta(bg)
	if err != nil || len(meta) != 1 || meta[0].SubjectID != id {
		t.Fatal(meta, err)
	}
}

func TestTwoUsersSameNamesIndependent(t *testing.T) {
	env := open(t)
	u2 := env.NewUser("second", "encrypted")
	h2 := env.HandleFor(u2, "encrypted", env.UK)
	a1 := ent(t, env.H, "person", "Ada", nil)
	a2 := ent(t, h2, "person", "Ada", nil)
	mems(t, env.H, a1, 3)
	mems(t, h2, a2, 1)
	c1 := qualify(t, env.H, "", vaultart.DefaultSettings())
	c2 := qualify(t, h2, "", vaultart.DefaultSettings())
	if find(c1, a1) == nil || len(c1) != 1 || find(c1, a2) != nil {
		t.Fatalf("user1 %+v", c1)
	}
	if len(c2) != 0 {
		t.Fatalf("user2 %+v", c2)
	}
	if err := vaultart.SaveSettings(bg, env.H, vaultart.Settings{Threshold: 1, Length: "long"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := vaultart.LoadSettings(bg, h2); got != vaultart.DefaultSettings() {
		t.Fatalf("settings leaked: %+v", got)
	}
	if c2 = qualify(t, h2, "", vaultart.Settings{Threshold: 1}); find(c2, a2) == nil {
		t.Fatal("user2 at threshold 1")
	}
}
