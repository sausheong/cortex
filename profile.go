package cortex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/sausheong/cortex/internal/llmutil"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/tenancy"
	"github.com/sausheong/cortex/internal/vecmath"
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

	owners, err := c.FindEntities(ctx, EntityFilter{Type: "person", Source: "owner"})
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
func partitionMemories(mems []Memory, cfg model.ProfileConfig, now time.Time) (static, dynamic []Memory) {
	cutoff := now.Add(-cfg.Window)

	var recent, rest []Memory
	for _, m := range mems {
		// Static memories are never dynamic: route straight to the static pool.
		if m.Static {
			rest = append(rest, m)
			continue
		}
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
	if len(recent) > cfg.RecentK {
		rest = append(rest, recent[cfg.RecentK:]...)
		recent = recent[:cfg.RecentK]
	}
	dynamic = recent

	// Static: confidence desc, then recency desc; capped to staticCap.
	sort.SliceStable(rest, func(i, j int) bool {
		if rest[i].Confidence != rest[j].Confidence {
			return rest[i].Confidence > rest[j].Confidence
		}
		return rest[i].CreatedAt.After(rest[j].CreatedAt)
	})
	if len(rest) > cfg.StaticCap {
		rest = rest[:cfg.StaticCap]
	}
	static = rest
	return static, dynamic
}

const profileStaticPrompt = "Below are stable, long-term facts about %s. " +
	"Write a concise, deduplicated bullet list that represents who they are as a whole person, " +
	"covering (where the facts support it): identity, background, work, strengths and capabilities, " +
	"preferences, and key relationships. Merge near-duplicate facts into one bullet and give no single " +
	"acquaintance or topic more than a bullet or two. One fact per line, no preamble. " +
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
	lines, distilled, _ = c.distillBucketErr(ctx, name, instruction, mems)
	return lines, distilled
}

