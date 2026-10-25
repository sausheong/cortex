package vaultart

import (
	"strings"
	"testing"
)

func TestSystemPromptHasNoMemoryContent(t *testing.T) {
	for _, kind := range []string{KindPerson, KindProject, KindTopic, "weird"} {
		for _, l := range []string{"short", "standard", "long", "x"} {
			a, b := SystemPrompt(kind, l), SystemPrompt(kind, l)
			if a != b {
				t.Fatal("prompt must be deterministic")
			}
			if strings.ContainsAny(a, "\u2014\u2013") {
				t.Fatalf("%s/%s has a dash", kind, l)
			}
			for _, want := range []string{"British English", "you met Kabir", "possibly", "Linkable", "never"} {
				if want == "Linkable" {
					want = "linkable"
				}
				if !strings.Contains(a, want) && !strings.Contains(strings.ToLower(a), strings.ToLower(want)) {
					t.Fatalf("%s/%s missing %q", kind, l, want)
				}
			}
		}
	}
	if !strings.Contains(SystemPrompt(KindPerson, ""), "How you know them") ||
		!strings.Contains(SystemPrompt(KindProject, ""), "Decisions") ||
		!strings.Contains(SystemPrompt(KindTopic, ""), "Your view") {
		t.Fatal("per kind sections missing")
	}
	if strings.Contains(SystemPrompt(KindPerson, ""), "Your view") {
		t.Fatal("sections leaked across kinds")
	}
}
