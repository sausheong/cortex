package cortex

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// PutChunk inserts a chunk and its content into the FTS5 virtual table.
// The operation is wrapped in a transaction. The chunk's ID and CreatedAt
// are set on the passed struct.
func (c *Cortex) PutChunk(ctx context.Context, ch *Chunk) error {
	ch.ID = newID()
	ch.CreatedAt = time.Now().UTC()

	metaJSON, err := json.Marshal(ch.Metadata)
	if err != nil {
		return fmt.Errorf("cortex: marshal chunk metadata: %w", err)
	}

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cortex: begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Insert into chunks table. Use NULL for entity_id when empty.
	var entityID any
	if ch.EntityID != "" {
		entityID = ch.EntityID
	}
	result, err := tx.ExecContext(ctx,
		`INSERT INTO chunks (id, entity_id, content, metadata, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		ch.ID, entityID, ch.Content, string(metaJSON), ch.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("cortex: insert chunk: %w", err)
	}

	// Get the rowid assigned by SQLite for FTS5 mapping.
	rowID, err := result.LastInsertId()
	if err != nil {
		return fmt.Errorf("cortex: get last insert id: %w", err)
	}

	// Insert into FTS5 virtual table with matching rowid.
	_, err = tx.ExecContext(ctx,
		`INSERT INTO chunks_fts (rowid, content) VALUES (?, ?)`,
		rowID, ch.Content,
	)
	if err != nil {
		return fmt.Errorf("cortex: insert chunk fts: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cortex: commit chunk: %w", err)
	}
	return nil
}

// SearchKeyword performs a full-text search on chunk content using FTS5.
// Results are ordered by relevance (rank). Returns up to limit results.
func (c *Cortex) SearchKeyword(ctx context.Context, query string, limit int) ([]Chunk, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT c.id, c.entity_id, c.content, c.metadata, c.created_at
		 FROM chunks c
		 JOIN chunks_fts f ON c.rowid = f.rowid
		 WHERE chunks_fts MATCH ?
		 ORDER BY f.rank
		 LIMIT ?`,
		query, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("cortex: keyword search: %w", err)
	}
	defer rows.Close()

	var chunks []Chunk
	for rows.Next() {
		var ch Chunk
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
