package memory

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/sausheong/cortex/internal/graph"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/vecmath"
)

// AugmentForEmbedding prepends a memory's linked-entity context to its
// content for the purpose of embedding only (fact-augmented keys, à la
// Anthropic Contextual Retrieval). The stored content, FTS index, and
// query-side embedding are unaffected — this text is passed solely to the
// embedder at ingest, so a semantically-thin memory ("ships in April")
// becomes findable by the entities it concerns. Names are sorted for a
// deterministic key; blank names are dropped; with no names the content is
// returned unchanged.
func AugmentForEmbedding(content string, entityNames []string) string {
	names := make([]string, 0, len(entityNames))
	for _, n := range entityNames {
		if strings.TrimSpace(n) != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return content
	}
	sort.Strings(names)
	return "Entities: " + strings.Join(names, ", ") + ". " + content
}

// RecallWithStrength runs Recall and adds an aggregate strength score plus
// an abstention hint. Use this when the caller (e.g. an agent) needs to
// decide whether the knowledge graph actually knows the answer.
func (c Ops) RecallWithStrength(ctx context.Context, query string, opts ...model.RecallOption) (model.RecallResult, error) {
	results, err := c.Recall(ctx, query, opts...)
	if err != nil {
		return model.RecallResult{}, err
	}
	out := model.RecallResult{Results: results}
	if len(results) == 0 {
		out.Abstain = true // Strength stays 0
		return out, nil
	}
	// Preferred path: an embedder gives an absolute cosine relevance signal,
	// which actually discriminates relevant from irrelevant top hits (the RRF
	// top score is ~constant and cannot). On any embed failure we log and fall
	// through to the RRF fallback so a dead embedder degrades, never errors.
	if c.Cfg.Embedder != nil {
		if s, ok := c.cosineStrength(ctx, query, results); ok {
			out.Strength = s
			out.Abstain = s < model.AbstainRelevanceThreshold
			return out, nil
		}
	}
	// Fallback (no embedder, or embed failed): confidence-weighted RRF top score.
	out.Strength = results[0].Score
	out.Abstain = out.Strength < model.AbstainThreshold
	return out, nil
}

// cosineStrength computes the abstention strength as the max cosine similarity
// between the query and the top results' content. It embeds the query plus the
// top AbstainTopK contents in a single batched call. Max (not mean): one strong
// hit should defeat abstention even when other returned results are weak.
// Returns (maxCos, true) on success, or (0, false) after logging on any embed
// error or short vector slice — the caller then falls back to the RRF signal.
func (c Ops) cosineStrength(ctx context.Context, query string, results []model.Result) (float64, bool) {
	n := len(results)
	if n > model.AbstainTopK {
		n = model.AbstainTopK
	}
	contents := make([]string, n)
	for i := 0; i < n; i++ {
		contents[i] = results[i].Content
	}

	vecs, err := c.Cfg.Embedder.Embed(ctx, append([]string{query}, contents...))
	if err != nil {
		c.Logf("cortex: abstention cosine embed failed: %v", err)
		return 0, false
	}
	if len(vecs) < 1+n {
		c.Logf("cortex: abstention cosine embed returned %d vectors, want %d", len(vecs), 1+n)
		return 0, false
	}

	queryVec := vecs[0]
	var maxCos float64
	for i := 0; i < n; i++ {
		if cos := float64(vecmath.Cosine(queryVec, vecs[i+1])); cos > maxCos {
			maxCos = cos
		}
	}
	return maxCos, true
}

