package vaultart_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sausheong/cortex/internal/enctest"
	"github.com/sausheong/cortex/internal/memory"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
	"github.com/sausheong/cortex/internal/vaultart"
	"github.com/sausheong/cortex/internal/workset"
)

func personSub(id, name string) vaultart.Subject {
	return vaultart.Subject{ID: id, Group: vaultart.GroupPeople, Kind: "entity", Name: name}
}

// putMem writes one memory with explicit confidence and source.
func putMem(t *testing.T, h *store.Handle, content, source string, conf float64, static bool, ids ...string) string {
	t.Helper()
	m := &model.Memory{Content: content, Source: source, Confidence: conf, Static: static, EntityIDs: ids}
	if err := memory.New(h).PutMemory(bg, m); err != nil {
		t.Fatal(err)
	}
	return m.ID
}

func bulk(t *testing.T, env *enctest.Env, id string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		conf := 0.1 + 0.8*float64(i%100)/100
		putMem(t, env.H, fmt.Sprintf("fact number %d about the subject, padded with some words to take space %s", i, strings.Repeat("x", 60)), "t", conf, false, id)
	}
}

func TestBuildInputRespectsBudgetMostRelevantFirst(t *testing.T) {
	env := open(t)
	id := ent(t, env.H, "person", "Ada", nil)
	bulk(t, env, id, 500)
	set := getSet(t, env)
	in, used, total := vaultart.BuildInput(set, personSub(id, "Ada"), "Sam", nil, vaultart.DefaultSettings(), 600)
	if total > 600 || total == 0 {
		t.Fatalf("total %d over budget 600", total)
	}
	if len(used) == 0 || len(used) >= 500 || len(used) != len(in.Memories) {
		t.Fatalf("used %d memories", len(used))
	}
	// Confidence must be non-increasing: the most relevant come first.
	for i := 1; i < len(in.Memories); i++ {
		if in.Memories[i].Confidence > in.Memories[i-1].Confidence {
			t.Fatalf("not ranked at %d", i)
		}
	}
	// Everything left out ranks no higher than the lowest included.
	last := in.Memories[len(in.Memories)-1].Confidence
	in2 := map[string]bool{}
	for _, u := range used {
		in2[u] = true
	}
	for _, m := range set.Memories() {
		if !in2[m.ID] && m.Confidence > last {
			t.Fatalf("memory with confidence %v left out while %v included", m.Confidence, last)
		}
	}
}

func TestBuildInputPinsCorrectionsUnderBudget(t *testing.T) {
	env := open(t)
	id := ent(t, env.H, "person", "Ada", nil)
	corr := putMem(t, env.H, "Ada actually prefers tea, not coffee.", vaultart.CorrectionSource, 0.05, false, id)
	env.Exec(`UPDATE memories SET created_at = created_at - interval '400 days', updated_at = updated_at - interval '400 days' WHERE id = $1`, corr)
	bulk(t, env, id, 500)
	set := getSet(t, env)
	in, used, _ := vaultart.BuildInput(set, personSub(id, "Ada"), "Sam", nil, vaultart.DefaultSettings(), 300)
	if used[0] != corr || !in.Memories[0].Correction {
		t.Fatalf("correction not pinned first: %+v", in.Memories[0])
	}
	for _, m := range in.Memories[1:] {
		if m.Correction {
			t.Fatal("only corrections carry the flag")
		}
	}
}

func TestBuildInputCorrectionsCapAndNewestFirst(t *testing.T) {
	env := open(t)
	id := ent(t, env.H, "person", "Ada", nil)
	var ids []string
	for i := 0; i < vaultart.MaxCorrections+3; i++ {
		ids = append(ids, putMem(t, env.H, fmt.Sprintf("correction %d", i), vaultart.CorrectionSource, 0.5, false, id))
		time.Sleep(2 * time.Millisecond)
	}
	set := getSet(t, env)
	in, _, _ := vaultart.BuildInput(set, personSub(id, "Ada"), "Sam", nil, vaultart.DefaultSettings(), 1)
	n := 0
	for _, m := range in.Memories {
		if m.Correction {
			n++
		}
	}
	if n != vaultart.MaxCorrections {
		t.Fatalf("pinned %d corrections", n)
	}
	if in.Memories[0].ID != ids[len(ids)-1] {
		t.Fatal("newest correction must come first")
	}
}

