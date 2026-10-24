package cortex

import (
	"fmt"
	"sort"
	"strings"
)

// The curated entity types. Extractors are asked to use these; anything else
// is mapped through entityTypeSynonyms or falls back to TypeConcept, with the
// original label kept in the entity's "type_label" attribute.
const (
	TypePerson       = "person"
	TypeOrganization = "organization"
	TypePlace        = "place"
	TypeProject      = "project"
	TypeProduct      = "product"
	TypeEvent        = "event"
	TypeGroup        = "group"
	TypeDocument     = "document"
	TypeAccount      = "account"
	TypeConcept      = "concept"
)

// EntityTypes lists the curated entity types with a one-line gloss each, in
// the order they are shown to extractors.
var EntityTypes = []struct{ Type, Gloss string }{
	{TypePerson, "an individual human"},
	{TypeOrganization, "a company, institution, agency, school, bank or merchant"},
	{TypePlace, "a country, city, region, address, venue or building"},
	{TypeProject, "a named piece of work, initiative or programme"},
	{TypeProduct, "software, an app, platform, model, service, device, card, plan or other offering"},
	{TypeEvent, "a meeting, conference, incident, holiday, appointment or dated occurrence"},
	{TypeGroup, "a team, community, chat group, channel or committee"},
	{TypeDocument, "a document, page, email, article, publication, podcast, video, file or URL"},
	{TypeAccount, "an account, email address, phone number, username or other identifier"},
	{TypeConcept, "a topic, idea, skill, role, field or anything else"},
}

// entityTypeSynonyms maps normalised labels extractors tend to invent onto
// the curated types.
var entityTypeSynonyms = map[string]string{
	"human": TypePerson, "individual": TypePerson, "people": TypePerson, "contact": TypePerson,
	"company": TypeOrganization, "org": TypeOrganization, "business": TypeOrganization, "institution": TypeOrganization,
	"agency": TypeOrganization, "government_agency": TypeOrganization, "bank": TypeOrganization, "merchant": TypeOrganization,
	"school": TypeOrganization, "university": TypeOrganization, "employer": TypeOrganization, "startup": TypeOrganization,
	"nonprofit": TypeOrganization, "brand": TypeOrganization, "vendor": TypeOrganization, "organisation": TypeOrganization,
	"location": TypePlace, "city": TypePlace, "country": TypePlace, "region": TypePlace, "address": TypePlace,
	"venue": TypePlace, "building": TypePlace, "state": TypePlace, "geo": TypePlace,
	"initiative": TypeProject, "programme": TypeProject, "program": TypeProject, "workstream": TypeProject,
	"research": TypeProject, "study": TypeProject,
	"software": TypeProduct, "application": TypeProduct, "app": TypeProduct, "platform": TypeProduct, "tool": TypeProduct,
	"technology": TypeProduct, "service": TypeProduct, "model": TypeProduct, "feature": TypeProduct, "device": TypeProduct,
	"subscription_plan": TypeProduct, "plan": TypeProduct, "financial_product": TypeProduct, "payment_card": TypeProduct,
	"card": TypeProduct, "loyalty_program": TypeProduct, "fund": TypeProduct, "library": TypeProduct, "framework": TypeProduct,
	"system": TypeProduct, "toolkit": TypeProduct, "operating_system": TypeProduct, "hardware": TypeProduct,
	"virtual_machine": TypeProduct, "runtime": TypeProduct, "agent": TypeProduct, "bot": TypeProduct,
	"assistant": TypeProduct, "api": TypeProduct, "integration": TypeProduct, "component": TypeProduct,
	"meeting": TypeEvent, "conference": TypeEvent, "incident": TypeEvent, "holiday": TypeEvent, "appointment": TypeEvent,
	"date": TypeEvent, "deadline": TypeEvent, "trip": TypeEvent, "financial_transaction": TypeEvent, "transaction": TypeEvent,
	"team": TypeGroup, "group_chat": TypeGroup, "whatsapp_group": TypeGroup, "chat": TypeGroup, "chat_group": TypeGroup,
	"community": TypeGroup, "channel": TypeGroup, "committee": TypeGroup, "family": TypeGroup, "department": TypeGroup,
	"file": TypeDocument, "page": TypeDocument, "email": TypeDocument, "article": TypeDocument, "publication": TypeDocument,
	"podcast": TypeDocument, "video": TypeDocument, "report": TypeDocument, "book": TypeDocument, "url": TypeDocument,
	"resource": TypeDocument, "source": TypeDocument, "paper": TypeDocument, "post": TypeDocument, "message": TypeDocument,
	"title": TypeDocument, "title_suggestion": TypeDocument, "creative_work": TypeDocument, "written_work": TypeDocument,
	"column": TypeDocument, "draft": TypeDocument, "newsletter": TypeDocument, "presentation": TypeDocument,
	"email_address": TypeAccount, "identifier": TypeAccount, "username": TypeAccount, "phone_number": TypeAccount,
	"handle": TypeAccount, "user_account": TypeAccount, "education_credential": TypeAccount,
	"topic": TypeConcept, "idea": TypeConcept, "skill": TypeConcept, "role": TypeConcept, "field": TypeConcept,
	"status": TypeConcept, "term": TypeConcept, "other": TypeConcept, "thing": TypeConcept, "misc": TypeConcept,
}