// Recall searches the knowledge graph using multiple retrieval strategies,
// merges the results via reciprocal rank fusion, and returns a unified
// ranked list of Result items.
func (c Ops) Recall(ctx context.Context, query string, opts ...model.RecallOption) ([]model.Result, error) {
	// Rerank defaults OFF: benchmarking on a real graph showed MMR diversity
	// reranking costs ~20pts of mid-rank recall (R@3/R@5) on fact-recall
	// queries, because corroborating memories about the same entity are
	// *similar* and MMR's diversity penalty wrongly demotes them as redundant.
	// Diversity is the wrong objective for "find the specific fact". Opt in via
	// WithRerank(true) for browse/explore use cases that want spread.
	cfg := &model.RecallConfig{Limit: 20, Rerank: false}
	for _, o := range opts {
		o(cfg)
	}

	// Resolve the temporal mode once; it governs the memory read paths.
	mode := model.TemporalMode{IncludeInvalid: cfg.IncludeInvalid, AsOf: cfg.AsOf}

	// Decompose the query into sub-queries.
	subQueries := c.decomposeQuery(ctx, query)

	// Time-aware query expansion (3.2): if the decomposition carries a
	// temporal_filter, derive a recall-global event-time window from its
	// parsed from/to and narrow the memory read paths by it. Absent or
	// unparseable bounds degrade to no narrowing.
	for _, sq := range subQueries {
		if sq.Type != "temporal_filter" {
			continue
		}
		if fromStr, ok := sq.Params["from"].(string); ok {
			mode.RangeFrom = model.ParseEventTime(fromStr)
		}
		if toStr, ok := sq.Params["to"].(string); ok {
			mode.RangeTo = model.ParseEventTime(toStr)
		}
	}

	// Execute sub-queries in parallel, collecting ranked lists and results.
	var (
		mu        sync.Mutex
		lists     [][]vecmath.Ranked
		resultMap = make(map[string]model.Result) // keyed by prefixed ID
	)

	var wg sync.WaitGroup
	for _, sq := range subQueries {
		sq := sq // capture
		wg.Add(1)
		go func() {
			defer wg.Done()
			items, results := c.executeSubQuery(ctx, sq, cfg.Limit, mode)
			mu.Lock()
			defer mu.Unlock()
			if len(items) > 0 {
				lists = append(lists, items)
			}
			for k, v := range results {
				resultMap[k] = v
			}
		}()
	}
	wg.Wait()

	// Graph-aware recall: seed from the entities the query names.
	var graph graphContext
	if !cfg.NoGraph {
		graph = c.buildGraphContext(ctx, query, mode)
		if items, results := c.recallSeedMemories(ctx, graph, cfg.Limit, mode); len(items) > 0 {
			lists = append(lists, items)
			for k, v := range results {
				if existing, ok := resultMap[k]; ok {
					// Keep the other path's richer result (e.g. its source
					// excerpt) but take the full entity links.
					existing.EntityIDs = v.EntityIDs
					resultMap[k] = existing
				} else {
					resultMap[k] = v
				}
			}
		}
	}

	if len(lists) == 0 {
		return []model.Result{}, nil
	}

	// Merge via reciprocal rank fusion.
	merged := vecmath.RRF(lists, 60)

	// Confidence-weight the fusion score: a result's rank score is its RRF
	// score scaled by how certain we are about it. Confidence is in [0,1];
	// coerced at ingest so unset == 1.0 (no penalty). A genuine 0.0 is the
	// least-trustworthy state and is preserved (not rescued), so it ranks
	// last. A defensive negative (only reachable via direct struct misuse) is
	// floored to 0.
	// The graph proximity boost multiplies in too: a result linked to an
	// entity the query names (or one near it) ranks higher. So does a small,
	// capped lift for memories recall has surfaced before.
	accessed := c.accessCounts(ctx, merged)
	weightedScore := func(rawScore float64, r model.Result) float64 {
		conf := r.Confidence
		if conf < 0 {
			conf = 0
		}
		key := "mem:" + r.ID
		if r.Type != "memory" {
			key = ""
		}
		return rawScore * conf * graph.boost(r.EntityIDs) * popularity(accessed[key])
	}

	final := make([]model.Result, 0, len(merged))

	// MMR diversity rerank (3.4): when enabled (default), an embedder is
	// configured, AND results came from more than one retrieval mode, reorder
	// the candidate set so near-duplicate results don't crowd out diverse ones.
	// This is a refinement over the prefixed merged keys (which carry
	// per-result vectors) — it reorders membership, never adds or drops
	// members; the limit cap below is the only reducer.
	//
	// The len(lists) > 1 gate matters: MMR's diversity penalty demotes
	// relevant-but-similar results, which is only worth it when there is an
	// independent retrieval signal (e.g. vector alongside keyword) to justify
	// the trade. With a single retrieval list — the only signal — reranking for
	// diversity measurably hurts mid-rank recall (benchmark: R@3/R@5 dropped
	// ~15-20pts on FTS-only) and rescues nothing, so we keep the fusion order.
	// This also makes a dead vector path (e.g. embedder over quota → empty
	// list) degrade cleanly to keyword order rather than a worse reranked one.
	if cfg.Rerank && c.Cfg.Embedder != nil && len(lists) > 1 && len(merged) > 1 {
		cands := make([]vecmath.MMRCand, 0, len(merged))
		for _, item := range merged {
			r, ok := resultMap[item.ID]
			if !ok {
				continue
			}
			cands = append(cands, vecmath.MMRCand{
				ID:    item.ID, // prefixed key (mem:/chunk:/entity:)
				Score: weightedScore(item.Score, r),
				Vec:   c.vectorForKey(ctx, item.ID),
			})
		}
		// limit == len(cands) → MMR reorders every candidate, drops none.
		order := vecmath.MMR(cands, model.RerankLambda, len(cands))
		for _, key := range order {
			r := resultMap[key]
			r.Score = weightedScore(rrfScore(merged, key), r)
			final = append(final, r)
		}
		// MMR defines the order; skip the plain confidence-weighted sort.
	} else {
		// Build final results from merged ranked items in fusion order, then
		// re-sort by the confidence-weighted score (rrfMerge sorted by raw
		// score).
		for _, item := range merged {
			if r, ok := resultMap[item.ID]; ok {
				r.Score = weightedScore(item.Score, r)
				final = append(final, r)
			}
		}
		sort.SliceStable(final, func(i, j int) bool {
			return final[i].Score > final[j].Score
		})
	}

	// Apply min-confidence filter (post-RRF, pre-limit).
	if cfg.MinConfidence > 0 {
		filtered := final[:0]
		for _, r := range final {
			if r.Confidence >= cfg.MinConfidence {
				filtered = append(filtered, r)
			}
		}
		final = filtered
	}

	// Apply limit.
	if len(final) > cfg.Limit {
		final = final[:cfg.Limit]
	}

	if cfg.TrackAccess {
		c.markAccessed(ctx, final)
	}
	return final, nil
}

