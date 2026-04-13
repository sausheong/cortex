package deterministic

import (
	"context"
	"regexp"
	"strings"

	"github.com/sausheong/cortex"
)

// wikilinkRe matches [[...]] patterns.
var wikilinkRe = regexp.MustCompile(`\[\[([^\]]+)\]\]`)

// Extractor extracts entities from content using deterministic rules
// (regex and simple parsing) without requiring an LLM.
type Extractor struct{}

// New returns a new deterministic Extractor.
func New() *Extractor {
	return &Extractor{}
}

// Extract parses content for YAML frontmatter entities and wikilink entities.
func (e *Extractor) Extract(_ context.Context, content string, contentType string) (*cortex.Extraction, error) {
	extraction := &cortex.Extraction{}

	// Extract from YAML frontmatter.
	if entity, ok := parseFrontmatter(content); ok {
		extraction.Entities = append(extraction.Entities, entity)
	}

	// Extract wikilinks.
	extraction.Entities = append(extraction.Entities, parseWikilinks(content)...)

	return extraction, nil
}

// parseFrontmatter extracts an entity from YAML-like frontmatter delimited
// by --- lines. It looks for name: and type: fields and stores everything
// else in Attributes.
func parseFrontmatter(content string) (cortex.Entity, bool) {
	if !strings.HasPrefix(strings.TrimSpace(content), "---") {
		return cortex.Entity{}, false
	}

	// Split on --- delimiters. The content between the first and second
	// --- markers is the frontmatter block.
	parts := strings.SplitN(content, "---", 3)
	if len(parts) < 3 {
		return cortex.Entity{}, false
	}

	block := strings.TrimSpace(parts[1])
	if block == "" {
		return cortex.Entity{}, false
	}

	entity := cortex.Entity{
		Source:     "frontmatter",
		Attributes: make(map[string]any),
	}

	lines := strings.Split(block, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])

		switch key {
		case "name":
			entity.Name = value
		case "type":
			entity.Type = value
		default:
			entity.Attributes[key] = value
		}
	}

	// Only return an entity if we got at least a name or type.
	if entity.Name == "" && entity.Type == "" {
		return cortex.Entity{}, false
	}

	return entity, true
}

// parseWikilinks finds all [[...]] patterns in content and returns a unique
// entity for each.
func parseWikilinks(content string) []cortex.Entity {
	matches := wikilinkRe.FindAllStringSubmatch(content, -1)
	seen := make(map[string]bool)
	var entities []cortex.Entity

	for _, m := range matches {
		name := strings.TrimSpace(m[1])
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		entities = append(entities, cortex.Entity{
			Type:   "document",
			Name:   name,
			Source: "markdown",
		})
	}

	return entities
}
