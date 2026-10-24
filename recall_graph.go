package cortex

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/sausheong/cortex/internal/model"
)

// Graph-aware recall. A query that names entities (by name or alias) gets
// those entities as seeds. Recall then (1) adds the seeds' own memories as
// one more ranked list in the fusion, and (2) boosts every result linked to
// an entity within seedHops of a seed, more for closer entities. Queries that
// name nothing are unaffected.

const (
	seedHops     = 2
	maxSeeds     = 5
	maxNgramSize = 5
)

// proximityWeight is the score multiplier bonus for a result linked to an
// entity at a given hop distance from a seed (index = hops).
var proximityWeight = [seedHops + 1]float64{0.5, 0.25, 0.1}

// querySeeds resolves the entities a query names: every word n-gram (up to
// maxNgramSize words) that equals an entity name or a name/identifier alias,
// ignoring case. Longer matches win; at most maxSeeds are returned.
func (c *Cortex) querySeeds(ctx context.Context, query string) ([]string, error) {
	grams := queryNgrams(query)
	if len(grams) == 0 {
		return nil, nil
	}
	rows, err := c.db.QueryContext(ctx,
		`SELECT e.id, lower(e.name) FROM entities e
		 WHERE e.user_id = ? AND e.deleted_at IS NULL AND lower(e.name) = ANY(?)
		 UNION
		 SELECT e.id, a.value_norm FROM entity_aliases a
		 JOIN entities e ON e.id = a.entity_id AND e.deleted_at IS NULL
		 WHERE a.user_id = ? AND a.value_norm = ANY(?)`,
		c.userID, grams, c.userID, grams)
	if err != nil {
		return nil, fmt.Errorf("cortex: query seeds: %w", err)
	}
	defer rows.Close()
	type hit struct {
		id    string
		words int
	}
	best := map[string]int{}
	for rows.Next() {
		var id, matched string
		if err := rows.Scan(&id, &matched); err != nil {
			return nil, fmt.Errorf("cortex: scan seed: %w", err)
		}
		if w := len(strings.Fields(matched)); w > best[id] {
			best[id] = w
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cortex: query seeds: %w", err)
	}
	hits := make([]hit, 0, len(best))
	for id, w := range best {
		hits = append(hits, hit{id, w})
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].words != hits[j].words {
			return hits[i].words > hits[j].words
		}
		return hits[i].id < hits[j].id
	})
	if len(hits) > maxSeeds {
		hits = hits[:maxSeeds]
	}
	ids := make([]string, len(hits))
	for i, h := range hits {
		ids[i] = h.id
	}
	return ids, nil
}

// queryNgrams returns the distinct lower-cased word n-grams of a query, up
// to maxNgramSize words, skipping single words shorter than three letters.
// Words keep inner punctuation (emails, "S.S.") but lose surrounding
// punctuation.
func queryNgrams(query string) []string {
	var words []string
	for _, w := range strings.Fields(strings.ToLower(query)) {
		w = strings.Trim(w, ".,;:!?()[]{}\"'`")
		w = strings.TrimSuffix(w, "'s")
		if w != "" {
			words = append(words, w)
		}
	}
	seen := map[string]bool{}
	var out []string
	for n := 1; n <= maxNgramSize; n++ {
		for i := 0; i+n <= len(words); i++ {
			g := strings.Join(words[i:i+n], " ")
			if n == 1 && len([]rune(g)) < 3 {
				continue
			}
			if !seen[g] {
				seen[g] = true
				out = append(out, g)
			}
		}
	}
	return out
}

