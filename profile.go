package cortex

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
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

const profileStaticPrompt = "Below are stable, long-term facts about %s. " +
	"Distill them into a concise deduplicated bullet list of who they are — " +
	"role, traits, durable preferences. One fact per line, no preamble. " +
	"Drop anything episodic or time-bound."

const profileDynamicPrompt = "Below are recent notes about %s. " +
	"Distill them into a short bullet list of their current context and recent " +
	"activity. One item per line, no preamble."

// distillBucket turns a set of memories into digest lines. With an LLM
// configured it asks Summarize for a bullet list (instruction prepended,
// name interpolated) and cleans the output into lines; on a nil LLM or any
// Summarize error it falls back to the raw memory contents and reports
// distilled=false (logging the error via logf when present). An empty bucket
// returns (nil, true) without calling the LLM.
func (c *Cortex) distillBucket(ctx context.Context, name, instruction string, mems []Memory) (lines []string, distilled bool) {
	if len(mems) == 0 {
		return nil, true
	}
	raw := make([]string, len(mems))
	for i, m := range mems {
		raw[i] = m.Content
	}

	if c.cfg.llm == nil {
		return raw, false
	}

	texts := append([]string{fmt.Sprintf(instruction, name)}, raw...)
	out, err := c.cfg.llm.Summarize(ctx, texts)
	if err != nil {
		if c.cfg.logf != nil {
			c.cfg.logf("cortex: profile distill (%s): %v; falling back to raw", name, err)
		}
		return raw, false
	}
	lines = cleanBullets(out)
	if len(lines) == 0 {
		// LLM returned nothing usable; fall back rather than emit an empty
		// distilled section.
		return raw, false
	}
	return lines, true
}

// cleanBullets splits Summarize output into trimmed, non-empty lines with any
// leading "- ", "* ", or "• " bullet marker removed.
func cleanBullets(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		t = strings.TrimPrefix(t, "- ")
		t = strings.TrimPrefix(t, "* ")
		t = strings.TrimPrefix(t, "• ")
		t = strings.TrimSpace(t)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// cachedProfile is the digest stored under attrProfile in an entity's
// attributes JSON.
type cachedProfile struct {
	Static      []string  `json:"static"`
	Dynamic     []string  `json:"dynamic"`
	BuiltAt     time.Time `json:"built_at"`
	MemoryCount int       `json:"memory_count"`
	Distilled   bool      `json:"distilled"`
}

// Profile returns the entity's context digest. It serves a cached digest when
// fresh, otherwise rebuilds and caches it. Freshness fails (forcing a rebuild)
// when there is no cache, the cache is older than the TTL, or the entity's
// current valid-memory count differs from the cached count.
func (c *Cortex) Profile(ctx context.Context, entityID string, opts ...ProfileOption) (Profile, error) {
	cfg := defaultProfileConfig()
	for _, o := range opts {
		o(&cfg)
	}
	now := time.Now().UTC()

	e, err := c.GetEntity(ctx, entityID)
	if err != nil {
		return Profile{}, err
	}

	mems, err := c.GetMemoriesByEntity(ctx, entityID)
	if err != nil {
		return Profile{}, err
	}

	if cp, ok := readCachedProfile(e); ok && isFresh(cp, len(mems), cfg, now) {
		return Profile{
			EntityID:  e.ID,
			Name:      e.Name,
			Static:    cp.Static,
			Dynamic:   cp.Dynamic,
			BuiltAt:   cp.BuiltAt,
			Distilled: cp.Distilled,
			Cached:    true,
		}, nil
	}
	return c.buildProfileFromMemories(ctx, e, mems, cfg, now)
}

// buildProfileFromMemories partitions + distills the given memories, persists
// the digest to the entity's attributes, and returns it with Cached=false.
func (c *Cortex) buildProfileFromMemories(ctx context.Context, e *Entity, mems []Memory, cfg profileConfig, now time.Time) (Profile, error) {
	staticMems, dynamicMems := partitionMemories(mems, cfg, now)
	staticLines, sDist := c.distillBucket(ctx, e.Name, profileStaticPrompt, staticMems)
	dynamicLines, dDist := c.distillBucket(ctx, e.Name, profileDynamicPrompt, dynamicMems)
	distilled := sDist && dDist

	cp := cachedProfile{
		Static:      staticLines,
		Dynamic:     dynamicLines,
		BuiltAt:     now,
		MemoryCount: len(mems),
		Distilled:   distilled,
	}
	if e.Attributes == nil {
		e.Attributes = map[string]any{}
	}
	e.Attributes[attrProfile] = cp
	if err := c.PutEntity(ctx, e); err != nil {
		return Profile{}, fmt.Errorf("cortex: persist profile: %w", err)
	}

	return Profile{
		EntityID:  e.ID,
		Name:      e.Name,
		Static:    staticLines,
		Dynamic:   dynamicLines,
		BuiltAt:   now,
		Distilled: distilled,
		Cached:    false,
	}, nil
}

// readCachedProfile decodes the cached digest from an entity's attributes.
// The value round-trips through JSON because attributes are stored as JSON
// (a freshly-set value is a cachedProfile; a reloaded value is a map).
func readCachedProfile(e *Entity) (cachedProfile, bool) {
	raw, ok := e.Attributes[attrProfile]
	if !ok {
		return cachedProfile{}, false
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return cachedProfile{}, false
	}
	var cp cachedProfile
	if err := json.Unmarshal(b, &cp); err != nil {
		return cachedProfile{}, false
	}
	if cp.BuiltAt.IsZero() {
		return cachedProfile{}, false
	}
	return cp, true
}

// isFresh reports whether a cached profile may be served without rebuilding.
func isFresh(cp cachedProfile, currentCount int, cfg profileConfig, now time.Time) bool {
	if cp.MemoryCount != currentCount {
		return false
	}
	if now.Sub(cp.BuiltAt) > cfg.ttl {
		return false
	}
	return true
}

// RefreshProfiles rebuilds the cached digest for every profile-eligible entity
// (owner + tracked). It forces a rebuild regardless of TTL so a scheduled
// Maintain run always refreshes. A single entity's build failure is recorded
// in the report's Skipped list and does not abort the pass.
func (c *Cortex) RefreshProfiles(ctx context.Context, opts ...ProfileOption) (ProfileReport, error) {
	ids, err := c.profileEligibleIDs(ctx)
	if err != nil {
		return ProfileReport{}, err
	}
	rep := ProfileReport{Scanned: len(ids)}
	// Force a rebuild each run by collapsing the TTL; caller opts still apply
	// after this (so an explicit WithProfileTTL would override, which is fine).
	forced := append([]ProfileOption{WithProfileTTL(0)}, opts...)
	for _, id := range ids {
		if _, err := c.Profile(ctx, id, forced...); err != nil {
			rep.Skipped = append(rep.Skipped, id)
			if c.cfg.logf != nil {
				c.cfg.logf("cortex: refresh profile %s: %v", id, err)
			}
			continue
		}
		rep.Rebuilt++
	}
	return rep, nil
}
