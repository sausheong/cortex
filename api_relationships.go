package cortex

import (
	"context"
	"time"

	"github.com/sausheong/cortex/internal/graph"
)

// PutRelationship upserts a relationship keyed on (source, target, type),
// with the type normalised by NormalizeType. A new edge gets a generated ULID;
// a repeat sighting of a live edge increments its EvidenceCount, refreshes
// LastSeenAt, keeps the higher confidence and merges attributes (new keys
// win), and the existing ID is set on the passed relationship.
func (c *Cortex) PutRelationship(ctx context.Context, r *Relationship) error {
	return graph.New(c.h).PutRelationship(ctx, r)
}

// InvalidateRelationship records that a relationship ended at t. The edge
// stays in the graph: as-of reads before t and RelIncludeInvalid still see
// it. Re-asserting it with PutRelationship reopens it.
func (c *Cortex) InvalidateRelationship(ctx context.Context, id string, t time.Time) error {
	return graph.New(c.h).InvalidateRelationship(ctx, id, t)
}

// GetRelationships returns the relationships where entityID is either the
// source or the target that hold now. Optional RelFilter functions can
// narrow the results (e.g. by relationship type) or change the time
// (RelAsOf, RelIncludeInvalid).
func (c *Cortex) GetRelationships(ctx context.Context, entityID string, filters ...RelFilter) ([]Relationship, error) {
	return graph.New(c.h).GetRelationships(ctx, entityID, filters...)
}

// Traverse performs a breadth-first walk from startID through the knowledge
// graph. It respects the configured depth (default 1) and optional edge type
// filters. Returns a Graph containing all visited entities and traversed
// relationships. Cycles are avoided via a visited set.
func (c *Cortex) Traverse(ctx context.Context, startID string, opts ...TraverseOption) (*Graph, error) {
	return graph.New(c.h).Traverse(ctx, startID, opts...)
}

// GraphNeighbourhood returns the centre entity with up to Limit-1 of its
// direct neighbours, ranked by how many relationships join them to the centre
// and then by recency, and the live relationships among the chosen nodes. An
// absent or foreign centre is ErrNotFound. It runs two queries after the
// centre lookup, both using the relationship source/target indexes.
func (c *Cortex) GraphNeighbourhood(ctx context.Context, centerID string, f GraphFilter) (*Neighbourhood, error) {
	return graph.New(c.h).GraphNeighbourhood(ctx, centerID, f)
}

// RenameEntity renames a live entity of this user. A name already used by
// another live entity of the same type returns *ErrEntityNameTaken. The
// entity's name embedding is dropped in the same transaction because it
// described the old name; re-embedding rebuilds it.
func (c *Cortex) RenameEntity(ctx context.Context, id string, name string) error {
	return graph.New(c.h).RenameEntity(ctx, id, name)
}

// SearchEntities finds live entities whose name contains q (case-insensitive,
// wildcards in q are literal), names starting with q first. Types, when given,
// restricts the result; ExcludeID drops one entity (used to pick a merge
// target). Results are capped at limit (default 20, max 100).
func (c *Cortex) SearchEntities(ctx context.Context, q string, types []string, excludeID string, limit int) ([]Entity, error) {
	return graph.New(c.h).SearchEntities(ctx, q, types, excludeID, limit)
}

// EntityTypes lists the distinct types of this user's live entities.
func (c *Cortex) EntityTypes(ctx context.Context) ([]string, error) {
	return graph.New(c.h).EntityTypes(ctx)
}

// EntityRelations returns up to limit live relationships touching the entity
// (newest first) and the total count. Relationships whose other end is gone
// are not counted.
func (c *Cortex) EntityRelations(ctx context.Context, id string, limit int) ([]EntityRelation, int, error) {
	return graph.New(c.h).EntityRelations(ctx, id, limit)
}

// EntityMentions returns the latest live memories linked to the entity and
// the total number of them.
func (c *Cortex) EntityMentions(ctx context.Context, id string, limit int) ([]Memory, int, error) {
	return graph.New(c.h).EntityMentions(ctx, id, limit)
}
