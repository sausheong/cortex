-- Multi-user data layer: users, per-user scoping, provenance, soft delete,
-- partial uniques, per-row embedding dimensions and generations.
-- Integer-prefixed names only (migrate.go runs Atoi on the prefix).

CREATE TABLE users (
	id                           TEXT PRIMARY KEY,
	google_sub                   TEXT UNIQUE,
	email                        TEXT NOT NULL DEFAULT '',
	email_verified               BOOLEAN NOT NULL DEFAULT FALSE,
	name                         TEXT NOT NULL DEFAULT '',
	created_at                   TIMESTAMPTZ NOT NULL DEFAULT now(),
	deleted_at                   TIMESTAMPTZ,
	active_embedding_generation  INT NOT NULL DEFAULT 1,
	owner_entity_id              TEXT,
	usage_json                   JSONB,
	last_maintenance_at          TIMESTAMPTZ,
	last_maintenance_error       TEXT
);

CREATE INDEX idx_users_email ON users(email);

INSERT INTO users (id, email, email_verified, name) VALUES ('u_bootstrap', '', FALSE, '');

-- user_id on every data table; existing rows belong to the bootstrap user.
ALTER TABLE entities        ADD COLUMN user_id TEXT NOT NULL DEFAULT 'u_bootstrap' REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE relationships   ADD COLUMN user_id TEXT NOT NULL DEFAULT 'u_bootstrap' REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE memories        ADD COLUMN user_id TEXT NOT NULL DEFAULT 'u_bootstrap' REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE memory_entities ADD COLUMN user_id TEXT NOT NULL DEFAULT 'u_bootstrap' REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE memory_edges    ADD COLUMN user_id TEXT NOT NULL DEFAULT 'u_bootstrap' REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE chunks          ADD COLUMN user_id TEXT NOT NULL DEFAULT 'u_bootstrap' REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE embeddings      ADD COLUMN user_id TEXT NOT NULL DEFAULT 'u_bootstrap' REFERENCES users(id) ON DELETE CASCADE;
ALTER TABLE sync_state      ADD COLUMN user_id TEXT NOT NULL DEFAULT 'u_bootstrap' REFERENCES users(id) ON DELETE CASCADE;

ALTER TABLE entities        ALTER COLUMN user_id DROP DEFAULT;
ALTER TABLE relationships   ALTER COLUMN user_id DROP DEFAULT;
ALTER TABLE memories        ALTER COLUMN user_id DROP DEFAULT;
ALTER TABLE memory_entities ALTER COLUMN user_id DROP DEFAULT;
ALTER TABLE memory_edges    ALTER COLUMN user_id DROP DEFAULT;
ALTER TABLE chunks          ALTER COLUMN user_id DROP DEFAULT;
ALTER TABLE embeddings      ALTER COLUMN user_id DROP DEFAULT;
ALTER TABLE sync_state      ALTER COLUMN user_id DROP DEFAULT;

CREATE INDEX idx_entities_user        ON entities(user_id);
CREATE INDEX idx_relationships_user   ON relationships(user_id);
CREATE INDEX idx_memories_user        ON memories(user_id);
CREATE INDEX idx_memory_entities_user ON memory_entities(user_id);
CREATE INDEX idx_memory_edges_user    ON memory_edges(user_id);
CREATE INDEX idx_chunks_user          ON chunks(user_id);
CREATE INDEX idx_embeddings_user      ON embeddings(user_id);
CREATE INDEX idx_sync_state_user      ON sync_state(user_id);

-- Provenance and soft delete.
ALTER TABLE entities      ADD COLUMN written_by_connection_id TEXT, ADD COLUMN deleted_at TIMESTAMPTZ, ADD COLUMN deleted_by TEXT, ADD COLUMN delete_batch_id TEXT;
ALTER TABLE relationships ADD COLUMN written_by_connection_id TEXT, ADD COLUMN deleted_at TIMESTAMPTZ, ADD COLUMN deleted_by TEXT, ADD COLUMN delete_batch_id TEXT;
ALTER TABLE memories      ADD COLUMN written_by_connection_id TEXT, ADD COLUMN deleted_at TIMESTAMPTZ, ADD COLUMN deleted_by TEXT, ADD COLUMN delete_batch_id TEXT;
ALTER TABLE chunks        ADD COLUMN written_by_connection_id TEXT, ADD COLUMN deleted_at TIMESTAMPTZ, ADD COLUMN deleted_by TEXT, ADD COLUMN delete_batch_id TEXT;

CREATE INDEX idx_entities_batch      ON entities(delete_batch_id)      WHERE delete_batch_id IS NOT NULL;
CREATE INDEX idx_relationships_batch ON relationships(delete_batch_id) WHERE delete_batch_id IS NOT NULL;
CREATE INDEX idx_memories_batch      ON memories(delete_batch_id)      WHERE delete_batch_id IS NOT NULL;
CREATE INDEX idx_chunks_batch        ON chunks(delete_batch_id)        WHERE delete_batch_id IS NOT NULL;

