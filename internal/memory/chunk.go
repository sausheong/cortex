package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
)

// PutChunk inserts a chunk and its content (its tsv column is generated).
// The operation is wrapped in a transaction. The chunk's ID and CreatedAt
// are set on the passed struct.
func (c Ops) PutChunk(ctx context.Context, ch *model.Chunk) error {
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
	_, err = tx.ExecContext(ctx,
		`INSERT INTO chunks (id, user_id, entity_id, content, metadata, created_at, written_by_connection_id)
		 VALUES (?, ?, ?, ?, ?::jsonb, ?, ?)`,
		ch.ID, c.UserID, entityID, ch.Content, string(metaJSON), ch.CreatedAt, c.NullConn(),
	)
	if err != nil {
		return fmt.Errorf("cortex: insert chunk: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cortex: commit chunk: %w", err)
	}
	return nil
}

// firstChunkBySource returns the content of one chunk whose metadata.source
// equals src, or "" if none. Used to re-inject supporting detail next to a
// ranked memory that shares the same source. Best-effort: errors return "".
func (c Ops) firstChunkBySource(ctx context.Context, src string) string {
	if src == "" {
		return ""
	}
	var content string
	err := c.DB.QueryRowContext(ctx,
		`SELECT content FROM chunks
		 WHERE user_id = ? AND deleted_at IS NULL AND metadata->>'source' = ?
		 ORDER BY created_at ASC LIMIT 1`, c.UserID, src,
	).Scan(&content)
	if err != nil {
		return ""
	}
	return content
}

// SearchKeyword performs a full-text search on chunk content using Postgres tsvector.
// Results are ordered by relevance (rank). Returns up to limit results.
func (c Ops) SearchKeyword(ctx context.Context, query string, limit int) ([]model.Chunk, error) {
	tsExpr, tsArgs := orTSQuery(query, 3)
	if tsExpr == "" {
		return nil, nil
	}
	rows, err := c.DB.QueryContext(ctx,
		`SELECT c.id, c.entity_id, c.content, c.metadata, c.created_at
		 FROM chunks c
		 WHERE c.user_id = ? AND c.deleted_at IS NULL AND c.tsv @@ `+tsExpr+`
		 ORDER BY ts_rank_cd(c.tsv, `+tsExpr+`) DESC
		 LIMIT ?`,
		append([]any{c.UserID, limit}, tsArgs...)...,
	)
	if err != nil {
		return nil, fmt.Errorf("cortex: keyword search: %w", err)
	}
	defer rows.Close()

	var chunks []model.Chunk
	for rows.Next() {
		var ch model.Chunk
		var entityID sql.NullString
		var metaJSON sql.NullString
		if err := rows.Scan(&ch.ID, &entityID, &ch.Content, &metaJSON, &ch.CreatedAt); err != nil {
			return nil, fmt.Errorf("cortex: scan chunk: %w", err)
		}
		if entityID.Valid {
			ch.EntityID = entityID.String
		}
		if metaJSON.Valid && metaJSON.String != "" {
			if err := json.Unmarshal([]byte(metaJSON.String), &ch.Metadata); err != nil {
				return nil, fmt.Errorf("cortex: unmarshal chunk metadata: %w", err)
			}
		}
		chunks = append(chunks, ch)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cortex: iterate chunks: %w", err)
	}
	return chunks, nil
}
