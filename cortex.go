package cortex

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sausheong/cortex/internal/llmutil"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/ontology"
)

// Remember extracts entities, relationships, memories, and chunks from the
// provided content and stores them in the knowledge graph.
func (c *Cortex) Remember(ctx context.Context, content string, opts ...RememberOption) error {
	cfg := &model.RememberConfig{MaxChunkSize: DefaultMaxChunkChars}
	for _, o := range opts {
		o(cfg)
	}

	if c.h.Cfg.Extractor == nil {
		return fmt.Errorf("cortex: no extractor configured")
	}

	// 1. Extract structured data from content. Long content is extracted
	// per chunk and merged; a failing chunk does not discard the others, the
	// caller gets an *ExtractPartialError after everything else is stored.
	parts := llmutil.SplitContent(content, cfg.MaxChunkSize)
	extraction, err := c.extractParts(ctx, content, parts, cfg.ContentType)
	var extractPartial error
	if err != nil {
		if extraction == nil || !IsExtractPartial(err) {
			return fmt.Errorf("cortex: extract: %w", err)
		}
		extractPartial = err
	}

	// 2. Store entities, reusing stored ones that are the same thing, and
	// build a name→ID map (keyed by the extracted name, case-insensitively)
	// for resolving relationships and memory links.
	nameToID := make(map[string]string)
	var entityIDs []string // distinct, in extraction order
	rc := &resolveContext{content: llmutil.TruncateRunes(content, 2000), ownerSpeaks: ownerVoice(cfg.Speaker, cfg.Source)}
	for i := range extraction.Entities {
		e := &extraction.Entities[i]
		if cfg.Source != "" && e.Source == "" {
			e.Source = cfg.Source
		}
		extracted := e.Name
		if err := c.ingestEntity(ctx, e, rc); err != nil {
			return fmt.Errorf("cortex: store entity %q: %w", extracted, err)
		}
		if !contains(entityIDs, e.ID) {
			entityIDs = append(entityIDs, e.ID)
		}
		nameToID[nameKey(extracted)] = e.ID
		nameToID[nameKey(e.Name)] = e.ID
	}

	// 3. Store relationships, resolving name-based IDs to real entity IDs
	// and mapping the verb onto the curated set (CanonicalRelation): a
	// synonym may swap the ends, and a verb that is unknown or does not fit
	// its ends' types is stored as related_to with the original as "label".
	idToType := make(map[string]string, len(extraction.Entities))
	for _, e := range extraction.Entities {
		idToType[e.ID], _ = CanonicalEntityType(e.Type)
	}
	for i := range extraction.Relationships {
		r := &extraction.Relationships[i]
		sourceID, srcOK := nameToID[nameKey(r.SourceID)]
		targetID, tgtOK := nameToID[nameKey(r.TargetID)]
		if !srcOK || !tgtOK || sourceID == targetID {
			continue // skip if either end doesn't resolve, or it is a self-loop
		}
		verb, swap, label := CanonicalRelation(r.Type, idToType[sourceID], idToType[targetID])
		if swap {
			sourceID, targetID = targetID, sourceID
		}
		r.SourceID, r.TargetID, r.Type = sourceID, targetID, verb
		if label != "" {
			if r.Attributes == nil {
				r.Attributes = map[string]any{}
			}
			if _, ok := r.Attributes["label"]; !ok {
				r.Attributes["label"] = label
			}
		}
		if cfg.Source != "" && r.Source == "" {
			r.Source = cfg.Source
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
	// Embedding failures do not abort the save: everything is stored, the
	// missing vectors are backfilled by nightly maintenance, and the caller
	// gets an *EmbedPartialError saying how many items are waiting.
	var unembedded model.EmbedFailures
	var chunkTargets []embedTarget
	for i, part := range parts {
		ch := &Chunk{Content: part}
		md := map[string]any{}
		if cfg.Source != "" {
			md["source"] = cfg.Source
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
		chunkTargets = append(chunkTargets, embedTarget{ch.ID, "chunk", ch.Content})
	}
	if c.h.Cfg.Embedder != nil {
		unembedded.Merge(c.embedAndStoreBatch(ctx, chunkTargets))
	}

	// 5. Store memories, linking each to the entities it is about.
	idToName := make(map[string]string, len(extraction.Entities))
	for _, e := range extraction.Entities {
		idToName[e.ID] = e.Name
	}

	var memTargets []embedTarget
	for i := range extraction.Memories {
		m := &extraction.Memories[i]
		m.EntityIDs = memoryEntityIDs(m.About, nameToID, entityIDs)
		// Entity-context names for fact-augmented memory embeddings (3.1).
		entityNames := make([]string, 0, len(m.EntityIDs))
		for _, id := range m.EntityIDs {
			entityNames = append(entityNames, idToName[id])
		}
		if cfg.Source != "" && m.Source == "" {
			m.Source = cfg.Source
		}
		if cfg.Speaker != "" && m.Speaker == "" {
			m.Speaker = cfg.Speaker
		}
		if err := c.PutMemory(ctx, m); err != nil {
			return fmt.Errorf("cortex: store memory: %w", err)
		}
		// Embed the fact-augmented key (entity context + content), not the
		// bare content, so thin memories are findable by their entities.
		// The stored content and FTS index keep the original text.
		memTargets = append(memTargets, embedTarget{m.ID, "memory", augmentForEmbedding(m.Content, entityNames)})
	}
	if c.h.Cfg.Embedder != nil {
		unembedded.Merge(c.embedAndStoreBatch(ctx, memTargets))
	}

	return errors.Join(extractPartial, unembedded.Err())
}

// ownerVoice reports whether text remembered with this speaker and source is
// in the owner's own voice, so "I" and "me" mean the owner.
func ownerVoice(speaker, source string) bool {
	switch strings.ToLower(strings.TrimSpace(speaker)) {
	case "user", "owner", "me", "self":
		return true
	}
	return source == "owner"
}

// nameKey is the key an extracted entity name is looked up by: lower-cased
// with whitespace collapsed, matching how mergeExtractions dedupes names.
func nameKey(name string) string { return ontology.NormKey(name) }

// memoryEntityIDs resolves a memory's "about" names to entity IDs. Names that
// match no extracted entity are ignored. A memory with no usable "about" —
// from an extractor that does not set it, or whose names all fail to resolve —
// falls back to every entity from the same extraction, which was the linking
// rule before per-memory links existed.
func memoryEntityIDs(about []string, nameToID map[string]string, all []string) []string {
	var ids []string
	for _, name := range about {
		if id, ok := nameToID[nameKey(name)]; ok && !contains(ids, id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return all
	}
	return ids
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
	tx, err := c.h.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cortex: begin transaction: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx,
		`SELECT id FROM entities WHERE user_id = ? AND source = ? AND deleted_at IS NULL`, c.h.UserID, f.Source)
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
	batchID, err := newBatch(ctx, tx, c.h.UserID, "forget")
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
