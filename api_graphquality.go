package cortex

import (
	"context"

	"github.com/sausheong/cortex/internal/graph"
)

// NormalizeGraph brings an existing graph onto the curated vocabulary, in one
// transaction recorded as a single undoable batch:
//
//  1. every entity type moves onto the curated set (an unknown label becomes
//     concept, keeping the label in the "type_label" attribute);
//  2. live entities that now share a name (ignoring case) and type are
//     merged, as are person entities named like the owner, into the owner;
//  3. every relationship verb moves onto the curated set (CanonicalRelation),
//     folding any edge that becomes identical to a live one.
//
// RestoreBatch with the report's BatchID undoes the whole run.
func (c *Cortex) NormalizeGraph(ctx context.Context, opts ...NormalizeOption) (NormalizeReport, error) {
	return graph.New(c.h).NormalizeGraph(ctx, opts...)
}

// Lint scans the graph and returns a structured report of cleanup
// candidates. Pure read operation — never mutates the graph.
func (c *Cortex) Lint(ctx context.Context, opts ...LintOption) (LintReport, error) {
	return graph.New(c.h).Lint(ctx, opts...)
}

// RenderNormalizeMarkdown renders a NormalizeReport for the CLI.
func RenderNormalizeMarkdown(r NormalizeReport) string { return graph.RenderNormalizeMarkdown(r) }
