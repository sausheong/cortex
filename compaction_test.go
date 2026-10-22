package cortex

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"
)

// compactSeed writes a memory with an explicit 8-dim embedding.
func compactSeed(t *testing.T, c *Cortex, content string, static bool, vec []float32, age time.Duration, entityIDs ...string) string {
	t.Helper()
	ctx := context.Background()
	m := &Memory{Content: content, Static: static, EntityIDs: entityIDs}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	if age > 0 {
		if _, err := c.db.ExecContext(ctx, `UPDATE memories SET created_at = ? WHERE id = ?`, time.Now().Add(-age), m.ID); err != nil {
			t.Fatal(err)
		}
	}
	g, err := c.ActiveGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if vec != nil {
		if err := c.PutEmbeddingGen(ctx, m.ID, "memory", g, vec); err != nil {
			t.Fatal(err)
		}
	}
	return m.ID
}

func axis(i int) []float32 {
	v := make([]float32, 8)
	v[i] = 1
	return v
}

func nearAxis(i int) []float32 {
	v := axis(i)
	v[(i+1)%8] = 0.1
	return v
}

func liveSnapshot(t *testing.T, c *Cortex) map[string]string {
	t.Helper()
	rows, err := c.db.QueryContext(context.Background(),
		`SELECT id, content FROM memories WHERE user_id = ? AND deleted_at IS NULL`, c.userID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, ct string
		if err := rows.Scan(&id, &ct); err != nil {
			t.Fatal(err)
		}
		out[id] = ct
	}
	return out
}

func echoLLM() *mockLLM {
	return &mockLLM{summarizeFn: func(_ context.Context, texts []string) (string, error) {
		return "SUMMARY: " + strings.Join(texts[1:], " | "), nil
	}}
}

