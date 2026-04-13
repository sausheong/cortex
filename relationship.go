package cortex

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// PutRelationship inserts a new relationship with a generated ULID.
// Attributes are stored as JSON TEXT.
func (c *Cortex) PutRelationship(ctx context.Context, r *Relationship) error {
	r.ID = newID()
	r.CreatedAt = time.Now().UTC()

	attrsJSON, err := json.Marshal(r.Attributes)
	if err != nil {
		return fmt.Errorf("cortex: marshal relationship attributes: %w", err)
	}

	_, err = c.db.ExecContext(ctx,
		`INSERT INTO relationships (id, source_id, target_id, type, attributes, source, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.SourceID, r.TargetID, r.Type, string(attrsJSON), r.Source, r.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("cortex: insert relationship: %w", err)
	}
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

	query := `SELECT id, source_id, target_id, type, attributes, source, created_at
		FROM relationships
		WHERE (source_id = ? OR target_id = ?)`
	args := []any{entityID, entityID}

	if cfg.relType != "" {
		query += " AND type = ?"
		args = append(args, cfg.relType)
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
		if err := rows.Scan(&r.ID, &r.SourceID, &r.TargetID, &r.Type, &attrsJSON, &r.Source, &r.CreatedAt); err != nil {
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
