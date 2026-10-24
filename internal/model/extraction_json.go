package model

import (
	"encoding/json"
	"strings"
)

// extractionJSON is the wire shape an LLM extractor returns.
type extractionJSON struct {
	Entities []struct {
		Type       string         `json:"type"`
		Name       string         `json:"name"`
		Attributes map[string]any `json:"attributes"`
		Confidence float64        `json:"confidence"`
	} `json:"entities"`
	Relationships []struct {
		Source     string         `json:"source"`
		Target     string         `json:"target"`
		Type       string         `json:"type"`
		Attributes map[string]any `json:"attributes"`
		Confidence float64        `json:"confidence"`
	} `json:"relationships"`
	Memories []json.RawMessage `json:"memories"`
}

// ParseExtractionJSON parses an LLM's extraction output into an Extraction.
// Markdown code fences are stripped first, since some upstreams wrap JSON in
// them despite a JSON-only prompt. Relationship sources and targets are
// entity names (Remember resolves them to IDs); each memory's "about" names
// the entities it concerns. A memory given as a bare string is accepted.
func ParseExtractionJSON(raw string) (*Extraction, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		if len(lines) > 2 {
			lines = lines[1 : len(lines)-1]
			raw = strings.Join(lines, "\n")
		}
	}

	var ej extractionJSON
	if err := json.Unmarshal([]byte(raw), &ej); err != nil {
		return nil, err
	}

	extraction := &Extraction{}
	for _, e := range ej.Entities {
		extraction.Entities = append(extraction.Entities, Entity{
			Type:       e.Type,
			Name:       e.Name,
			Attributes: e.Attributes,
			Confidence: e.Confidence,
		})
	}
	for _, r := range ej.Relationships {
		extraction.Relationships = append(extraction.Relationships, Relationship{
			SourceID:   r.Source,
			TargetID:   r.Target,
			Type:       r.Type,
			Attributes: r.Attributes,
			Confidence: r.Confidence,
		})
	}
	for _, m := range ej.Memories {
		var memObj struct {
			Content     string   `json:"content"`
			About       []string `json:"about"`
			Confidence  float64  `json:"confidence"`
			ValidAt     string   `json:"valid_at"`
			Static      bool     `json:"static"`
			ForgetAfter string   `json:"forget_after"`
		}
		if err := json.Unmarshal(m, &memObj); err == nil && memObj.Content != "" {
			extraction.Memories = append(extraction.Memories, Memory{
				Content:     memObj.Content,
				About:       memObj.About,
				Confidence:  memObj.Confidence,
				ValidAt:     ParseEventTime(memObj.ValidAt),
				Static:      memObj.Static,
				ForgetAfter: ParseEventTime(memObj.ForgetAfter),
			})
			continue
		}
		var memStr string
		if err := json.Unmarshal(m, &memStr); err == nil && memStr != "" {
			extraction.Memories = append(extraction.Memories, Memory{Content: memStr})
		}
	}
	return extraction, nil
}
