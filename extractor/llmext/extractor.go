package llmext

import (
	"context"

	"github.com/sausheong/cortex"
)

// extractionPrompt instructs the LLM to return structured extraction data.
const extractionPrompt = `Analyze the following text and extract structured knowledge.
Return a JSON object with the following fields:
- "entities": array of objects with "name", "type", and optional "attributes" (key-value pairs)
- "relationships": array of objects with "source_id", "target_id", "type", and optional "attributes"
- "memories": array of objects with "content" (a concise factual statement) and optional "entity_ids"

Extract all people, organizations, places, concepts, and other notable entities.
Identify relationships between entities (e.g., works_at, knows, located_in).
Create memories for key facts and statements.

Return ONLY valid JSON, no markdown formatting.`

// Extractor delegates entity extraction to an LLM.
type Extractor struct {
	llm cortex.LLM
}

// New creates a new LLM-backed Extractor.
func New(llm cortex.LLM) *Extractor {
	return &Extractor{llm: llm}
}

// Extract sends the content to the LLM with the extraction prompt and
// returns the parsed result. If the LLM returns nil Parsed data, an
// empty Extraction is returned.
func (e *Extractor) Extract(ctx context.Context, content string, contentType string) (*cortex.Extraction, error) {
	result, err := e.llm.Extract(ctx, content, extractionPrompt)
	if err != nil {
		return nil, err
	}

	if result.Parsed == nil {
		return &cortex.Extraction{}, nil
	}

	return result.Parsed, nil
}
