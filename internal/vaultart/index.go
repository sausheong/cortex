package vaultart

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
	"github.com/sausheong/cortex/internal/workset"
)

const (
	// eventsSplit is the number of qualifying events above which they list
	// under their own Events section instead of folding into Projects.
	eventsSplit = 12
	// newlyQualifiedWindow is how recent the latest linked memory must be for
	// a not-yet-written subject to count as newly qualified.
	newlyQualifiedWindow = 14 * 24 * time.Hour
	// recentCap caps the "Recently changed" list.
	recentCap = 10
	// alsoMentionedCap caps AlsoMentioned.
	alsoMentionedCap = 12
	// nowBucket is how coarsely the cache key follows the clock, since
	// NewlyQualified depends on it.
	nowBucket = 10 * time.Minute
)

// IndexEntry is one row of Vault home.
type IndexEntry struct {
	Subject    Subject
	Name       string
	Descriptor string
	Path       string
	Group      Group
	// Kind is the canonical entity type ("person", "organization", "project",
	// "event") or "topic". Events keep Group projects.
	Kind string
	// WrittenAt is nil until an article exists.
	WrittenAt *time.Time
	// Changed: an article exists and is stale or its stored fingerprint no
	// longer matches the current data.
	Changed bool
	// NewlyQualified: the subject qualifies, has no article row, and its
	// latest linked memory is within 14 days.
	NewlyQualified bool

	activity time.Time // latest linked memory, for ordering Recent
}

// Index is the article index of one user: what is listed on Vault home and
// how entities, topics and URLs map to articles. It is built from the graph
// alone, with no model call and no article body decrypted.
type Index struct {
	People, Projects, Events, Topics, Recent []IndexEntry
	// CanWrite is whether the user's chat model implements
	// model.ArticleWriter. BuildIndex leaves it false; the caller sets it on
	// its own copy with WithCanWrite so the cached index stays shared.
	CanWrite bool

	set     *workset.Set
	items   []*idxItem
	byID    map[string]*idxItem
	byShort map[Group]map[string][]*idxItem
	topicOf map[string]*idxItem // topic member entity id -> topic
	rel     *lazyIndex
}

// lazyIndex builds the relationship index on first use and is shared by every
// copy of an Index.
type lazyIndex struct {
	once sync.Once
	ix   *index
}

type idxItem struct {
	entry IndexEntry
	slug  string
	short string
}

// WithCanWrite returns a shallow copy of the index with CanWrite set. The
// shared maps are read-only after build, so copies are safe.
func (x *Index) WithCanWrite(v bool) *Index {
	c := *x
	c.CanWrite = v
	return &c
}

func (x *Index) relIndex() *index {
	x.rel.once.Do(func() { x.rel.ix = buildIndex(x.set) })
	return x.rel.ix
}

type cacheEntry struct {
	key string
	ix  *Index
}

var (
	indexMu    sync.Mutex
	indexCache = map[string]cacheEntry{}
)

func init() {
	workset.OnEvict(DropIndex)
}

// DropIndex forgets the cached index of one user; it runs when the user's
// working set is evicted so no decrypted names outlive it.
func DropIndex(userID string) {
	indexMu.Lock()
	delete(indexCache, userID)
	indexMu.Unlock()
}

// ResetIndexes forgets every cached index.
func ResetIndexes() {
	indexMu.Lock()
	indexCache = map[string]cacheEntry{}
	indexMu.Unlock()
}

func latestMemory(set *workset.Set) map[string]time.Time {
	out := map[string]time.Time{}
	for _, m := range set.MemoriesMode(model.TemporalMode{}) {
		for _, id := range m.EntityIDs {
			if m.CreatedAt.After(out[id]) {
				out[id] = m.CreatedAt
			}
		}
	}
	return out
}

