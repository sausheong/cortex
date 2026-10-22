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

-- Columns added by v0.4.0 ensureColumn migrations.
ALTER TABLE entities ADD COLUMN confidence REAL NOT NULL DEFAULT 1.0;
ALTER TABLE relationships ADD COLUMN confidence REAL NOT NULL DEFAULT 1.0;
ALTER TABLE memories ADD COLUMN confidence REAL NOT NULL DEFAULT 1.0;
ALTER TABLE memories ADD COLUMN valid_at DATETIME;
ALTER TABLE memories ADD COLUMN invalid_at DATETIME;
ALTER TABLE memories ADD COLUMN expired_at DATETIME;
ALTER TABLE memories ADD COLUMN speaker TEXT;
ALTER TABLE memories ADD COLUMN static INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memories ADD COLUMN last_decay_at DATETIME;
ALTER TABLE memories ADD COLUMN forget_after DATETIME;