func TestBuildInputLongMemoryTruncated(t *testing.T) {
	env := open(t)
	id := ent(t, env.H, "person", "Ada", nil)
	long := putMem(t, env.H, strings.Repeat("a", 5000), "t", 0.9, false, id)
	set := getSet(t, env)
	in, _, _ := vaultart.BuildInput(set, personSub(id, "Ada"), "Sam", nil, vaultart.DefaultSettings(), 6000)
	if got := len([]rune(in.Memories[0].Content)); got > 2010 || got < 2000 {
		t.Fatalf("prompt content length %d", got)
	}
	m, _ := set.MemoryByID(long)
	if len(m.Content) != 5000 {
		t.Fatal("stored memory must be untouched")
	}
}

func TestBuildInputOwnerName(t *testing.T) {
	env := open(t)
	id := ent(t, env.H, "person", "Ada", map[string]any{"role": "engineer"})
	other := ent(t, env.H, "organization", "Acme", nil)
	if err := graphRel(env.H, id, other); err != nil {
		t.Fatal(err)
	}
	bulk(t, env, id, 3)
	if err := addAlias(env.H, id, "Countess"); err != nil {
		t.Fatal(err)
	}
	set := getSet(t, env)
	in, _, _ := vaultart.BuildInput(set, personSub(id, "Ada"), "Sam", []vaultart.Candidate{{ID: other, Name: "Acme"}, {ID: id, Name: "Ada"}}, vaultart.DefaultSettings(), 6000)
	if in.Owner != "Sam" || in.Subject.Name != "Ada" || in.Subject.Type != "person" {
		t.Fatalf("%+v", in)
	}
	if in.Attributes["role"] != "engineer" || len(in.Aliases) != 1 || in.Aliases[0] != "Countess" {
		t.Fatalf("attrs/aliases: %+v %+v", in.Attributes, in.Aliases)
	}
	if len(in.Relations) != 1 || in.Relations[0].Other != "Acme" || in.Relations[0].Direction != "out" || in.Relations[0].OtherDescriptor == "" {
		t.Fatalf("relations: %+v", in.Relations)
	}
	if len(in.Linkable) != 1 || in.Linkable[0] != "Acme" {
		t.Fatalf("linkable must exclude the subject: %+v", in.Linkable)
	}
}

func TestBuildInputTopicUnionsMembers(t *testing.T) {
	env := open(t)
	a := ent(t, env.H, "concept", "Tea", nil)
	b := ent(t, env.H, "concept", "Kettle", nil)
	ma := putMem(t, env.H, "tea fact", "t", 0.5, false, a)
	mb := putMem(t, env.H, "kettle fact", "t", 0.5, false, b)
	both := putMem(t, env.H, "both fact", "t", 0.5, false, a, b)
	set := getSet(t, env)
	sub := vaultart.Subject{ID: "tp1", Group: vaultart.GroupTopics, Kind: "topic", Name: "Tea", Members: []string{a, b}}
	in, used, _ := vaultart.BuildInput(set, sub, "Sam", nil, vaultart.DefaultSettings(), 6000)
	if len(used) != 3 {
		t.Fatalf("want union of 3, got %v", used)
	}
	got := map[string]bool{}
	for _, u := range used {
		got[u] = true
	}
	if !got[ma] || !got[mb] || !got[both] {
		t.Fatal("missing member memory")
	}
	if in.Subject.Kind != "topic" || len(in.Subject.Members) != 2 || in.Subject.Members[0] != "Kettle" {
		t.Fatalf("%+v", in.Subject)
	}
	if ids := vaultart.MemoryIDs(set, sub); len(ids) != 3 {
		t.Fatalf("MemoryIDs %v", ids)
	}
}

func workSetFor(h *store.Handle) (*workset.Set, error) { return workset.For(bg, h) }