func TestProposeMergesDuplicates(t *testing.T) {
	c := openTestDB(t)
	c.SetLLM(echoLLM())
	a := compactSeed(t, c, "likes tea", false, axis(0), 0)
	b := compactSeed(t, c, "enjoys tea", false, nearAxis(0), 0)
	compactSeed(t, c, "owns a bike", false, axis(4), 0)
	got, err := c.ProposeCompaction(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kind != CompactMerge {
		t.Fatalf("want one merge, got %+v", got)
	}
	ids := append([]string(nil), got[0].SourceIDs...)
	sort.Strings(ids)
	want := []string{a, b}
	sort.Strings(want)
	if strings.Join(ids, ",") != strings.Join(want, ",") || !strings.HasPrefix(got[0].Result, "SUMMARY:") {
		t.Fatalf("unexpected change %+v", got[0])
	}
}

func TestRollupExcludesStatic(t *testing.T) {
	c := openTestDB(t)
	c.SetLLM(echoLLM())
	ctx := context.Background()
	e := &Entity{Type: "person", Name: "Pat"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	old := 120 * 24 * time.Hour
	var dyn []string
	for i, txt := range []string{"went to a", "went to b", "went to c"} {
		dyn = append(dyn, compactSeed(t, c, txt, false, axis(i), old, e.ID))
	}
	st := compactSeed(t, c, "is vegetarian", true, axis(5), old, e.ID)
	got, err := c.ProposeCompaction(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kind != CompactRollup {
		t.Fatalf("want one rollup, got %+v", got)
	}
	for _, id := range got[0].SourceIDs {
		if id == st {
			t.Fatal("static memory in rollup")
		}
	}
	if len(got[0].SourceIDs) != len(dyn) {
		t.Fatalf("sources = %v", got[0].SourceIDs)
	}
}

func applyMerge(t *testing.T, c *Cortex) (a, b string, batch string, changes []Change) {
	t.Helper()
	c.SetLLM(echoLLM())
	c.SetEmbedder(&testEmbedder{})
	a = compactSeed(t, c, "likes tea", false, axis(0), 0)
	b = compactSeed(t, c, "enjoys tea", false, nearAxis(0), 0)
	compactSeed(t, c, "owns a bike", false, axis(4), 0)
	changes, err := c.ProposeCompaction(context.Background())
	if err != nil || len(changes) != 1 {
		t.Fatalf("propose: %v %+v", err, changes)
	}
	batch, err = c.ApplyCompaction(context.Background(), changes)
	if err != nil {
		t.Fatal(err)
	}
	return a, b, batch, changes
}

func TestApplyReducesCountAndLinksDerives(t *testing.T) {
	c := openTestDB(t)
	a, b, batch, _ := applyMerge(t, c)
	if batch == "" {
		t.Fatal("empty batch id")
	}
	live := liveSnapshot(t, c)
	if len(live) != 2 {
		t.Fatalf("live = %v", live)
	}
	if _, ok := live[a]; ok {
		t.Fatal("source still live")
	}
	var rid string
	for id, ct := range live {
		if strings.HasPrefix(ct, "SUMMARY:") {
			rid = id
		}
	}
	if rid == "" {
		t.Fatal("no result memory")
	}
	edges, err := c.GetMemoryEdgesByType(context.Background(), rid, EdgeDerives)
	if err != nil {
		t.Fatal(err)
	}
	targets := map[string]bool{}
	for _, e := range edges {
		if e.SourceID == rid {
			targets[e.TargetID] = true
		}
	}
	if !targets[a] || !targets[b] {
		t.Fatalf("derives edges = %+v", edges)
	}
}

func TestUndoRestoresExactly(t *testing.T) {
	c := openTestDB(t)
	c.SetLLM(echoLLM())
	c.SetEmbedder(&testEmbedder{})
	compactSeed(t, c, "likes tea", false, axis(0), 0)
	compactSeed(t, c, "enjoys tea", false, nearAxis(0), 0)
	compactSeed(t, c, "owns a bike", false, axis(4), 0)
	before := liveSnapshot(t, c)
	changes, err := c.ProposeCompaction(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	batch, err := c.ApplyCompaction(context.Background(), changes)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.UndoCompaction(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	after := liveSnapshot(t, c)
	if len(after) != len(before) {
		t.Fatalf("before %v after %v", before, after)
	}
	for id, ct := range before {
		if after[id] != ct {
			t.Fatalf("memory %s: %q != %q", id, after[id], ct)
		}
	}
}

func TestApplyOnlySelected(t *testing.T) {
	c := openTestDB(t)
	c.SetLLM(echoLLM())
	c.SetEmbedder(&testEmbedder{})
	compactSeed(t, c, "likes tea", false, axis(0), 0)
	compactSeed(t, c, "enjoys tea", false, nearAxis(0), 0)
	x := compactSeed(t, c, "likes cake", false, axis(3), 0)
	y := compactSeed(t, c, "enjoys cake", false, nearAxis(3), 0)
	changes, err := c.ProposeCompaction(context.Background())
	if err != nil || len(changes) != 2 {
		t.Fatalf("propose: %v %+v", err, changes)
	}
	var pick Change
	for _, ch := range changes {
		for _, id := range ch.SourceIDs {
			if id == x || id == y {
				pick = ch
			}
		}
	}
	if _, err := c.ApplyCompaction(context.Background(), []Change{pick}); err != nil {
		t.Fatal(err)
	}
	live := liveSnapshot(t, c)
	if len(live) != 3 { // 4 - 2 + 1
		t.Fatalf("live = %v", live)
	}
	if _, ok := live[x]; ok {
		t.Fatal("selected source still live")
	}
}

func TestIsolationCompaction(t *testing.T) {
	a, b := twoUsers(t)
	ctx := context.Background()
	bm := compactSeed(t, b, "b secret", false, axis(1), 0)
	am := compactSeed(t, a, "a own", false, axis(2), 0)
	_, err := a.ApplyCompaction(ctx, []Change{{Kind: CompactMerge, SourceIDs: []string{am, bm}, Result: "x"}})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v", err)
	}
	if len(liveSnapshot(t, a)) != 1 || len(liveSnapshot(t, b)) != 1 {
		t.Fatal("rows changed")
	}
	// b's batch is foreign to a.
	bb, err := b.ApplyCompaction(ctx, []Change{{Kind: CompactMerge, SourceIDs: []string{bm}, Result: "b merged"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.UndoCompaction(ctx, bb); !errors.Is(err, ErrNotFound) {
		t.Fatalf("undo err = %v", err)
	}
	if len(liveSnapshot(t, b)) != 1 {
		t.Fatal("b changed by a's undo")
	}
}
