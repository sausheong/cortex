-- Per-user encryption: schema for ciphertext columns, blind indexes, key
-- wraps and WebAuthn. Written once and complete; later tasks do not amend it.
-- Legacy rows keep enc = FALSE and their plaintext columns.

-- Users.
ALTER TABLE users ADD COLUMN IF NOT EXISTS encryption_state TEXT NOT NULL DEFAULT 'none';
ALTER TABLE users ADD CONSTRAINT users_encryption_state_check
	CHECK (encryption_state IN ('none', 'migrating', 'encrypted'));
ALTER TABLE users ADD COLUMN IF NOT EXISTS usage_ct BYTEA;

-- Key wraps and WebAuthn.
CREATE TABLE IF NOT EXISTS user_key_wraps (
	user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	kind         TEXT NOT NULL,
	ref_id       TEXT NOT NULL,
	wrapped_uk   BYTEA NOT NULL,
	pending      BOOLEAN NOT NULL DEFAULT FALSE,
	created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
	expires_at   TIMESTAMPTZ,
	last_used_at TIMESTAMPTZ,
	revoked_at   TIMESTAMPTZ,
	PRIMARY KEY (user_id, kind, ref_id)
);
CREATE INDEX IF NOT EXISTS idx_user_key_wraps_expires ON user_key_wraps (expires_at);

