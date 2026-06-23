package cortex

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// augmentForEmbedding prepends a memory's linked-entity context to its
// content for the purpose of embedding only (fact-augmented keys, à la
// Anthropic Contextual Retrieval). The stored content, FTS index, and
// query-side embedding are unaffected — this text is passed solely to the
// embedder at ingest, so a semantically-thin memory ("ships in April")
// becomes findable by the entities it concerns. Names are sorted for a
// deterministic key; blank names are dropped; with no names the content is
// returned unchanged.
func augmentForEmbedding(content string, entityNames []string) string {
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
func (c *Cortex) RecallWithStrength(ctx context.Context, query string, opts ...RecallOption) (RecallResult, error) {
	results, err := c.Recall(ctx, query, opts...)
	if err != nil {
		return RecallResult{}, err
	}
	out := RecallResult{Results: results}
	if len(results) > 0 {
		out.Strength = results[0].Score
	}
	out.Abstain = out.Strength < AbstainThreshold
	return out, nil
}

// Recall searches the knowledge graph using multiple retrieval strategies,
// merges the results via reciprocal rank fusion, and returns a unified
// ranked list of Result items.
func (c *Cortex) Recall(ctx context.Context, query string, opts ...RecallOption) ([]Result, error) {
	cfg := &recallConfig{limit: 20}
	for _, o := range opts {
		o(cfg)
	}

	// Resolve the temporal mode once; it governs the memory read paths.
	mode := temporalMode{includeInvalid: cfg.includeInvalid, asOf: cfg.asOf}

	// Decompose the query into sub-queries.
	subQueries := c.decomposeQuery(ctx, query)

	// Execute sub-queries in parallel, collecting ranked lists and results.
	var (
		mu        sync.Mutex
		lists     [][]rankedItem
		resultMap = make(map[string]Result) // keyed by prefixed ID
	)

	var wg sync.WaitGroup
	for _, sq := range subQueries {
		sq := sq // capture
		wg.Add(1)
		go func() {
			defer wg.Done()
			items, results := c.executeSubQuery(ctx, sq, cfg.limit, mode)
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

	if len(lists) == 0 {
		return []Result{}, nil
	}

	// Merge via reciprocal rank fusion.
	merged := rrfMerge(lists, 60)

	// Build final results from merged ranked items, weighting the fusion
	// score by each result's confidence so equally-ranked items are broken
	// by how certain we are about them. Confidence is in [0,1]; coerced at
	// ingest so unset == 1.0 (no penalty). A genuine 0.0 is the least-
	// trustworthy state and is preserved (not rescued), so it ranks last.
	final := make([]Result, 0, len(merged))
	for _, item := range merged {
		if r, ok := resultMap[item.id]; ok {
			conf := r.Confidence
			if conf < 0 {
				conf = 0 // defensive: confidence is coerced to [0,1] at ingest; a
				// negative here would only come from direct struct misuse.
				// A genuine 0.0 is the least-trustworthy state and must rank last.
			}
			r.Score = item.score * conf
			final = append(final, r)
		}
	}

	// Re-sort by the confidence-weighted score (rrfMerge sorted by raw score).
	sort.SliceStable(final, func(i, j int) bool {
		return final[i].Score > final[j].Score
	})

	// Apply min-confidence filter (post-RRF, pre-limit).
	if cfg.minConfidence > 0 {
		filtered := final[:0]
		for _, r := range final {
			if r.Confidence >= cfg.minConfidence {
				filtered = append(filtered, r)
			}
		}
		final = filtered
	}

	// Apply limit.
	if len(final) > cfg.limit {
		final = final[:cfg.limit]
	}

	return final, nil
}

// decomposeQuery uses the LLM to break a query into sub-queries.
// Falls back to keyword_search + memory_lookup if no LLM is configured
// or if decomposition returns no results.
func (c *Cortex) decomposeQuery(ctx context.Context, query string) []StructuredQuery {
	if c.cfg.llm != nil {
		sqs, err := c.cfg.llm.Decompose(ctx, query)
		if err == nil && len(sqs) > 0 {
			return sqs
		}
	}

	// Fallback: keyword search + memory lookup with the raw query.
	return []StructuredQuery{
		{Type: "keyword_search", Params: map[string]any{"query": query}},
		{Type: "memory_lookup", Params: map[string]any{"query": query}},
		{Type: "memory_vector", Params: map[string]any{"query": query}},
		{Type: "vector_search", Params: map[string]any{"query": query}},
	}
}

// executeSubQuery runs a single sub-query and returns ranked items plus
// a map of prefixed-ID to Result for later lookup.
func (c *Cortex) executeSubQuery(ctx context.Context, sq StructuredQuery, limit int, mode temporalMode) ([]rankedItem, map[string]Result) {
	query, _ := sq.Params["query"].(string)
	if query == "" {
		return nil, nil
	}

	switch sq.Type {
	case "memory_lookup":
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

func (c *Cortex) recallMemories(ctx context.Context, query string, limit int, mode temporalMode) ([]rankedItem, map[string]Result) {
	mems, err := c.searchMemoriesMode(ctx, query, limit, mode)
	if err != nil || len(mems) == 0 {
		return nil, nil
	}

	items := make([]rankedItem, len(mems))
	results := make(map[string]Result, len(mems))
	for i, m := range mems {
		key := "mem:" + m.ID
		items[i] = rankedItem{id: key, rank: i}
		res := Result{
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

func (c *Cortex) recallMemoryVector(ctx context.Context, query string, limit int, mode temporalMode) ([]rankedItem, map[string]Result) {
	if c.cfg.embedder == nil {
		return nil, nil
	}
	mems, err := c.searchMemoryVectorMode(ctx, query, limit, mode)
	if err != nil || len(mems) == 0 {
		return nil, nil
	}

	items := make([]rankedItem, len(mems))
	results := make(map[string]Result, len(mems))
	for i, m := range mems {
		key := "mem:" + m.ID
		items[i] = rankedItem{id: key, rank: i}
		res := Result{
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

func (c *Cortex) recallKeyword(ctx context.Context, query string, limit int) ([]rankedItem, map[string]Result) {
	chunks, err := c.SearchKeyword(ctx, query, limit)
	if err != nil || len(chunks) == 0 {
		return nil, nil
	}

	items := make([]rankedItem, len(chunks))
	results := make(map[string]Result, len(chunks))
	for i, ch := range chunks {
		key := "chunk:" + ch.ID
		items[i] = rankedItem{id: key, rank: i}
		conf := 1.0
		if ch.EntityID != "" {
			if e, err := c.GetEntity(ctx, ch.EntityID); err == nil {
				conf = e.Confidence
			}
		}
		results[key] = Result{
			Type:       "chunk",
			Content:    ch.Content,
			Confidence: conf,
			Metadata:   ch.Metadata,
		}
	}
	return items, results
}

func (c *Cortex) recallVector(ctx context.Context, query string, limit int) ([]rankedItem, map[string]Result) {
	if c.cfg.embedder == nil {
		return nil, nil
	}

	chunks, err := c.SearchVector(ctx, query, limit)
	if err != nil || len(chunks) == 0 {
		return nil, nil
	}

	items := make([]rankedItem, len(chunks))
	results := make(map[string]Result, len(chunks))
	for i, ch := range chunks {
		key := "chunk:" + ch.ID
		items[i] = rankedItem{id: key, rank: i}
		conf := 1.0
		if ch.EntityID != "" {
			if e, err := c.GetEntity(ctx, ch.EntityID); err == nil {
				conf = e.Confidence
			}
		}
		results[key] = Result{
			Type:       "chunk",
			Content:    ch.Content,
			Confidence: conf,
			Metadata:   ch.Metadata,
		}
	}
	return items, results
}

func (c *Cortex) recallGraph(ctx context.Context, query string, limit int) ([]rankedItem, map[string]Result) {
	// Find entities matching the query by name.
	entities, err := c.FindEntities(ctx, EntityFilter{NameLike: "%" + query + "%"})
	if err != nil || len(entities) == 0 {
		return nil, nil
	}

	// Traverse from the first matching entity.
	graph, err := c.Traverse(ctx, entities[0].ID, WithDepth(1))
	if err != nil || graph == nil {
		return nil, nil
	}

	items := make([]rankedItem, 0, len(graph.Entities))
	results := make(map[string]Result, len(graph.Entities))
	for i, e := range graph.Entities {
		key := "entity:" + e.ID
		items = append(items, rankedItem{id: key, rank: i})

		// Build a content summary for the entity.
		content := fmt.Sprintf("%s (%s)", e.Name, e.Type)
		// Include relationship info.
		var relParts []string
		for _, r := range graph.Relationships {
			if r.SourceID == e.ID || r.TargetID == e.ID {
				relParts = append(relParts, r.Type)
			}
		}
		if len(relParts) > 0 {
			content += " [" + strings.Join(relParts, ", ") + "]"
		}

		results[key] = Result{
			Type:       "entity",
			Content:    content,
			Confidence: e.Confidence,
			EntityIDs:  []string{e.ID},
			Source:     e.Source,
		}
	}
	return items, results
}