// CanonicalEntityType maps a type label onto the curated set. known reports
// whether the label was curated or a known synonym; when it is false the
// result is TypeConcept and the caller should keep the original label.
func CanonicalEntityType(label string) (canonical string, known bool) {
	t := NormalizeType(label)
	for _, et := range EntityTypes {
		if et.Type == t {
			return t, true
		}
	}
	if c, ok := entityTypeSynonyms[t]; ok {
		return c, true
	}
	if c, ok := lookupEntityType(strings.TrimSuffix(t, "s")); ok {
		return c, true
	}
	// A compound label is usually a kind of its last word: "ai coding
	// product", "software project", "researcher group".
	if i := strings.LastIndexByte(t, '_'); i >= 0 {
		head := t[i+1:]
		if c, ok := lookupEntityType(head); ok {
			return c, true
		}
		if c, ok := lookupEntityType(strings.TrimSuffix(head, "s")); ok {
			return c, true
		}
	}
	return TypeConcept, false
}

// lookupEntityType matches a normalised label against the curated types and
// their synonyms.
func lookupEntityType(t string) (string, bool) {
	for _, et := range EntityTypes {
		if et.Type == t {
			return t, true
		}
	}
	c, ok := entityTypeSynonyms[t]
	return c, ok
}

// The curated relationship verbs. Each reads source-verb-target.
const (
	RelWorksAt          = "works_at"
	RelMemberOf         = "member_of"
	RelLeads            = "leads"
	RelFounded          = "founded"
	RelReportsTo        = "reports_to"
	RelKnows            = "knows"
	RelFamilyOf         = "family_of"
	RelCommunicatedWith = "communicated_with"
	RelLivesIn          = "lives_in"
	RelLocatedIn        = "located_in"
	RelPartOf           = "part_of"
	RelOwns             = "owns"
	RelUses             = "uses"
	RelCreated          = "created"
	RelParticipatedIn   = "participated_in"
	RelPartneredWith    = "partnered_with"
	RelAcquired         = "acquired"
	RelInvestedIn       = "invested_in"
	RelPaid             = "paid"
	RelAbout            = "about"
	RelRelatedTo        = "related_to"
)

// relSpec describes a curated verb: what it means and which entity types may
// sit at each end (nil allows any type).
type relSpec struct {
	Verb   string
	Gloss  string
	From   []string
	To     []string
	single bool // at most one current target per source (see step 5)
}

var (
	anyType  []string
	agents   = []string{TypePerson, TypeOrganization, TypeGroup}
	people   = []string{TypePerson}
	orgish   = []string{TypeOrganization, TypeGroup, TypeProject}
	creative = []string{TypeDocument, TypeProduct, TypeProject, TypeEvent, TypeConcept}
)

