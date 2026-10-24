package graph

import (
	"context"
	"fmt"
	"time"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/ontology"
	"github.com/sausheong/cortex/internal/store"
)

// PutRelationship upserts a relationship keyed on (source, target, type),
// with the type normalised by NormalizeType. A new edge gets a generated ULID;
// a repeat sighting of a live edge increments its EvidenceCount, refreshes
// LastSeenAt, keeps the higher confidence and merges attributes (new keys
// win), and the existing ID is set on the passed relationship.
func (c Ops) PutRelationship(ctx context.Context, r *model.Relationship) error {
	if err := c.RequireKey(); err != nil {
		return err
	}
	if err := store.RequireEntity(ctx, c.DB, c.UserID, r.SourceID); err != nil {
		return err
	}
	if err := store.RequireEntity(ctx, c.DB, c.UserID, r.TargetID); err != nil {
		return err
	}
	r.Type = ontology.NormalizeType(r.Type)
	r.Confidence = model.CoerceConfidence(r.Confidence)
	now := time.Now().UTC()

	attrs := r.Attributes
	if attrs == nil {
		attrs = map[string]any{}
	}
	return c.putRelationshipEnc(ctx, r, attrs, now)
}

// InvalidateRelationship records that a relationship ended at t. The edge
// stays in the graph: as-of reads before t and RelIncludeInvalid still see
// it. Re-asserting it with PutRelationship reopens it.
func (c Ops) InvalidateRelationship(ctx context.Context, id string, t time.Time) error {
	res, err := c.DB.ExecContext(ctx,
		`UPDATE relationships SET invalid_at = ? WHERE id = ? AND user_id = ? AND deleted_at IS NULL`,
		t.UTC(), id, c.UserID)
	if err != nil {
		return fmt.Errorf("cortex: invalidate relationship: %w", err)
	}
	if rowsAffected(res) == 0 {
		return model.NotFound("cortex: relationship not found")
	}
	return nil
}

// EdgeValidityClause is the SQL predicate (no leading AND) and args for the
// relationships that hold under a filter: now by default, as of a time with
// RelAsOf, or all with RelIncludeInvalid.
func EdgeValidityClause(alias string, includeInvalid bool, asOf *time.Time) (string, []any) {
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
func (c Ops) GetRelationships(ctx context.Context, entityID string, filters ...model.RelFilter) ([]model.Relationship, error) {
	cfg := &model.RelFilterConfig{}
	for _, f := range filters {
		f(cfg)
	}
	if err := store.RequireEntity(ctx, c.DB, c.UserID, entityID); err != nil {
		return nil, err
	}

	validity, vargs := EdgeValidityClause("", cfg.IncludeInvalid, cfg.AsOf)
	query := `SELECT ` + relCols + `
		FROM relationships
		WHERE user_id = ? AND deleted_at IS NULL AND (source_id = ? OR target_id = ?) AND ` + validity
	args := append([]any{c.UserID, entityID, entityID}, vargs...)

	if cfg.RelType != "" {
		rt := ontology.NormalizeType(cfg.RelType)
		query += " AND type_blind = ?"
		args = append(args, c.BlindNorm(rt))
	}

	rows, err := c.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("cortex: get relationships: %w", err)
	}
	defer rows.Close()

	var rels []model.Relationship
	for rows.Next() {
		r, err := c.scanRelationship(rows)
		if err != nil {
			return nil, err
		}
		rels = append(rels, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cortex: iterate relationships: %w", err)
	}
	return rels, nil
}
