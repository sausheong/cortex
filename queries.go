package cortex

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sausheong/cortex/internal/graph"
	"github.com/sausheong/cortex/internal/memory"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
	"github.com/sausheong/cortex/internal/tenancy"
)

// Curated graph queries: the questions agents ask most, answered in one call
// each, so an agent does not have to chain traversals.

const whoIsMemories = 20

// WhoIs resolves a mention (a name, alias, email, phone or handle) and
// returns the entity with its aliases, current relationships (named) and
// current memories, static ones first. When the mention does not resolve
// confidently, the card is empty and Candidates lists the closest entities.
func (c *Cortex) WhoIs(ctx context.Context, mention string) (*EntityCard, error) {
	res, err := c.ResolveEntity(ctx, mention, "")
	if err != nil {
		return nil, err
	}
	if res.Entity == nil {
		return &EntityCard{Resolution: res, Candidates: res.Candidates, Aliases: []Alias{}, Relationships: []NamedEdge{}, Memories: []Memory{}}, nil
	}
	card, err := c.Describe(ctx, res.Entity.ID)
	if err != nil {
		return nil, err
	}
	card.Resolution = res
	return card, nil
}

// Describe returns the EntityCard of a known entity.
func (c *Cortex) Describe(ctx context.Context, entityID string) (*EntityCard, error) {
	e, err := c.GetEntity(ctx, entityID)
	if err != nil {
		return nil, err
	}
	card := &EntityCard{Entity: *e}
	if card.Aliases, err = c.Aliases(ctx, e.ID); err != nil {
		return nil, err
	}
	rels, err := c.GetRelationships(ctx, e.ID)
	if err != nil {
		return nil, err
	}
	if card.Relationships, err = c.nameEdges(ctx, rels); err != nil {
		return nil, err
	}
	mems, err := c.GetMemoriesByEntity(ctx, e.ID)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(mems, func(i, j int) bool {
		if mems[i].Static != mems[j].Static {
			return mems[i].Static
		}
		return mems[i].CreatedAt.After(mems[j].CreatedAt)
	})
	if len(mems) > whoIsMemories {
		mems = mems[:whoIsMemories]
	}
	card.Memories = mems
	if card.Aliases == nil {
		card.Aliases = []Alias{}
	}
	if card.Memories == nil {
		card.Memories = []Memory{}
	}
	return card, nil
}

// nameEdges attaches both ends' names to relationships.
func (c *Cortex) nameEdges(ctx context.Context, rels []Relationship) ([]NamedEdge, error) {
	ids := map[string]bool{}
	for _, r := range rels {
		ids[r.SourceID], ids[r.TargetID] = true, true
	}
	names, err := c.entityNames(ctx, ids)
	if err != nil {
		return nil, err
	}
	out := make([]NamedEdge, 0, len(rels))
	for _, r := range rels {
		out = append(out, NamedEdge{Relationship: r, SourceName: names[r.SourceID], TargetName: names[r.TargetID]})
	}
	return out, nil
}

// entityNames loads the names of the given entities, including deleted ones
// (an edge in history may outlive an end).
func (c *Cortex) entityNames(ctx context.Context, ids map[string]bool) (map[string]string, error) {
	names := map[string]string{}
	if len(ids) == 0 {
		return names, nil
	}
	list := make([]string, 0, len(ids))
	for id := range ids {
		list = append(list, id)
	}
	rows, err := c.h.DB.QueryContext(ctx,
		`SELECT id, name FROM entities WHERE user_id = ? AND id = ANY(?)`, c.h.UserID, list)
	if err != nil {
		return nil, fmt.Errorf("cortex: entity names: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, fmt.Errorf("cortex: scan entity name: %w", err)
		}
		names[id] = name
	}
	return names, rows.Err()
}

// maxPathHops bounds Path's search.
const maxPathHops = 6