// RelationshipVerbs lists the curated relationship verbs in the order they
// are shown to extractors.
var RelationshipVerbs = []relSpec{
	{RelWorksAt, "is employed by or works for", people, []string{TypeOrganization}, false},
	{RelMemberOf, "belongs to a group, team, organisation or project", agents, orgish, false},
	{RelLeads, "heads, manages or runs", people, orgish, false},
	{RelFounded, "founded or co-founded", agents, []string{TypeOrganization, TypeProject, TypeProduct, TypeGroup}, false},
	{RelReportsTo, "has as their manager", people, people, true},
	{RelKnows, "knows, is a friend, colleague or contact of", people, people, false},
	{RelFamilyOf, "is a relative or partner of (label says which)", people, people, false},
	{RelCommunicatedWith, "messaged, emailed, called, met or invited", agents, agents, false},
	{RelLivesIn, "lives in", people, []string{TypePlace}, true},
	{RelLocatedIn, "is based or situated in", anyType, []string{TypePlace}, false},
	{RelPartOf, "is a part, subsidiary or component of", anyType, anyType, false},
	{RelOwns, "owns or holds", agents, anyType, false},
	{RelUses, "uses, runs on, subscribes to or has signed up for", anyType, []string{TypeProduct, TypeAccount, TypeOrganization}, false},
	{RelCreated, "created, wrote, built or published", agents, creative, false},
	{RelParticipatedIn, "took part in, attended, works on or contributes to", agents, []string{TypeEvent, TypeProject, TypeGroup}, false},
	{RelPartneredWith, "partners or collaborates with", agents, agents, false},
	{RelAcquired, "bought or acquired", agents, anyType, false},
	{RelInvestedIn, "invested in or funded", agents, anyType, false},
	{RelPaid, "paid, sent money to or spent at", agents, agents, false},
	{RelAbout, "is about, discusses or mentions", []string{TypeDocument, TypeEvent, TypeGroup}, anyType, false},
	{RelRelatedTo, "is related in another way (label says how)", anyType, anyType, false},
}

