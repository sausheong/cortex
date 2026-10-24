package cortex

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// PutRelationship upserts a relationship keyed on (source, target, type),
// with the type normalised by NormalizeType. A new edge gets a generated ULID;
// a repeat sighting of a live edge increments its EvidenceCount, refreshes
// LastSeenAt, keeps the higher confidence and merges attributes (new keys
// win), and the existing ID is set on the passed relationship.
func (c *Cortex) PutRelationship(ctx context.Context, r *Relationship) error {
	if err := requireEntity(ctx, c.db, c.userID, r.SourceID); err != nil {
		return err
	}
	if err := requireEntity(ctx, c.db, c.userID, r.TargetID); err != nil {
		return err
	}
	r.Type = NormalizeType(r.Type)
	r.Confidence = coerceConfidence(r.Confidence)
	now := time.Now().UTC()

	attrs := r.Attributes
	if attrs == nil {
		attrs = map[string]any{}
	}
	attrsJSON, err := json.Marshal(attrs)
	if err != nil {
		return fmt.Errorf("cortex: marshal relationship attributes: %w", err)
	}

	err = c.db.QueryRowContext(ctx,
		`INSERT INTO relationships (id, user_id, source_id, target_id, type, attributes, source, confidence, created_at, last_seen_at, written_by_connection_id)
		 VALUES (?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?, ?, ?)
		 ON CONFLICT (user_id, source_id, target_id, type) WHERE deleted_at IS NULL DO UPDATE SET
		   evidence_count = relationships.evidence_count + 1,
		   last_seen_at = EXCLUDED.last_seen_at,
		   confidence = GREATEST(relationships.confidence, EXCLUDED.confidence),
		   attributes = CASE WHEN jsonb_typeof(relationships.attributes) = 'object'
		                     THEN relationships.attributes || EXCLUDED.attributes
		                     ELSE EXCLUDED.attributes END
		 RETURNING id, created_at, evidence_count, confidence`,
		newID(), c.userID, r.SourceID, r.TargetID, r.Type, string(attrsJSON), r.Source, r.Confidence, now, now, c.nullConn(),
	).Scan(&r.ID, &r.CreatedAt, &r.EvidenceCount, &r.Confidence)
	if err != nil {
		return fmt.Errorf("cortex: upsert relationship: %w", err)
	}
	r.LastSeenAt = now
	return nil
}

// GetRelationships returns all relationships where entityID is either the
// source or the target. Optional RelFilter functions can narrow the results
// (e.g. by relationship type).
func (c *Cortex) GetRelationships(ctx context.Context, entityID string, filters ...RelFilter) ([]Relationship, error) {
	cfg := &relFilterConfig{}
	for _, f := range filters {
		f(cfg)
	}
	if err := requireEntity(ctx, c.db, c.userID, entityID); err != nil {
		return nil, err
	}

	query := `SELECT id, source_id, target_id, type, attributes, source, confidence, created_at, evidence_count, last_seen_at
		FROM relationships
		WHERE user_id = ? AND deleted_at IS NULL AND (source_id = ? OR target_id = ?)`
	args := []any{c.userID, entityID, entityID}

	if cfg.relType != "" {
		query += " AND type = ?"
		args = append(args, NormalizeType(cfg.relType))
	}

	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("cortex: get relationships: %w", err)
	}
	defer rows.Close()

	var rels []Relationship
	for rows.Next() {
		var r Relationship
		var attrsJSON sql.NullString
		if err := rows.Scan(&r.ID, &r.SourceID, &r.TargetID, &r.Type, &attrsJSON, &r.Source, &r.Confidence, &r.CreatedAt, &r.EvidenceCount, &r.LastSeenAt); err != nil {
			return nil, fmt.Errorf("cortex: scan relationship: %w", err)
		}
		if attrsJSON.Valid && attrsJSON.String != "" {
			if err := json.Unmarshal([]byte(attrsJSON.String), &r.Attributes); err != nil {
				return nil, fmt.Errorf("cortex: unmarshal relationship attributes: %w", err)
			}
		}
		rels = append(rels, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cortex: iterate relationships: %w", err)
	}
	return rels, nil
}
