package graph

import (
	"testing"

	"github.com/sausheong/cortex/internal/ontology"
)

func TestNormalizeType(t *testing.T) {
	cases := map[string]string{
		"Person":         "person",
		" person ":       "person",
		"PERSON":         "person",
		"Works At":       "works_at",
		"works-at":       "works_at",
		"works__at":      "works_at",
		"email address":  "email_address",
		"_group_chat_":   "group_chat",
		"":               "",
		"located_in":     "located_in",
		"WhatsApp Group": "whatsapp_group",
	}
	for in, want := range cases {
		if got := ontology.NormalizeType(in); got != want {
			t.Errorf("ontology.NormalizeType(%q) = %q, want %q", in, got, want)
		}
	}
}