// relSynonyms maps normalised verbs extractors tend to invent onto the
// curated verbs. inverse means the synonym reads target-verb-source, so
// source and target are swapped ("employs" -> works_at reversed).
var relSynonyms = map[string]struct {
	verb    string
	inverse bool
}{
	"employed_by": {RelWorksAt, false}, "employee_of": {RelWorksAt, false}, "works_for": {RelWorksAt, false},
	"employed_at": {RelWorksAt, false}, "works_in": {RelWorksAt, false}, "affiliated_with": {RelMemberOf, false},
	"employs": {RelWorksAt, true}, "has_employee": {RelWorksAt, true},
	"belongs_to": {RelMemberOf, false}, "part_of_team": {RelMemberOf, false}, "in_group": {RelMemberOf, false},
	"joined": {RelMemberOf, false}, "member": {RelMemberOf, false}, "has_member": {RelMemberOf, true}, "includes_member": {RelMemberOf, true},
	"manages": {RelLeads, false}, "heads": {RelLeads, false}, "runs": {RelLeads, false}, "ceo_of": {RelLeads, false},
	"director_of": {RelLeads, false}, "managed_by": {RelLeads, true}, "led_by": {RelLeads, true},
	"founder_of": {RelFounded, false}, "co_founded": {RelFounded, false}, "cofounded": {RelFounded, false},
	"started": {RelFounded, false}, "founded_by": {RelFounded, true},
	"reports_to_manager": {RelReportsTo, false}, "manager_is": {RelReportsTo, false},
	"friend_of": {RelKnows, false}, "colleague_of": {RelKnows, false}, "connected_to": {RelKnows, false},
	"acquainted_with": {RelKnows, false}, "friends_with": {RelKnows, false}, "works_with": {RelKnows, false},
	"married_to": {RelFamilyOf, false}, "spouse_of": {RelFamilyOf, false}, "parent_of": {RelFamilyOf, false},
	"child_of": {RelFamilyOf, false}, "sibling_of": {RelFamilyOf, false}, "partner_of": {RelFamilyOf, false},
	"sent_message_to": {RelCommunicatedWith, false}, "messaged": {RelCommunicatedWith, false},
	"emailed": {RelCommunicatedWith, false}, "contacted": {RelCommunicatedWith, false}, "called": {RelCommunicatedWith, false},
	"met_with": {RelCommunicatedWith, false}, "met": {RelCommunicatedWith, false}, "wrote_to": {RelCommunicatedWith, false},
	"replied_to": {RelCommunicatedWith, false}, "sent_connection_invitation_to": {RelCommunicatedWith, false},
	"invited": {RelCommunicatedWith, false}, "communicates_with": {RelCommunicatedWith, false},
	"sent_notice_about": {RelAbout, false}, "received_by": {RelCommunicatedWith, true}, "received_from": {RelCommunicatedWith, true},
	"resides_in": {RelLivesIn, false}, "lives_at": {RelLivesIn, false}, "moved_to": {RelLivesIn, false},
	"based_in": {RelLocatedIn, false}, "headquartered_in": {RelLocatedIn, false}, "situated_in": {RelLocatedIn, false},
	"in": {RelLocatedIn, false}, "located_at": {RelLocatedIn, false},
	"subsidiary_of": {RelPartOf, false}, "division_of": {RelPartOf, false}, "component_of": {RelPartOf, false},
	"has_part": {RelPartOf, true}, "contains": {RelPartOf, true}, "includes": {RelPartOf, true},
	"owner_of": {RelOwns, false}, "has_account": {RelOwns, false}, "holds": {RelOwns, false},
	"owned_by": {RelOwns, true}, "account_of": {RelOwns, true},
	"uses_technology": {RelUses, false}, "runs_on": {RelUses, false}, "built_with": {RelUses, false},
	"subscribed_to": {RelUses, false}, "signed_up_for": {RelUses, false}, "uses_account_of": {RelUses, false},
	"runs_services_on": {RelUses, false}, "used_by": {RelUses, true}, "subscribed_by": {RelUses, true},
	"used": {RelUses, false}, "tried_and_liked": {RelUses, false},
	"authored": {RelCreated, false}, "wrote": {RelCreated, false}, "developed": {RelCreated, false},
	"built": {RelCreated, false}, "published": {RelCreated, false}, "made": {RelCreated, false},
	"designed":   {RelCreated, false},
	"created_by": {RelCreated, true}, "authored_by": {RelCreated, true}, "written_by": {RelCreated, true},
	"developed_by": {RelCreated, true}, "published_by": {RelCreated, true},
	"attended": {RelParticipatedIn, false}, "spoke_at": {RelParticipatedIn, false}, "works_on": {RelParticipatedIn, false},
	"contributes_to": {RelParticipatedIn, false}, "involved_in": {RelParticipatedIn, false}, "participates_in": {RelParticipatedIn, false},
	"organized": {RelParticipatedIn, false}, "hosted": {RelParticipatedIn, false},
	"partners_with": {RelPartneredWith, false}, "collaborates_with": {RelPartneredWith, false}, "allied_with": {RelPartneredWith, false},
	"bought": {RelAcquired, false}, "purchased": {RelAcquired, false}, "acquired_by": {RelAcquired, true},
	"funded": {RelInvestedIn, false}, "invests_in": {RelInvestedIn, false}, "backed": {RelInvestedIn, false},
	"funded_by":     {RelInvestedIn, true},
	"sent_money_to": {RelPaid, false}, "transferred_funds_to": {RelPaid, false}, "spent_at": {RelPaid, false},
	"paid_to": {RelPaid, false}, "used_card": {RelUses, false}, "paid_by": {RelPaid, true},
	"discusses": {RelAbout, false}, "mentions": {RelAbout, false}, "concerns": {RelAbout, false},
	"related_to_topic": {RelAbout, false}, "covers": {RelAbout, false}, "describes": {RelAbout, false},
	"associated_with": {RelRelatedTo, false}, "linked_to": {RelRelatedTo, false}, "relates_to": {RelRelatedTo, false},
	"covers_topic": {RelAbout, false}, "references": {RelAbout, false},
	"refers_to": {RelAbout, false}, "cites": {RelAbout, false}, "summarizes": {RelAbout, false},
	"develops": {RelCreated, false}, "maintains": {RelCreated, false}, "builds": {RelCreated, false},
	"sent_email_to": {RelCommunicatedWith, false}, "meets_with": {RelCommunicatedWith, false},
	"emails": {RelCommunicatedWith, false}, "messages": {RelCommunicatedWith, false}, "spoke_with": {RelCommunicatedWith, false},
	"holds_stake_in": {RelInvestedIn, false}, "shareholder_of": {RelInvestedIn, false},
	"partner_agency": {RelPartneredWith, false}, "partner_of_org": {RelPartneredWith, false},
	"invited_to": {RelParticipatedIn, false}, "registered_for": {RelParticipatedIn, false},
	"involves": {RelParticipatedIn, true}, "involves_agency": {RelParticipatedIn, true}, "features": {RelParticipatedIn, true},
	"integrates_with": {RelUses, false}, "depends_on": {RelUses, false}, "relies_on": {RelUses, false},
}

