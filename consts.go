package cortex

import "github.com/sausheong/cortex/internal/model"

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
