-- Gated cleanup: removes the plaintext path after the owner migration.
--
-- Refuses (RAISE EXCEPTION, counts only, never content) while any user is
-- still migrating, any encrypted user's row still holds plaintext, or any
-- unenrolled (state 'none') user holds data. Do not merge or deploy until the
-- production post-check has reported zero plaintext. Unenrolled users with no
-- data pass; they get model.ErrEnrollRequired and enroll like new accounts.

DO $$
DECLARE
	r record;
	n bigint;
	migrating bigint;
	leftover bigint := 0;
	holders bigint := 0;
BEGIN
	SELECT count(*) INTO migrating FROM users WHERE encryption_state = 'migrating';
	FOR r IN
		SELECT * FROM (VALUES
			('entities', 'enc IS NOT TRUE OR name IS NOT NULL OR type IS NOT NULL OR source IS NOT NULL OR attributes IS NOT NULL', 'NOT x.is_owner'),
			('entity_aliases', 'enc IS NOT TRUE OR value IS NOT NULL OR value_norm IS NOT NULL OR source IS NOT NULL', 'TRUE'),
			('relationships', 'enc IS NOT TRUE OR type IS NOT NULL OR source IS NOT NULL OR attributes IS NOT NULL', 'TRUE'),
			('memories', 'enc IS NOT TRUE OR content IS NOT NULL OR source IS NOT NULL OR speaker IS NOT NULL OR tsv IS NOT NULL', 'TRUE'),
			('memory_edges', 'enc IS NOT TRUE OR source IS NOT NULL', 'TRUE'),
			('chunks', 'enc IS NOT TRUE OR content IS NOT NULL OR metadata IS NOT NULL OR tsv IS NOT NULL', 'TRUE'),
			('embeddings', 'enc IS NOT TRUE OR embedding IS NOT NULL', 'TRUE'),
			('compactions', 'proposal_json IS NOT NULL', 'TRUE'),
			('delete_batch_changes', 'enc IS NOT TRUE OR row_json IS NOT NULL', 'TRUE'),
			('jobs', $p$COALESCE(payload, '') <> ''$p$, 'TRUE'),
			('user_settings', 'enc IS NOT TRUE', 'TRUE'),
			('sync_state', 'enc IS NOT TRUE OR state IS NOT NULL', 'TRUE')
		) AS v(t, p, extra)
	LOOP
		EXECUTE format(
			'SELECT count(*) FROM %I x WHERE (%s) AND x.user_id IN (SELECT id FROM users WHERE encryption_state <> ''none'')',
			r.t, r.p) INTO n;
		leftover := leftover + n;
		EXECUTE format(
			'SELECT count(*) FROM %I x WHERE (%s) AND %s AND x.user_id IN (SELECT id FROM users WHERE encryption_state = ''none'')',
			r.t, r.p, r.extra) INTO n;
		holders := holders + n;
	END LOOP;
	IF migrating > 0 OR leftover > 0 OR holders > 0 THEN
		RAISE EXCEPTION 'cortex: migration 012 refused: % user(s) still migrating, % row(s) still holding plaintext, % row(s) held by unenrolled users; finish the owner migration, enroll or delete those users (cortex users delete), then retry',
			migrating, leftover, holders;
	END IF;
END $$;

-- Unenrolled users with no data: drop the plaintext owner entity sign-up
-- wrote; enrollment creates it sealed.
UPDATE users SET owner_entity_id = NULL WHERE encryption_state = 'none';
DELETE FROM entities WHERE is_owner AND user_id IN (SELECT id FROM users WHERE encryption_state = 'none');

-- Legacy partial indexes, tsvector GIN indexes and every HNSW index.
DROP INDEX IF EXISTS idx_entities_user_name_type;
DROP INDEX IF EXISTS idx_entities_type;
DROP INDEX IF EXISTS idx_entities_name;
DROP INDEX IF EXISTS idx_entities_source;
DROP INDEX IF EXISTS idx_relationships_unique_live;
DROP INDEX IF EXISTS idx_relationships_type;
DROP INDEX IF EXISTS idx_chunks_tsv;
DROP INDEX IF EXISTS idx_memories_tsv;
DROP INDEX IF EXISTS idx_entity_aliases_legacy_uniq;
DROP INDEX IF EXISTS idx_entity_aliases_value;
DO $$
DECLARE r record;
BEGIN
	FOR r IN
		SELECT c.relname FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = current_schema() AND c.relkind = 'i'
		  AND (c.relname ~ '^emb_hnsw_(entity|memory|chunk)_[0-9]+$' OR c.relname ~ '^idx_embeddings_hnsw_')
	LOOP
		EXECUTE format('DROP INDEX %I', r.relname);
	END LOOP;