// distillBucketErr is distillBucket plus the Summarize error (nil when the LLM
// was absent, returned nothing usable, or succeeded). Transient provider
// failures are retried before falling back to raw contents.
func (c *Cortex) distillBucketErr(ctx context.Context, name, instruction string, mems []Memory) (lines []string, distilled bool, err error) {
	if len(mems) == 0 {
		return nil, true, nil
	}
	raw := make([]string, len(mems))
	for i, m := range mems {
		raw[i] = m.Content
	}

	if c.h.Cfg.LLM == nil {
		return raw, false, nil
	}

	texts := append([]string{fmt.Sprintf(instruction, name)}, raw...)
	out, err := llmutil.RetryLLM(ctx, func() (string, error) { return c.h.Cfg.LLM.Summarize(ctx, texts) })
	if err != nil {
		if c.h.Cfg.Logf != nil {
			c.h.Cfg.Logf("cortex: profile distill (%s): %v; falling back to raw", name, err)
		}
		return raw, false, err
	}
	lines = cleanBullets(out)
	if len(lines) == 0 {
		// LLM returned nothing usable; fall back rather than emit an empty
		// distilled section.
		return raw, false, nil
	}
	return lines, true, nil
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
	cfg := model.DefaultProfileConfig()
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

	// A cached undistilled digest is rebuilt once an LLM is available.
	if cp, ok := readCachedProfile(e); ok && isFresh(cp, len(mems), cfg, now) && (cp.Distilled || c.h.Cfg.LLM == nil) {
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
	p, _, err := c.buildProfile(ctx, e, mems, cfg, now)
	return p, err
}

// CachedProfile returns the owner entity's stored digest without ever
// building one or calling a model. ok is false when the user has no owner
// entity or no digest has been built yet.
func (c *Cortex) CachedProfile(ctx context.Context) (Profile, bool, error) {
	id, err := tenancy.New(c.h).OwnerEntityID(ctx)
	if errors.Is(err, ErrNotFound) {
		return Profile{}, false, nil
	}
	if err != nil {
		return Profile{}, false, err
	}
	e, err := c.GetEntity(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return Profile{}, false, nil
	}
	if err != nil {
		return Profile{}, false, err
	}
	cp, ok := readCachedProfile(e)
	if !ok {
		return Profile{}, false, nil
	}
	return Profile{EntityID: e.ID, Name: e.Name, Static: cp.Static, Dynamic: cp.Dynamic,
		BuiltAt: cp.BuiltAt, Distilled: cp.Distilled, Cached: true}, true, nil
}

// buildProfileFromMemories partitions + distills the given memories, persists
// the digest to the entity's attributes, and returns it with Cached=false.
func (c *Cortex) buildProfileFromMemories(ctx context.Context, e *Entity, mems []Memory, cfg model.ProfileConfig, now time.Time) (Profile, error) {
	p, _, err := c.buildProfile(ctx, e, mems, cfg, now)
	return p, err
}

// buildProfile is buildProfileFromMemories plus the first distill error.
// The static half is built from a diverse candidate selection (see
// selectStaticCandidates) rather than the N highest-confidence memories.
func (c *Cortex) buildProfile(ctx context.Context, e *Entity, mems []Memory, cfg model.ProfileConfig, now time.Time) (p Profile, distillErr, err error) {
	poolCfg := cfg
	poolCfg.StaticCap = len(mems) + 1 // partition keeps the whole static pool; diversity trims it
	staticPool, dynamicMems := partitionMemories(mems, poolCfg, now)
	staticMems := selectStaticCandidates(staticPool, e.ID, cfg.StaticCap)
	staticLines, sDist, sErr := c.distillBucketErr(ctx, e.Name, profileStaticPrompt, staticMems)
	dynamicLines, dDist, dErr := c.distillBucketErr(ctx, e.Name, profileDynamicPrompt, dynamicMems)
	distilled := sDist && dDist
	distillErr = sErr
	if distillErr == nil {
		distillErr = dErr
	}

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
		return Profile{}, distillErr, fmt.Errorf("cortex: persist profile: %w", err)
	}

	return Profile{
		EntityID:  e.ID,
		Name:      e.Name,
		Static:    staticLines,
		Dynamic:   dynamicLines,
		BuiltAt:   now,
		Distilled: distilled,
		Cached:    false,
	}, distillErr, nil
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
func isFresh(cp cachedProfile, currentCount int, cfg model.ProfileConfig, now time.Time) bool {
	if cp.MemoryCount != currentCount {
		return false
	}
	if now.Sub(cp.BuiltAt) > cfg.TTL {
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
	cfg := model.DefaultProfileConfig()
	for _, o := range opts {
		o(&cfg)
	}
	for _, id := range ids {
		p, derr, err := c.rebuildProfile(ctx, id, cfg)
		if err != nil {
			if ctx.Err() != nil {
				return rep, ctx.Err()
			}
			rep.Skipped = append(rep.Skipped, id)
			if c.h.Cfg.Logf != nil {
				c.h.Cfg.Logf("cortex: refresh profile %s: %v", id, err)
			}
			continue
		}
		rep.Rebuilt++
		if !p.Distilled && derr != nil {
			rep.Undistilled++
			llmutil.AddReportError(&rep.Errors, "distill profile %s: %v", id, derr)
		}
	}
	return rep, nil
}

// rebuildProfile forces a rebuild of one entity's digest, ignoring the cache.
func (c *Cortex) rebuildProfile(ctx context.Context, entityID string, cfg model.ProfileConfig) (p Profile, distillErr, err error) {
	e, err := c.GetEntity(ctx, entityID)
	if err != nil {
		return Profile{}, nil, err
	}
	mems, err := c.GetMemoriesByEntity(ctx, entityID)
	if err != nil {
		return Profile{}, nil, err
	}
	return c.buildProfile(ctx, e, mems, cfg, time.Now().UTC())
}

// Static-candidate selection tuning.
const (
	// staticNearDupJaccard is the word-set similarity at/above which two
	// memories count as near-duplicates (only the better-ranked one is kept).
	staticNearDupJaccard = 0.6
	// staticPerEntityCap bounds how many candidates may be co-linked to any one
	// other entity, so a single acquaintance cannot dominate the static half.
	staticPerEntityCap = 5
)

var (
	nonWordRE = regexp.MustCompile(`[^\p{L}\p{N}]+`)
	// profileSignalWords flag identity / preference / capability / background
	// content, which is what a profile's static half should be made of.
	profileSignalWords = []string{
		"is a ", "is an ", "works ", "working ", "work as", "role", "job", "career", "founder", "engineer",
		"lives", "based in", "born", "grew up", "from ", "studied", "degree", "graduated", "university",
		"prefers", "prefer", "likes", "loves", "enjoys", "favorite", "favourite", "dislikes", "hates",
		"skilled", "expert", "experience", "proficient", "good at", "speaks", "knows how", "strength",
		"background", "married", "wife", "husband", "daughter", "son ", "family", "hobby", "interested in",
	}
)

func normalizeText(s string) string {
	return strings.TrimSpace(nonWordRE.ReplaceAllString(strings.ToLower(s), " "))
}

func wordSet(norm string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, w := range strings.Fields(norm) {
		set[w] = struct{}{}
	}
	return set
}

// staticPriority ranks a memory for inclusion in the static half: identity-ish
// content and confidence first, a Static flag as a bonus.
func staticPriority(m Memory) float64 {
	score := m.Confidence
	if m.Static {
		score += 0.5
	}
	low := strings.ToLower(m.Content)
	for _, w := range profileSignalWords {
		if strings.Contains(low, w) {
			score += 0.4
			break
		}
	}
	return score
}

// selectStaticCandidates picks up to limit memories to feed the static
// distillation, favouring breadth over recency: candidates are ranked by
// staticPriority (then recency), near-duplicates of an already-chosen memory
// (same normalized text or word-set Jaccard >= staticNearDupJaccard) are
// dropped, and at most staticPerEntityCap chosen memories may link to any one
// entity other than selfID.
func selectStaticCandidates(pool []Memory, selfID string, limit int) []Memory {
	if limit <= 0 || len(pool) == 0 {
		return nil
	}
	ranked := append([]Memory(nil), pool...)
	sort.SliceStable(ranked, func(i, j int) bool {
		pi, pj := staticPriority(ranked[i]), staticPriority(ranked[j])
		if pi != pj {
			return pi > pj
		}
		return ranked[i].CreatedAt.After(ranked[j].CreatedAt)
	})

	var out []Memory
	var sets []map[string]struct{}
	perEntity := map[string]int{}
	for _, m := range ranked {
		if len(out) >= limit {
			break
		}
		ws := wordSet(normalizeText(m.Content))
		if len(ws) == 0 {
			continue
		}
		dup := false
		for _, o := range sets {
			if vecmath.Jaccard(ws, o) >= staticNearDupJaccard {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		var others []string
		for _, id := range m.EntityIDs {
			if id != selfID {
				others = append(others, id)
			}
		}
		full := false
		for _, id := range others {
			if perEntity[id] >= staticPerEntityCap {
				full = true
				break
			}
		}
		if full {
			continue
		}
		for _, id := range others {
			perEntity[id]++
		}
		out = append(out, m)
		sets = append(sets, ws)
	}
	return out
}
