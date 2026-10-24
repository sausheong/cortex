-- Edges get validity windows like memories: valid_at is when the
-- relationship began (NULL = unknown, treat as always), invalid_at when it
-- ended. A closed edge stays live (not deleted) so history and as-of reads
-- keep it.
ALTER TABLE relationships ADD COLUMN IF NOT EXISTS valid_at TIMESTAMPTZ;
ALTER TABLE relationships ADD COLUMN IF NOT EXISTS invalid_at TIMESTAMPTZ;

-- Memories count how often recall surfaced them to an agent, and when last;
-- decay restarts its clock on use, and ranking gives a small lift to
-- memories that keep proving useful.
ALTER TABLE memories ADD COLUMN IF NOT EXISTS access_count INTEGER NOT NULL DEFAULT 0;
ALTER TABLE memories ADD COLUMN IF NOT EXISTS last_accessed_at TIMESTAMPTZ;