// BuildIndex returns the user's article index. It reads the article rows
// without opening any article body (one slug is opened per written subject),
// qualifies entities, loads topics and fingerprints the written subjects. The
// result is cached per user and reused while the working-set version, the
// settings, the article rows and the clock bucket are unchanged.
func BuildIndex(ctx context.Context, h *store.Handle, set *workset.Set, ownerID string, s Settings, now time.Time) (*Index, error) {
	s = s.Normalize()
	metas, err := h.ListArticleMeta(ctx)
	if err != nil {
		return nil, err
	}
	var maxAt time.Time
	stale := 0
	for _, m := range metas {
		if m.WrittenAt.After(maxAt) {
			maxAt = m.WrittenAt
		}
		if m.Stale {
			stale++
		}
	}
	key := fmt.Sprintf("%p|%s|%s|%d.%t.%s|%d|%d|%d|%d", h.DB, set.Version(), ownerID,
		s.Threshold, s.IncludeConnected, s.Length, len(metas), maxAt.UnixNano(), stale, now.UnixNano()/int64(nowBucket))
	indexMu.Lock()
	if c, ok := indexCache[h.UserID]; ok && c.key == key {
		indexMu.Unlock()
		return c.ix, nil
	}
	indexMu.Unlock()

	slugs, err := h.ListArticleSlugs(ctx)
	if err != nil {
		return nil, err
	}
	topics, err := LoadTopics(ctx, h, set, s)
	if err != nil {
		return nil, err
	}
	cands := Qualify(set, ownerID, s, now)
	latest := latestMemory(set)

	rows := map[string]store.ArticleRow{}
	for _, m := range metas {
		rows[m.SubjectID] = m
	}

	x := &Index{
		set:     set,
		byID:    map[string]*idxItem{},
		byShort: map[Group]map[string][]*idxItem{},
		topicOf: map[string]*idxItem{},
		rel:     &lazyIndex{},
	}
	var subs []Subject
	add := func(sub Subject, name, desc, kind, derivedSlug, short string, activity time.Time) {
		it := &idxItem{short: short}
		e := IndexEntry{Subject: sub, Name: name, Descriptor: desc, Group: sub.Group, Kind: kind, activity: activity}
		it.slug = derivedSlug
		kindWant := "entity"
		if sub.Kind == "topic" {
			kindWant = "topic"
		}
		if r, ok := rows[sub.ID]; ok && r.Kind == kindWant {
			if sl := slugs[sub.ID]; sl != "" {
				it.slug = sl
			}
			at := r.WrittenAt
			e.WrittenAt = &at
		}
		e.Path = Path(sub.Group, it.slug)
		it.entry = e
		x.items = append(x.items, it)
		x.byID[sub.ID] = it
		if x.byShort[sub.Group] == nil {
			x.byShort[sub.Group] = map[string][]*idxItem{}
		}
		x.byShort[sub.Group][short] = append(x.byShort[sub.Group][short], it)
		subs = append(subs, sub)
	}

	for _, c := range cands {
		sub := Subject{ID: c.ID, Group: c.Group, Kind: "entity", Name: c.Name}
		add(sub, c.Name, c.Descriptor, c.Kind, ArticleSlug(c.Name, c.ID), ShortID(c.ID), latest[c.ID])
	}
	topicIDs := map[string]bool{}
	for _, t := range topics {
		var act time.Time
		for _, m := range t.Members {
			if latest[m].After(act) {
				act = latest[m]
			}
		}
		// Subject.Name is the stable title (strongest member), not the
		// model-written one: the fingerprint covers it, and the written
		// title is set after the article is stored.
		sub := Subject{ID: t.ID, Group: GroupTopics, Kind: "topic", Name: t.Title, Members: append([]string(nil), t.Members...)}
		derived := t.Slug
		if derived == "" {
			derived = ArticleSlug(t.Title, t.ID)
		}
		short := t.ShortID
		if short == "" {
			short = ShortID(t.ID)
		}
		add(sub, t.DisplayTitle(), typeLabel("topic"), "topic", derived, short, act)
		topicIDs[t.ID] = true
		it := x.byID[t.ID]
		for _, m := range t.Members {
			x.topicOf[m] = it
		}
	}

	// Fingerprints only for subjects that have an article to compare with.
	var written []Subject
	for _, sub := range subs {
		if x.byID[sub.ID].entry.WrittenAt != nil {
			written = append(written, sub)
		}
	}
	ownerName := ""
	if e, ok := set.EntityByID(ownerID); ok && ownerID != "" {
		ownerName = e.Name
	}
	fps := FingerprintAll(h, set, written, ownerName, s)

	var recent []*idxItem
	for _, it := range x.items {
		e := &it.entry
		if r, ok := rows[e.Subject.ID]; ok && e.WrittenAt != nil {
			fp := fps[e.Subject.ID]
			e.Changed = r.Stale || len(fp) == 0 || !bytes.Equal(fp, r.Fingerprint)
		} else if !it.entry.activity.IsZero() && now.Sub(it.entry.activity) <= newlyQualifiedWindow {
			e.NewlyQualified = true
		}
		if e.Changed || e.NewlyQualified {
			recent = append(recent, it)
		}
	}

	nEvents := 0
	for _, it := range x.items {
		if it.entry.Kind == "event" {
			nEvents++
		}
	}
	split := nEvents > eventsSplit
	for _, it := range x.items {
		e := it.entry
		switch {
		case e.Group == GroupTopics:
			x.Topics = append(x.Topics, e)
		case e.Group == GroupPeople:
			x.People = append(x.People, e)
		case e.Kind == "event" && split:
			x.Events = append(x.Events, e)
		default:
			x.Projects = append(x.Projects, e)
		}
	}
	// The owner leads People.
	for i, e := range x.People {
		if ownerID != "" && e.Subject.ID == ownerID {
			copy(x.People[1:i+1], x.People[:i])
			x.People[0] = e
			break
		}
	}

	sort.SliceStable(recent, func(i, j int) bool {
		a, b := recent[i].entry, recent[j].entry
		if !a.activity.Equal(b.activity) {
			return a.activity.After(b.activity)
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Subject.ID < b.Subject.ID
	})
	if len(recent) > recentCap {
		recent = recent[:recentCap]
	}
	for _, it := range recent {
		x.Recent = append(x.Recent, it.entry)
	}

	indexMu.Lock()
	indexCache[h.UserID] = cacheEntry{key: key, ix: x}
	indexMu.Unlock()
	return x, nil
}

// PathFor returns the Vault path of the article for an entity: its own when
// it qualifies, else its topic's when it is a topic member.
func (x *Index) PathFor(entityID string) (string, bool) {
	if it, ok := x.byID[entityID]; ok && it.entry.Subject.Kind == "entity" {
		return it.entry.Path, true
	}
	if it, ok := x.topicOf[entityID]; ok {
		return it.entry.Path, true
	}
	return "", false
}

// Lookup resolves a URL group and slug to a subject by the short id in the
// slug; the name part is decorative. It returns the canonical slug so a
// caller can redirect a wrong name part. An ambiguous short id is not found.
func (x *Index) Lookup(group Group, slug string) (Subject, string, bool) {
	short, ok := ParseSlug(slug)
	if !ok {
		return Subject{}, "", false
	}
	hits := x.byShort[group][short]
	if len(hits) != 1 {
		return Subject{}, "", false
	}
	return hits[0].entry.Subject, hits[0].slug, true
}

// AlsoMentioned lists the names of entities related to the subject (by a live
// relationship or a shared live memory) that have no article of their own and
// no topic, strongest first. They are shown as plain text, never links.
func (x *Index) AlsoMentioned(sub Subject) []string {
	ix := x.relIndex()
	own := map[string]bool{}
	for _, id := range sub.entityIDs() {
		own[id] = true
	}
	score := map[string]int{}
	for id := range own {
		for _, r := range ix.rels[id] {
			if r.InvalidAt != nil {
				continue
			}
			other := r.SourceID
			if other == id {
				other = r.TargetID
			}
			score[other]++
		}
		for _, m := range ix.mems[id] {
			for _, o := range m.EntityIDs {
				score[o]++
			}
		}
	}
	type cand struct {
		name string
		n    int
	}
	var cs []cand
	seen := map[string]bool{}
	for id, n := range score {
		if own[id] {
			continue
		}
		if _, ok := x.PathFor(id); ok {
			continue
		}
		e, ok := x.set.EntityByID(id)
		if !ok || IsAccountLike(e.Type) || e.Name == "" || seen[e.Name] {
			continue
		}
		seen[e.Name] = true
		cs = append(cs, cand{e.Name, n})
	}
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].n != cs[j].n {
			return cs[i].n > cs[j].n
		}
		return cs[i].name < cs[j].name
	})
	if len(cs) > alsoMentionedCap {
		cs = cs[:alsoMentionedCap]
	}
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = c.name
	}
	return out
}