// vectorForKey loads the stored embedding for a prefixed merged key so the
// MMR reranker can compare results by similarity. Memory and chunk results
// have vectors written at ingest (looked up by ref type); entities have no
// embedding, so they contribute a nil vector and rank on relevance alone.
func (c Ops) vectorForKey(ctx context.Context, key string) []float32 {
	prefix, id, ok := strings.Cut(key, ":")
	if !ok {
		return nil
	}
	switch prefix {
	case "mem":
		return c.EmbeddingByRef(ctx, id, "memory")
	case "chunk":
		return c.EmbeddingByRef(ctx, id, "chunk")
	default: // "entity" (no embedding) or unknown
		return nil
	}
}

// rrfScore returns the raw fusion score for a prefixed key from the merged
// list, or 0 if absent. Used to recover the score after MMR reorders by key.
func rrfScore(merged []vecmath.Ranked, key string) float64 {
	for _, item := range merged {
		if item.ID == key {
			return item.Score
		}
	}
	return 0
}

// decomposeQuery uses the LLM to break a query into sub-queries.
// Falls back to keyword_search + memory_lookup if no LLM is configured
// or if decomposition returns no results.
func (c Ops) decomposeQuery(ctx context.Context, query string) []model.StructuredQuery {
	if c.Cfg.LLM != nil {
		sqs, err := c.Cfg.LLM.Decompose(ctx, query)
		if err == nil && len(sqs) > 0 {
			return sqs
		}
	}

	// Fallback: keyword search + memory lookup with the raw query.
	return []model.StructuredQuery{
		{Type: "keyword_search", Params: map[string]any{"query": query}},
		{Type: "memory_lookup", Params: map[string]any{"query": query}},
		{Type: "memory_vector", Params: map[string]any{"query": query}},
		{Type: "vector_search", Params: map[string]any{"query": query}},
	}
}