// relSpecFor returns the curated spec for verb, if it is one.
func relSpecFor(verb string) (relSpec, bool) {
	for _, r := range RelationshipVerbs {
		if r.Verb == verb {
			return r, true
		}
	}
	return relSpec{}, false
}

// CanonicalRelation maps a relationship label onto the curated verbs, given
// the (curated) types at each end. It returns the verb to store, whether the
// ends must be swapped, and the original label when information would be
// lost (an unknown verb becomes related_to; a known verb whose end types do
// not fit its spec becomes related_to too). label is empty when the verb is
// curated as given.
func CanonicalRelation(rawVerb, fromType, toType string) (verb string, swap bool, label string) {
	v := NormalizeType(rawVerb)
	verb, swap = v, false
	if _, ok := relSpecFor(v); !ok {
		syn, ok := relSynonyms[v]
		if !ok {
			return RelRelatedTo, false, v
		}
		verb, swap = syn.verb, syn.inverse
		label = v
	}
	from, to := fromType, toType
	if swap {
		from, to = to, from
	}
	spec, _ := relSpecFor(verb)
	if !typeFits(spec.From, from) || !typeFits(spec.To, to) {
		return RelRelatedTo, false, v
	}
	if verb == RelFamilyOf || verb == RelRelatedTo {
		// These verbs lean on the label for meaning; keep it even when curated.
		if label == "" && v != verb {
			label = v
		}
	}
	return verb, swap, label
}

// typeFits reports whether t satisfies allowed (nil allows anything; an
// uncurated t, or concept, is not held against the spec).
func typeFits(allowed []string, t string) bool {
	if allowed == nil {
		return true
	}
	c, known := CanonicalEntityType(t)
	if !known || c == TypeConcept {
		return true // concept is the catch-all; it cannot veto a verb
	}
	for _, a := range allowed {
		if a == c {
			return true
		}
	}
	return false
}

// VocabularyPrompt renders the curated entity types and relationship verbs
// as instructions for an extraction prompt.
func VocabularyPrompt() string {
	var b strings.Builder
	b.WriteString("Use exactly one of these entity types:\n")
	for _, et := range EntityTypes {
		fmt.Fprintf(&b, "- %s: %s\n", et.Type, et.Gloss)
	}
	b.WriteString("\nUse exactly one of these relationship types, reading source-type-target; put any finer detail (e.g. \"married_to\", \"CTO\") in the relationship's \"attributes\" as \"label\":\n")
	for _, r := range RelationshipVerbs {
		fmt.Fprintf(&b, "- %s: %s\n", r.Verb, r.Gloss)
	}
	b.WriteString("\nPut identifiers you see for an entity (email, phone, handle) in its \"attributes\" under those keys rather than creating separate entities for them.\n")
	return b.String()
}

// singleValuedVerbs are curated verbs where a source has at most one current
// target, so asserting a new target closes the previous edge.
func singleValuedVerbs() []string {
	var out []string
	for _, r := range RelationshipVerbs {
		if r.single {
			out = append(out, r.Verb)
		}
	}
	sort.Strings(out)
	return out
}
