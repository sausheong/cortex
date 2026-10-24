package maintain

import (
	"testing"
	"time"

	"github.com/sausheong/cortex/internal/model"
)

func TestPartitionMemories_RecencyAndCaps(t *testing.T) {
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	cfg := model.ProfileConfig{RecentK: 2, Window: 30 * 24 * time.Hour, StaticCap: 3}

	mk := func(content string, daysAgo int, conf float64) model.Memory {
		return model.Memory{
			Content:    content,
			Confidence: conf,
			CreatedAt:  now.AddDate(0, 0, -daysAgo),
		}
	}
	mems := []model.Memory{
		mk("recent-1", 1, 0.5), // dynamic candidate (newest)
		mk("recent-2", 2, 0.5), // dynamic candidate
		mk("recent-3", 3, 0.5), // in window but beyond recentK -> static
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
	s, d := partitionMemories(nil, model.DefaultProfileConfig(), time.Now())
	if len(s) != 0 || len(d) != 0 {
		t.Errorf("expected empty partitions, got static=%d dynamic=%d", len(s), len(d))
	}
}

func TestPartitionMemories_StaticAlwaysStatic(t *testing.T) {
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	cfg := model.ProfileConfig{RecentK: 5, Window: 30 * 24 * time.Hour, StaticCap: 15}

	mk := func(content string, daysAgo int, static bool) model.Memory {
		return model.Memory{Content: content, Confidence: 0.9, CreatedAt: now.AddDate(0, 0, -daysAgo), Static: static}
	}
	mems := []model.Memory{
		mk("identity-today", 0, true), // static, created TODAY -> must be static[]
		mk("episode-today", 1, false), // non-static, recent -> dynamic[]
		mk("identity-old", 200, true), // static, old -> static[]
	}

	static, dynamic := partitionMemories(mems, cfg, now)

	inStatic := func(s string) bool {
		for _, m := range static {
			if m.Content == s {
				return true
			}
		}
		return false
	}
	inDynamic := func(s string) bool {
		for _, m := range dynamic {
			if m.Content == s {
				return true
			}
		}
		return false
	}

	if !inStatic("identity-today") {
		t.Error("static memory created today must be in static[]")
	}
	if inDynamic("identity-today") {
		t.Error("static memory must never be in dynamic[]")
	}
	if !inStatic("identity-old") {
		t.Error("old static memory must be in static[]")
	}
	if !inDynamic("episode-today") {
		t.Error("recent non-static memory must be in dynamic[]")
	}
}
