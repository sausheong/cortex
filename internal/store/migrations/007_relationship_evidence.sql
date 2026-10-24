-- Relationships become upserts keyed on (user, source, target, type): seeing
-- the same edge again strengthens it (evidence_count, last_seen_at) instead
-- of inserting a duplicate row.
ALTER TABLE relationships ADD COLUMN IF NOT EXISTS evidence_count INTEGER NOT NULL DEFAULT 1;
ALTER TABLE relationships ADD COLUMN IF NOT EXISTS last_seen_at TIMESTAMPTZ;
UPDATE relationships SET last_seen_at = created_at WHERE last_seen_at IS NULL;
ALTER TABLE relationships ALTER COLUMN last_seen_at SET DEFAULT now();
ALTER TABLE relationships ALTER COLUMN last_seen_at SET NOT NULL;

-- Normalise type labels the way NormalizeType does ("Works At" -> works_at).
UPDATE relationships
   SET type = btrim(regexp_replace(lower(btrim(type)), '[[:space:]_-]+', '_', 'g'), '_')
 WHERE type <> btrim(regexp_replace(lower(btrim(type)), '[[:space:]_-]+', '_', 'g'), '_');

-- Fold live duplicates into the oldest row, which keeps the count of rows it
-- absorbed, the latest sighting and the highest confidence.
UPDATE relationships r
   SET evidence_count = k.n, last_seen_at = k.last_seen, confidence = k.conf
  FROM (SELECT id,
               row_number() OVER w AS rn,
               count(*)        OVER (PARTITION BY user_id, source_id, target_id, type) AS n,
               max(created_at) OVER (PARTITION BY user_id, source_id, target_id, type) AS last_seen,
               max(confidence) OVER (PARTITION BY user_id, source_id, target_id, type) AS conf
          FROM relationships
         WHERE deleted_at IS NULL
        WINDOW w AS (PARTITION BY user_id, source_id, target_id, type ORDER BY created_at, id)) k
 WHERE r.id = k.id AND k.rn = 1 AND k.n > 1;

DELETE FROM relationships r
 USING (SELECT id,
               row_number() OVER (PARTITION BY user_id, source_id, target_id, type ORDER BY created_at, id) AS rn
          FROM relationships
         WHERE deleted_at IS NULL) k
 WHERE r.id = k.id AND k.rn > 1;

CREATE UNIQUE INDEX IF NOT EXISTS idx_relationships_unique_live
    ON relationships (user_id, source_id, target_id, type) WHERE deleted_at IS NULL;
