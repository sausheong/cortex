package cortex

import (
	"context"
	"testing"
)

func TestTrackProfile_SetsMarker(t *testing.T) { forEachMode(t, testTrackProfile_SetsMarker) }

func testTrackProfile_SetsMarker(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "Alice"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := c.TrackProfile(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetEntity(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := got.Attributes[attrProfiled].(bool); !v {
		t.Errorf("expected _profiled=true, attrs=%v", got.Attributes)
	}
}

func TestUntrackProfile_RemovesMarkerAndCache(t *testing.T) {
	forEachMode(t, testUntrackProfile_RemovesMarkerAndCache)
}

func testUntrackProfile_RemovesMarkerAndCache(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "Bob", Attributes: map[string]any{
		attrProfiled: true,
		attrProfile:  map[string]any{"static": []string{"x"}},
	}}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	if err := c.UntrackProfile(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetEntity(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.Attributes[attrProfiled]; ok {
		t.Error("expected _profiled removed")
	}
	if _, ok := got.Attributes[attrProfile]; ok {
		t.Error("expected _profile cache removed")
	}
}

func TestProfileEligibleIDs_OwnerAndTracked(t *testing.T) {
	forEachMode(t, testProfileEligibleIDs_OwnerAndTracked)
}

func testProfileEligibleIDs_OwnerAndTracked(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	owner := &Entity{Type: "person", Name: "Me", Source: "owner"}
	if err := c.PutEntity(ctx, owner); err != nil {
		t.Fatal(err)
	}
	markOwner(t, c, owner.ID)
	tracked := &Entity{Type: "person", Name: "Alice"}
	if err := c.PutEntity(ctx, tracked); err != nil {
		t.Fatal(err)
	}
	if err := c.TrackProfile(ctx, tracked.ID); err != nil {
		t.Fatal(err)
	}
	untracked := &Entity{Type: "person", Name: "Carol"}
	if err := c.PutEntity(ctx, untracked); err != nil {
		t.Fatal(err)
	}

	ids, err := c.profileEligibleIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	set := map[string]bool{}
	for _, id := range ids {
		set[id] = true
	}
	if !set[owner.ID] || !set[tracked.ID] {
		t.Errorf("expected owner+tracked eligible, got %v", ids)
	}
	if set[untracked.ID] {
		t.Errorf("untracked entity should not be eligible, got %v", ids)
	}
}

func TestDistillBucket_RawFallbackNoLLM(t *testing.T) {
	forEachMode(t, testDistillBucket_RawFallbackNoLLM)
}

func testDistillBucket_RawFallbackNoLLM(t *testing.T) {
	c := openTestDB(t) // openTestDB wires no LLM by default in root package
	// Ensure no LLM.
	c.SetLLM(nil)
	ctx := context.Background()

	mems := []Memory{{Content: "Alice is a staff engineer"}, {Content: "Alice likes Go"}}
	lines, distilled := c.distillBucket(ctx, "Alice", profileStaticPrompt, mems)
	if distilled {
		t.Error("expected distilled=false with no LLM")
	}
	if len(lines) != 2 || lines[0] != "Alice is a staff engineer" {
		t.Errorf("expected raw contents, got %v", lines)
	}
}

func TestDistillBucket_LLMBullets(t *testing.T) { forEachMode(t, testDistillBucket_LLMBullets) }

func testDistillBucket_LLMBullets(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	c.SetLLM(&mockLLM{
		summarizeFn: func(_ context.Context, texts []string) (string, error) {
			return "- bullet one\n- bullet two", nil
		},
	})

	mems := []Memory{{Content: "x"}, {Content: "y"}}
	lines, distilled := c.distillBucket(ctx, "Alice", profileStaticPrompt, mems)
	if !distilled {
		t.Error("expected distilled=true")
	}
	if len(lines) != 2 || lines[0] != "bullet one" || lines[1] != "bullet two" {
		t.Errorf("expected cleaned bullets, got %v", lines)
	}
}

func TestDistillBucket_LLMErrorFallsBack(t *testing.T) {
	forEachMode(t, testDistillBucket_LLMErrorFallsBack)
}

func testDistillBucket_LLMErrorFallsBack(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	c.SetLLM(&mockLLM{
		summarizeFn: func(_ context.Context, _ []string) (string, error) {
			return "", context.DeadlineExceeded
		},
	})

	mems := []Memory{{Content: "raw fact"}}
	lines, distilled := c.distillBucket(ctx, "Alice", profileStaticPrompt, mems)
	if distilled {
		t.Error("expected distilled=false after LLM error")
	}
	if len(lines) != 1 || lines[0] != "raw fact" {
		t.Errorf("expected raw fallback, got %v", lines)
	}
}

func TestDistillBucket_EmptyNoCall(t *testing.T) { forEachMode(t, testDistillBucket_EmptyNoCall) }

func testDistillBucket_EmptyNoCall(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	called := false
	c.SetLLM(&mockLLM{summarizeFn: func(_ context.Context, _ []string) (string, error) {
		called = true
		return "x", nil
	}})
	lines, distilled := c.distillBucket(ctx, "Alice", profileStaticPrompt, nil)
	if called {
		t.Error("LLM should not be called for empty bucket")
	}
	if len(lines) != 0 || !distilled {
		t.Errorf("expected empty distilled result, got lines=%v distilled=%v", lines, distilled)
	}
}

func TestProfile_BuildsAndCaches(t *testing.T) { forEachMode(t, testProfile_BuildsAndCaches) }

func testProfile_BuildsAndCaches(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	c.SetLLM(nil) // raw fallback, deterministic

	e := &Entity{Type: "person", Name: "Alice"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	m := &Memory{Content: "Alice is a staff engineer", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}

	p, err := c.Profile(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Cached {
		t.Error("first call should build, not serve cache")
	}
	if p.Name != "Alice" {
		t.Errorf("name = %q", p.Name)
	}
	// One recent memory -> dynamic.
	if len(p.Dynamic) != 1 || p.Dynamic[0] != "Alice is a staff engineer" {
		t.Errorf("dynamic = %v", p.Dynamic)
	}

	// Second call: nothing changed -> served from cache.
	p2, err := c.Profile(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !p2.Cached {
		t.Error("second call should serve cache")
	}
}

func TestProfile_RebuildsWhenMemoryCountChanges(t *testing.T) {
	forEachMode(t, testProfile_RebuildsWhenMemoryCountChanges)
}

func testProfile_RebuildsWhenMemoryCountChanges(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	c.SetLLM(nil)

	e := &Entity{Type: "person", Name: "Bob"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Profile(ctx, e.ID); err != nil {
		t.Fatal(err)
	}

	// Add a memory; count changes -> dirty.
	m := &Memory{Content: "Bob joined", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	p, err := c.Profile(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if p.Cached {
		t.Error("expected rebuild after memory count changed")
	}
}

func TestProfile_RebuildsWhenTTLExpired(t *testing.T) {
	forEachMode(t, testProfile_RebuildsWhenTTLExpired)
}

func testProfile_RebuildsWhenTTLExpired(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	c.SetLLM(nil)

	e := &Entity{Type: "person", Name: "Carol"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Profile(ctx, e.ID); err != nil {
		t.Fatal(err)
	}
	// TTL of 0 forces staleness on the next read.
	p, err := c.Profile(ctx, e.ID, WithProfileTTL(0))
	if err != nil {
		t.Fatal(err)
	}
	if p.Cached {
		t.Error("expected rebuild with TTL=0")
	}
}

func TestProfile_EntityNotFound(t *testing.T) { forEachMode(t, testProfile_EntityNotFound) }

func testProfile_EntityNotFound(t *testing.T) {
	c := openTestDB(t)
	if _, err := c.Profile(context.Background(), "nope"); err == nil {
		t.Error("expected error for missing entity")
	}
}

func TestRefreshProfiles_BuildsEligible(t *testing.T) {
	forEachMode(t, testRefreshProfiles_BuildsEligible)
}

func testRefreshProfiles_BuildsEligible(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	c.SetLLM(nil)

	owner := &Entity{Type: "person", Name: "Me", Source: "owner"}
	if err := c.PutEntity(ctx, owner); err != nil {
		t.Fatal(err)
	}
	markOwner(t, c, owner.ID)
	tracked := &Entity{Type: "person", Name: "Alice"}
	if err := c.PutEntity(ctx, tracked); err != nil {
		t.Fatal(err)
	}
	if err := c.TrackProfile(ctx, tracked.ID); err != nil {
		t.Fatal(err)
	}
	untracked := &Entity{Type: "person", Name: "Carol"}
	if err := c.PutEntity(ctx, untracked); err != nil {
		t.Fatal(err)
	}

	rep, err := c.RefreshProfiles(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Scanned != 2 || rep.Rebuilt != 2 {
		t.Errorf("scanned=%d rebuilt=%d, want 2/2", rep.Scanned, rep.Rebuilt)
	}
	// Untracked entity should have no cached profile.
	got, _ := c.GetEntity(ctx, untracked.ID)
	if _, ok := got.Attributes[attrProfile]; ok {
		t.Error("untracked entity should not be profiled")
	}
}
