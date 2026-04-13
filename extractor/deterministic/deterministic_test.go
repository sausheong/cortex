package deterministic

import (
	"context"
	"testing"
)

func TestExtractFrontmatter(t *testing.T) {
	ext := New()
	content := `---
type: person
name: Alice
role: engineer
---
Some body text here.`

	result, err := ext.Extract(context.Background(), content, "text/markdown")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Entities) < 1 {
		t.Fatal("expected at least 1 entity from frontmatter")
	}

	e := result.Entities[0]
	if e.Type != "person" {
		t.Errorf("expected type 'person', got %q", e.Type)
	}
	if e.Name != "Alice" {
		t.Errorf("expected name 'Alice', got %q", e.Name)
	}
	if e.Source != "frontmatter" {
		t.Errorf("expected source 'frontmatter', got %q", e.Source)
	}
	if e.Attributes == nil {
		t.Fatal("expected attributes to be non-nil")
	}
	if e.Attributes["role"] != "engineer" {
		t.Errorf("expected attribute role='engineer', got %v", e.Attributes["role"])
	}
}

func TestExtractWikilinks(t *testing.T) {
	ext := New()
	content := `This document mentions [[Stripe]] and also [[Bob]].`

	result, err := ext.Extract(context.Background(), content, "text/markdown")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Entities) != 2 {
		t.Fatalf("expected 2 entities from wikilinks, got %d", len(result.Entities))
	}

	names := map[string]bool{}
	for _, e := range result.Entities {
		names[e.Name] = true
		if e.Type != "document" {
			t.Errorf("expected type 'document', got %q", e.Type)
		}
		if e.Source != "markdown" {
			t.Errorf("expected source 'markdown', got %q", e.Source)
		}
	}
	if !names["Stripe"] {
		t.Error("expected entity named 'Stripe'")
	}
	if !names["Bob"] {
		t.Error("expected entity named 'Bob'")
	}
}

func TestExtractNoMarkdownContent(t *testing.T) {
	ext := New()
	content := `This is plain text with no frontmatter or wikilinks.`

	result, err := ext.Extract(context.Background(), content, "text/plain")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.Entities) != 0 {
		t.Errorf("expected 0 entities, got %d", len(result.Entities))
	}
}
