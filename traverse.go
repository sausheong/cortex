package cortex

import (
	"context"
	"fmt"

	"github.com/sausheong/cortex/internal/model"
)

// contains reports whether ss contains s.
func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// Traverse performs a breadth-first walk from startID through the knowledge
// graph. It respects the configured depth (default 1) and optional edge type
// filters. Returns a Graph containing all visited entities and traversed
// relationships. Cycles are avoided via a visited set.
func (c *Cortex) Traverse(ctx context.Context, startID string, opts ...TraverseOption) (*Graph, error) {
	cfg := &model.TraverseConfig{Depth: 1}
	for _, o := range opts {
		o(cfg)
	}
	edgeTypes := make([]string, len(cfg.EdgeTypes))
	for i, t := range cfg.EdgeTypes {
		edgeTypes[i] = NormalizeType(t)
	}

	// Get the start entity.
	start, err := c.GetEntity(ctx, startID)
	if err != nil {
		return nil, fmt.Errorf("cortex: traverse start entity: %w", err)
	}

	graph := &Graph{
		Entities:      []Entity{*start},
		Relationships: []Relationship{},
	}

	visited := map[string]bool{startID: true}

	// BFS frontier: IDs to explore at the current depth level.
	frontier := []string{startID}

	for level := 0; level < cfg.Depth; level++ {
		var nextFrontier []string

		for _, entityID := range frontier {
			rels, err := c.GetRelationships(ctx, entityID, cfg.RelFilters...)
			if err != nil {
				return nil, fmt.Errorf("cortex: traverse relationships: %w", err)
			}

			for _, rel := range rels {
				// If edge type filter is set, skip non-matching edges.
				if len(edgeTypes) > 0 && !contains(edgeTypes, rel.Type) {
					continue
				}

				// Determine the neighbor (the other end of the edge).
				neighborID := rel.TargetID
				if neighborID == entityID {
					neighborID = rel.SourceID
				}

				// Add the relationship to the graph.
				graph.Relationships = append(graph.Relationships, rel)

				// If we haven't visited this neighbor, add it.
				if !visited[neighborID] {
					visited[neighborID] = true

					neighbor, err := c.GetEntity(ctx, neighborID)
					if err != nil {
						return nil, fmt.Errorf("cortex: traverse get neighbor: %w", err)
					}
					graph.Entities = append(graph.Entities, *neighbor)
					nextFrontier = append(nextFrontier, neighborID)
				}
			}
		}

		frontier = nextFrontier
	}

	return graph, nil
}
