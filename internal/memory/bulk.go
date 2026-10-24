package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
)

// MemoriesByEntityAll returns every currently-valid memory grouped by the
// entity it is linked to, in one query. It is the bulk form of
// GetMemoriesByEntity for whole-graph builds (the vault), which would
// otherwise make one round trip per entity plus one per memory. Memories are
// ordered oldest first; EntityIDs is filled from the same links.
func (c Ops) MemoriesByEntityAll(ctx context.Context) (map[string][]model.Memory, error) {
	clause, targs := model.TemporalMode{}.Clause("m")
	args := append([]any{c.UserID}, targs...)
	rows, err := c.DB.QueryContext(ctx,
		`SELECT me.entity_id, m.id, m.content, m.source, m.speaker, m.confidence, m.created_at, m.updated_at, m.valid_at, m.invalid_at, m.expired_at, m.static, m.forget_after, COALESCE(m.written_by_connection_id, '')
		 FROM memories m
		 JOIN memory_entities me ON m.id = me.memory_id AND me.user_id = m.user_id
		 JOIN entities e ON e.id = me.entity_id AND e.user_id = me.user_id AND e.deleted_at IS NULL
		 WHERE m.user_id = ? AND m.deleted_at IS NULL AND `+clause+`
		 ORDER BY m.created_at, m.id`,
		args...,
	)
	if err != nil {
		return nil, fmt.Errorf("cortex: memories by entity: %w", err)
	}
	defer rows.Close()

	out := map[string][]model.Memory{}
	links := map[string][]string{} // memory id -> linked entity ids
	for rows.Next() {
		var eid string
		var m model.Memory
		var spk sql.NullString
		var vat, iat, eat, fa sql.NullTime
		if err := rows.Scan(&eid, &m.ID, &m.Content, &m.Source, &spk, &m.Confidence, &m.CreatedAt, &m.UpdatedAt, &vat, &iat, &eat, &m.Static, &fa, &m.WrittenBy); err != nil {
			return nil, fmt.Errorf("cortex: scan memory: %w", err)
		}
		if spk.Valid {
			m.Speaker = spk.String
		}
		m.ValidAt = store.NullTimePtr(vat)
		m.InvalidAt = store.NullTimePtr(iat)
		m.ExpiredAt = store.NullTimePtr(eat)
		m.ForgetAfter = store.NullTimePtr(fa)
		out[eid] = append(out[eid], m)
		links[m.ID] = append(links[m.ID], eid)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cortex: iterate memories: %w", err)
	}
	for eid, ms := range out {
		for i := range ms {
			ms[i].EntityIDs = links[ms[i].ID]
		}
		out[eid] = ms
	}
	return out, nil
}

// AllRelationships returns every live relationship for the user in one
// query, ordered by id. It is the bulk form of GetRelationships.
func (c Ops) AllRelationships(ctx context.Context) ([]model.Relationship, error) {
	rows, err := c.DB.QueryContext(ctx,
		`SELECT id, source_id, target_id, type, attributes, source, confidence, created_at
		 FROM relationships
		 WHERE user_id = ? AND deleted_at IS NULL
		 ORDER BY id`,
		c.UserID,
	)
	if err != nil {
		return nil, fmt.Errorf("cortex: all relationships: %w", err)
	}
	defer rows.Close()

	var rels []model.Relationship
	for rows.Next() {
		var r model.Relationship
		var attrsJSON sql.NullString
		if err := rows.Scan(&r.ID, &r.SourceID, &r.TargetID, &r.Type, &attrsJSON, &r.Source, &r.Confidence, &r.CreatedAt); err != nil {
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
