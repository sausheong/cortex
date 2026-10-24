-- Stable identifiers for entities: other names, email addresses, phone
-- numbers and handles. Ingest resolves a mention to an entity through these
-- before falling back to name matching, so "sausheong@gmail.com", "Sau
-- Sheong" and "Chang Sau Sheong" can all land on one person.
CREATE TABLE IF NOT EXISTS entity_aliases (
	user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	entity_id  TEXT NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
	kind       TEXT NOT NULL,
	value      TEXT NOT NULL,
	value_norm TEXT NOT NULL,
	source     TEXT,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (user_id, kind, value_norm)
);
CREATE INDEX IF NOT EXISTS idx_entity_aliases_entity ON entity_aliases (user_id, entity_id);
CREATE INDEX IF NOT EXISTS idx_entity_aliases_value ON entity_aliases (user_id, value_norm);

-- A merge records the alias it adds for the dropped entity's name as an
-- 'insert' change, so undoing the merge removes it.
ALTER TABLE delete_batch_changes DROP CONSTRAINT IF EXISTS delete_batch_changes_op_check;
ALTER TABLE delete_batch_changes ADD CONSTRAINT delete_batch_changes_op_check
	CHECK (op IN ('retarget', 'delete', 'update', 'insert'));
