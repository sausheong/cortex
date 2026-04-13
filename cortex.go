package cortex

import (
	"context"
	"database/sql"
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

// Forget removes knowledge from the graph based on the provided filter.
// Supports filtering by EntityID or Source. Uses a transaction to ensure
// atomicity. Orphaned memories (with no remaining entity links) are cleaned up.
func (c *Cortex) Forget(ctx context.Context, f Filter) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cortex: begin transaction: %w", err)
	}
	defer tx.Rollback()

	if f.EntityID != "" {
		if err := c.forgetByEntity(ctx, tx, f.EntityID); err != nil {
			return err
		}
	} else if f.Source != "" {
		if err := c.forgetBySource(ctx, tx, f.Source); err != nil {
			return err
		}
	} else {
		return fmt.Errorf("cortex: forget requires EntityID or Source filter")
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cortex: commit forget: %w", err)
	}
	return nil
}

// forgetByEntity removes all data associated with a single entity.
func (c *Cortex) forgetByEntity(ctx context.Context, tx *sql.Tx, entityID string) error {
	// 1. Delete embeddings for chunks linked to entity.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM embeddings WHERE ref_type = 'chunk' AND ref_id IN
		 (SELECT id FROM chunks WHERE entity_id = ?)`, entityID,
	); err != nil {
		return fmt.Errorf("cortex: delete chunk embeddings: %w", err)
	}

	// 2. Delete FTS entries for chunks linked to entity.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM chunks_fts WHERE rowid IN
		 (SELECT rowid FROM chunks WHERE entity_id = ?)`, entityID,
	); err != nil {
		return fmt.Errorf("cortex: delete chunk fts: %w", err)
	}

	// 3. Delete chunks linked to entity.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM chunks WHERE entity_id = ?`, entityID,
	); err != nil {
		return fmt.Errorf("cortex: delete chunks: %w", err)
	}

	// 4. Delete memory_entities links for entity.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM memory_entities WHERE entity_id = ?`, entityID,
	); err != nil {
		return fmt.Errorf("cortex: delete memory entity links: %w", err)
	}

	// 5. Delete orphaned memories (no remaining entity links) and their embeddings.
	if err := c.deleteOrphanedMemories(ctx, tx); err != nil {
		return err
	}

	// 6. Delete relationships involving entity.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM relationships WHERE source_id = ? OR target_id = ?`,
		entityID, entityID,
	); err != nil {
		return fmt.Errorf("cortex: delete relationships: %w", err)
	}

	// 7. Delete the entity itself.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM entities WHERE id = ?`, entityID,
	); err != nil {
		return fmt.Errorf("cortex: delete entity: %w", err)
	}

	return nil
}

// forgetBySource removes all data associated with a given source.
func (c *Cortex) forgetBySource(ctx context.Context, tx *sql.Tx, source string) error {
	// Get all entity IDs for this source.
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM entities WHERE source = ?`, source,
	)
	if err != nil {
		return fmt.Errorf("cortex: query entities by source: %w", err)
	}
	var entityIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("cortex: scan entity id: %w", err)
		}
		entityIDs = append(entityIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("cortex: iterate entity ids: %w", err)
	}

	// For each entity: delete embeddings, chunks, memory links, relationships.
	for _, entityID := range entityIDs {
		// Delete chunk embeddings.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM embeddings WHERE ref_type = 'chunk' AND ref_id IN
			 (SELECT id FROM chunks WHERE entity_id = ?)`, entityID,
		); err != nil {
			return fmt.Errorf("cortex: delete chunk embeddings for source: %w", err)
		}

		// Delete chunk FTS entries.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM chunks_fts WHERE rowid IN
			 (SELECT rowid FROM chunks WHERE entity_id = ?)`, entityID,
		); err != nil {
			return fmt.Errorf("cortex: delete chunk fts for source: %w", err)
		}

		// Delete chunks.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM chunks WHERE entity_id = ?`, entityID,
		); err != nil {
			return fmt.Errorf("cortex: delete chunks for source: %w", err)
		}

		// Delete memory entity links.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM memory_entities WHERE entity_id = ?`, entityID,
		); err != nil {
			return fmt.Errorf("cortex: delete memory entity links for source: %w", err)
		}

		// Delete relationships.
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM relationships WHERE source_id = ? OR target_id = ?`,
			entityID, entityID,
		); err != nil {
			return fmt.Errorf("cortex: delete relationships for source: %w", err)
		}
	}

	// Delete entities by source.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM entities WHERE source = ?`, source,
	); err != nil {
		return fmt.Errorf("cortex: delete entities by source: %w", err)
	}

	// Delete orphaned memories and their embeddings.
	if err := c.deleteOrphanedMemories(ctx, tx); err != nil {
		return err
	}

	return nil
}

// deleteOrphanedMemories removes memories that have no remaining entity links,
// along with their embeddings.
func (c *Cortex) deleteOrphanedMemories(ctx context.Context, tx *sql.Tx) error {
	// Find orphaned memory IDs.
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM memories WHERE id NOT IN (SELECT DISTINCT memory_id FROM memory_entities)`,
	)
	if err != nil {
		return fmt.Errorf("cortex: find orphaned memories: %w", err)
	}
	var orphanIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return fmt.Errorf("cortex: scan orphaned memory id: %w", err)
		}
		orphanIDs = append(orphanIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("cortex: iterate orphaned memories: %w", err)
	}

	// Delete embeddings and memories for each orphan.
	for _, id := range orphanIDs {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM embeddings WHERE ref_type = 'memory' AND ref_id = ?`, id,
		); err != nil {
			return fmt.Errorf("cortex: delete orphaned memory embedding: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM memories WHERE id = ?`, id,
		); err != nil {
			return fmt.Errorf("cortex: delete orphaned memory: %w", err)
		}
	}

	return nil
}
