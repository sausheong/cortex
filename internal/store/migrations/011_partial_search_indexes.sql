-- Legacy (plaintext) search indexes become partial on enc IS NOT TRUE, so they
-- stop indexing ciphertext rows (whose searchable columns are NULL) and the
-- legacy queries, which carry the same predicate, can still use them. Encrypted
-- users search an in-memory working set and never touch these indexes.
-- Dropped for good by the gated cleanup migration after the post-check.

DROP INDEX IF EXISTS idx_chunks_tsv;
CREATE INDEX idx_chunks_tsv ON chunks USING GIN (tsv) WHERE enc IS NOT TRUE;
DROP INDEX IF EXISTS idx_memories_tsv;
CREATE INDEX idx_memories_tsv ON memories USING GIN (tsv) WHERE enc IS NOT TRUE;

-- Rebuild every per-dimension HNSW index in its partial form. Catalog access
-- is limited to this schema's own index entries (no pg_get_indexdef over other
-- schemas, which can race with their concurrent DDL).
DO $$
DECLARE
	r record;
	rt text;
	d int;
BEGIN
	FOR r IN
		WITH mine AS MATERIALIZED (
			SELECT c.relname AS indexname, i.indpred, i.indrelid
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			JOIN pg_index i ON i.indexrelid = c.oid
			WHERE n.nspname = current_schema() AND c.relkind = 'i'
			  AND c.relname ~ '^emb_hnsw_(entity|memory|chunk)_[0-9]+$'
		)
		SELECT indexname FROM mine
		WHERE indpred IS NULL OR pg_get_expr(indpred, indrelid) NOT LIKE '%enc IS NOT TRUE%'
	LOOP
		rt := substring(r.indexname from '^emb_hnsw_(entity|memory|chunk)_');
		d := substring(r.indexname from '_([0-9]+)$')::int;
		EXECUTE format('DROP INDEX %I', r.indexname);
		EXECUTE format(
			'CREATE INDEX %I ON embeddings USING hnsw ((embedding::halfvec(%s)) halfvec_cosine_ops) WHERE ref_type = %L AND dims = %s AND enc IS NOT TRUE',
			r.indexname, d, rt, d);
	END LOOP;
END $$;
