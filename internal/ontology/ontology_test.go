package ontology

import (
	"strings"
	"testing"
)

func TestCanonicalEntityType(t *testing.T) {
	cases := []struct {
		in    string
		want  string
		known bool
	}{
		{"Person", TypePerson, true},
		{"company", TypeOrganization, true},
		{"WhatsApp Group", TypeGroup, true},
		{"email address", TypeAccount, true},
		{"countries", TypeConcept, false}, // "countrie" is not a synonym
		{"countrys", TypePlace, true},
		{"loyalty_programs", TypeProduct, true},
		{"spaceship", TypeConcept, false},
		{"AI coding product", TypeProduct, true},
		{"software project", TypeProject, true},
		{"researcher group", TypeGroup, true},
		{"Software Frameworks", TypeProduct, true},
		{"System", TypeProduct, true},
		{"title_suggestion", TypeDocument, true},
		{"cache directory", TypeConcept, false},
	}
	for _, c := range cases {
		got, known := CanonicalEntityType(c.in)
		if got != c.want || known != c.known {
			t.Errorf("CanonicalEntityType(%q) = %q,%v want %q,%v", c.in, got, known, c.want, c.known)
		}
	}
}

func TestCanonicalRelation(t *testing.T) {
	cases := []struct {
		verb, from, to string
		want           string
		swap           bool
		label          string
	}{
		{"works_at", TypePerson, TypeOrganization, RelWorksAt, false, ""},
		{"Employed By", TypePerson, TypeOrganization, RelWorksAt, false, "employed_by"},
		{"employs", TypeOrganization, TypePerson, RelWorksAt, true, "employs"},
		{"married_to", TypePerson, TypePerson, RelFamilyOf, false, "married_to"},
		{"requested_updated_permissions_on_account_of", TypeOrganization, TypePerson, RelRelatedTo, false, "requested_updated_permissions_on_account_of"},
		{"works_at", TypeOrganization, TypePerson, RelRelatedTo, false, "works_at"}, // ends don't fit
		{"lives_in", TypePerson, "spaceship", RelLivesIn, false, ""},                // uncurated end isn't held against it
	}
	for _, c := range cases {
		v, swap, label := CanonicalRelation(c.verb, c.from, c.to)
		if v != c.want || swap != c.swap || label != c.label {
			t.Errorf("CanonicalRelation(%q, %s, %s) = %q,%v,%q want %q,%v,%q", c.verb, c.from, c.to, v, swap, label, c.want, c.swap, c.label)
		}
	}
}

func TestVocabularyPromptListsEverything(t *testing.T) {
	p := VocabularyPrompt()
	for _, et := range EntityTypes {
		if !strings.Contains(p, "- "+et.Type+":") {
			t.Errorf("prompt missing entity type %s", et.Type)
		}
	}
	for _, r := range RelationshipVerbs {
		if !strings.Contains(p, "- "+r.Verb+":") {
			t.Errorf("prompt missing verb %s", r.Verb)
		}
	}
}