// Path returns a shortest chain of current relationships linking two
// entities (in either direction), at most maxHops long (default 4, capped at
// 6), or nil when they are not connected within that distance. The owner is
// only used as an end, never as a stepping stone: everything connects
// through the owner, which says nothing.
func (c *Cortex) Path(ctx context.Context, fromID, toID string, maxHops int) ([]NamedEdge, error) {
	if err := store.RequireEntity(ctx, c.h.DB, c.h.UserID, fromID); err != nil {
		return nil, err
	}
	if err := store.RequireEntity(ctx, c.h.DB, c.h.UserID, toID); err != nil {
		return nil, err
	}
	if fromID == toID {
		return []NamedEdge{}, nil
	}
	if maxHops <= 0 {
		maxHops = 4
	}
	if maxHops > maxPathHops {
		maxHops = maxPathHops
	}
	ownerID, _ := tenancy.New(c.h).OwnerEntityID(ctx)
	validity, vargs := graph.EdgeValidityClause("r", false, nil)
	const next = `CASE WHEN r.source_id = w.node THEN r.target_id ELSE r.source_id END`
	args := []any{fromID, fromID, c.h.UserID}
	args = append(args, vargs...)
	args = append(args, maxHops, ownerID, toID, toID)
	var edgeIDs string
	err := c.h.DB.QueryRowContext(ctx,
		`WITH RECURSIVE walk(node, visited, edges, depth) AS (
		   SELECT ?::text, ARRAY[?::text], ARRAY[]::text[], 0
		   UNION ALL
		   SELECT `+next+`, w.visited || `+next+`, w.edges || r.id, w.depth + 1
		   FROM walk w
		   JOIN relationships r ON r.user_id = ? AND r.deleted_at IS NULL AND `+validity+`
		    AND (r.source_id = w.node OR r.target_id = w.node)
		   WHERE w.depth < ? AND NOT `+next+` = ANY(w.visited)
		     AND (w.depth = 0 OR w.node <> ?) AND w.node <> ?
		 )
		 SELECT array_to_string(edges, ',') FROM walk WHERE node = ? ORDER BY depth LIMIT 1`,
		args...,
	).Scan(&edgeIDs)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cortex: path: %w", err)
	}
	ids := strings.Split(edgeIDs, ",")
	rels := make([]Relationship, 0, len(ids))
	for _, id := range ids {
		r, err := c.getRelationship(ctx, id)
		if err != nil {
			return nil, err
		}
		rels = append(rels, *r)
	}
	return c.nameEdges(ctx, rels)
}

// getRelationship loads one relationship by ID.
func (c *Cortex) getRelationship(ctx context.Context, id string) (*Relationship, error) {
	var r Relationship
	var attrsJSON sql.NullString
	var validAt, invalidAt sql.NullTime
	err := c.h.DB.QueryRowContext(ctx,
		`SELECT id, source_id, target_id, type, attributes, source, confidence, created_at, evidence_count, last_seen_at, valid_at, invalid_at
		 FROM relationships WHERE id = ? AND user_id = ?`, id, c.h.UserID,
	).Scan(&r.ID, &r.SourceID, &r.TargetID, &r.Type, &attrsJSON, &r.Source, &r.Confidence, &r.CreatedAt, &r.EvidenceCount, &r.LastSeenAt, &validAt, &invalidAt)
	if err != nil {
		return nil, fmt.Errorf("cortex: get relationship: %w", err)
	}
	if attrsJSON.Valid && attrsJSON.String != "" {
		if err := json.Unmarshal([]byte(attrsJSON.String), &r.Attributes); err != nil {
			return nil, fmt.Errorf("cortex: unmarshal relationship attributes: %w", err)
		}
	}
	r.ValidAt, r.InvalidAt = store.NullTimePtr(validAt), store.NullTimePtr(invalidAt)
	return &r, nil
}

