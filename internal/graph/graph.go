package graph

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/tenancy"
)

// GraphNeighbourhood returns the centre entity with up to Limit-1 of its
// direct neighbours, ranked by how many relationships join them to the centre
// and then by recency, and the live relationships among the chosen nodes. An
// absent or foreign centre is ErrNotFound. It runs two queries after the
// centre lookup, both using the relationship source/target indexes.
func (c Ops) GraphNeighbourhood(ctx context.Context, centerID string, f model.GraphFilter) (*model.Neighbourhood, error) {
	center, err := c.GetEntity(ctx, centerID)
	if err != nil {
		return nil, err
	}
	limit := f.Limit
	if limit < 1 {
		limit = 60
	}
	if limit > 500 {
		limit = 500
	}
	g := &model.Neighbourhood{Nodes: []model.GraphNode{{ID: center.ID, Type: center.Type, Name: center.Name, Center: true}}}
	return g, c.graphNeighbourhoodEnc(ctx, g, centerID, f, limit)
}

// RenameEntity renames a live entity of this user. A name already used by
// another live entity of the same type returns *ErrEntityNameTaken. The
// entity's name embedding is dropped in the same transaction because it
// described the old name; re-embedding rebuilds it.
func (c Ops) RenameEntity(ctx context.Context, id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("cortex: entity name is empty")
	}
	if err := c.RequireKey(); err != nil {
		return err
	}
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cortex: begin rename: %w", err)
	}
	defer tx.Rollback()
	return c.renameEntityEnc(ctx, tx, id, name)
}

// SearchEntities finds live entities whose name contains q (case-insensitive,
// wildcards in q are literal), names starting with q first. Types, when given,
// restricts the result; ExcludeID drops one entity (used to pick a merge
// target). Results are capped at limit (default 20, max 100).
func (c Ops) SearchEntities(ctx context.Context, q string, types []string, excludeID string, limit int) ([]model.Entity, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	return c.searchEntitiesEnc(ctx, q, types, excludeID, limit)
}

// EntityTypes lists the distinct types of this user's live entities.
func (c Ops) EntityTypes(ctx context.Context) ([]string, error) {
	return c.entityTypesEnc(ctx)
}

// EntityRelations returns up to limit live relationships touching the entity
// (newest first) and the total count. Relationships whose other end is gone
// are not counted.
func (c Ops) EntityRelations(ctx context.Context, id string, limit int) ([]model.EntityRelation, int, error) {
	if limit < 1 {
		limit = 50
	}
	return c.entityRelationsEnc(ctx, id, limit)
}

// EntityMentions returns the latest live memories linked to the entity and
// the total number of them.
func (c Ops) EntityMentions(ctx context.Context, id string, limit int) ([]model.Memory, int, error) {
	return tenancy.New(c.Handle).ListMemories(ctx, model.MemoryListFilter{EntityID: id}, limit, 0)
}
