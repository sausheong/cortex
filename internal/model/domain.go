package model

import (
	"context"
	"time"
)

// Change is one proposed compaction: Result replaces SourceIDs.
type Change struct {
	Kind      string   `json:"kind"` // "merge" | "combine" | "rollup"
	SourceIDs []string `json:"source_ids"`
	Result    string   `json:"result"`
	Static    bool     `json:"static"`
	// Sources holds the source memory contents for display in a preview.
	Sources []string `json:"sources,omitempty"`
}

// Alias is one stable identifier of an entity.
type Alias struct {
	EntityID  string    `json:"entity_id"`
	Kind      string    `json:"kind"`
	Value     string    `json:"value"`
	Source    string    `json:"source,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Resolution is the outcome of resolving a mention to a stored entity.
// Entity is nil when no candidate was confident enough; Candidates then
// holds the closest stored entities, best first.
type Resolution struct {
	Entity     *Entity        `json:"entity,omitempty"`
	Method     string         `json:"method,omitempty"`
	Score      float64        `json:"score"`
	Candidates []ScoredEntity `json:"candidates,omitempty"`
}

// ScoredEntity is a resolution candidate with its match score in [0, 1].
type ScoredEntity struct {
	Entity Entity  `json:"entity"`
	Score  float64 `json:"score"`
}

// EntityMention is what an EntityDisambiguator sees: the extracted name and
// type, and the text it came from.
type EntityMention struct {
	Name    string
	Type    string
	Context string
}

// EntityChoice is a disambiguator's verdict: the chosen candidate's ID, or
// empty for "none of these — it is a new entity", with a confidence in [0, 1].
type EntityChoice struct {
	ID         string  `json:"id"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

// EntityDisambiguator is an optional LLM capability: given a mention and a
// short list of plausible stored entities, pick the one it refers to.
// Resolution asks it only when name similarity alone is inconclusive.
type EntityDisambiguator interface {
	DisambiguateEntity(ctx context.Context, mention EntityMention, candidates []Entity) (EntityChoice, error)
}

// GraphNode is one entity in a graph neighbourhood.
type GraphNode struct {
	ID, Type, Name string
	Center         bool
	Rels           int // relationships joining it to the centre
}

// GraphEdge is one live relationship between two nodes of a neighbourhood.
type GraphEdge struct {
	ID, SourceID, TargetID, Type string
}

// GraphFilter narrows a neighbourhood. Zero fields are ignored. The centre is
// always kept, whatever the filter.
type GraphFilter struct {
	Types []string
	// Since keeps only entities mentioned by a live memory created at or
	// after this time.
	Since *time.Time
	Limit int // total nodes including the centre; clamped to 1..500
}

// Neighbourhood is a centre plus its nearest neighbours.
type Neighbourhood struct {
	Nodes []GraphNode
	Edges []GraphEdge
	// Truncated is set when more neighbours matched than the cap allowed.
	Truncated bool
}

// EntityRelation is one live relationship of an entity, with the other end
// named. Outgoing means the entity is the source.
type EntityRelation struct {
	ID, Type  string
	Outgoing  bool
	OtherID   string
	OtherName string
	OtherType string
	CreatedAt time.Time
}

// EntityCard is everything WhoIs knows about one entity, without an LLM.
type EntityCard struct {
	Entity        Entity         `json:"entity"`
	Resolution    *Resolution    `json:"resolution,omitempty"`
	Aliases       []Alias        `json:"aliases"`
	Relationships []NamedEdge    `json:"relationships"`
	Memories      []Memory       `json:"memories"`
	Candidates    []ScoredEntity `json:"candidates,omitempty"`
}

// NamedEdge is a relationship with both ends' names, read source-type-target.
type NamedEdge struct {
	Relationship
	SourceName string `json:"source_name"`
	TargetName string `json:"target_name"`
}

// String renders the edge as "Source type Target".
func (e NamedEdge) String() string {
	s := e.SourceName + " " + e.Type + " " + e.TargetName
	if l, ok := e.Attributes["label"].(string); ok && l != "" && l != e.Type {
		s += " (" + l + ")"
	}
	return s
}

// TimelineEvent is one dated thing that happened to an entity.
type TimelineEvent struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"` // memory, relationship_started, relationship_ended
	Text string    `json:"text"`
	ID   string    `json:"id"`
}

