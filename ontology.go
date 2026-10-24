package cortex

import "github.com/sausheong/cortex/internal/ontology"

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
