package vaultart

import (
	"sort"
	"strings"
)

// Groups is a set of index rows split like Vault home.
type Groups struct {
	People, Projects, Events, Topics []IndexEntry
}

// Empty reports whether no group has a row.
func (g Groups) Empty() bool {
	return len(g.People)+len(g.Projects)+len(g.Events)+len(g.Topics) == 0
}

// Search keeps the rows whose name or alias contains q, case-insensitively.
// It only ever looks at subjects that qualify (the index holds no others), so
// it cannot reveal an entity without an article. A topic matches on its title
// only: its members are not qualifying names. An empty q matches nothing.
func (x *Index) Search(q string) Groups {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return Groups{}
	}
	aliases := map[string][]string{}
	for _, a := range x.set.Aliases() {
		aliases[a.EntityID] = append(aliases[a.EntityID], a.Value)
	}
	match := func(e IndexEntry) bool {
		if strings.Contains(strings.ToLower(e.Name), q) {
			return true
		}
		if e.Subject.Kind != "entity" {
			return false
		}
		for _, a := range aliases[e.Subject.ID] {
			if strings.Contains(strings.ToLower(a), q) {
				return true
			}
		}
		return false
	}
	keep := func(in []IndexEntry) []IndexEntry {
		var out []IndexEntry
		for _, e := range in {
			if match(e) {
				out = append(out, e)
			}
		}
		return out
	}
	return Groups{People: keep(x.People), Projects: keep(x.Projects), Events: keep(x.Events), Topics: keep(x.Topics)}
}

// Linkables maps every name and alias of the qualifying subjects other than
// self to its article, as Validate wants them. A name shared by two different
// articles is left out, so it renders as plain text rather than guess.
func (x *Index) Linkables(self Subject) map[string]LinkTarget {
	out := map[string]LinkTarget{}
	dup := map[string]bool{}
	put := func(name string, t LinkTarget) {
		name = strings.TrimSpace(name)
		if name == "" || dup[name] {
			return
		}
		if prev, ok := out[name]; ok && (prev.Group != t.Group || prev.Slug != t.Slug) {
			delete(out, name)
			dup[name] = true
			return
		}
		out[name] = t
	}
	byID := map[string]LinkTarget{}
	for _, it := range x.items {
		e := it.entry
		if e.Subject.ID == self.ID {
			continue
		}
		t := LinkTarget{Group: e.Group, Slug: it.slug, Name: e.Name}
		if e.Subject.Kind == "entity" {
			byID[e.Subject.ID] = t
		}
		put(e.Name, t)
	}
	for _, a := range x.set.Aliases() {
		if t, ok := byID[a.EntityID]; ok {
			put(a.Value, t)
		}
	}
	return out
}

// Related lists the other articles most connected to the subject, strongest
// first: each live relationship and each shared live memory counts one. An
// entity that belongs to a topic counts toward the topic's article. Only
// subjects that qualify appear, so every row has an article to open.
func (x *Index) Related(sub Subject, limit int) []IndexEntry {
	self := x.byID[sub.ID]
	ix := x.relIndex()
	own := map[string]bool{}
	for _, id := range sub.entityIDs() {
		own[id] = true
	}
	score := map[*idxItem]int{}
	add := func(id string) {
		if own[id] {
			return
		}
		var it *idxItem
		if i, ok := x.byID[id]; ok && i.entry.Subject.Kind == "entity" {
			it = i
		} else if t, ok := x.topicOf[id]; ok {
			it = t
		}
		if it == nil || it == self {
			return
		}
		score[it]++
	}
	for id := range own {
		for _, r := range ix.rels[id] {
			if r.InvalidAt != nil {
				continue
			}
			other := r.SourceID
			if other == id {
				other = r.TargetID
			}
			add(other)
		}
		for _, m := range ix.mems[id] {
			for _, o := range m.EntityIDs {
				add(o)
			}
		}
	}
	items := make([]*idxItem, 0, len(score))
	for it := range score {
		items = append(items, it)
	}
	sort.Slice(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if score[a] != score[b] {
			return score[a] > score[b]
		}
		if a.entry.Name != b.entry.Name {
			return a.entry.Name < b.entry.Name
		}
		return a.entry.Subject.ID < b.entry.Subject.ID
	})
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	out := make([]IndexEntry, len(items))
	for i, it := range items {
		out[i] = it.entry
	}
	return out
}

// NamesFor returns every name and alias that resolves to the article at
// group and slug, longest first, for placing inline links in prose.
func NamesFor(links map[string]LinkTarget, t LinkTarget) []string {
	var out []string
	for name, lt := range links {
		if lt.Group == t.Group && lt.Slug == t.Slug {
			out = append(out, name)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if len(out[i]) != len(out[j]) {
			return len(out[i]) > len(out[j])
		}
		return out[i] < out[j]
	})
	return out
}

// Entry returns the index row of a subject (an entity id or a topic id).
func (x *Index) Entry(id string) (IndexEntry, bool) {
	it, ok := x.byID[id]
	if !ok {
		return IndexEntry{}, false
	}
	return it.entry, true
}