END $$;

-- The encrypted unique indexes lose their "enc" predicate (the flag goes away).
DROP INDEX IF EXISTS idx_entities_user_blind;
CREATE UNIQUE INDEX idx_entities_user_blind ON entities (user_id, name_blind, type_blind)
	WHERE deleted_at IS NULL;
DROP INDEX IF EXISTS idx_relationships_unique_live_enc;
CREATE UNIQUE INDEX idx_relationships_unique_live
	ON relationships (user_id, source_id, target_id, type_blind)
	WHERE deleted_at IS NULL;
DROP INDEX IF EXISTS idx_entity_aliases_enc_uniq;
CREATE UNIQUE INDEX idx_entity_aliases_uniq ON entity_aliases (user_id, kind, value_blind);
CREATE INDEX idx_embeddings_ref_type ON embeddings (user_id, ref_type, generation);

ALTER TABLE entities DROP CONSTRAINT IF EXISTS entities_plain_check;
ALTER TABLE relationships DROP CONSTRAINT IF EXISTS relationships_plain_check;
ALTER TABLE memories DROP CONSTRAINT IF EXISTS memories_plain_check;
ALTER TABLE chunks DROP CONSTRAINT IF EXISTS chunks_plain_check;
ALTER TABLE entity_aliases DROP CONSTRAINT IF EXISTS entity_aliases_plain_check;

-- Plaintext content columns and the enc flags.
ALTER TABLE entities
	DROP COLUMN enc, DROP COLUMN name, DROP COLUMN type, DROP COLUMN source, DROP COLUMN attributes;
ALTER TABLE entity_aliases
	DROP COLUMN enc, DROP COLUMN value, DROP COLUMN value_norm, DROP COLUMN source;
ALTER TABLE relationships
	DROP COLUMN enc, DROP COLUMN type, DROP COLUMN source, DROP COLUMN attributes;
-- The generated tsv column depends on content, so it goes first.
ALTER TABLE memories DROP COLUMN tsv;
ALTER TABLE memories
	DROP COLUMN enc, DROP COLUMN content, DROP COLUMN source, DROP COLUMN speaker;
ALTER TABLE memory_edges DROP COLUMN enc, DROP COLUMN source;
ALTER TABLE chunks DROP COLUMN tsv;
ALTER TABLE chunks
	DROP COLUMN enc, DROP COLUMN content, DROP COLUMN metadata;
ALTER TABLE embeddings DROP COLUMN enc, DROP COLUMN embedding;
ALTER TABLE sync_state DROP COLUMN enc, DROP COLUMN state;
ALTER TABLE compactions DROP COLUMN enc, DROP COLUMN proposal_json;
ALTER TABLE jobs DROP COLUMN enc, DROP COLUMN payload;
ALTER TABLE delete_batch_changes DROP COLUMN enc, DROP COLUMN row_json;
ALTER TABLE user_settings
	DROP COLUMN enc, DROP COLUMN provider, DROP COLUMN base_url, DROP COLUMN chat_model,
	DROP COLUMN embed_model, DROP COLUMN embed_base_url, DROP COLUMN api_key_sealed,
	DROP COLUMN embed_key_sealed, DROP COLUMN active_embed_sealed;

-- Ciphertext columns every row must carry.
ALTER TABLE entities
	ALTER COLUMN name_ct SET NOT NULL, ALTER COLUMN type_ct SET NOT NULL,
	ALTER COLUMN name_blind SET NOT NULL, ALTER COLUMN type_blind SET NOT NULL;
ALTER TABLE entity_aliases ALTER COLUMN value_ct SET NOT NULL, ALTER COLUMN value_blind SET NOT NULL;
ALTER TABLE relationships ALTER COLUMN type_ct SET NOT NULL, ALTER COLUMN type_blind SET NOT NULL;
ALTER TABLE memories ALTER COLUMN content_ct SET NOT NULL;
ALTER TABLE chunks ALTER COLUMN content_ct SET NOT NULL;
ALTER TABLE embeddings ALTER COLUMN embedding_ct SET NOT NULL;
ALTER TABLE sync_state ALTER COLUMN state_ct SET NOT NULL;
ALTER TABLE delete_batch_changes ALTER COLUMN row_ct SET NOT NULL;

-- No account is mid-migration any more.
ALTER TABLE users DROP CONSTRAINT IF EXISTS users_encryption_state_check;
ALTER TABLE users ADD CONSTRAINT users_encryption_state_check
	CHECK (encryption_state IN ('none', 'encrypted'));
