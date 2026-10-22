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

	// 4. Store raw content as one or more chunks. Long content is split so
	// each chunk fits within the embedder's context window; each split is
	// stored as its own Chunk row with its own embedding, but they share
	// the same source and (when split) carry part metadata so a recall can
	// reassemble or reference the originating thread.
	parts := splitContent(content, cfg.maxChunkSize)
	for i, part := range parts {
		ch := &Chunk{Content: part}
		md := map[string]any{}
		if cfg.source != "" {
			md["source"] = cfg.source
		}
		if len(parts) > 1 {
			md["part"] = i + 1
			md["parts"] = len(parts)
		}
		if len(md) > 0 {
			ch.Metadata = md
		}
		if err := c.PutChunk(ctx, ch); err != nil {
			return fmt.Errorf("cortex: store chunk: %w", err)
		}
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
	}

	// 5. Store memories, linking each to all extracted entities.
	allEntityIDs := make([]string, 0, len(nameToID))
	for _, id := range nameToID {
		allEntityIDs = append(allEntityIDs, id)
	}

	// Entity-context names for fact-augmented memory embeddings (3.1).
	entityNames := make([]string, 0, len(nameToID))
	for name := range nameToID {
		entityNames = append(entityNames, name)
	}

	for i := range extraction.Memories {
		m := &extraction.Memories[i]
		m.EntityIDs = allEntityIDs
		if cfg.source != "" && m.Source == "" {
			m.Source = cfg.source
		}
		if cfg.speaker != "" && m.Speaker == "" {
			m.Speaker = cfg.speaker
		}
		if err := c.PutMemory(ctx, m); err != nil {
			return fmt.Errorf("cortex: store memory: %w", err)
		}
		// Embed the memory if embedder is configured. Embed the
		// fact-augmented key (entity context + content), not the bare
		// content, so thin memories are findable by their entities. The
		// stored content and FTS index keep the original text.
		if c.cfg.embedder != nil {
			augmented := augmentForEmbedding(m.Content, entityNames)
			vecs, err := c.cfg.embedder.Embed(ctx, []string{augmented})
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

// Forget removes knowledge from the graph based on the provided filter
// (EntityID or Source). The removal is a recoverable soft delete: matching
// entities, their relationships and chunks are marked deleted in one batch
// (see ListDeleteBatches, RestoreBatch) and purged after 30 days. Memory
// links are kept, so a memory whose only entities were forgotten is still
// recalled.
func (c *Cortex) Forget(ctx context.Context, f Filter) error {
	if f.EntityID != "" {
		_, err := c.softDeleteEntity(ctx, f.EntityID, "user", "forget")
		return err
	}
	if f.Source == "" {
		return fmt.Errorf("cortex: forget requires EntityID or Source filter")
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cortex: begin transaction: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM entities WHERE user_id = ? AND source = ? AND deleted_at IS NULL`, c.userID, f.Source)
	if err != nil {
		return fmt.Errorf("cortex: query entities by source: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("cortex: scan entity id: %w", err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("cortex: iterate entity ids: %w", err)
	}
	if len(ids) == 0 {
		return nil
	}
	batchID, err := newBatch(ctx, tx, c.userID, "forget")
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := c.softDeleteEntityTx(ctx, tx, batchID, "user", id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cortex: commit forget: %w", err)
	}
	return nil
}
