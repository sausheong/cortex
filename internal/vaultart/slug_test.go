package vaultart_test

import (
	"strings"
	"testing"

	"github.com/sausheong/cortex/internal/graph"
	"github.com/sausheong/cortex/internal/vaultart"
	"github.com/sausheong/cortex/internal/workset"
)

func TestSlugAsciiFoldAndEmpty(t *testing.T) {
	cases := map[string]string{
		"Crème Brûlée & Co.": "creme-brulee-co",
		"  Hello   World  ":  "hello-world",
		"Straße Œuvre Łódź":  "strasse-oeuvre-lodz",
		"日本語":                "untitled",
		"":                   "untitled",
		"---":                "untitled",
		"Project X-2":        "project-x-2",
	}
	for in, want := range cases {
		if got := vaultart.Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
	long := vaultart.Slug(strings.Repeat("word ", 40))
	if len(long) > 48 || strings.HasSuffix(long, "-") {
		t.Fatalf("long slug %q (%d)", long, len(long))
	}
}

func TestSlugStableAfterRename(t *testing.T) {
	env := open(t)
	a := ent(t, env.H, "concept", "Sourdough", nil)
	b := ent(t, env.H, "concept", "Starter", nil)
	mems(t, env.H, a, 3)
	mems(t, env.H, b, 1)
	linkMem(t, env.H, 2, a, b)
	set, _ := workset.For(bg, env.H)
	first, err := vaultart.LoadTopics(bg, env.H, set, vaultart.DefaultSettings())
	if err != nil || len(first) != 1 {
		t.Fatalf("%v %+v", err, first)
	}
	if err := graph.New(env.H).RenameEntity(bg, a, "Bread baking"); err != nil {
		t.Fatal(err)
	}
	workset.Invalidate(env.UserID)
	set, _ = workset.For(bg, env.H)
	second, err := vaultart.LoadTopics(bg, env.H, set, vaultart.DefaultSettings())
	if err != nil || len(second) != 1 {
		t.Fatalf("%v %+v", err, second)
	}
	if first[0].ID != second[0].ID || first[0].Slug != second[0].Slug {
		t.Fatalf("slug or id moved: %+v -> %+v", first[0], second[0])
	}
	if !strings.HasPrefix(first[0].Slug, "sourdough-") || second[0].Title != "Bread baking" {
		t.Fatalf("%+v %+v", first[0], second[0])
	}
}

func TestParseSlugAndWrongNamePart(t *testing.T) {
	id := "01HZX3ABCDEFGHJKMNPQRSTVWX"
	slug := vaultart.ArticleSlug("Alice Smith", id)
	short, ok := vaultart.ParseSlug(slug)
	if !ok || short != vaultart.ShortID(id) || short != "pqrstvwx" {
		t.Fatalf("%q %v", short, ok)
	}
	wrong, ok := vaultart.ParseSlug("someone-else-entirely-" + vaultart.ShortID(id))
	if !ok || wrong != short {
		t.Fatalf("wrong name part must still resolve: %q %v", wrong, ok)
	}
	for _, bad := range []string{"", "nohyphen", "alice-short", "alice-toolongshortid", "-pqrstvwx", "alice-pqrst!wx"} {
		if _, ok := vaultart.ParseSlug(bad); ok {
			t.Errorf("ParseSlug(%q) should fail", bad)
		}
	}
	if got := vaultart.Path(vaultart.GroupPeople, slug); got != "/vault/people/"+slug {
		t.Fatal(got)
	}
}

func TestShortIDAmbiguity(t *testing.T) {
	a := "01HZX3AAAAAAAAAAAAAAAAAAAA"
	b := "01HZX4ZZZZZZZZZZZZAAAAAAAA" // same last 8 as a
	c := "01HZX3BBBBBBBBBBBBBBBBBBBB"
	ids := []string{a, b, c}
	if _, found, amb := vaultart.ResolveShortID(ids, vaultart.ShortID(a)); found || !amb {
		t.Fatalf("found=%v amb=%v", found, amb)
	}
	id, found, amb := vaultart.ResolveShortID(ids, vaultart.ShortID(c))
	if !found || amb || id != c {
		t.Fatal(id, found, amb)
	}
	if _, found, amb := vaultart.ResolveShortID(ids, "zzzzzzzz"); found || amb {
		t.Fatal("unknown should be neither")
	}
}