-- Uniqueness, rescoped by user and partial on live rows.
DROP INDEX idx_entities_name_type;
CREATE UNIQUE INDEX idx_entities_user_name_type ON entities(user_id, name, type) WHERE deleted_at IS NULL;

ALTER TABLE memory_edges DROP CONSTRAINT memory_edges_source_id_target_id_type_key;
CREATE UNIQUE INDEX idx_memory_edges_user_uniq ON memory_edges(user_id, source_id, target_id, type);

ALTER TABLE memory_entities DROP CONSTRAINT memory_entities_pkey;
ALTER TABLE memory_entities ADD PRIMARY KEY (user_id, memory_id, entity_id);

ALTER TABLE sync_state DROP CONSTRAINT sync_state_pkey;
ALTER TABLE sync_state ADD PRIMARY KEY (user_id, connector);

-- Soft-delete batches and their change records (restore / reversible merge).
CREATE TABLE delete_batches (
	id          TEXT PRIMARY KEY,
	user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	kind        TEXT NOT NULL,
	created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
	restored_at TIMESTAMPTZ
);
CREATE INDEX idx_delete_batches_user ON delete_batches(user_id);

CREATE TABLE delete_batch_changes (
	id         BIGSERIAL PRIMARY KEY,
	batch_id   TEXT NOT NULL REFERENCES delete_batches(id) ON DELETE CASCADE,
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	table_name TEXT NOT NULL,
	op         TEXT NOT NULL CHECK (op IN ('retarget', 'delete', 'update')),
	row_json   JSONB NOT NULL
);
CREATE INDEX idx_delete_batch_changes_batch ON delete_batch_changes(batch_id);
CREATE INDEX idx_delete_batch_changes_user  ON delete_batch_changes(user_id);

UPDATE users SET owner_entity_id = (
	SELECT id FROM entities WHERE source = 'owner' AND user_id = 'u_bootstrap'
	ORDER BY created_at, id LIMIT 1)
WHERE id = 'u_bootstrap';

-- Embeddings: guarded conversion. A legacy deployment that ran the old
-- ensureVectorSchema has a typed halfvec(N) column and three HNSW indexes.
DO $$
DECLARE
	legacy boolean;
	d int;
	rt text;
	dimlist int[] := ARRAY[]::int[];
	meta text;
BEGIN
	SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'embeddings' AND column_name = 'embedding'
	) INTO legacy;

	IF legacy THEN
		DROP INDEX IF EXISTS idx_embeddings_hnsw_entity;
		DROP INDEX IF EXISTS idx_embeddings_hnsw_memory;
		DROP INDEX IF EXISTS idx_embeddings_hnsw_chunk;
		ALTER TABLE embeddings ALTER COLUMN embedding TYPE halfvec;
		ALTER TABLE embeddings ADD COLUMN dims INT;
		UPDATE embeddings SET dims = vector_dims(embedding);
		ALTER TABLE embeddings ALTER COLUMN dims SET NOT NULL;
		ALTER TABLE embeddings ADD COLUMN generation INT NOT NULL DEFAULT 1;

		SELECT array_agg(DISTINCT x) INTO dimlist FROM (
			SELECT dims AS x FROM embeddings
		) s;
		SELECT value INTO meta FROM cortex_meta WHERE key = 'embedding_dims';
		IF meta IS NOT NULL AND meta ~ '^[0-9]+$' THEN
			dimlist := array_append(COALESCE(dimlist, ARRAY[]::int[]), meta::int);
		END IF;
		FOR d IN SELECT DISTINCT unnest(COALESCE(dimlist, ARRAY[]::int[])) LOOP
			FOREACH rt IN ARRAY ARRAY['entity', 'memory', 'chunk'] LOOP
				EXECUTE format(
					'CREATE INDEX %I ON embeddings USING hnsw ((embedding::halfvec(%s)) halfvec_cosine_ops) WHERE ref_type = %L AND dims = %s',
					'emb_hnsw_' || rt || '_' || d, d, rt, d);
			END LOOP;
		END LOOP;
		DELETE FROM cortex_meta WHERE key = 'embedding_dims';
	ELSE
		ALTER TABLE embeddings ADD COLUMN embedding halfvec NOT NULL;
		ALTER TABLE embeddings ADD COLUMN dims INT NOT NULL;
		ALTER TABLE embeddings ADD COLUMN generation INT NOT NULL DEFAULT 1;
	END IF;
END $$;

CREATE UNIQUE INDEX idx_embeddings_user_ref_gen ON embeddings(user_id, ref_type, ref_id, generation);