// Timeline returns an entity's history, oldest first: its memories (dated by
// when they became true, else when they were recorded), and the start and
// end of its relationships. since, when non-zero, drops earlier events;
// limit (default 100) keeps the most recent. Retired memories are included,
// so the timeline shows what used to be true.
func (c *Cortex) Timeline(ctx context.Context, entityID string, since time.Time, limit int) ([]TimelineEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	mems, err := memory.New(c.h).GetMemoriesByEntityMode(ctx, entityID, model.TemporalMode{IncludeInvalid: true})
	if err != nil {
		return nil, err
	}
	var out []TimelineEvent
	for _, m := range mems {
		at := m.CreatedAt
		if m.ValidAt != nil {
			at = *m.ValidAt
		}
		out = append(out, TimelineEvent{At: at, Kind: "memory", Text: m.Content, ID: m.ID})
	}
	rels, err := c.GetRelationships(ctx, entityID, RelIncludeInvalid())
	if err != nil {
		return nil, err
	}
	named, err := c.nameEdges(ctx, rels)
	if err != nil {
		return nil, err
	}
	for _, e := range named {
		start := e.CreatedAt
		if e.ValidAt != nil {
			start = *e.ValidAt
		}
		out = append(out, TimelineEvent{At: start, Kind: "relationship_started", Text: e.String(), ID: e.ID})
		if e.InvalidAt != nil {
			out = append(out, TimelineEvent{At: *e.InvalidAt, Kind: "relationship_ended", Text: e.String(), ID: e.ID})
		}
	}
	if !since.IsZero() {
		kept := out[:0]
		for _, ev := range out {
			if !ev.At.Before(since) {
				kept = append(kept, ev)
			}
		}
		out = kept
	}
	// At the same moment an ending comes before a start, and both before the
	// facts recorded then ("moved from London to Singapore" reads in order).
	rank := map[string]int{"relationship_ended": 0, "relationship_started": 1, "memory": 2}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.Before(out[j].At)
		}
		return rank[out[i].Kind] < rank[out[j].Kind]
	})
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	if out == nil {
		out = []TimelineEvent{}
	}
	return out, nil
}

// WhatChanged lists what was added to or retired from the graph since t:
// new entities, memories and relationships (by when they were recorded),
// memories that stopped being current, and relationships whose end falls
// after t (by when they ended, which for a back-dated end may be before it
// was recorded). Each list holds at most limit items (default 50), newest
// first.
func (c *Cortex) WhatChanged(ctx context.Context, since time.Time, limit int) (*Changes, error) {
	if limit <= 0 {
		limit = 50
	}
	ch := &Changes{Since: since}

	erows, err := c.h.DB.QueryContext(ctx,
		`SELECT id, type, name, COALESCE(source, ''), confidence, created_at, updated_at FROM entities
		 WHERE user_id = ? AND deleted_at IS NULL AND created_at >= ? ORDER BY created_at DESC LIMIT ?`,
		c.h.UserID, since, limit)
	if err != nil {
		return nil, fmt.Errorf("cortex: changed entities: %w", err)
	}
	for erows.Next() {
		var e Entity
		if err := erows.Scan(&e.ID, &e.Type, &e.Name, &e.Source, &e.Confidence, &e.CreatedAt, &e.UpdatedAt); err != nil {
			erows.Close()
			return nil, fmt.Errorf("cortex: scan changed entity: %w", err)
		}
		ch.NewEntities = append(ch.NewEntities, e)
	}
	erows.Close()

	memQuery := func(where string) ([]Memory, error) {
		rows, err := c.h.DB.QueryContext(ctx,
			`SELECT id, content, COALESCE(source, ''), confidence, created_at, valid_at, invalid_at, expired_at FROM memories
			 WHERE user_id = ? AND deleted_at IS NULL AND `+where+` LIMIT ?`, c.h.UserID, since, limit)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []Memory
		for rows.Next() {
			var m Memory
			var validAt, invalidAt, expiredAt sql.NullTime
			if err := rows.Scan(&m.ID, &m.Content, &m.Source, &m.Confidence, &m.CreatedAt, &validAt, &invalidAt, &expiredAt); err != nil {
				return nil, err
			}
			m.ValidAt, m.InvalidAt, m.ExpiredAt = store.NullTimePtr(validAt), store.NullTimePtr(invalidAt), store.NullTimePtr(expiredAt)
			out = append(out, m)
		}
		return out, rows.Err()
	}
	if ch.NewMemories, err = memQuery(`created_at >= ? ORDER BY created_at DESC`); err != nil {
		return nil, fmt.Errorf("cortex: changed memories: %w", err)
	}
	if ch.RetiredMemories, err = memQuery(`GREATEST(invalid_at, expired_at) >= ? ORDER BY GREATEST(invalid_at, expired_at) DESC`); err != nil {
		return nil, fmt.Errorf("cortex: retired memories: %w", err)
	}

	relQuery := func(where string) ([]NamedEdge, error) {
		rows, err := c.h.DB.QueryContext(ctx,
			`SELECT id FROM relationships WHERE user_id = ? AND deleted_at IS NULL AND `+where+` LIMIT ?`, c.h.UserID, since, limit)
		if err != nil {
			return nil, err
		}
		var ids []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
		rels := make([]Relationship, 0, len(ids))
		for _, id := range ids {
			r, err := c.getRelationship(ctx, id)
			if err != nil {
				return nil, err
			}
			rels = append(rels, *r)
		}
		return c.nameEdges(ctx, rels)
	}
	if ch.NewRelationships, err = relQuery(`created_at >= ? ORDER BY created_at DESC`); err != nil {
		return nil, fmt.Errorf("cortex: changed relationships: %w", err)
	}
	if ch.EndedRelationships, err = relQuery(`invalid_at >= ? ORDER BY invalid_at DESC`); err != nil {
		return nil, fmt.Errorf("cortex: ended relationships: %w", err)
	}

	if ch.NewEntities == nil {
		ch.NewEntities = []Entity{}
	}
	if ch.NewMemories == nil {
		ch.NewMemories = []Memory{}
	}
	if ch.RetiredMemories == nil {
		ch.RetiredMemories = []Memory{}
	}
	return ch, nil
}

