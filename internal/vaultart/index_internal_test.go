package vaultart

import "testing"

// Entity ids are random, so two subjects sharing a short id cannot be made
// through the store (topic short ids are unique in the database). The guard
// is exercised on a hand-built index.
func TestLookupAmbiguousShortIDIsNotFound(t *testing.T) {
	mk := func(id, name string) *idxItem {
		sub := Subject{ID: id, Group: GroupPeople, Kind: "entity", Name: name}
		return &idxItem{slug: ArticleSlug(name, id), short: ShortID(id), entry: IndexEntry{Subject: sub, Group: GroupPeople}}
	}
	a, b, c := mk("01AAAAAAAAAAAAAAAAAAAAAAAA", "Ada"), mk("01BBBBBBBBBBBBBBBBBBAAAAAAAA", "Bo"), mk("01CCCCCCCCCCCCCCCCCCCCCCCZ", "Cy")
	a.short, b.short = "aaaaaaaa", "aaaaaaaa"
	c.short = "cccccccz"
	x := &Index{byShort: map[Group]map[string][]*idxItem{GroupPeople: {
		"aaaaaaaa": {a, b}, "cccccccz": {c},
	}}}
	if _, _, ok := x.Lookup(GroupPeople, "ada-aaaaaaaa"); ok {
		t.Fatal("ambiguous short id must be not found")
	}
	if _, slug, ok := x.Lookup(GroupPeople, "anything-cccccccz"); !ok || slug != c.slug {
		t.Fatalf("%q %v", slug, ok)
	}
}