CREATE TABLE IF NOT EXISTS webauthn_credentials (
	id            TEXT PRIMARY KEY,
	user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	credential_id BYTEA NOT NULL UNIQUE,
	public_key    BYTEA NOT NULL,
	sign_count    BIGINT NOT NULL DEFAULT 0,
	transports    TEXT[],
	prf_salt      BYTEA,
	name          TEXT NOT NULL DEFAULT '',
	created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
	last_used_at  TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_webauthn_credentials_user ON webauthn_credentials (user_id);

CREATE TABLE IF NOT EXISTS webauthn_ceremonies (
	id_hash    TEXT PRIMARY KEY,
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	kind       TEXT NOT NULL,
	data       JSONB,
	expires_at TIMESTAMPTZ NOT NULL
);

-- Entities.
ALTER TABLE entities
	ADD COLUMN IF NOT EXISTS enc BOOLEAN NOT NULL DEFAULT FALSE,
	ADD COLUMN IF NOT EXISTS is_owner BOOLEAN NOT NULL DEFAULT FALSE,
	ADD COLUMN IF NOT EXISTS name_ct BYTEA,
	ADD COLUMN IF NOT EXISTS type_ct BYTEA,
	ADD COLUMN IF NOT EXISTS source_ct BYTEA,
	ADD COLUMN IF NOT EXISTS attributes_ct BYTEA,
	ADD COLUMN IF NOT EXISTS name_blind BYTEA,
	ADD COLUMN IF NOT EXISTS type_blind BYTEA;
UPDATE entities SET is_owner = TRUE WHERE source = 'owner';
ALTER TABLE entities ALTER COLUMN name DROP NOT NULL;
ALTER TABLE entities ALTER COLUMN type DROP NOT NULL;
ALTER TABLE entities ADD CONSTRAINT entities_plain_check
	CHECK (enc OR (name IS NOT NULL AND type IS NOT NULL));
CREATE INDEX IF NOT EXISTS idx_entities_owner ON entities (user_id) WHERE is_owner;

DROP INDEX IF EXISTS idx_entities_user_name_type;
CREATE UNIQUE INDEX idx_entities_user_name_type ON entities (user_id, name, type)
	WHERE deleted_at IS NULL AND enc IS NOT TRUE;
CREATE UNIQUE INDEX IF NOT EXISTS idx_entities_user_blind ON entities (user_id, name_blind, type_blind)
	WHERE deleted_at IS NULL AND enc;
DROP INDEX IF EXISTS idx_entities_type;
DROP INDEX IF EXISTS idx_entities_name;
DROP INDEX IF EXISTS idx_entities_source;
CREATE INDEX idx_entities_type ON entities (type) WHERE enc IS NOT TRUE;
CREATE INDEX idx_entities_name ON entities (name) WHERE enc IS NOT TRUE;
CREATE INDEX idx_entities_source ON entities (source) WHERE enc IS NOT TRUE;

-- Relationships.
ALTER TABLE relationships
	ADD COLUMN IF NOT EXISTS enc BOOLEAN NOT NULL DEFAULT FALSE,
	ADD COLUMN IF NOT EXISTS type_ct BYTEA,
	ADD COLUMN IF NOT EXISTS source_ct BYTEA,
	ADD COLUMN IF NOT EXISTS attributes_ct BYTEA,
	ADD COLUMN IF NOT EXISTS type_blind BYTEA;
ALTER TABLE relationships ALTER COLUMN type DROP NOT NULL;
ALTER TABLE relationships ADD CONSTRAINT relationships_plain_check
	CHECK (enc OR type IS NOT NULL);
DROP INDEX IF EXISTS idx_relationships_unique_live;
CREATE UNIQUE INDEX idx_relationships_unique_live
	ON relationships (user_id, source_id, target_id, type)
	WHERE deleted_at IS NULL AND enc IS NOT TRUE;
CREATE UNIQUE INDEX IF NOT EXISTS idx_relationships_unique_live_enc
	ON relationships (user_id, source_id, target_id, type_blind)
	WHERE deleted_at IS NULL AND enc;
DROP INDEX IF EXISTS idx_relationships_type;
CREATE INDEX idx_relationships_type ON relationships (type) WHERE enc IS NOT TRUE;

-- Memories.
ALTER TABLE memories
	ADD COLUMN IF NOT EXISTS enc BOOLEAN NOT NULL DEFAULT FALSE,
	ADD COLUMN IF NOT EXISTS content_ct BYTEA,
	ADD COLUMN IF NOT EXISTS source_ct BYTEA,
	ADD COLUMN IF NOT EXISTS speaker_ct BYTEA;
ALTER TABLE memories ALTER COLUMN content DROP NOT NULL;
ALTER TABLE memories ADD CONSTRAINT memories_plain_check
	CHECK (enc OR content IS NOT NULL);

-- Memory edges.
ALTER TABLE memory_edges
	ADD COLUMN IF NOT EXISTS enc BOOLEAN NOT NULL DEFAULT FALSE,
	ADD COLUMN IF NOT EXISTS source_ct BYTEA;

-- Chunks.
ALTER TABLE chunks
	ADD COLUMN IF NOT EXISTS enc BOOLEAN NOT NULL DEFAULT FALSE,
	ADD COLUMN IF NOT EXISTS content_ct BYTEA,
	ADD COLUMN IF NOT EXISTS metadata_ct BYTEA,
	ADD COLUMN IF NOT EXISTS source_blind BYTEA;
ALTER TABLE chunks ALTER COLUMN content DROP NOT NULL;
ALTER TABLE chunks ADD CONSTRAINT chunks_plain_check
	CHECK (enc OR content IS NOT NULL);

-- Embeddings.
ALTER TABLE embeddings
	ADD COLUMN IF NOT EXISTS enc BOOLEAN NOT NULL DEFAULT FALSE,
	ADD COLUMN IF NOT EXISTS embedding_ct BYTEA;
ALTER TABLE embeddings ALTER COLUMN embedding DROP NOT NULL;

-- Sync state.
ALTER TABLE sync_state
	ADD COLUMN IF NOT EXISTS enc BOOLEAN NOT NULL DEFAULT FALSE,
	ADD COLUMN IF NOT EXISTS state_ct BYTEA;
ALTER TABLE sync_state ALTER COLUMN state DROP NOT NULL;

-- Entity aliases. The PK cannot be partial and forces NOT NULL, so it is
-- replaced by a stable row id (the AAD row id) and two partial uniques that
-- keep today's per-user, cross-entity uniqueness.
ALTER TABLE entity_aliases
	ADD COLUMN IF NOT EXISTS enc BOOLEAN NOT NULL DEFAULT FALSE,
	ADD COLUMN IF NOT EXISTS id TEXT,
	ADD COLUMN IF NOT EXISTS value_ct BYTEA,
	ADD COLUMN IF NOT EXISTS value_blind BYTEA,
	ADD COLUMN IF NOT EXISTS source_ct BYTEA;
UPDATE entity_aliases SET id = gen_random_uuid()::text WHERE id IS NULL;
ALTER TABLE entity_aliases ALTER COLUMN id SET DEFAULT gen_random_uuid()::text;
ALTER TABLE entity_aliases ALTER COLUMN id SET NOT NULL;
ALTER TABLE entity_aliases ADD CONSTRAINT entity_aliases_id_key UNIQUE (id);
ALTER TABLE entity_aliases DROP CONSTRAINT IF EXISTS entity_aliases_pkey;
ALTER TABLE entity_aliases ALTER COLUMN value DROP NOT NULL;
ALTER TABLE entity_aliases ALTER COLUMN value_norm DROP NOT NULL;
ALTER TABLE entity_aliases ADD CONSTRAINT entity_aliases_plain_check
	CHECK (enc OR (value IS NOT NULL AND value_norm IS NOT NULL));
CREATE UNIQUE INDEX idx_entity_aliases_legacy_uniq ON entity_aliases (user_id, kind, value_norm)
	WHERE enc IS NOT TRUE;
CREATE UNIQUE INDEX idx_entity_aliases_enc_uniq ON entity_aliases (user_id, kind, value_blind)
	WHERE enc;
DROP INDEX IF EXISTS idx_entity_aliases_value;
CREATE INDEX idx_entity_aliases_value ON entity_aliases (user_id, value_norm) WHERE enc IS NOT TRUE;

-- Compactions, jobs, delete batches, settings.
ALTER TABLE compactions
	ADD COLUMN IF NOT EXISTS enc BOOLEAN NOT NULL DEFAULT FALSE,
	ADD COLUMN IF NOT EXISTS proposal_ct BYTEA;
ALTER TABLE jobs
	ADD COLUMN IF NOT EXISTS enc BOOLEAN NOT NULL DEFAULT FALSE,
	ADD COLUMN IF NOT EXISTS payload_ct BYTEA;
ALTER TABLE delete_batch_changes
	ADD COLUMN IF NOT EXISTS enc BOOLEAN NOT NULL DEFAULT FALSE,
	ADD COLUMN IF NOT EXISTS row_ct BYTEA;
ALTER TABLE delete_batch_changes ALTER COLUMN row_json DROP NOT NULL;
-- Undo of a merge repopulates rows from row_json with jsonb_populate_record,
-- which yields NULL for fields the stored row predates; enc is NOT NULL.
UPDATE delete_batch_changes SET row_json = row_json || '{"enc": false}'::jsonb
	WHERE row_json IS NOT NULL AND table_name IN ('relationships', 'embeddings')
	  AND row_json->'enc' IS NULL;
ALTER TABLE user_settings
	ADD COLUMN IF NOT EXISTS enc BOOLEAN NOT NULL DEFAULT FALSE,
	ADD COLUMN IF NOT EXISTS settings_ct BYTEA;

-- Tokens and connections.
ALTER TABLE agent_tokens ADD COLUMN IF NOT EXISTS expires_at TIMESTAMPTZ;
ALTER TABLE connections ADD COLUMN IF NOT EXISTS needs_reconnect BOOLEAN NOT NULL DEFAULT FALSE;

-- Users created before this instant are legacy (migration banner); users
-- created at or after it must enroll before stores are accepted.
INSERT INTO cortex_meta (key, value) VALUES ('encryption_required_since', now()::text)
	ON CONFLICT DO NOTHING;
