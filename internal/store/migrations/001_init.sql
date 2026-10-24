CREATE EXTENSION IF NOT EXISTS vector;

CREATE TABLE cortex_meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);

CREATE TABLE entities (
	id         TEXT PRIMARY KEY,
	type       TEXT NOT NULL,
	name       TEXT NOT NULL,
	attributes JSONB,
	source     TEXT,
	confidence DOUBLE PRECISION NOT NULL DEFAULT 1.0,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_entities_type ON entities(type);
CREATE INDEX idx_entities_name ON entities(name);
CREATE INDEX idx_entities_source ON entities(source);
CREATE UNIQUE INDEX idx_entities_name_type ON entities(name, type);

CREATE TABLE relationships (
	id         TEXT PRIMARY KEY,
	source_id  TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
	target_id  TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
	type       TEXT NOT NULL,
	attributes JSONB,
	source     TEXT,
	confidence DOUBLE PRECISION NOT NULL DEFAULT 1.0,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_relationships_source_id ON relationships(source_id);
CREATE INDEX idx_relationships_target_id ON relationships(target_id);
CREATE INDEX idx_relationships_type ON relationships(type);

CREATE TABLE chunks (
	id         TEXT PRIMARY KEY,
	entity_id  TEXT REFERENCES entities(id) ON DELETE SET NULL,
	content    TEXT NOT NULL,
	metadata   JSONB,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	tsv        tsvector GENERATED ALWAYS AS (to_tsvector('english', left(content, 200000))) STORED
);

CREATE INDEX idx_chunks_entity_id ON chunks(entity_id);
CREATE INDEX idx_chunks_tsv ON chunks USING GIN (tsv);

CREATE TABLE memories (
	id            TEXT PRIMARY KEY,
	content       TEXT NOT NULL,
	source        TEXT,
	confidence    DOUBLE PRECISION NOT NULL DEFAULT 1.0,
	valid_at      TIMESTAMPTZ,
	invalid_at    TIMESTAMPTZ,
	expired_at    TIMESTAMPTZ,
	speaker       TEXT,
	static        BOOLEAN NOT NULL DEFAULT FALSE,
	last_decay_at TIMESTAMPTZ,
	forget_after  TIMESTAMPTZ,
	created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
	tsv           tsvector GENERATED ALWAYS AS (to_tsvector('english', left(content, 200000))) STORED
);

CREATE INDEX idx_memories_tsv ON memories USING GIN (tsv);

CREATE TABLE memory_entities (
	memory_id TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
	entity_id TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
	PRIMARY KEY (memory_id, entity_id)
);

CREATE TABLE memory_edges (
	id         TEXT PRIMARY KEY,
	source_id  TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
	target_id  TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
	type       TEXT NOT NULL,
	source     TEXT,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	UNIQUE (source_id, target_id, type)
);

CREATE INDEX idx_memory_edges_source ON memory_edges(source_id);
CREATE INDEX idx_memory_edges_target ON memory_edges(target_id);

-- The embedding column and its HNSW indexes are dimension-dependent and are
-- created by the vector DDL on first Open (see vector.go).
CREATE TABLE embeddings (
	id         TEXT PRIMARY KEY,
	ref_id     TEXT NOT NULL,
	ref_type   TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_embeddings_ref ON embeddings(ref_id, ref_type);

CREATE TABLE sync_state (
	connector  TEXT PRIMARY KEY,
	state      TEXT NOT NULL,
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
