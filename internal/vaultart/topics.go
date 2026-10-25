package vaultart

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
	"github.com/sausheong/cortex/internal/workset"
)

const (
	propagationRounds = 20
	// keepChange is the largest share of the persisted member set that may
	// change before topics are reclustered from scratch.
	keepChange = 0.10
	// reuseOverlap is the member overlap needed to carry a topic's id and
	// slug across a recluster.
	reuseOverlap = 0.5
	// maxPairEntities bounds the pairs a single memory contributes to the
	// weighted graph.
	maxPairEntities = 50
)

// Topic is a cluster of concept-like entities that gets one article. The
// clustering functions fill Members, Memories, Strongest and Title;
// StableTopics assigns ID, ShortID and Slug.
type Topic struct {
	ID, ShortID, Slug, Title, WrittenTitle string
	Members                                []string // entity ids, sorted
	Memories                               int      // distinct live memories across members
	Strongest                              string   // member id with the most live memories
}

// DisplayTitle is the model-written title when there is one, else the name
// of the strongest member.
func (t Topic) DisplayTitle() string {
	if t.WrittenTitle != "" {
		return t.WrittenTitle
	}
	return t.Title
}

// topicGraph is the weighted graph over topic-eligible entities.
type topicGraph struct {
	ids    []string // sorted eligible ids
	names  map[string]string
	adj    map[string]map[string]int
	mems   map[string][]string // entity id -> live memory ids
	counts map[string]int
}

func buildTopicGraph(set *workset.Set) *topicGraph {
	g := &topicGraph{
		names: map[string]string{},
		adj:   map[string]map[string]int{},
		mems:  map[string][]string{},
	}
	for _, e := range set.Entities() {
		if TopicEligible(e.Type) {
			g.ids = append(g.ids, e.ID)
			g.names[e.ID] = e.Name
		}
	}
	sort.Strings(g.ids)
	bump := func(a, b string) {
		if a == b {
			return
		}
		if g.adj[a] == nil {
			g.adj[a] = map[string]int{}
		}
		if g.adj[b] == nil {
			g.adj[b] = map[string]int{}
		}
		g.adj[a][b]++
		g.adj[b][a]++
	}
	for _, r := range set.RelationshipsFiltered(false, nil, time.Now()) {
		_, a := g.names[r.SourceID]
		_, b := g.names[r.TargetID]
		if a && b {
			bump(r.SourceID, r.TargetID)
		}
	}
	for _, m := range set.MemoriesMode(model.TemporalMode{}) {
		var in []string
		seen := map[string]bool{}
		for _, id := range m.EntityIDs {
			if _, ok := g.names[id]; ok && !seen[id] {
				seen[id] = true
				in = append(in, id)
			}
		}
		sort.Strings(in)
		for _, id := range in {
			g.mems[id] = append(g.mems[id], m.ID)
		}
		if len(in) > maxPairEntities {
			in = in[:maxPairEntities]
		}
		for i := range in {
			for j := i + 1; j < len(in); j++ {
				bump(in[i], in[j])
			}
		}
	}
	g.counts = map[string]int{}
	for id, ms := range g.mems {
		g.counts[id] = len(ms)
	}
	return g
}

// distinct counts the distinct live memories across members.
func (g *topicGraph) distinct(members []string) int {
	seen := map[string]bool{}
	for _, id := range members {
		for _, m := range g.mems[id] {
			seen[m] = true
		}
	}
	return len(seen)
}

// qualifies applies the rule every topic must meet: two or more members and
// together at least threshold live memories.
func (g *topicGraph) qualifies(members []string, threshold int) bool {
	return len(members) >= 2 && g.distinct(members) >= threshold
}

// fill derives Title, Strongest and Memories from the members.
func (g *topicGraph) fill(t *Topic) {
	sort.Strings(t.Members)
	best := ""
	for _, id := range t.Members {
		if best == "" {
			best = id
			continue
		}
		cb, ci := g.counts[best], g.counts[id]
		if ci > cb || (ci == cb && (g.names[id] < g.names[best] || (g.names[id] == g.names[best] && id < best))) {
			best = id
		}
	}
	t.Strongest = best
	t.Title = g.names[best]
	t.Memories = g.distinct(t.Members)
}

