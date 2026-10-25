package vaultart_test

import (
	"fmt"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/sausheong/cortex/internal/enctest"
	"github.com/sausheong/cortex/internal/graph"
	"github.com/sausheong/cortex/internal/memory"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
	"github.com/sausheong/cortex/internal/vaultart"
	"github.com/sausheong/cortex/internal/workset"
)

// linkMem writes n memories each tagged with all of ids, returning their ids.
func linkMem(t *testing.T, h *store.Handle, n int, ids ...string) []string {
	t.Helper()
	var out []string
	for i := 0; i < n; i++ {
		m := &model.Memory{Content: fmt.Sprintf("shared fact %d %v", i, ids), Source: "t", EntityIDs: ids}
		if err := memory.New(h).PutMemory(bg, m); err != nil {
			t.Fatal(err)
		}
		out = append(out, m.ID)
	}
	return out
}

// concepts creates n named concept entities, each with two own memories,
// tied into a ring by shared memories. It returns the ids.
func concepts(t *testing.T, h *store.Handle, prefix string, n int) []string {
	t.Helper()
	var ids []string
	for i := 0; i < n; i++ {
		id := ent(t, h, "concept", fmt.Sprintf("%s %02d", prefix, i), nil)
		mems(t, h, id, 2)
		ids = append(ids, id)
	}
	for i := range ids {
		linkMem(t, h, 2, ids[i], ids[(i+1)%len(ids)])
	}
	return ids
}

func getSet(t *testing.T, env *enctest.Env) *workset.Set {
	t.Helper()
	workset.Invalidate(env.UserID)
	set, err := workset.For(bg, env.H)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func memberSet(ts []vaultart.Topic) map[string]string {
	out := map[string]string{}
	for _, t := range ts {
		for _, m := range t.Members {
			out[m] = t.ID
		}
	}
	return out
}

func TestClusterTopicsDeterministic(t *testing.T) {
	env := open(t)
	concepts(t, env.H, "Alpha", 5)
	concepts(t, env.H, "Beta", 4)
	concepts(t, env.H, "Gamma", 6)
	set := getSet(t, env)
	s := vaultart.DefaultSettings()
	want := vaultart.ClusterTopics(set, s)
	if len(want) != 3 {
		t.Fatalf("want 3 topics, got %+v", want)
	}
	for seed := int64(1); seed <= 20; seed++ {
		got := vaultart.ClusterShuffled(set, s, seed)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("seed %d differs:\n%+v\n%+v", seed, got, want)
		}
		if again := vaultart.ClusterTopics(set, s); !reflect.DeepEqual(again, want) {
			t.Fatal("repeat run differs")
		}
	}
}

func TestClusterOneTopicPerEntity(t *testing.T) {
	env := open(t)
	a := concepts(t, env.H, "Alpha", 5)
	b := concepts(t, env.H, "Beta", 5)
	linkMem(t, env.H, 1, a[0], b[0]) // weak bridge
	ts := vaultart.ClusterTopics(getSet(t, env), vaultart.DefaultSettings())
	seen := map[string]bool{}
	for _, tp := range ts {
		for _, m := range tp.Members {
			if seen[m] {
				t.Fatalf("entity %s in two topics", m)
			}
			seen[m] = true
		}
	}
	if len(seen) != 10 {
		t.Fatalf("expected all 10 entities placed, got %d", len(seen))
	}
}

func TestClusterNeedsThresholdMemories(t *testing.T) {
	env := open(t)
	a := ent(t, env.H, "concept", "Tea", nil)
	b := ent(t, env.H, "concept", "Kettle", nil)
	solo := ent(t, env.H, "concept", "Lonely", nil)
	mems(t, env.H, solo, 9) // plenty of memories but a cluster of one
	linkMem(t, env.H, 2, a, b)
	s := vaultart.DefaultSettings() // threshold 3
	if ts := vaultart.ClusterTopics(getSet(t, env), s); len(ts) != 0 {
		t.Fatalf("2 memories must not qualify: %+v", ts)
	}
	linkMem(t, env.H, 1, a, b)
	ts := vaultart.ClusterTopics(getSet(t, env), s)
	if len(ts) != 1 || ts[0].Memories != 3 || len(ts[0].Members) != 2 {
		t.Fatalf("%+v", ts)
	}
}

