package cortex

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Recall searches the knowledge graph using multiple retrieval strategies,
// merges the results via reciprocal rank fusion, and returns a unified
// ranked list of Result items.
func (c *Cortex) Recall(ctx context.Context, query string, opts ...RecallOption) ([]Result, error) {
	cfg := &recallConfig{limit: 20}
	for _, o := range opts {
		o(cfg)
	}

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
			items, results := c.executeSubQuery(ctx, sq, cfg.limit)
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

	// Build final results from merged ranked items.
	final := make([]Result, 0, len(merged))
	for _, item := range merged {
		if r, ok := resultMap[item.id]; ok {
			r.Score = item.score
			final = append(final, r)
		}
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
	}
}

// executeSubQuery runs a single sub-query and returns ranked items plus
// a map of prefixed-ID to Result for later lookup.
func (c *Cortex) executeSubQuery(ctx context.Context, sq StructuredQuery, limit int) ([]rankedItem, map[string]Result) {
	query, _ := sq.Params["query"].(string)
	if query == "" {
		return nil, nil
	}

	switch sq.Type {
	case "memory_lookup":
		return c.recallMemories(ctx, query, limit)
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

func (c *Cortex) recallMemories(ctx context.Context, query string, limit int) ([]rankedItem, map[string]Result) {
	mems, err := c.SearchMemories(ctx, query, limit)
	if err != nil || len(mems) == 0 {
		return nil, nil
	}

	items := make([]rankedItem, len(mems))
	results := make(map[string]Result, len(mems))
	for i, m := range mems {
		key := "mem:" + m.ID
		items[i] = rankedItem{id: key, rank: i}
		results[key] = Result{
			Type:      "memory",
			Content:   m.Content,
			EntityIDs: m.EntityIDs,
			Source:    m.Source,
		}
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
		results[key] = Result{
			Type:     "chunk",
			Content:  ch.Content,
			Metadata: ch.Metadata,
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
		results[key] = Result{
			Type:     "chunk",
			Content:  ch.Content,
			Metadata: ch.Metadata,
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
			Type:      "entity",
			Content:   content,
			EntityIDs: []string{e.ID},
			Source:    e.Source,
		}
	}
	return items, results
}
