package cortex

import (
	"context"
	"time"

	"github.com/sausheong/cortex/internal/graph"
	"github.com/sausheong/cortex/internal/ontology"
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

// The curated entity types. Extractors are asked to use these; anything else
// is mapped through a synonym table or falls back to TypeConcept, with the
// original label kept in the entity's "type_label" attribute.
const (
	TypePerson       = ontology.TypePerson
	TypeOrganization = ontology.TypeOrganization
	TypePlace        = ontology.TypePlace
	TypeProject      = ontology.TypeProject
	TypeProduct      = ontology.TypeProduct
	TypeEvent        = ontology.TypeEvent
	TypeGroup        = ontology.TypeGroup
	TypeDocument     = ontology.TypeDocument
	TypeAccount      = ontology.TypeAccount
	TypeConcept      = ontology.TypeConcept
)

// The curated relationship verbs. Each reads source-verb-target.
const (
	RelWorksAt          = ontology.RelWorksAt
	RelMemberOf         = ontology.RelMemberOf
	RelLeads            = ontology.RelLeads
	RelFounded          = ontology.RelFounded
	RelReportsTo        = ontology.RelReportsTo
	RelKnows            = ontology.RelKnows
	RelFamilyOf         = ontology.RelFamilyOf
	RelCommunicatedWith = ontology.RelCommunicatedWith
	RelLivesIn          = ontology.RelLivesIn
	RelLocatedIn        = ontology.RelLocatedIn
	RelPartOf           = ontology.RelPartOf
	RelOwns             = ontology.RelOwns
	RelUses             = ontology.RelUses
	RelCreated          = ontology.RelCreated
	RelParticipatedIn   = ontology.RelParticipatedIn
	RelPartneredWith    = ontology.RelPartneredWith
	RelAcquired         = ontology.RelAcquired
	RelInvestedIn       = ontology.RelInvestedIn
	RelPaid             = ontology.RelPaid
	RelAbout            = ontology.RelAbout
	RelRelatedTo        = ontology.RelRelatedTo
)

// EntityTypes lists the curated entity types with a one-line gloss each, in
// the order they are shown to extractors.
var EntityTypes = ontology.EntityTypes

// RelationshipVerbs lists the curated relationship verbs in the order they
// are shown to extractors.
var RelationshipVerbs = ontology.RelationshipVerbs

// CanonicalEntityType maps a type label onto the curated set. known reports
// whether the label was recognised; an unrecognised label maps to TypeConcept.
func CanonicalEntityType(label string) (canonical string, known bool) {
	return ontology.CanonicalEntityType(label)
}

// CanonicalRelation maps a relationship label onto the curated verbs, given
// the types at each end. swap reports that source and target must be
// exchanged; label carries the original wording when it was not exact.
func CanonicalRelation(rawVerb, fromType, toType string) (verb string, swap bool, label string) {
	return ontology.CanonicalRelation(rawVerb, fromType, toType)
}

// VocabularyPrompt renders the curated entity types and relationship verbs
// as instructions for an extraction prompt.
func VocabularyPrompt() string { return ontology.VocabularyPrompt() }

// NormalizeType canonicalises an entity or relationship type label:
// lower-cased, trimmed, with runs of spaces, hyphens and underscores
// collapsed to one underscore.
func NormalizeType(t string) string { return ontology.NormalizeType(t) }

// NormalizeGraph brings an existing graph onto the curated vocabulary, in one
// transaction recorded as a single undoable batch:
//
//  1. every entity type moves onto the curated set (an unknown label becomes
//     concept, keeping the label in the "type_label" attribute);
//  2. live entities that now share a name (ignoring case) and type are
//     merged, as are person entities named like the owner, into the owner;
//  3. every relationship verb moves onto the curated set (CanonicalRelation),
//     folding any edge that becomes identical to a live one.
//
// RestoreBatch with the report's BatchID undoes the whole run.
func (c *Cortex) NormalizeGraph(ctx context.Context, opts ...NormalizeOption) (NormalizeReport, error) {
	return graph.New(c.h).NormalizeGraph(ctx, opts...)
}

// Lint scans the graph and returns a structured report of cleanup
// candidates. Pure read operation — never mutates the graph.
func (c *Cortex) Lint(ctx context.Context, opts ...LintOption) (LintReport, error) {
	return graph.New(c.h).Lint(ctx, opts...)
}

// RenderNormalizeMarkdown renders a NormalizeReport for the CLI.
func RenderNormalizeMarkdown(r NormalizeReport) string { return graph.RenderNormalizeMarkdown(r) }
