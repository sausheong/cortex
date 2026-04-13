package cortex

import (
	"context"
	"fmt"
)

// Remember extracts entities, relationships, memories, and chunks from the
// provided content and stores them in the knowledge graph.
func (c *Cortex) Remember(ctx context.Context, content string, opts ...RememberOption) error {
	cfg := &rememberConfig{}
	for _, o := range opts {
		o(cfg)
	}

	if c.cfg.extractor == nil {
		return fmt.Errorf("cortex: no extractor configured")
	}

	// 1. Extract structured data from content.
	extraction, err := c.cfg.extractor.Extract(ctx, content, cfg.contentType)
	if err != nil {
		return fmt.Errorf("cortex: extract: %w", err)
	}

	// 2. Store entities and build name→ID map for relationship resolution.
	nameToID := make(map[string]string)
	for i := range extraction.Entities {
		e := &extraction.Entities[i]
		if cfg.source != "" && e.Source == "" {
			e.Source = cfg.source
		}
		if err := c.PutEntity(ctx, e); err != nil {
			return fmt.Errorf("cortex: store entity %q: %w", e.Name, err)
		}
		nameToID[e.Name] = e.ID
	}

	// 3. Store relationships, resolving name-based IDs to real entity IDs.
	for i := range extraction.Relationships {
		r := &extraction.Relationships[i]
		sourceID, srcOK := nameToID[r.SourceID]
		targetID, tgtOK := nameToID[r.TargetID]
		if !srcOK || !tgtOK {
			continue // skip if either end doesn't resolve
		}
		r.SourceID = sourceID
		r.TargetID = targetID
		if cfg.source != "" && r.Source == "" {
			r.Source = cfg.source
		}
		if err := c.PutRelationship(ctx, r); err != nil {
			return fmt.Errorf("cortex: store relationship: %w", err)
		}
	}

	// 4. Store raw content as a chunk.
	ch := &Chunk{Content: content}
	if cfg.source != "" {
		ch.Metadata = map[string]any{"source": cfg.source}
	}
	if err := c.PutChunk(ctx, ch); err != nil {
		return fmt.Errorf("cortex: store chunk: %w", err)
	}
	// Embed the chunk if embedder is configured.
	if c.cfg.embedder != nil {
		vecs, err := c.cfg.embedder.Embed(ctx, []string{ch.Content})
		if err != nil {
			return fmt.Errorf("cortex: embed chunk: %w", err)
		}
		if len(vecs) > 0 {
			if err := c.putEmbedding(ctx, ch.ID, "chunk", vecs[0]); err != nil {
				return fmt.Errorf("cortex: store chunk embedding: %w", err)
			}
		}
	}

	// 5. Store memories, linking each to all extracted entities.
	allEntityIDs := make([]string, 0, len(nameToID))
	for _, id := range nameToID {
		allEntityIDs = append(allEntityIDs, id)
	}

	for i := range extraction.Memories {
		m := &extraction.Memories[i]
		m.EntityIDs = allEntityIDs
		if cfg.source != "" && m.Source == "" {
			m.Source = cfg.source
		}
		if err := c.PutMemory(ctx, m); err != nil {
			return fmt.Errorf("cortex: store memory: %w", err)
		}
		// Embed the memory if embedder is configured.
		if c.cfg.embedder != nil {
			vecs, err := c.cfg.embedder.Embed(ctx, []string{m.Content})
			if err != nil {
				return fmt.Errorf("cortex: embed memory: %w", err)
			}
			if len(vecs) > 0 {
				if err := c.putEmbedding(ctx, m.ID, "memory", vecs[0]); err != nil {
					return fmt.Errorf("cortex: store memory embedding: %w", err)
				}
			}
		}
	}

	return nil
}
