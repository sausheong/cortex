package cortex

import (
	"context"

	"github.com/sausheong/cortex/internal/memory"
)

// RecallWithStrength runs Recall and adds an aggregate strength score plus
// an abstention hint. Use this when the caller (e.g. an agent) needs to
// decide whether the knowledge graph actually knows the answer.
func (c *Cortex) RecallWithStrength(ctx context.Context, query string, opts ...RecallOption) (RecallResult, error) {
	return memory.New(c.h).RecallWithStrength(ctx, query, opts...)
}

// Recall searches the knowledge graph using multiple retrieval strategies,
// merges the results via reciprocal rank fusion, and returns a unified
// ranked list of Result items.
func (c *Cortex) Recall(ctx context.Context, query string, opts ...RecallOption) ([]Result, error) {
	return memory.New(c.h).Recall(ctx, query, opts...)
}
