package hybrid

import (
	"context"

	"github.com/sausheong/cortex"
)

// Extractor composes two extractors (typically deterministic + LLM) and
// merges their results into a single Extraction.
type Extractor struct {
	deterministic cortex.Extractor
	llm           cortex.Extractor
}

// New creates a hybrid Extractor that runs deterministic first, then llm,
// and merges the results.
func New(deterministic, llm cortex.Extractor) *Extractor {
	return &Extractor{
		deterministic: deterministic,
		llm:           llm,
	}
}

// Extract runs both extractors and appends entities, relationships, and
// memories from each into a single Extraction.
func (e *Extractor) Extract(ctx context.Context, content string, contentType string) (*cortex.Extraction, error) {
	detResult, err := e.deterministic.Extract(ctx, content, contentType)
	if err != nil {
		return nil, err
	}

	llmResult, err := e.llm.Extract(ctx, content, contentType)
	if err != nil {
		return nil, err
	}

	merged := &cortex.Extraction{}
	merged.Entities = append(merged.Entities, detResult.Entities...)
	merged.Entities = append(merged.Entities, llmResult.Entities...)
	merged.Relationships = append(merged.Relationships, detResult.Relationships...)
	merged.Relationships = append(merged.Relationships, llmResult.Relationships...)
	merged.Memories = append(merged.Memories, detResult.Memories...)
	merged.Memories = append(merged.Memories, llmResult.Memories...)

	return merged, nil
}
