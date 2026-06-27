package cortex

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// Reserved entity attribute keys for profiles.
const (
	attrProfile  = "_profile"  // cached digest (cachedProfile, JSON)
	attrProfiled = "_profiled" // tracking marker (bool)
)

// TrackProfile marks an entity for automatic profile refresh during Maintain.
// The owner entity is always eligible regardless of this marker.
func (c *Cortex) TrackProfile(ctx context.Context, entityID string) error {
	e, err := c.GetEntity(ctx, entityID)
	if err != nil {
		return err
	}
	if e.Attributes == nil {
		e.Attributes = map[string]any{}
	}
	e.Attributes[attrProfiled] = true
	return c.PutEntity(ctx, e)
}

// UntrackProfile removes the tracking marker and drops any cached digest.
func (c *Cortex) UntrackProfile(ctx context.Context, entityID string) error {
	e, err := c.GetEntity(ctx, entityID)
	if err != nil {
		return err
	}
	if e.Attributes != nil {
		delete(e.Attributes, attrProfiled)
		delete(e.Attributes, attrProfile)
	}
	return c.PutEntity(ctx, e)
}

// profileEligibleIDs returns the IDs of entities that get auto-refreshed
// profiles: the owner entity (source=owner) plus any entity flagged via
// TrackProfile. Order is owner(s) first, then tracked; duplicates removed.
func (c *Cortex) profileEligibleIDs(ctx context.Context) ([]string, error) {
	seen := map[string]bool{}
	var ids []string

	owners, err := c.FindEntities(ctx, EntityFilter{Source: "owner"})
	if err != nil {
		return nil, fmt.Errorf("cortex: find owner entities: %w", err)
	}
	for _, e := range owners {
		if !seen[e.ID] {
			seen[e.ID] = true
			ids = append(ids, e.ID)
		}
	}

	all, err := c.FindEntities(ctx, EntityFilter{})
	if err != nil {
		return nil, fmt.Errorf("cortex: find entities: %w", err)
	}
	for _, e := range all {
		if v, _ := e.Attributes[attrProfiled].(bool); v && !seen[e.ID] {
			seen[e.ID] = true
			ids = append(ids, e.ID)
		}
	}
	return ids, nil
}

// partitionMemories splits an entity's memories into a recent "dynamic" set
// and a stable "static" set. Dynamic = memories created within the recency
// window, newest first, capped at recentK. Static = everything else, ranked
// by confidence then recency, capped at staticCap. A memory in the recent
// window is only ever dynamic (dynamic wins); statics are drawn from the rest.
func partitionMemories(mems []Memory, cfg profileConfig, now time.Time) (static, dynamic []Memory) {
	cutoff := now.Add(-cfg.window)

	var recent, rest []Memory
	for _, m := range mems {
		if m.CreatedAt.After(cutoff) || m.CreatedAt.Equal(cutoff) {
			recent = append(recent, m)
		} else {
			rest = append(rest, m)
		}
	}

	// Dynamic: newest first, capped to recentK. Overflow falls back to static.
	sort.SliceStable(recent, func(i, j int) bool {
		return recent[i].CreatedAt.After(recent[j].CreatedAt)
	})
	if len(recent) > cfg.recentK {
		rest = append(rest, recent[cfg.recentK:]...)
		recent = recent[:cfg.recentK]
	}
	dynamic = recent

	// Static: confidence desc, then recency desc; capped to staticCap.
	sort.SliceStable(rest, func(i, j int) bool {
		if rest[i].Confidence != rest[j].Confidence {
			return rest[i].Confidence > rest[j].Confidence
		}
		return rest[i].CreatedAt.After(rest[j].CreatedAt)
	})
	if len(rest) > cfg.staticCap {
		rest = rest[:cfg.staticCap]
	}
	static = rest
	return static, dynamic
}