func TestTopicNameIsStrongestMember(t *testing.T) {
	env := open(t)
	a := ent(t, env.H, "concept", "Zebra", nil)
	b := ent(t, env.H, "concept", "Apple", nil)
	c := ent(t, env.H, "concept", "Mango", nil)
	mems(t, env.H, a, 5)
	mems(t, env.H, b, 2)
	mems(t, env.H, c, 2)
	linkMem(t, env.H, 2, a, b, c)
	ts := vaultart.ClusterTopics(getSet(t, env), vaultart.DefaultSettings())
	if len(ts) != 1 || ts[0].Strongest != a || ts[0].Title != "Zebra" || ts[0].DisplayTitle() != "Zebra" {
		t.Fatalf("%+v", ts)
	}
	tp := ts[0]
	tp.WrittenTitle = "Things in the pantry"
	if tp.DisplayTitle() != "Things in the pantry" {
		t.Fatal(tp.DisplayTitle())
	}
}

func loadTopics(t *testing.T, env *enctest.Env, s vaultart.Settings) []vaultart.Topic {
	t.Helper()
	ts, err := vaultart.LoadTopics(bg, env.H, getSet(t, env), s)
	if err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestStableTopicsKeepIdsUnderSmallChange(t *testing.T) {
	env := open(t)
	s := vaultart.DefaultSettings()
	var first []string
	for i := 0; i < 8; i++ {
		ids := concepts(t, env.H, fmt.Sprintf("Cluster%d", i), 5)
		first = append(first, ids...)
	}
	before := loadTopics(t, env, s)
	if len(before) != 8 {
		t.Fatalf("%d topics", len(before))
	}
	// One new entity (1 in 40) joins cluster 0.
	extra := ent(t, env.H, "concept", "Cluster0 newcomer", nil)
	mems(t, env.H, extra, 1)
	linkMem(t, env.H, 3, extra, first[0], first[1])
	set := getSet(t, env)
	prev, err := env.H.GetTopics(bg)
	if err != nil {
		t.Fatal(err)
	}
	out, re := vaultart.StableTopics(prev, vaultart.ClusterTopics(set, s), set, s)
	if re {
		t.Fatal("should not recluster for 1 in 40")
	}
	if len(out) != 8 {
		t.Fatalf("%d topics", len(out))
	}
	was, now := memberSet(before), memberSet(out)
	for m, id := range was {
		if now[m] != id {
			t.Fatalf("member %s moved topic %s -> %s", m, id, now[m])
		}
	}
	if now[extra] != was[first[0]] {
		t.Fatal("newcomer should join the neighbouring topic")
	}
	slugs := map[string]string{}
	for _, tp := range before {
		slugs[tp.ID] = tp.Slug
	}
	for _, tp := range out {
		if slugs[tp.ID] != tp.Slug {
			t.Fatalf("slug changed for %s", tp.ID)
		}
	}
	// A vanished member is dropped without changing the topic's id.
	if _, err := graph.New(env.H).SoftDeleteEntity(bg, first[39], "test"); err != nil {
		t.Fatal(err)
	}
	set = getSet(t, env)
	prev, _ = env.H.GetTopics(bg)
	out2, re := vaultart.StableTopics(prev, vaultart.ClusterTopics(set, s), set, s)
	if re || len(out2) != 8 {
		t.Fatalf("re=%v n=%d", re, len(out2))
	}
	if _, ok := memberSet(out2)[first[39]]; ok {
		t.Fatal("vanished member still present")
	}
	if memberSet(out2)[first[0]] != was[first[0]] {
		t.Fatal("topic id moved")
	}
}

func TestStableTopicsReclusterOverTenPercent(t *testing.T) {
	env := open(t)
	s := vaultart.DefaultSettings()
	x := concepts(t, env.H, "Xray", 6) // now: one topic of 6
	g := concepts(t, env.H, "Golf", 4) // now: one topic of 4
	pq := concepts(t, env.H, "Pq", 6)  // now: one topic of 6
	iso := make([]string, 3)           // isolated entities used in stale prev rows
	for i := range iso {
		iso[i] = ent(t, env.H, "concept", fmt.Sprintf("Isolated %d", i), nil)
	}
	set := getSet(t, env)
	now := vaultart.ClusterTopics(set, s)
	if len(now) != 3 {
		t.Fatalf("%+v", now)
	}
	prev := []store.TopicRow{
		{TopicID: "T-X", ShortID: "aaaaaaa1", Slug: "old-x-aaaaaaa1", Title: "Old X", WrittenTitle: "Kept", Members: x[:3]},                   // 3 of 6: 50 percent, reused
		{TopicID: "T-G", ShortID: "aaaaaaa2", Slug: "old-g-aaaaaaa2", Title: "Old G", Members: g},                                             // 100 percent, reused
		{TopicID: "T-P", ShortID: "aaaaaaa3", Slug: "old-p-aaaaaaa3", Title: "Old P", Members: append(append([]string{}, pq[:2]...), iso...)}, // 2 of 6: not reused
	}
	out, re := vaultart.StableTopics(prev, now, set, s)
	if !re {
		t.Fatal("expected recluster")
	}
	byMember := memberSet(out)
	if byMember[x[0]] != "T-X" || byMember[g[0]] != "T-G" {
		t.Fatalf("ids not reused: %+v", out)
	}
	if byMember[pq[0]] == "T-P" || byMember[pq[0]] == "" {
		t.Fatalf("under 50 percent must not reuse id: %q", byMember[pq[0]])
	}
	for _, tp := range out {
		if tp.ID == "T-X" && (tp.Slug != "old-x-aaaaaaa1" || tp.WrittenTitle != "Kept") {
			t.Fatalf("slug or written title lost: %+v", tp)
		}
		if tp.ID == "T-P" {
			t.Fatal("T-P should be gone")
		}
		if len(tp.Members) < 2 {
			t.Fatalf("%+v", tp)
		}
	}
}

func TestStableTopicsDropsPersistedTopicBelowThreshold(t *testing.T) {
	env := open(t)
	s := vaultart.DefaultSettings()
	keep := concepts(t, env.H, "Keep", 4)
	a := ent(t, env.H, "concept", "Fading A", nil)
	b := ent(t, env.H, "concept", "Fading B", nil)
	own := append(mems(t, env.H, a, 1), mems(t, env.H, b, 1)...)
	own = append(own, linkMem(t, env.H, 2, a, b)...)
	before := loadTopics(t, env, s)
	if len(before) != 2 {
		t.Fatalf("%+v", before)
	}
	// Retire all but two live memories: 2 < threshold 3, members unchanged.
	for _, id := range own[:2] {
		env.Exec(`UPDATE memories SET invalid_at = now() - interval '1 hour' WHERE id = $1`, id)
	}
	after := loadTopics(t, env, s)
	if len(after) != 1 {
		t.Fatalf("faded topic should disappear: %+v", after)
	}
	if _, ok := memberSet(after)[keep[0]]; !ok {
		t.Fatal("other topic lost")
	}
	rows, err := env.H.GetTopics(bg)
	if err != nil || len(rows) != 1 {
		t.Fatalf("persisted rows: %v %+v", err, rows)
	}
}

func TestLoadTopicsConcurrentNoDropRecreate(t *testing.T) {
	env := open(t)
	s := vaultart.DefaultSettings()
	concepts(t, env.H, "Alpha", 5)
	concepts(t, env.H, "Beta", 5)
	set := getSet(t, env)
	var wg sync.WaitGroup
	results := make([][]vaultart.Topic, 20)
	errs := make([]error, 20)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = vaultart.LoadTopics(bg, env.H, set, s)
		}(i)
	}
	wg.Wait()
	slugs := func(ts []vaultart.Topic) []string {
		var out []string
		for _, tp := range ts {
			out = append(out, tp.ID+"|"+tp.Slug)
		}
		sort.Strings(out)
		return out
	}
	want := slugs(results[0])
	if len(want) != 2 {
		t.Fatalf("%v", want)
	}
	for i := range results {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		if !reflect.DeepEqual(slugs(results[i]), want) {
			t.Fatalf("goroutine %d saw %v, want %v", i, slugs(results[i]), want)
		}
	}
	rows, err := env.H.GetTopics(bg)
	if err != nil || len(rows) != 2 {
		t.Fatalf("%v %+v", err, rows)
	}
}
