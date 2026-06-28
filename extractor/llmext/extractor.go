package llmext

import (
	"context"

	"github.com/sausheong/cortex"
)

// extractionPrompt instructs the LLM to return structured extraction data.
const extractionPrompt = `Analyze the following text and extract structured knowledge.
Return a JSON object with the following fields:
- "entities": array of objects with "name", "type", optional "attributes", and "confidence"
- "relationships": array of objects with "source" (entity name), "target" (entity name), "type", optional "attributes", and "confidence"
- "memories": array of objects with "content" (a concise factual statement), optional "entity_ids", optional "static" (boolean), and "confidence"

For each item, "confidence" is a float between 0.0 and 1.0 expressing how
certain you are about that specific extracted claim:
- 1.0  = directly stated in the text, unambiguous
- 0.7  = strongly implied or paraphrased
- 0.4  = inferred or interpretive
- 0.2  = speculative or weakly supported

Be honest. It is better to mark something low-confidence than to omit it
or to claim certainty you don't have.

For each memory, "static" marks whether it is a stable fact:
- true  = identity facts and durable preferences (role, hometown,
          "prefers dark mode", relationships, long-term traits)
- false = episodic or time-bound facts (meetings, "learning X this week",
          "exam tomorrow", anything with a date or that will expire)
When unsure, use false.

- "forget_after": string (date) — set ONLY for memories that stop being relevant after a known point: appointments, deadlines, "today"/"tomorrow"/"this week", anything time-bound. Resolve relative phrases against today's date and return an absolute date (e.g. today is 2026-06-27, "tomorrow" -> "2026-06-28"). Omit for durable facts. A memory with forget_after should NOT be marked static.

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