// neighbourhood returns every entity within hops of the seeds, with its
// distance, over the relationships that hold under mode (now by default).
// The owner is not walked through unless it is itself a seed: it touches
// nearly everything, so expanding it would boost the whole graph.
func (c *Cortex) neighbourhood(ctx context.Context, seeds []string, hops int, mode model.TemporalMode) (map[string]int, error) {
	dist := map[string]int{}
	if len(seeds) == 0 {
		return dist, nil
	}
	ownerID, _ := c.OwnerEntityID(ctx)
	validity, vargs := edgeValidityClause("r", mode.IncludeInvalid, mode.AsOf)
	args := append([]any{seeds, c.userID}, vargs...)
	args = append(args, hops, ownerID, c.userID)
	rows, err := c.db.QueryContext(ctx,
		`WITH RECURSIVE walk(id, depth) AS (
		   SELECT unnest(?::text[]), 0
		   UNION
		   SELECT CASE WHEN r.source_id = w.id THEN r.target_id ELSE r.source_id END, w.depth + 1
		   FROM walk w
		   JOIN relationships r ON r.user_id = ? AND r.deleted_at IS NULL AND `+validity+`
		    AND (r.source_id = w.id OR r.target_id = w.id)
		   WHERE w.depth < ? AND (w.depth = 0 OR w.id <> ?)
		 )
		 SELECT w.id, min(w.depth) FROM walk w
		 JOIN entities e ON e.id = w.id AND e.user_id = ? AND e.deleted_at IS NULL
		 GROUP BY w.id`, args...)
	if err != nil {
		return nil, fmt.Errorf("cortex: neighbourhood: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var d int
		if err := rows.Scan(&id, &d); err != nil {
			return nil, fmt.Errorf("cortex: scan neighbourhood: %w", err)
		}
		dist[id] = d
	}
	return dist, rows.Err()
}

// graphContext is what one Recall knows about the entities its query names.
type graphContext struct {
	seeds []string
	dist  map[string]int
}

func (c *Cortex) buildGraphContext(ctx context.Context, query string, mode model.TemporalMode) graphContext {
	seeds, err := c.querySeeds(ctx, query)
	if err != nil {
		c.logf("cortex: recall graph seeds: %v", err)
		return graphContext{}
	}
	if len(seeds) == 0 {
		return graphContext{}
	}
	dist, err := c.neighbourhood(ctx, seeds, seedHops, mode)
	if err != nil {
		c.logf("cortex: recall graph neighbourhood: %v", err)
		return graphContext{seeds: seeds}
	}
	return graphContext{seeds: seeds, dist: dist}
}

// boost returns the score multiplier for a result linked to entityIDs: one
// plus the proximity weight of its closest linked entity, or 1 when none is
// in the neighbourhood.
func (g graphContext) boost(entityIDs []string) float64 {
	best := -1
	for _, id := range entityIDs {
		if d, ok := g.dist[id]; ok && (best < 0 || d < best) {
			best = d
		}
	}
	if best < 0 || best >= len(proximityWeight) {
		return 1
	}
	return 1 + proximityWeight[best]
}

// recallSeedMemories ranks the memories linked directly to the seed
// entities, honouring the temporal mode: memories linked to more seeds
// first, then higher confidence, then newer.
func (c *Cortex) recallSeedMemories(ctx context.Context, g graphContext, limit int, mode model.TemporalMode) ([]rankedItem, map[string]Result) {
	if len(g.seeds) == 0 {
		return nil, nil
	}
	clause, targs := mode.Clause("m")
	args := []any{c.userID, g.seeds}
	args = append(args, targs...)
	args = append(args, limit)
	rows, err := c.db.QueryContext(ctx,
		`SELECT m.id, m.content, COALESCE(m.source, ''), COALESCE(m.speaker, ''), m.confidence
		 FROM memories m
		 JOIN memory_entities me ON me.memory_id = m.id AND me.user_id = m.user_id
		 WHERE m.user_id = ? AND m.deleted_at IS NULL AND me.entity_id = ANY(?) AND `+clause+`
		 GROUP BY m.id
		 ORDER BY count(*) DESC, m.confidence DESC, m.created_at DESC, m.id
		 LIMIT ?`, args...)
	if err != nil {
		c.logf("cortex: recall seed memories: %v", err)
		return nil, nil
	}
	defer rows.Close()
	var items []rankedItem
	results := map[string]Result{}
	for rows.Next() {
		var r Result
		if err := rows.Scan(&r.ID, &r.Content, &r.Source, &r.Speaker, &r.Confidence); err != nil {
			c.logf("cortex: scan seed memory: %v", err)
			return nil, nil
		}
		r.Type = "memory"
		key := "mem:" + r.ID
		items = append(items, rankedItem{id: key, rank: len(items)})
		results[key] = r
	}
	if err := rows.Err(); err != nil {
		c.logf("cortex: recall seed memories: %v", err)
		return nil, nil
	}
	// Load each memory's full entity links so boosting sees all of them.
	for key, r := range results {
		if ids, err := c.loadMemoryEntityIDs(ctx, r.ID); err == nil {
			r.EntityIDs = ids
			results[key] = r
		}
	}
	return items, results
}

// popularityCap bounds how much access history can lift a memory's score,
// so frequently-recalled memories cannot crowd out better matches.
const popularityCap = 0.15

// popularity is the score multiplier for a memory recalled n times before:
// 1 + 0.05·log2(1+n), capped at 1 + popularityCap.
func popularity(n int) float64 {
	if n <= 0 {
		return 1
	}
	return 1 + math.Min(popularityCap, 0.05*math.Log2(1+float64(n)))
}

// accessCounts loads access_count for the memory keys ("mem:<id>") among
// items, keyed by key.
func (c *Cortex) accessCounts(ctx context.Context, items []rankedItem) map[string]int {
	var ids []string
	for _, it := range items {
		if id, ok := strings.CutPrefix(it.id, "mem:"); ok {
			ids = append(ids, id)
		}
	}
	out := map[string]int{}
	if len(ids) == 0 {
		return out
	}
	rows, err := c.db.QueryContext(ctx,
		`SELECT id, access_count FROM memories WHERE user_id = ? AND id = ANY(?) AND access_count > 0`, c.userID, ids)
	if err != nil {
		c.logf("cortex: recall access counts: %v", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return out
		}
		out["mem:"+id] = n
	}
	return out
}

// markAccessed records that recall returned these results: each memory's
// access_count goes up by one and last_accessed_at moves to now.
func (c *Cortex) markAccessed(ctx context.Context, results []Result) {
	var ids []string
	for _, r := range results {
		if r.Type == "memory" {
			ids = append(ids, r.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	if _, err := c.db.ExecContext(ctx,
		`UPDATE memories SET access_count = access_count + 1, last_accessed_at = now()
		 WHERE user_id = ? AND id = ANY(?)`, c.userID, ids); err != nil {
		c.logf("cortex: record recall access: %v", err)
	}
}