func sortTopics(ts []Topic) {
	sort.Slice(ts, func(i, j int) bool {
		a, b := ts[i], ts[j]
		if a.Memories != b.Memories {
			return a.Memories > b.Memories
		}
		if a.Title != b.Title {
			return a.Title < b.Title
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Members[0] < b.Members[0]
	})
}

// ClusterTopics groups topic-eligible entities by weighted label
// propagation. It is deterministic: ids are visited in sorted order, at most
// 20 rounds run, and ties go to the smallest label. A cluster needs two or
// more members and at least s.Threshold distinct live memories together;
// each entity lands in at most one topic. IDs and slugs are left empty;
// StableTopics assigns them.
func ClusterTopics(set *workset.Set, s Settings) []Topic {
	s = s.Normalize()
	return clusterOn(buildTopicGraph(set), s)
}

func clusterOn(g *topicGraph, s Settings) []Topic {
	ids := append([]string(nil), g.ids...)
	sort.Strings(ids) // never rely on the caller's order
	label := make(map[string]string, len(ids))
	for _, id := range ids {
		label[id] = id
	}
	for round := 0; round < propagationRounds; round++ {
		changed := false
		for _, id := range ids {
			w := map[string]int{}
			for nb, n := range g.adj[id] {
				w[label[nb]] += n
			}
			if len(w) == 0 {
				continue
			}
			bestL, bestW := "", -1
			for l, n := range w {
				if n > bestW || (n == bestW && l < bestL) {
					bestL, bestW = l, n
				}
			}
			if bestL != label[id] {
				label[id] = bestL
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	groups := map[string][]string{}
	for _, id := range ids {
		groups[label[id]] = append(groups[label[id]], id)
	}
	var out []Topic
	for _, members := range groups {
		if !g.qualifies(members, s.Threshold) {
			continue
		}
		t := Topic{Members: append([]string(nil), members...)}
		g.fill(&t)
		out = append(out, t)
	}
	sortTopics(out)
	return out
}

func rowsToTopics(rows []store.TopicRow) []Topic {
	out := make([]Topic, 0, len(rows))
	for _, r := range rows {
		out = append(out, Topic{ID: r.TopicID, ShortID: r.ShortID, Slug: r.Slug, Title: r.Title,
			WrittenTitle: r.WrittenTitle, Members: append([]string(nil), r.Members...)})
	}
	return out
}

func topicsToRows(ts []Topic) []store.TopicRow {
	out := make([]store.TopicRow, 0, len(ts))
	for _, t := range ts {
		out = append(out, store.TopicRow{TopicID: t.ID, ShortID: t.ShortID, Slug: t.Slug, Title: t.Title,
			WrittenTitle: t.WrittenTitle, Members: append([]string(nil), t.Members...)})
	}
	return out
}

// newTopicID makes a fresh id whose short id collides with none in taken.
func newTopicID(taken map[string]bool) (id, short string) {
	for {
		id = store.NewID()
		short = ShortID(id)
		if !taken[short] {
			taken[short] = true
			return id, short
		}
	}
}

// StableTopics keeps topic identity steady between loads. prev are the
// persisted topics and now a fresh clustering of the same set.
//
// When the topic-member set changed by 10 percent or less, persisted topics
// are kept with their ids, slugs and written titles: members that vanished
// are dropped and members that appeared join the neighbouring topic they are
// most strongly connected to. Clusters in now made only of unassigned
// entities are added as new topics. Otherwise now is adopted, reusing a
// previous topic's id and slug where members overlap it by at least half.
// Either way every topic must still have two or more members and
// s.Threshold live memories, and disappears if it does not.
func StableTopics(prev []store.TopicRow, now []Topic, set *workset.Set, s Settings) (out []Topic, reclustered bool) {
	s = s.Normalize()
	g := buildTopicGraph(set)
	old := rowsToTopics(prev)

	inPrev, inNow := map[string]bool{}, map[string]bool{}
	for _, t := range old {
		for _, m := range t.Members {
			inPrev[m] = true
		}
	}
	for _, t := range now {
		for _, m := range t.Members {
			inNow[m] = true
		}
	}
	diff := 0
	for m := range inPrev {
		if !inNow[m] {
			diff++
		}
	}
	for m := range inNow {
		if !inPrev[m] {
			diff++
		}
	}
	taken := map[string]bool{}
	for _, t := range old {
		taken[t.ShortID] = true
	}

	if len(old) > 0 && float64(diff) <= keepChange*float64(len(inPrev)) {
		return keepTopics(g, old, now, inPrev, taken, s), false
	}
	return adoptTopics(g, old, now, taken), true
}

func keepTopics(g *topicGraph, old, now []Topic, inPrev map[string]bool, taken map[string]bool, s Settings) []Topic {
	owner := map[string]int{} // entity -> index in kept
	kept := make([]Topic, 0, len(old))
	for _, t := range old {
		var members []string
		for _, m := range t.Members {
			if _, ok := g.names[m]; ok {
				members = append(members, m)
			}
		}
		t.Members = members
		kept = append(kept, t)
	}
	for i, t := range kept {
		for _, m := range t.Members {
			owner[m] = i
		}
	}
	// Added members join the neighbouring kept topic with the strongest
	// connection (ties to the smallest topic id).
	var added []string
	for _, t := range now {
		for _, m := range t.Members {
			if !inPrev[m] {
				added = append(added, m)
			}
		}
	}
	sort.Strings(added)
	for _, m := range added {
		w := map[int]int{}
		for nb, n := range g.adj[m] {
			if i, ok := owner[nb]; ok {
				w[i] += n
			}
		}
		best, bw := -1, 0
		for i, n := range w {
			if n > bw || (n == bw && best >= 0 && kept[i].ID < kept[best].ID) {
				best, bw = i, n
			}
		}
		if best >= 0 {
			kept[best].Members = append(kept[best].Members, m)
			owner[m] = best
		}
	}
	var out []Topic
	assigned := map[string]bool{}
	for _, t := range kept {
		if !g.qualifies(t.Members, s.Threshold) {
			continue
		}
		g.fill(&t)
		for _, m := range t.Members {
			assigned[m] = true
		}
		out = append(out, t)
	}
	// Wholly new clusters of unassigned entities become new topics.
	for _, t := range now {
		free := true
		for _, m := range t.Members {
			if assigned[m] {
				free = false
				break
			}
		}
		if !free || !g.qualifies(t.Members, s.Threshold) {
			continue
		}
		nt := Topic{Members: append([]string(nil), t.Members...)}
		g.fill(&nt)
		nt.ID, nt.ShortID = newTopicID(taken)
		nt.Slug = ArticleSlug(nt.Title, nt.ID)
		for _, m := range nt.Members {
			assigned[m] = true
		}
		out = append(out, nt)
	}
	sortTopics(out)
	return out
}

func adoptTopics(g *topicGraph, old, now []Topic, taken map[string]bool) []Topic {
	type pair struct {
		n, p, ov int
		ratio    float64
	}
	var pairs []pair
	for ni, nt := range now {
		nm := map[string]bool{}
		for _, m := range nt.Members {
			nm[m] = true
		}
		for pi, pt := range old {
			ov := 0
			for _, m := range pt.Members {
				if nm[m] {
					ov++
				}
			}
			den := len(pt.Members)
			if len(nt.Members) > den {
				den = len(nt.Members)
			}
			if ov > 0 && float64(ov) >= reuseOverlap*float64(den) {
				pairs = append(pairs, pair{ni, pi, ov, float64(ov) / float64(den)})
			}
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		a, b := pairs[i], pairs[j]
		if a.ratio != b.ratio {
			return a.ratio > b.ratio
		}
		if a.ov != b.ov {
			return a.ov > b.ov
		}
		return old[a.p].ID < old[b.p].ID
	})
	nowUsed, oldUsed := map[int]bool{}, map[int]bool{}
	reuse := map[int]int{}
	for _, p := range pairs {
		if nowUsed[p.n] || oldUsed[p.p] {
			continue
		}
		nowUsed[p.n], oldUsed[p.p] = true, true
		reuse[p.n] = p.p
	}
	// Short ids of topics that will not survive are free to reuse.
	for pi, t := range old {
		if !oldUsed[pi] {
			delete(taken, t.ShortID)
		}
	}
	out := make([]Topic, 0, len(now))
	for ni, nt := range now {
		t := Topic{Members: append([]string(nil), nt.Members...)}
		g.fill(&t)
		if pi, ok := reuse[ni]; ok {
			t.ID, t.ShortID, t.Slug, t.WrittenTitle = old[pi].ID, old[pi].ShortID, old[pi].Slug, old[pi].WrittenTitle
		} else {
			t.ID, t.ShortID = newTopicID(taken)
			t.Slug = ArticleSlug(t.Title, t.ID)
		}
		out = append(out, t)
	}
	sortTopics(out)
	return out
}

var topicLocks sync.Map // user id -> *sync.Mutex

func sameRows(a, b []store.TopicRow) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]store.TopicRow{}
	for _, r := range a {
		m[r.TopicID] = r
	}
	for _, r := range b {
		o, ok := m[r.TopicID]
		if !ok || o.ShortID != r.ShortID || o.Slug != r.Slug || o.Title != r.Title ||
			o.WrittenTitle != r.WrittenTitle || len(o.Members) != len(r.Members) {
			return false
		}
		x := append([]string(nil), o.Members...)
		y := append([]string(nil), r.Members...)
		sort.Strings(x)
		sort.Strings(y)
		for i := range x {
			if x[i] != y[i] {
				return false
			}
		}
	}
	return true
}

// LoadTopics returns the user's topics, clustering and persisting them when
// they changed. It is serialised per user and re-reads the persisted rows
// inside the lock, so concurrent page loads cannot drop and recreate topics
// or invalidate slugs in flight. Rows are written only when the result
// differs from what is stored, through the diff-applying PutTopics.
func LoadTopics(ctx context.Context, h *store.Handle, set *workset.Set, s Settings) ([]Topic, error) {
	mu, _ := topicLocks.LoadOrStore(h.UserID, &sync.Mutex{})
	l := mu.(*sync.Mutex)
	l.Lock()
	defer l.Unlock()

	prev, err := h.GetTopics(ctx)
	if err != nil {
		return nil, err
	}
	s = s.Normalize()
	out, _ := StableTopics(prev, ClusterTopics(set, s), set, s)
	rows := topicsToRows(out)
	if !sameRows(prev, rows) {
		if err := h.PutTopics(ctx, rows); err != nil {
			return nil, err
		}
	}
	return out, nil
}
