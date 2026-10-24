-- Looking up an entity's memories filtered memory_entities by (user_id,
-- entity_id), which the (user_id, memory_id, entity_id) primary key cannot
-- serve, so every lookup scanned all of the user's links. Refresh planner
-- statistics too: bulk-loaded tables otherwise keep stale estimates.
CREATE INDEX IF NOT EXISTS idx_memory_entities_user_entity ON memory_entities (user_id, entity_id);
ANALYZE memories;
ANALYZE memory_entities;
ANALYZE entities;
ANALYZE relationships;