// executeSubQuery runs a single sub-query and returns ranked items plus
// a map of prefixed-ID to Result for later lookup.
func (c Ops) executeSubQuery(ctx context.Context, sq model.StructuredQuery, limit int, mode model.TemporalMode) ([]vecmath.Ranked, map[string]model.Result) {
	query, _ := sq.Params["query"].(string)
	if query == "" {
		return nil, nil
	}

	switch sq.Type {
	case "memory_lookup":
		return c.recallMemories(ctx, query, limit, mode)
	case "temporal_filter":
		return c.recallMemories(ctx, query, limit, mode)
	case "memory_vector":
		return c.recallMemoryVector(ctx, query, limit, mode)
	case "keyword_search":
		return c.recallKeyword(ctx, query, limit)
	case "vector_search":
		return c.recallVector(ctx, query, limit)
	case "graph_traverse":
		return c.recallGraph(ctx, query, limit)
	default:
		return nil, nil
	}
}

func (c Ops) recallMemories(ctx context.Context, query string, limit int, mode model.TemporalMode) ([]vecmath.Ranked, map[string]model.Result) {
	mems, err := c.SearchMemoriesMode(ctx, query, limit, mode)
	if err != nil || len(mems) == 0 {
		return nil, nil
	}

	items := make([]vecmath.Ranked, len(mems))
	results := make(map[string]model.Result, len(mems))
	for i, m := range mems {
		key := "mem:" + m.ID
		items[i] = vecmath.Ranked{ID: key, Rank: i}
		res := model.Result{
			ID:         m.ID,
			Type:       "memory",
			Content:    m.Content,
			Confidence: m.Confidence,
			EntityIDs:  m.EntityIDs,
			Source:     m.Source,
			Speaker:    m.Speaker,
		}
		if excerpt := c.firstChunkBySource(ctx, m.Source); excerpt != "" {
			res.Metadata = map[string]any{"source_excerpt": excerpt}
		}
		results[key] = res
	}
	return items, results
}

// logf emits a diagnostic via the configured logger, or no-ops when none is
// set. Used to surface best-effort failures (e.g. embedder errors) that recall
// otherwise swallows, so a dead embedding endpoint doesn't masquerade as
// "no results".
func (c Ops) logf(format string, args ...any) {
	if c.Cfg.Logf != nil {
		c.Cfg.Logf(format, args...)
	}
}

func (c Ops) recallMemoryVector(ctx context.Context, query string, limit int, mode model.TemporalMode) ([]vecmath.Ranked, map[string]model.Result) {
	if c.Cfg.Embedder == nil {
		return nil, nil
	}
	mems, err := c.searchMemoryVectorMode(ctx, query, limit, mode)
	if err != nil {
		c.Logf("cortex: recall memory_vector embed/search failed: %v", err)
		return nil, nil
	}
	if len(mems) == 0 {
		return nil, nil
	}

	items := make([]vecmath.Ranked, len(mems))
	results := make(map[string]model.Result, len(mems))
	for i, m := range mems {
		key := "mem:" + m.ID
		items[i] = vecmath.Ranked{ID: key, Rank: i}
		res := model.Result{
			ID:         m.ID,
			Type:       "memory",
			Content:    m.Content,
			Confidence: m.Confidence,
			EntityIDs:  m.EntityIDs,
			Source:     m.Source,
			Speaker:    m.Speaker,
		}
		if excerpt := c.firstChunkBySource(ctx, m.Source); excerpt != "" {
			res.Metadata = map[string]any{"source_excerpt": excerpt}
		}
		results[key] = res
	}
	return items, results
}