// Changes is what WhatChanged found since a point in time.
type Changes struct {
	Since              time.Time   `json:"since"`
	NewEntities        []Entity    `json:"new_entities"`
	NewMemories        []Memory    `json:"new_memories"`
	RetiredMemories    []Memory    `json:"retired_memories"`
	NewRelationships   []NamedEdge `json:"new_relationships"`
	EndedRelationships []NamedEdge `json:"ended_relationships"`
}

// LinkRequest is an explicit relationship to record: ends by entity ID or by
// name/identifier, with the types to use if an end must be created.
type LinkRequest struct {
	Source, SourceType string
	Target, TargetType string
	Type               string
	Provenance         string
}

// NormalizeReport describes what NormalizeGraph changed (or, in a dry run,
// would change).
type NormalizeReport struct {
	DryRun bool `json:"dry_run"`
	// BatchID identifies the batch that RestoreBatch uses to undo the run.
	BatchID string `json:"batch_id,omitempty"`
	// Retyped lists entities whose type label moved onto the curated set.
	Retyped []RetypedEntity `json:"retyped"`
	// Merged lists entities folded into another of the same name and kind,
	// including duplicates of the owner.
	Merged []MergedEntity `json:"merged"`
	// RelationshipsRetyped counts edges whose verb moved onto the curated set
	// (possibly swapping ends); RelationshipsFolded counts edges absorbed into
	// an identical live edge.
	RelationshipsRetyped int `json:"relationships_retyped"`
	RelationshipsFolded  int `json:"relationships_folded"`
	// UnmappedVerbs counts verbs with no curated equivalent, now stored as
	// related_to with the original kept as the "label" attribute.
	UnmappedVerbs map[string]int `json:"unmapped_verbs,omitempty"`
}

// RetypedEntity is one entity whose type changed.
type RetypedEntity struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	From string `json:"from"`
	To   string `json:"to"`
}

// MergedEntity is one entity merged into another.
type MergedEntity struct {
	KeepID   string `json:"keep_id"`
	DropID   string `json:"drop_id"`
	Name     string `json:"name"`
	DropName string `json:"drop_name"`
	Type     string `json:"type"`
}

type NormalizeOption func(*NormalizeConfig)

type NormalizeConfig struct{ DryRun bool }

// WithNormalizeDryRun reports what NormalizeGraph would change without
// writing anything.
func WithNormalizeDryRun() NormalizeOption { return func(c *NormalizeConfig) { c.DryRun = true } }

// User is an account that owns a private graph.
type User struct {
	ID            string
	GoogleSub     string
	Email         string
	Name          string
	EmailVerified bool
	CreatedAt     time.Time
	DeletedAt     *time.Time
}

// Usage is a user's storage footprint.
type Usage struct {
	Bytes    int64 `json:"bytes"`
	Memories int   `json:"memories"`
	Entities int   `json:"entities"`
	Chunks   int   `json:"chunks"`
	Deleted  int   `json:"deleted"`
}

// ConnectionNew is a connection's memories created after a point in time.
type ConnectionNew struct {
	ConnectionID string // "" for memories no connection wrote
	Count        int
	Latest       []Memory // newest first, at most the requested number
}

// MemoryListFilter narrows ListMemories. Zero fields are ignored.
type MemoryListFilter struct {
	Query string
	// WrittenBy keeps memories written by this connection ID; NoConnection
	// keeps those no connection wrote (added directly).
	WrittenBy string
	// EntityID keeps memories linked to this entity (a foreign ID matches none).
	EntityID string
	From, To *time.Time // created_at in [From, To)
	// Since keeps memories created strictly after this instant. It composes
	// with From and To, and matches NewMemoriesSince.
	Since  *time.Time
	Oldest bool // oldest first; the default is newest first. Ties break on id.
}

// EmbedItem is one row that needs an embedding: the text to embed and the
// ref it belongs to. Intended for the re-embed job.
type EmbedItem struct {
	ID      string
	RefType string // "entity", "memory" or "chunk"
	Text    string
}

// DeleteFilter selects memories for SoftDeleteMemories. Set fields combine
// with AND; at least one must be set. All matches every live memory.
type DeleteFilter struct {
	IDs       []string
	WrittenBy string
	OlderThan *time.Time
	Query     string
	All       bool
}

// DeleteBatch describes one recoverable soft delete (or merge).
type DeleteBatch struct {
	ID            string    `json:"id"`
	Kind          string    `json:"kind"`
	CreatedAt     time.Time `json:"created_at"`
	Memories      int       `json:"memories"`
	Entities      int       `json:"entities"`
	Relationships int       `json:"relationships"`
	Chunks        int       `json:"chunks"`
}
