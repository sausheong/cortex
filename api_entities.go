package cortex

import (
	"context"

	"github.com/sausheong/cortex/internal/graph"
)

// PutEntity upserts an entity by (name, type), with the type normalised by
// NormalizeType. If an entity with the same name and type already exists,
// its attributes, source, and updated_at are updated and the existing ID is
// set on the passed entity. Otherwise a new entity is inserted with a
// generated ULID. When e.ID names a live entity (as after GetEntity), that
// row is updated in place instead, keeping its stored type spelling if it
// normalises to the same type, so a read-modify-write never forks a row
// whose type predates normalisation.
func (c *Cortex) PutEntity(ctx context.Context, e *Entity) error {
	return graph.New(c.h).PutEntity(ctx, e)
}

// GetEntity retrieves an entity by ID. Returns an error if not found.
func (c *Cortex) GetEntity(ctx context.Context, id string) (*Entity, error) {
	return graph.New(c.h).GetEntity(ctx, id)
}

// FindEntities returns entities matching the given filter. All filter fields
// are optional — an empty filter returns all entities.
func (c *Cortex) FindEntities(ctx context.Context, f EntityFilter) ([]Entity, error) {
	return graph.New(c.h).FindEntities(ctx, f)
}

// MergeEntities merges the drop entity into the keep entity, atomically.
// All references to dropID (relationships, memory_entities, chunks,
// embeddings) are re-targeted to keepID; duplicates that would arise
// from the re-target are collapsed; the drop entity's attributes are
// unioned into the keep entity (keep wins on conflicts); a `merged_from`
// provenance record is appended to keep's attributes; finally the drop
// entity row is deleted. On any error, all changes are rolled back.
func (c *Cortex) MergeEntities(ctx context.Context, keepID string, dropID string) (MergeStats, error) {
	return graph.New(c.h).MergeEntities(ctx, keepID, dropID)
}

// MergeEntitiesDryRun runs the merge algorithm but always ROLLBACKs the
// transaction, returning the same MergeStats a real run would produce
// without writing any changes. Used by `cortex merge --dry-run`.
func (c *Cortex) MergeEntitiesDryRun(ctx context.Context, keepID string, dropID string) (MergeStats, error) {
	return graph.New(c.h).MergeEntitiesDryRun(ctx, keepID, dropID)
}

// AddAlias records value as an identifier of entityID. kind is one of the
// Alias* constants; empty means detect it with DetectAliasKind. Adding an
// alias the entity already has is a no-op; one that belongs to another live
// entity fails with ErrAliasTaken.
func (c *Cortex) AddAlias(ctx context.Context, entityID string, kind string, value string) error {
	return graph.New(c.h).AddAlias(ctx, entityID, kind, value)
}

// Aliases returns the identifiers recorded for an entity.
func (c *Cortex) Aliases(ctx context.Context, entityID string) ([]Alias, error) {
	return graph.New(c.h).Aliases(ctx, entityID)
}

// RemoveAlias deletes an alias. kind empty means detect it from value.
func (c *Cortex) RemoveAlias(ctx context.Context, kind string, value string) error {
	return graph.New(c.h).RemoveAlias(ctx, kind, value)
}

// ResolveEntity resolves a mention to the stored entity it most likely names,
// without creating anything or consulting an LLM. typ narrows name matching
// to one (normalised) type; empty matches any type. Identifier aliases
// (email, phone, handle) match regardless of type.
func (c *Cortex) ResolveEntity(ctx context.Context, mention string, typ string) (*Resolution, error) {
	return graph.New(c.h).ResolveEntity(ctx, mention, typ)
}

// DisambiguatePrompt is the system prompt LLM providers use to implement
// EntityDisambiguator.
const DisambiguatePrompt = graph.DisambiguatePrompt

// DetectAliasKind guesses the kind of an identifier: an email address, a
// phone number (seven or more digits, optionally formatted), an @handle, or
// otherwise a name.
func DetectAliasKind(value string) string { return graph.DetectAliasKind(value) }

// NameSimilarity scores how alike two entity names are, from 0 to 1. Case,
// punctuation and spacing are ignored.
func NameSimilarity(a, b string) float64 { return graph.NameSimilarity(a, b) }

// FormatDisambiguation renders a mention and its candidates as the user
// message for DisambiguatePrompt.
func FormatDisambiguation(m EntityMention, candidates []Entity) string {
	return graph.FormatDisambiguation(m, candidates)
}

// ParseEntityChoice parses a reply to DisambiguatePrompt, tolerating a
// markdown code fence.
func ParseEntityChoice(raw string) (EntityChoice, error) { return graph.ParseEntityChoice(raw) }
