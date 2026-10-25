package vaultart_test

import (
	"strings"
	"testing"
	"time"

	"github.com/sausheong/cortex/internal/graph"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/vaultart"
)

func legacySlugOf(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), " ", "-")
}

// Every folder the old projection could write maps back to an article when
// the entity qualifies (or its topic does) and to nothing when it does not.
func TestLegacyEveryFolderTable(t *testing.T) {
	env := open(t)
	cases := []struct {
		typ, folder string
		wantOK      bool
	}{
		{"person", "people", true},
		{"organization", "organizations", true},
		{"project", "projects", true},
		{"event", "events", true},
		{"concept", "concepts", true}, // folded into a topic
		{"document", "documents", true},
		{"email address", "email-addresss", false},
		{"email address", "email addresss", false},
		{"merchant", "merchants", true},    // canonicalises to a project
		{"person", "organizations", false}, // wrong folder
	}
	names := map[string]string{}
	var topicIDs []string
	for i, c := range cases {
		name := "Name " + string(rune('A'+i)) + " Thing"
		names[c.typ+"|"+c.folder] = name
		id := ent(t, env.H, c.typ, name, nil)
		mems(t, env.H, id, 3)
		if c.typ == "concept" || c.typ == "document" {
			topicIDs = append(topicIDs, id)
		}
	}
	// Concept and document entities cluster only when connected.
	putMem(t, env.H, "link", "t", 0.9, false, topicIDs...)
	ix := buildIx(t, env, "", vaultart.DefaultSettings(), time.Now())
	set := getSet(t, env)
	for _, c := range cases {
		name := names[c.typ+"|"+c.folder]
		path := c.folder + "/" + legacySlugOf(name) + ".md"
		got, ok := vaultart.ResolveLegacy(path, ix, set)
		if c.wantOK && !ok {
			t.Errorf("%s: not resolved", path)
		}
		if !c.wantOK && ok {
			t.Errorf("%s: resolved to %s", path, got)
		}
		if ok && !strings.HasPrefix(got, "/vault/") {
			t.Errorf("%s -> %s", path, got)
		}
	}
}

func TestLegacySuffixedAndPlainForms(t *testing.T) {
	env := open(t)
	id := ent(t, env.H, "person", "Ada Lovelace", nil)
	mems(t, env.H, id, 3)
	ix := buildIx(t, env, "", vaultart.DefaultSettings(), time.Now())
	set := getSet(t, env)
	want := ix.People[0].Path
	tail := strings.ToLower(id[len(id)-6:])
	for _, p := range []string{"people/ada-lovelace.md", "people/ada-lovelace-" + tail + ".md"} {
		got, ok := vaultart.ResolveLegacy(p, ix, set)
		if !ok || got != want {
			t.Errorf("%s: %q %v, want %q", p, got, ok, want)
		}
	}
	for _, p := range []string{
		"people/ada-lovelace", "people/ada-lovelace.md/", "PEOPLE/ada-lovelace.md", "people/ada-lovelace-zzzzzz.md",
		"people/nobody.md", "ada-lovelace.md", "people//ada-lovelace.md", "../people/ada-lovelace.md", "people/a/b.md", "",
	} {
		if got, ok := vaultart.ResolveLegacy(p, ix, set); ok {
			t.Errorf("%q resolved to %q", p, got)
		}
	}
	for _, p := range []string{"index.md", "log.md"} {
		if got, ok := vaultart.ResolveLegacy(p, ix, set); !ok || got != "/vault" {
			t.Errorf("%s: %q %v", p, got, ok)
		}
	}
}

func TestLegacyNonQualifyingAndAmbiguous(t *testing.T) {
	env := open(t)
	low := ent(t, env.H, "person", "Low Count", nil)
	mems(t, env.H, low, 2)
	a := ent(t, env.H, "person", "Sam Smith", nil)
	b := ent(t, env.H, "person", "Sam-Smith", nil)
	mems(t, env.H, a, 3)
	mems(t, env.H, b, 3)
	ix := buildIx(t, env, "", vaultart.DefaultSettings(), time.Now())
	set := getSet(t, env)
	if _, ok := vaultart.ResolveLegacy("people/low-count.md", ix, set); ok {
		t.Error("non-qualifying resolved")
	}
	if _, ok := vaultart.ResolveLegacy("people/sam-smith.md", ix, set); ok {
		t.Error("ambiguous plain slug resolved")
	}
	// The collision form names one of them exactly.
	tail := strings.ToLower(a[len(a)-6:])
	got, ok := vaultart.ResolveLegacy("people/sam-smith-"+tail+".md", ix, set)
	if !ok || got != pathOf(ix, a) {
		t.Errorf("suffixed: %q %v", got, ok)
	}
}

func pathOf(ix *vaultart.Index, id string) string {
	p, _ := ix.PathFor(id)
	return p
}

func TestIndexSearchOnlyQualifyingNamesAndAliases(t *testing.T) {
	env := open(t)
	ada := ent(t, env.H, "person", "Ada Lovelace", nil)
	mems(t, env.H, ada, 3)
	low := ent(t, env.H, "person", "Lowly Person", nil)
	mems(t, env.H, low, 1)
	if err := graph.New(env.H).AddAlias(bg, ada, model.AliasName, "Countess"); err != nil {
		t.Fatal(err)
	}
	ix := buildIx(t, env, "", vaultart.DefaultSettings(), time.Now())
	if g := ix.Search("lovelace"); len(g.People) != 1 {
		t.Fatalf("%+v", g)
	}
	if g := ix.Search("countess"); len(g.People) != 1 {
		t.Fatalf("alias: %+v", g)
	}
	if g := ix.Search("lowly"); !g.Empty() {
		t.Fatalf("non-qualifying found: %+v", g)
	}
	if g := ix.Search("  "); !g.Empty() {
		t.Fatal("blank query matched")
	}
}

func TestIndexRelatedOrderedAndLinkables(t *testing.T) {
	env := open(t)
	a := ent(t, env.H, "person", "Ada", nil)
	b := ent(t, env.H, "person", "Bea", nil)
	c := ent(t, env.H, "person", "Cy", nil)
	for _, id := range []string{a, b, c} {
		mems(t, env.H, id, 3)
	}
	putMem(t, env.H, "ada and bea", "t", 0.9, false, a, b)
	putMem(t, env.H, "ada and bea again", "t", 0.9, false, a, b)
	putMem(t, env.H, "ada and cy", "t", 0.9, false, a, c)
	ix := buildIx(t, env, "", vaultart.DefaultSettings(), time.Now())
	sub := entryFor(ix, a).Subject
	rel := ix.Related(sub, 5)
	if len(rel) != 2 || rel[0].Name != "Bea" || rel[1].Name != "Cy" {
		t.Fatalf("%+v", rel)
	}
	if got := ix.Related(sub, 1); len(got) != 1 {
		t.Fatal("limit")
	}
	lk := ix.Linkables(sub)
	if _, ok := lk["Ada"]; ok {
		t.Fatal("self linkable")
	}
	if lk["Bea"].Name != "Bea" || lk["Cy"].Slug == "" {
		t.Fatalf("%+v", lk)
	}
}
