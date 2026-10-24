package cortex

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ingestEntity stores an extracted entity, reusing the stored entity it
// refers to rather than creating a near-duplicate. Resolution runs the
// cascade in resolve: aliases, the owner, the same name ignoring case, fuzzy
// similarity, then an EntityDisambiguator when one is configured. On a match
// the extracted attributes fill in keys the stored entity lacks (stored
// values win, so a cached profile or merge provenance is never clobbered),
// confidence keeps the higher value, e.ID is set to the stored entity, and a
// differing extracted name is remembered as an alias. Identifiers found in
// the attributes (email, phone, handle) are recorded as aliases either way.
func (c *Cortex) ingestEntity(ctx context.Context, e *Entity, rc *resolveContext) error {
	e.Type = NormalizeType(e.Type)
	extracted := e.Name
	res, err := c.resolve(ctx, e.Name, e.Type, e.Attributes, rc, true)
	if err != nil {
		return err
	}
	if res.Entity == nil {
		if err := c.PutEntity(ctx, e); err != nil {
			return err
		}
	} else {
		if err := c.absorbIntoEntity(ctx, res.Entity, e); err != nil {
			return err
		}
		if res.Method != ResolvedByExact && normalizeAlias(AliasName, extracted) != normalizeAlias(AliasName, e.Name) &&
			!selfMentions[normalizeAlias(AliasName, extracted)] && DetectAliasKind(extracted) == AliasName {
			c.addAliasQuiet(ctx, e.ID, AliasName, extracted, "ingest")
		}
	}
	for _, id := range identifiers(extracted, e.Attributes) {
		c.addAliasQuiet(ctx, e.ID, id[0], id[1], "ingest")
	}
	rc.resolved = append(rc.resolved, e.ID)
	return nil
}

// findSameEntity returns the live entity named name (ignoring case) whose
// type normalises to typ, preferring an exact name and type match, or nil.
func (c *Cortex) findSameEntity(ctx context.Context, name, typ string) (*Entity, error) {
	var id string
	err := c.db.QueryRowContext(ctx,
		`SELECT id FROM entities
		 WHERE user_id = ? AND deleted_at IS NULL AND lower(name) = lower(?) AND `+normTypeSQL("type")+` = ?
		 ORDER BY (name = ?) DESC, (type = ?) DESC, created_at, id
		 LIMIT 1`,
		c.userID, name, typ, name, typ,
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cortex: find entity: %w", err)
	}
	return c.GetEntity(ctx, id)
}

// absorbIntoEntity folds an extracted entity into the stored one it matched:
// missing attribute keys are added, confidence keeps the higher value, and
// a non-owner source is refreshed. e.ID and e.Name take the stored values.
func (c *Cortex) absorbIntoEntity(ctx context.Context, stored, e *Entity) error {
	attrs := stored.Attributes
	if attrs == nil {
		attrs = map[string]any{}
	}
	for k, v := range e.Attributes {
		if _, ok := attrs[k]; !ok {
			attrs[k] = v
		}
	}
	attrsJSON, err := json.Marshal(attrs)
	if err != nil {
		return fmt.Errorf("cortex: marshal attributes: %w", err)
	}
	conf := coerceConfidence(e.Confidence)
	if stored.Confidence > conf {
		conf = stored.Confidence
	}
	now := time.Now().UTC()
	if _, err := c.db.ExecContext(ctx,
		`UPDATE entities SET attributes = ?::jsonb, confidence = ?, updated_at = ?,
		   source = CASE WHEN source = 'owner' OR ? = '' THEN source ELSE ? END
		 WHERE id = ? AND user_id = ?`,
		string(attrsJSON), conf, now, e.Source, e.Source, stored.ID, c.userID,
	); err != nil {
		return fmt.Errorf("cortex: update entity: %w", err)
	}
	e.ID = stored.ID
	e.Name = stored.Name
	e.Type = stored.Type
	e.Attributes = attrs
	e.Confidence = conf
	e.CreatedAt = stored.CreatedAt
	e.UpdatedAt = now
	return nil
}
