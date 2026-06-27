package cortex

import (
	"context"
	"testing"
	"time"
)

func TestTrackProfile_SetsMarker(t *testing.T) {
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
	c := openTestDB(t)
	ctx := context.Background()

	owner := &Entity{Type: "person", Name: "Me", Source: "owner"}
	if err := c.PutEntity(ctx, owner); err != nil {
		t.Fatal(err)
	}
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

func TestPartitionMemories_RecencyAndCaps(t *testing.T) {
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	cfg := profileConfig{recentK: 2, window: 30 * 24 * time.Hour, staticCap: 3}

	mk := func(content string, daysAgo int, conf float64) Memory {
		return Memory{
			Content:    content,
			Confidence: conf,
			CreatedAt:  now.AddDate(0, 0, -daysAgo),
		}
	}
	mems := []Memory{
		mk("recent-1", 1, 0.5),  // dynamic candidate (newest)
		mk("recent-2", 2, 0.5),  // dynamic candidate
		mk("recent-3", 3, 0.5),  // in window but beyond recentK -> static
		mk("old-high", 100, 0.9),
		mk("old-mid", 200, 0.7),
		mk("old-low", 300, 0.1), // should be dropped by staticCap=3
	}

	static, dynamic := partitionMemories(mems, cfg, now)

	if len(dynamic) != 2 {
		t.Fatalf("dynamic len = %d, want 2", len(dynamic))
	}
	if dynamic[0].Content != "recent-1" || dynamic[1].Content != "recent-2" {
		t.Errorf("dynamic order wrong: %q, %q", dynamic[0].Content, dynamic[1].Content)
	}
	if len(static) != 3 {
		t.Fatalf("static len = %d, want 3 (cap)", len(static))
	}
	// static sorted by confidence desc: old-high(0.9), old-mid(0.7), recent-3(0.5)
	if static[0].Content != "old-high" || static[1].Content != "old-mid" || static[2].Content != "recent-3" {
		t.Errorf("static order wrong: %v", []string{static[0].Content, static[1].Content, static[2].Content})
	}
	for _, m := range static {
		if m.Content == "old-low" {
			t.Error("old-low should have been dropped by staticCap")
		}
	}
}

func TestPartitionMemories_Empty(t *testing.T) {
	s, d := partitionMemories(nil, defaultProfileConfig(), time.Now())
	if len(s) != 0 || len(d) != 0 {
		t.Errorf("expected empty partitions, got static=%d dynamic=%d", len(s), len(d))
	}
}
