package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
)

// PutChunk inserts a chunk, sealing its content and metadata.
// The operation is wrapped in a transaction. The chunk's ID and CreatedAt
// are set on the passed struct.
func (c Ops) PutChunk(ctx context.Context, ch *model.Chunk) error {
	if err := c.RequireKey(); err != nil {
		return err
	}
	ch.ID = store.NewID()
	ch.CreatedAt = time.Now().UTC()
	ch.WrittenBy = c.ConnID

	metaJSON, err := json.Marshal(ch.Metadata)
	if err != nil {
		return fmt.Errorf("cortex: marshal chunk metadata: %w", err)
	}

	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cortex: begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Insert into chunks table. Use NULL for entity_id when empty.
	var entityID any
	if ch.EntityID != "" {
		entityID = ch.EntityID
	}
	if ch.EntityID != "" {
		if err := store.RequireEntity(ctx, tx, c.UserID, ch.EntityID); err != nil {
			return err
		}
	}
	contentCT, err := c.SealStr(store.TblChunks, ch.ID, "content", ch.Content)
	if err != nil {
		return err
	}
	metaCT, err := c.Seal(store.TblChunks, ch.ID, "metadata", metaJSON)
	if err != nil {
		return err
	}
	var blind []byte
	if src := chunkSource(ch.Metadata); src != "" {
		blind = c.BlindNorm(src)
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO chunks (id, user_id, entity_id, content_ct, metadata_ct, source_blind, created_at, written_by_connection_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		ch.ID, c.UserID, entityID, contentCT, metaCT, blind, ch.CreatedAt, c.NullConn())
	if err != nil {
		return fmt.Errorf("cortex: insert chunk: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cortex: commit chunk: %w", err)
	}
	c.NotifyWrite("chunk")
	return nil
}

// firstChunkBySource returns the content of one chunk whose metadata.source
// equals src, or "" if none. Used to re-inject supporting detail next to a
// ranked memory that shares the same source. Best-effort: errors return "".
// Chunks match on source_blind.
func (c Ops) firstChunkBySource(ctx context.Context, src string) string {
	if src == "" || c.Key == nil {
		return ""
	}
	var id string
	var contentCT, metaCT []byte
	err := c.DB.QueryRowContext(ctx,
		`SELECT id, content_ct, metadata_ct FROM chunks
		 WHERE user_id = ? AND deleted_at IS NULL AND source_blind = ?
		 ORDER BY created_at ASC LIMIT 1`, c.UserID, c.BlindNorm(src),
	).Scan(&id, &contentCT, &metaCT)
	if err != nil {
		return ""
	}
	ch := model.Chunk{ID: id}
	if err := c.openChunk(&ch, contentCT, metaCT); err != nil {
		return ""
	}
	return ch.Content
}

// SearchKeyword performs a keyword (BM25) search over chunk content, best
// first, over the decrypted working set. Returns up to limit results.
func (c Ops) SearchKeyword(ctx context.Context, query string, limit int) ([]model.Chunk, error) {
	if strings.TrimSpace(query) == "" {
		return nil, c.RequireKey()
	}
	return c.searchKeywordEnc(ctx, query, limit)
}