func (c Ops) recallKeyword(ctx context.Context, query string, limit int) ([]vecmath.Ranked, map[string]model.Result) {
	chunks, err := c.SearchKeyword(ctx, query, limit)
	if err != nil || len(chunks) == 0 {
		return nil, nil
	}

	items := make([]vecmath.Ranked, len(chunks))
	results := make(map[string]model.Result, len(chunks))
	for i, ch := range chunks {
		key := "chunk:" + ch.ID
		items[i] = vecmath.Ranked{ID: key, Rank: i}
		conf := 1.0
		if ch.EntityID != "" {
			if e, err := graph.New(c.Handle).GetEntity(ctx, ch.EntityID); err == nil {
				conf = e.Confidence
			}
		}
		results[key] = model.Result{
			ID:         ch.ID,
			Type:       "chunk",
			Content:    ch.Content,
			Confidence: conf,
			Metadata:   ch.Metadata,
		}
	}
	return items, results
}

func (c Ops) recallVector(ctx context.Context, query string, limit int) ([]vecmath.Ranked, map[string]model.Result) {
	if c.Cfg.Embedder == nil {
		return nil, nil
	}

	chunks, err := c.SearchVector(ctx, query, limit)
	if err != nil {
		c.Logf("cortex: recall vector_search embed/search failed: %v", err)
		return nil, nil
	}
	if len(chunks) == 0 {
		return nil, nil
	}

	items := make([]vecmath.Ranked, len(chunks))
	results := make(map[string]model.Result, len(chunks))
	for i, ch := range chunks {
		key := "chunk:" + ch.ID
		items[i] = vecmath.Ranked{ID: key, Rank: i}
		conf := 1.0
		if ch.EntityID != "" {
			if e, err := graph.New(c.Handle).GetEntity(ctx, ch.EntityID); err == nil {
				conf = e.Confidence
			}
		}
		results[key] = model.Result{
			ID:         ch.ID,
			Type:       "chunk",
			Content:    ch.Content,
			Confidence: conf,
			Metadata:   ch.Metadata,
		}
	}
	return items, results
}

func (c Ops) recallGraph(ctx context.Context, query string, limit int) ([]vecmath.Ranked, map[string]model.Result) {
	// Seed from the entities the sub-query names, then list each seed and
	// its neighbours, nearest first, summarised with their relationships.
	g := c.buildGraphContext(ctx, query, model.TemporalMode{})
	if len(g.seeds) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(g.dist))
	for id, d := range g.dist {
		if d <= 1 {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		if g.dist[ids[i]] != g.dist[ids[j]] {
			return g.dist[ids[i]] < g.dist[ids[j]]
		}
		return ids[i] < ids[j]
	})
	if len(ids) > limit {
		ids = ids[:limit]
	}

	items := make([]vecmath.Ranked, 0, len(ids))
	results := make(map[string]model.Result, len(ids))
	for _, id := range ids {
		e, err := graph.New(c.Handle).GetEntity(ctx, id)
		if err != nil {
			continue
		}
		content := fmt.Sprintf("%s (%s)", e.Name, e.Type)
		if rels, err := graph.New(c.Handle).GetRelationships(ctx, e.ID); err == nil && len(rels) > 0 {
			var parts []string
			for _, r := range rels {
				parts = append(parts, r.Type)
			}
			content += " [" + strings.Join(parts, ", ") + "]"
		}
		key := "entity:" + e.ID
		items = append(items, vecmath.Ranked{ID: key, Rank: len(items)})
		results[key] = model.Result{
			ID:         e.ID,
			Type:       "entity",
			Content:    content,
			Confidence: e.Confidence,
			EntityIDs:  []string{e.ID},
			Source:     e.Source,
		}
	}
	return items, results
}
