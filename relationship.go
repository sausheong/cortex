package cortex

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/ontology"
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
	r.Confidence = model.CoerceConfidence(r.Confidence)
	now := time.Now().UTC()

	attrs := r.Attributes
	if attrs == nil {
		attrs = map[string]any{}
	}
	attrsJSON, err := json.Marshal(attrs)
	if err != nil {
		return fmt.Errorf("cortex: marshal relationship attributes: %w", err)
	}

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cortex: begin relationship: %w", err)
	}
	defer tx.Rollback()

	requestedStart := r.ValidAt // the caller's start; r.ValidAt becomes the stored one
	var validAt, invalidAt sql.NullTime
	err = tx.QueryRowContext(ctx,
		`INSERT INTO relationships (id, user_id, source_id, target_id, type, attributes, source, confidence, created_at, last_seen_at, valid_at, invalid_at, written_by_connection_id)
		 VALUES (?, ?, ?, ?, ?, ?::jsonb, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (user_id, source_id, target_id, type) WHERE deleted_at IS NULL DO UPDATE SET
		   evidence_count = relationships.evidence_count + 1,
		   last_seen_at = EXCLUDED.last_seen_at,
		   confidence = GREATEST(relationships.confidence, EXCLUDED.confidence),
		   attributes = CASE WHEN jsonb_typeof(relationships.attributes) = 'object'
		                     THEN relationships.attributes || EXCLUDED.attributes
		                     ELSE EXCLUDED.attributes END,
		   valid_at = CASE WHEN relationships.invalid_at IS NOT NULL AND EXCLUDED.invalid_at IS NULL
		                   THEN COALESCE(EXCLUDED.valid_at, now())
		                   ELSE COALESCE(relationships.valid_at, EXCLUDED.valid_at) END,
		   invalid_at = EXCLUDED.invalid_at
		 RETURNING id, created_at, evidence_count, confidence, valid_at, invalid_at`,
		newID(), c.userID, r.SourceID, r.TargetID, r.Type, string(attrsJSON), r.Source, r.Confidence, now, now,
		r.ValidAt, r.InvalidAt, c.nullConn(),
	).Scan(&r.ID, &r.CreatedAt, &r.EvidenceCount, &r.Confidence, &validAt, &invalidAt)
	if err != nil {
		return fmt.Errorf("cortex: upsert relationship: %w", err)
	}
	r.ValidAt, r.InvalidAt = nullTimePtr(validAt), nullTimePtr(invalidAt)

	// A single-valued verb (lives_in, reports_to) holds one current target per
	// source: asserting a new one closes the others.
	if r.InvalidAt == nil && contains(ontology.SingleValuedVerbs(), r.Type) {
		// Close at the new edge's start, else at the database's now(): the
		// "holds now" check compares with the database clock, which may lag
		// this process's.
		var closeAt any
		if requestedStart != nil {
			closeAt = *requestedStart
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE relationships SET invalid_at = COALESCE(?::timestamptz, now())
			 WHERE user_id = ? AND deleted_at IS NULL AND invalid_at IS NULL
			   AND source_id = ? AND type = ? AND target_id <> ?`,
			closeAt, c.userID, r.SourceID, r.Type, r.TargetID); err != nil {
			return fmt.Errorf("cortex: close superseded relationships: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cortex: commit relationship: %w", err)
	}
	r.LastSeenAt = now
	return nil
}

// InvalidateRelationship records that a relationship ended at t. The edge
// stays in the graph: as-of reads before t and RelIncludeInvalid still see
// it. Re-asserting it with PutRelationship reopens it.
func (c *Cortex) InvalidateRelationship(ctx context.Context, id string, t time.Time) error {
	res, err := c.db.ExecContext(ctx,
		`UPDATE relationships SET invalid_at = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL`,
		t.UTC(), id, c.userID)
	if err != nil {
		return fmt.Errorf("cortex: invalidate relationship: %w", err)
	}
	if rowsAffected(res) == 0 {
		return model.NotFound("cortex: relationship not found")
	}
	return nil
}

// edgeValidityClause is the SQL predicate (no leading AND) and args for the
// relationships that hold under a filter: now by default, as of a time with
// RelAsOf, or all with RelIncludeInvalid.
func edgeValidityClause(alias string, includeInvalid bool, asOf *time.Time) (string, []any) {
	switch {
	case includeInvalid:
		return "TRUE", nil
	case asOf != nil:
		return fmt.Sprintf("(%s <= ? AND (%s IS NULL OR %s <= ?) AND (%s IS NULL OR %s > ?))",
			model.Qualify(alias, "created_at"),
			model.Qualify(alias, "valid_at"), model.Qualify(alias, "valid_at"),
			model.Qualify(alias, "invalid_at"), model.Qualify(alias, "invalid_at")), []any{*asOf, *asOf, *asOf}
	default:
		return fmt.Sprintf("(%s IS NULL OR %s > now())", model.Qualify(alias, "invalid_at"), model.Qualify(alias, "invalid_at")), nil
	}
}

// GetRelationships returns the relationships where entityID is either the
// source or the target that hold now. Optional RelFilter functions can
// narrow the results (e.g. by relationship type) or change the time
// (RelAsOf, RelIncludeInvalid).
func (c *Cortex) GetRelationships(ctx context.Context, entityID string, filters ...RelFilter) ([]Relationship, error) {
	cfg := &model.RelFilterConfig{}
	for _, f := range filters {
		f(cfg)
	}
	if err := requireEntity(ctx, c.db, c.userID, entityID); err != nil {
		return nil, err
	}

	validity, vargs := edgeValidityClause("", cfg.IncludeInvalid, cfg.AsOf)
	query := `SELECT id, source_id, target_id, type, attributes, source, confidence, created_at, evidence_count, last_seen_at, valid_at, invalid_at
		FROM relationships
		WHERE user_id = ? AND deleted_at IS NULL AND (source_id = ? OR target_id = ?) AND ` + validity
	args := append([]any{c.userID, entityID, entityID}, vargs...)

	if cfg.RelType != "" {
		query += " AND type = ?"
		args = append(args, NormalizeType(cfg.RelType))
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
		var validAt, invalidAt sql.NullTime
		if err := rows.Scan(&r.ID, &r.SourceID, &r.TargetID, &r.Type, &attrsJSON, &r.Source, &r.Confidence, &r.CreatedAt, &r.EvidenceCount, &r.LastSeenAt, &validAt, &invalidAt); err != nil {
			return nil, fmt.Errorf("cortex: scan relationship: %w", err)
		}
		if attrsJSON.Valid && attrsJSON.String != "" {
			if err := json.Unmarshal([]byte(attrsJSON.String), &r.Attributes); err != nil {
				return nil, fmt.Errorf("cortex: unmarshal relationship attributes: %w", err)
			}
		}
		r.ValidAt, r.InvalidAt = nullTimePtr(validAt), nullTimePtr(invalidAt)
		rels = append(rels, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cortex: iterate relationships: %w", err)
	}
	return rels, nil
}
