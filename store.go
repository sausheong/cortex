package cortex

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Cortex is the main knowledge graph store backed by SQLite.
type Cortex struct {
	db  *sql.DB
	cfg config
}

const schemaSQL = `
PRAGMA journal_mode=WAL;
PRAGMA foreign_keys=ON;

CREATE TABLE IF NOT EXISTS entities (
	id         TEXT PRIMARY KEY,
	type       TEXT NOT NULL,
	name       TEXT NOT NULL,
	attributes TEXT,
	source     TEXT,
	created_at DATETIME NOT NULL DEFAULT (datetime('now')),
	updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_entities_type ON entities(type);
CREATE INDEX IF NOT EXISTS idx_entities_name ON entities(name);
CREATE INDEX IF NOT EXISTS idx_entities_source ON entities(source);

CREATE TABLE IF NOT EXISTS relationships (
	id         TEXT PRIMARY KEY,
	source_id  TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
	target_id  TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
	type       TEXT NOT NULL,
	attributes TEXT,
	source     TEXT,
	created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_relationships_source_id ON relationships(source_id);
CREATE INDEX IF NOT EXISTS idx_relationships_target_id ON relationships(target_id);
CREATE INDEX IF NOT EXISTS idx_relationships_type ON relationships(type);

CREATE TABLE IF NOT EXISTS chunks (
	id         TEXT PRIMARY KEY,
	entity_id  TEXT REFERENCES entities(id) ON DELETE SET NULL,
	content    TEXT NOT NULL,
	metadata   TEXT,
	created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_chunks_entity_id ON chunks(entity_id);

CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(
	content,
	content_rowid='rowid'
);

CREATE TABLE IF NOT EXISTS memories (
	id         TEXT PRIMARY KEY,
	content    TEXT NOT NULL,
	source     TEXT,
	created_at DATETIME NOT NULL DEFAULT (datetime('now')),
	updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
	content,
	content_rowid='rowid'
);

CREATE TABLE IF NOT EXISTS memory_entities (
	memory_id TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
	entity_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
	PRIMARY KEY (memory_id, entity_id)
);

CREATE TABLE IF NOT EXISTS memory_edges (
	id         TEXT PRIMARY KEY,
	source_id  TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
	target_id  TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
	type       TEXT NOT NULL,
	source     TEXT,
	created_at DATETIME NOT NULL DEFAULT (datetime('now')),
	UNIQUE (source_id, target_id, type)
);

CREATE INDEX IF NOT EXISTS idx_memory_edges_source ON memory_edges(source_id);
CREATE INDEX IF NOT EXISTS idx_memory_edges_target ON memory_edges(target_id);

CREATE TABLE IF NOT EXISTS embeddings (
	id         TEXT PRIMARY KEY,
	ref_id     TEXT NOT NULL,
	ref_type   TEXT NOT NULL,
	vector     BLOB NOT NULL,
	created_at DATETIME NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_embeddings_ref ON embeddings(ref_id, ref_type);

CREATE TABLE IF NOT EXISTS sync_state (
	connector TEXT PRIMARY KEY,
	state     TEXT NOT NULL,
	updated_at DATETIME NOT NULL DEFAULT (datetime('now'))
);
`

// ensureColumn adds column to table if it does not already exist.
// Idempotent — safe to call on every Open. Used in lieu of a migrations
// framework for single-column additions. ddl is the column definition
// (everything after the column name in ALTER TABLE), e.g.
// "REAL NOT NULL DEFAULT 1.0".
func ensureColumn(db *sql.DB, table, column, ddl string) error {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return fmt.Errorf("cortex: pragma_table_info(%s): %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if name == column {
			return nil // already present
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, ddl)
	if _, err := db.Exec(stmt); err != nil {
		return fmt.Errorf("cortex: %s: %w", stmt, err)
	}
	return nil
}

// Open creates or opens a Cortex knowledge graph stored at the given path.
func Open(path string, opts ...Option) (*Cortex, error) {
	// Ensure parent directory exists.
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("cortex: create directory: %w", err)
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("cortex: open database: %w", err)
	}

	// Apply schema.
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("cortex: apply schema: %w", err)
	}

	for _, t := range []string{"entities", "relationships", "memories"} {
		if err := ensureColumn(db, t, "confidence", "REAL NOT NULL DEFAULT 1.0"); err != nil {
			db.Close()
			return nil, fmt.Errorf("cortex: migrate %s.confidence: %w", t, err)
		}
	}

	for _, col := range []string{"valid_at", "invalid_at", "expired_at"} {
		if err := ensureColumn(db, "memories", col, "DATETIME"); err != nil {
			db.Close()
			return nil, fmt.Errorf("cortex: migrate memories.%s: %w", col, err)
		}
	}

	if err := ensureColumn(db, "memories", "speaker", "TEXT"); err != nil {
		db.Close()
		return nil, fmt.Errorf("cortex: migrate memories.speaker: %w", err)
	}

	if err := ensureColumn(db, "memories", "static", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		db.Close()
		return nil, fmt.Errorf("cortex: migrate memories.static: %w", err)
	}

	if err := ensureColumn(db, "memories", "last_decay_at", "DATETIME"); err != nil {
		db.Close()
		return nil, fmt.Errorf("cortex: migrate memories.last_decay_at: %w", err)
	}

	if err := ensureColumn(db, "memories", "forget_after", "DATETIME"); err != nil {
		db.Close()
		return nil, fmt.Errorf("cortex: migrate memories.forget_after: %w", err)
	}

	// Backfill memories_fts for databases created before the FTS table existed.
	if _, err := db.Exec(
		`INSERT INTO memories_fts (rowid, content)
		 SELECT m.rowid, m.content FROM memories m
		 WHERE m.rowid NOT IN (SELECT rowid FROM memories_fts)`,
	); err != nil {
		db.Close()
		return nil, fmt.Errorf("cortex: backfill memories_fts: %w", err)
	}

	c := &Cortex{db: db}
	for _, o := range opts {
		o(&c.cfg)
	}
	return c, nil
}

// Close closes the underlying database connection.
func (c *Cortex) Close() error {
	if c.db != nil {
		return c.db.Close()
	}
	return nil
}

// SetExtractor sets the extractor on the Cortex instance.
func (c *Cortex) SetExtractor(e Extractor) { c.cfg.extractor = e }

// SetLLM sets the LLM on the Cortex instance.
func (c *Cortex) SetLLM(l LLM) { c.cfg.llm = l }

// SetEmbedder sets the embedder on the Cortex instance.
func (c *Cortex) SetEmbedder(e Embedder) { c.cfg.embedder = e }

// GetSyncState returns the last-known sync state for a connector.
func (c *Cortex) GetSyncState(ctx context.Context, connector string) (string, error) {
	var state string
	err := c.db.QueryRowContext(ctx,
		"SELECT state FROM sync_state WHERE connector = ?", connector,
	).Scan(&state)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("cortex: get sync state: %w", err)
	}
	return state, nil
}

// SetSyncState upserts the sync state for a connector.
func (c *Cortex) SetSyncState(ctx context.Context, connector, state string) error {
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO sync_state (connector, state, updated_at)
		 VALUES (?, ?, datetime('now'))
		 ON CONFLICT(connector) DO UPDATE SET state = excluded.state, updated_at = excluded.updated_at`,
		connector, state,
	)
	if err != nil {
		return fmt.Errorf("cortex: set sync state: %w", err)
	}
	return nil
}