// Link records a relationship known for certain (from structured data such
// as an email header), without free-text extraction. Each end is an entity
// ID, or a name or identifier resolved like an ingested mention; one that
// resolves to nothing is created with the given type (default person), and
// an identifier used as its name is kept as an alias. The verb is mapped
// onto the curated vocabulary as in Remember.
func (c *Cortex) Link(ctx context.Context, r LinkRequest) (*NamedEdge, error) {
	rc := &graph.ResolveContext{}
	src, err := c.linkEnd(ctx, r.Source, r.SourceType, r.Provenance, rc)
	if err != nil {
		return nil, err
	}
	tgt, err := c.linkEnd(ctx, r.Target, r.TargetType, r.Provenance, rc)
	if err != nil {
		return nil, err
	}
	if src.ID == tgt.ID {
		return nil, fmt.Errorf("cortex: both ends are %q", src.Name)
	}
	st, _ := CanonicalEntityType(src.Type)
	tt, _ := CanonicalEntityType(tgt.Type)
	verb, swap, label := CanonicalRelation(r.Type, st, tt)
	if swap {
		src, tgt = tgt, src
	}
	rel := &Relationship{SourceID: src.ID, TargetID: tgt.ID, Type: verb, Source: r.Provenance}
	if label != "" {
		rel.Attributes = map[string]any{"label": label}
	}
	if err := c.PutRelationship(ctx, rel); err != nil {
		return nil, err
	}
	return &NamedEdge{Relationship: *rel, SourceName: src.Name, TargetName: tgt.Name}, nil
}

// linkEnd finds or creates one end of a Link.
func (c *Cortex) linkEnd(ctx context.Context, ref, typ, provenance string, rc *graph.ResolveContext) (*Entity, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("cortex: empty entity reference")
	}
	if e, err := c.GetEntity(ctx, ref); err == nil {
		return e, nil
	}
	canonical := ""
	if typ != "" {
		canonical, _ = CanonicalEntityType(typ)
	}
	res, err := graph.New(c.h).Resolve(ctx, ref, canonical, nil, rc, false)
	if err != nil {
		return nil, err
	}
	if res.Entity != nil {
		return res.Entity, nil
	}
	if canonical == "" {
		canonical = TypePerson
	}
	e := &Entity{Name: ref, Type: canonical, Source: provenance}
	if err := c.PutEntity(ctx, e); err != nil {
		return nil, err
	}
	if kind := DetectAliasKind(ref); kind != AliasName {
		graph.New(c.h).AddAliasQuiet(ctx, e.ID, kind, ref, "link")
	}
	return e, nil
}
