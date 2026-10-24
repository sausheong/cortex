-- Connections can be hidden from the Connections page without deleting the
-- row, so memories keep a stable "written by <name>" label.
ALTER TABLE connections ADD COLUMN removed_at TIMESTAMPTZ;
