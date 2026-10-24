package cortex

import (
	"github.com/sausheong/cortex/internal/model"
)

// Type aliases re-export the shared types so that cortex.Memory and friends
// remain the public names. They are aliases, not new types, so method sets
// and errors.As targets are identical.

type (
	Alias               = model.Alias
	Change              = model.Change
	Changes             = model.Changes
	Chunk               = model.Chunk
	ConflictPair        = model.ConflictPair
	ConnectionNew       = model.ConnectionNew
	DecayChange         = model.DecayChange
	DecayOption         = model.DecayOption
	DecayReport         = model.DecayReport
	DeleteBatch         = model.DeleteBatch
	DeleteFilter        = model.DeleteFilter
	DuplicatePair       = model.DuplicatePair
	EmbedItem           = model.EmbedItem
	EmbedPartialError   = model.EmbedPartialError
	Embedder            = model.Embedder
	Entity              = model.Entity
	EntityCard          = model.EntityCard
	EntityChoice        = model.EntityChoice
	EntityDisambiguator = model.EntityDisambiguator
	EntityFilter        = model.EntityFilter
	EntityMention       = model.EntityMention
	EntityRef           = model.EntityRef
	EntityRelation      = model.EntityRelation
	ErrEntityNameTaken  = model.ErrEntityNameTaken
	ExpireChange        = model.ExpireChange
	ExpireOption        = model.ExpireOption
	ExpireReport        = model.ExpireReport
	ExtractPartialError = model.ExtractPartialError
	Extraction          = model.Extraction
	ExtractionResult    = model.ExtractionResult
	Extractor           = model.Extractor
	Filter              = model.Filter
	Graph               = model.Graph
	GraphEdge           = model.GraphEdge
	GraphFilter         = model.GraphFilter
	GraphNode           = model.GraphNode
	LLM                 = model.LLM
	LinkRequest         = model.LinkRequest
	LintOption          = model.LintOption
	LintReport          = model.LintReport
	MaintainOption      = model.MaintainOption
	MaintainReport      = model.MaintainReport
	Memory              = model.Memory
	MemoryEdge          = model.MemoryEdge
	MemoryListFilter    = model.MemoryListFilter
	MemoryRef           = model.MemoryRef
	MemoryRelation      = model.MemoryRelation
	MergeStats          = model.MergeStats
	MergedEntity        = model.MergedEntity
	NamedEdge           = model.NamedEdge
	Neighbourhood       = model.Neighbourhood
	NormalizeOption     = model.NormalizeOption
	NormalizeReport     = model.NormalizeReport
	Option              = model.Option
	Profile             = model.Profile
	ProfileOption       = model.ProfileOption
	ProfileReport       = model.ProfileReport
	RecallOption        = model.RecallOption
	RecallResult        = model.RecallResult
	ReconcileOption     = model.ReconcileOption
	ReconcileReport     = model.ReconcileReport
	Reconciler          = model.Reconciler
	RejectedPair        = model.RejectedPair
	RejectedRelation    = model.RejectedRelation
	RelFilter           = model.RelFilter
	RelateOption        = model.RelateOption
	RelationDetector    = model.RelationDetector
	RelationProposal    = model.RelationProposal
	RelationReport      = model.RelationReport
	Relationship        = model.Relationship
	RememberOption      = model.RememberOption
	Resolution          = model.Resolution
	Result              = model.Result
	RetypedEntity       = model.RetypedEntity
	ScoredEntity        = model.ScoredEntity
	StructuredQuery     = model.StructuredQuery
	Supersession        = model.Supersession
	TimelineEvent       = model.TimelineEvent
	TraverseOption      = model.TraverseOption
	Usage               = model.Usage
	User                = model.User
)

// Constants and sentinel errors, re-declared from the model package with the
// same values.

const (
	AbstainRelevanceThreshold = model.AbstainRelevanceThreshold
	AbstainThreshold          = model.AbstainThreshold
	AbstainTopK               = model.AbstainTopK
	AliasEmail                = model.AliasEmail
	AliasHandle               = model.AliasHandle
	AliasName                 = model.AliasName
	AliasPhone                = model.AliasPhone
	BootstrapUserID           = model.BootstrapUserID
	CompactCombine            = model.CompactCombine
	CompactMerge              = model.CompactMerge
	CompactRollup             = model.CompactRollup
	DefaultMaxChunkChars      = model.DefaultMaxChunkChars
	EdgeDerives               = model.EdgeDerives
	EdgeExtends               = model.EdgeExtends
	EdgeSupersedes            = model.EdgeSupersedes
	NoConnection              = model.NoConnection
	ProfileDefaultRecentK     = model.ProfileDefaultRecentK
	ProfileDefaultStaticCap   = model.ProfileDefaultStaticCap
	ProfileDefaultTTL         = model.ProfileDefaultTTL
	ProfileDefaultWindow      = model.ProfileDefaultWindow
	RerankLambda              = model.RerankLambda
	ResolvedByAlias           = model.ResolvedByAlias
	ResolvedByExact           = model.ResolvedByExact
	ResolvedByFuzzy           = model.ResolvedByFuzzy
	ResolvedByLLM             = model.ResolvedByLLM
	ResolvedByOwner           = model.ResolvedByOwner
)

var (
	ErrAliasTaken      = model.ErrAliasTaken
	ErrNotFound        = model.ErrNotFound
	ErrStaleGeneration = model.ErrStaleGeneration
)
