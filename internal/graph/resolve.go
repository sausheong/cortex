package graph

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/ontology"
)

// IngestEntity stores an extracted entity, reusing the stored entity it
// refers to rather than creating a near-duplicate. The type is mapped onto
// the curated set first (CanonicalEntityType); an unknown label becomes
// concept with the label kept in the "type_label" attribute. Resolution runs the
// cascade in resolve: aliases, the owner, the same name ignoring case, fuzzy
// similarity, then an EntityDisambiguator when one is configured. On a match
// the extracted attributes fill in keys the stored entity lacks (stored
// values win, so a cached profile or merge provenance is never clobbered),
// confidence keeps the higher value, e.ID is set to the stored entity, and a
// differing extracted name is remembered as an alias. Identifiers found in
// the attributes (email, phone, handle) are recorded as aliases either way.
func (c Ops) IngestEntity(ctx context.Context, e *model.Entity, rc *ResolveContext) error {
	canonical, known := ontology.CanonicalEntityType(e.Type)
	if label := ontology.NormalizeType(e.Type); !known && label != "" {
		if e.Attributes == nil {
			e.Attributes = map[string]any{}
		}
		if _, ok := e.Attributes["type_label"]; !ok {
			e.Attributes["type_label"] = label
		}
	}
	e.Type = canonical
	extracted := e.Name
	res, err := c.Resolve(ctx, e.Name, e.Type, e.Attributes, rc, true)
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
		if res.Method != model.ResolvedByExact && normalizeAlias(model.AliasName, extracted) != normalizeAlias(model.AliasName, e.Name) &&
			!userMentions[normalizeAlias(model.AliasName, extracted)] && !firstPerson[normalizeAlias(model.AliasName, extracted)] &&
			DetectAliasKind(extracted) == model.AliasName {
			c.AddAliasQuiet(ctx, e.ID, model.AliasName, extracted, "ingest")
		}
	}
	for _, id := range identifiers(extracted, e.Attributes) {
		c.AddAliasQuiet(ctx, e.ID, id[0], id[1], "ingest")
	}
	rc.resolved = append(rc.resolved, e.ID)
	return nil
}

// findSameEntity returns the live entity named name (ignoring case) whose
// type is of the same kind as typ (see kindMatches), preferring an exact
// name, or nil. Encrypted rows match on name_blind; legacy rows (states none
// and migrating) on lower(name), and a legacy hit is rewritten as encrypted
// while migrating.
func (c Ops) findSameEntity(ctx context.Context, name, typ string) (*model.Entity, error) {
	rows, err := c.DB.QueryContext(ctx,
		`SELECT id, enc, type, type_ct, name, name_ct FROM entities
		 WHERE user_id = ? AND deleted_at IS NULL
		   AND ((enc AND name_blind = ?) OR (?::boolean AND enc IS NOT TRUE AND lower(name) = lower(?)))
		 ORDER BY created_at, id`,
		c.UserID, c.BlindNorm(name), c.legacyOK(), name)
	if err != nil {
		return nil, fmt.Errorf("cortex: find entity: %w", err)
	}
	type cand struct {
		id    string
		enc   bool
		exact bool
	}
	var cands []cand
	for rows.Next() {
		var e model.Entity
		var enc bool
		var t, n sql.NullString
		var typeCT, nameCT []byte
		if err := rows.Scan(&e.ID, &enc, &t, &typeCT, &n, &nameCT); err != nil {
			rows.Close()
			return nil, fmt.Errorf("cortex: scan entity: %w", err)
		}
		if err := c.openNameType(&e, enc, n, t, nameCT, typeCT); err != nil {
			rows.Close()
			return nil, err
		}
		if kindMatches(e.Type, typ) {
			cands = append(cands, cand{e.ID, enc, e.Name == name})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cortex: find entity: %w", err)
	}
	if len(cands) == 0 {
		return nil, nil
	}
	best := cands[0]
	for _, cd := range cands {
		if cd.exact {
			best = cd
			break
		}
	}
	e, err := c.GetEntity(ctx, best.id)
	if err != nil {
		return nil, err
	}
	if !best.enc {
		c.rewriteEntityLegacy(ctx, e)
	}
	return e, nil
}

// kindMatches reports whether a stored entity type is the same kind as typ:
// equal after normalisation, or mapping to the same curated type. An empty
// typ matches everything, and so does concept on either side: it is the
// catch-all, so a concept and a product of the same name are one thing typed
// loosely once.
func kindMatches(stored, typ string) bool {
	if typ == "" {
		return true
	}
	st := ontology.NormalizeType(stored)
	if st == typ {
		return true
	}
	sc, _ := ontology.CanonicalEntityType(st)
	tc, _ := ontology.CanonicalEntityType(typ)
	return sc == tc || sc == ontology.TypeConcept || tc == ontology.TypeConcept
}

// absorbIntoEntity folds an extracted entity into the stored one it matched:
// missing attribute keys are added, confidence keeps the higher value, and
// a non-owner source is refreshed. e.ID and e.Name take the stored values.
func (c Ops) absorbIntoEntity(ctx context.Context, stored, e *model.Entity) error {
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
	conf := model.CoerceConfidence(e.Confidence)
	if stored.Confidence > conf {
		conf = stored.Confidence
	}
	now := time.Now().UTC()
	if c.Encrypted() {
		var isOwner bool
		if err := c.DB.QueryRowContext(ctx, `SELECT is_owner FROM entities WHERE id = ? AND user_id = ?`, stored.ID, c.UserID).Scan(&isOwner); err != nil {
			return fmt.Errorf("cortex: update entity: %w", err)
		}
		source := e.Source
		if isOwner || source == "" {
			source = stored.Source
		}
		if _, err := c.writeEntityEnc(ctx, c.DB, entWrite{id: stored.ID, name: stored.Name, typ: stored.Type, source: source,
			attrs: attrs, conf: conf, updated: now}); err != nil {
			return err
		}
		c.NotifyWrite("entity")
	} else if _, err := c.DB.ExecContext(ctx,
		`UPDATE entities SET attributes = ?::jsonb, confidence = ?, updated_at = ?,
		   source = CASE WHEN is_owner OR ? = '' THEN source ELSE ? END
		 WHERE id = ? AND user_id = ?`,
		string(attrsJSON), conf, now, e.Source, e.Source, stored.ID, c.UserID,
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
