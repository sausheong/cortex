package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/sausheong/cortex"
)

// RegisterTools attaches the cortex tool set to an MCP server. Exposed so
// the cortex CLI can wire it up before serving on stdio or HTTP.
func RegisterTools(s *server.MCPServer, cx *cortex.Cortex) {
	// --- remember ---
	s.AddTool(
		mcp.NewTool("remember",
			mcp.WithDescription("Save a fact to the user's personal knowledge graph (cortex). Call this whenever the user shares information about themselves, a person, a project, a decision, or a preference that would be useful in a future conversation. Entities and relationships are extracted automatically."),
			mcp.WithString("content", mcp.Required(), mcp.Description("The text content to remember")),
			mcp.WithString("source", mcp.Description("Optional source label for the content")),
			mcp.WithString("speaker", mcp.Description("Who asserted this (e.g. \"user\", \"assistant\", a document name). Optional provenance label.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			content, err := req.RequireString("content")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			var opts []cortex.RememberOption
			if src := req.GetString("source", ""); src != "" {
				opts = append(opts, cortex.WithSource(src))
			}
			if spk := req.GetString("speaker", ""); spk != "" {
				opts = append(opts, cortex.WithSpeaker(spk))
			}
			if err := cx.Remember(ctx, content, opts...); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText("remembered"), nil
		},
	)

	// --- recall ---
	s.AddTool(
		mcp.NewTool("recall",
			mcp.WithDescription("Search the user's personal knowledge graph (cortex). This contains everything the user has told the assistant to remember about themselves, their work, the people they know, and their projects. USE FIRST for any question about who/what/why — before web search, before any other tool. The web does not know the user's personal context; this does. The response includes an \"abstain\" flag: when true, any matches are too weak or low-confidence to rely on — tell the user you don't have reliable information rather than guessing."),
			mcp.WithString("query", mcp.Required(), mcp.Description("The query to search for")),
			mcp.WithNumber("limit", mcp.Description("Maximum number of results (default 20)")),
			mcp.WithNumber("min_confidence", mcp.Description("Filter results below this confidence threshold (0.0-1.0). Default 0 = no filter.")),
			mcp.WithString("as_of", mcp.Description("Optional RFC3339 timestamp; recall the graph as it was valid at this time (point-in-time history).")),
			mcp.WithBoolean("include_invalid", mcp.Description("Include retired/superseded memories that are no longer current. Default false.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			query, err := req.RequireString("query")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			var opts []cortex.RecallOption
			if limit := req.GetInt("limit", 0); limit > 0 {
				opts = append(opts, cortex.WithLimit(limit))
			}
			if mc := req.GetFloat("min_confidence", 0); mc > 0 {
				if mc > 1 {
					return mcp.NewToolResultError("min_confidence must be between 0 and 1"), nil
				}
				opts = append(opts, cortex.WithMinConfidence(mc))
			}
			if asOfStr := req.GetString("as_of", ""); asOfStr != "" {
				ts, err := time.Parse(time.RFC3339, asOfStr)
				if err != nil {
					return mcp.NewToolResultError("as_of must be an RFC3339 timestamp"), nil
				}
				opts = append(opts, cortex.WithAsOf(ts))
			}
			if req.GetBool("include_invalid", false) {
				opts = append(opts, cortex.WithIncludeInvalid())
			}
			out, err := cx.RecallWithStrength(ctx, query, opts...)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(out)
		},
	)

	// --- forget ---
	s.AddTool(
		mcp.NewTool("forget",
			mcp.WithDescription("Remove knowledge from the graph by entity ID or source."),
			mcp.WithString("entity_id", mcp.Description("Entity ID to forget")),
			mcp.WithString("source", mcp.Description("Source label to forget")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			entityID := req.GetString("entity_id", "")
			source := req.GetString("source", "")
			if entityID == "" && source == "" {
				return mcp.NewToolResultError("either entity_id or source is required"), nil
			}
			filter := cortex.Filter{EntityID: entityID, Source: source}
			if err := cx.Forget(ctx, filter); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText("forgotten"), nil
		},
	)

	// --- get_entity ---
	s.AddTool(
		mcp.NewTool("get_entity",
			mcp.WithDescription("Retrieve an entity by its ID."),
			mcp.WithString("id", mcp.Required(), mcp.Description("The entity ID")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id, err := req.RequireString("id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			entity, err := cx.GetEntity(ctx, id)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(entity)
		},
	)

	// --- find_entities ---
	s.AddTool(
		mcp.NewTool("find_entities",
			mcp.WithDescription("Find entities matching optional filters (type, name, source)."),
			mcp.WithString("type", mcp.Description("Filter by entity type")),
			mcp.WithString("name", mcp.Description("Filter by name (LIKE pattern)")),
			mcp.WithString("source", mcp.Description("Filter by source")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			filter := cortex.EntityFilter{
				Type:     req.GetString("type", ""),
				NameLike: req.GetString("name", ""),
				Source:   req.GetString("source", ""),
			}
			entities, err := cx.FindEntities(ctx, filter)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(entities)
		},
	)

	// --- get_relationships ---
	s.AddTool(
		mcp.NewTool("get_relationships",
			mcp.WithDescription("Get relationships for an entity, optionally filtered by type."),
			mcp.WithString("entity_id", mcp.Required(), mcp.Description("The entity ID")),
			mcp.WithString("type", mcp.Description("Filter by relationship type")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			entityID, err := req.RequireString("entity_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			var filters []cortex.RelFilter
			if relType := req.GetString("type", ""); relType != "" {
				filters = append(filters, cortex.RelTypeFilter(relType))
			}
			rels, err := cx.GetRelationships(ctx, entityID, filters...)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(rels)
		},
	)

	// --- traverse ---
	s.AddTool(
		mcp.NewTool("traverse",
			mcp.WithDescription("Traverse the knowledge graph from a starting entity using BFS."),
			mcp.WithString("start_id", mcp.Required(), mcp.Description("Starting entity ID")),
			mcp.WithNumber("depth", mcp.Description("Traversal depth (default 1)")),
			mcp.WithString("edge_types", mcp.Description("Comma-separated edge types to follow")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			startID, err := req.RequireString("start_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			var opts []cortex.TraverseOption
			if depth := req.GetInt("depth", 0); depth > 0 {
				opts = append(opts, cortex.WithDepth(depth))
			}
			if edgeTypesStr := req.GetString("edge_types", ""); edgeTypesStr != "" {
				types := strings.Split(edgeTypesStr, ",")
				for i := range types {
					types[i] = strings.TrimSpace(types[i])
				}
				opts = append(opts, cortex.WithEdgeTypes(types...))
			}
			graph, err := cx.Traverse(ctx, startID, opts...)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(graph)
		},
	)

	// --- search ---
	s.AddTool(
		mcp.NewTool("search",
			mcp.WithDescription("Search the knowledge graph using keyword, vector, or memory search."),
			mcp.WithString("query", mcp.Required(), mcp.Description("The search query")),
			mcp.WithString("mode", mcp.Required(), mcp.Description("Search mode"), mcp.Enum("keyword", "vector", "memory")),
			mcp.WithNumber("limit", mcp.Description("Maximum number of results (default 10)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			query, err := req.RequireString("query")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			mode, err := req.RequireString("mode")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			limit := req.GetInt("limit", 10)

			switch mode {
			case "keyword":
				chunks, err := cx.SearchKeyword(ctx, query, limit)
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				return jsonResult(chunks)
			case "vector":
				chunks, err := cx.SearchVector(ctx, query, limit)
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				return jsonResult(chunks)
			case "memory":
				memories, err := cx.SearchMemories(ctx, query, limit)
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				return jsonResult(memories)
			default:
				return mcp.NewToolResultError(fmt.Sprintf("unknown search mode: %s", mode)), nil
			}
		},
	)

	// --- merge ---
	s.AddTool(
		mcp.NewTool("merge",
			mcp.WithDescription("Merge one entity into another, re-targeting all relationships, memory links, and chunks. Use to clean up duplicate entities surfaced by lint."),
			mcp.WithString("keep_id", mcp.Required(), mcp.Description("Entity ID to keep")),
			mcp.WithString("drop_id", mcp.Required(), mcp.Description("Entity ID to merge into keep and delete")),
			mcp.WithBoolean("dry_run", mcp.Description("Simulate the merge and roll back; returns the stats without modifying the graph")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			keepID, err := req.RequireString("keep_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			dropID, err := req.RequireString("drop_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			dryRun := req.GetBool("dry_run", false)
			var stats cortex.MergeStats
			if dryRun {
				stats, err = cx.MergeEntitiesDryRun(ctx, keepID, dropID)
			} else {
				stats, err = cx.MergeEntities(ctx, keepID, dropID)
			}
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(stats)
		},
	)

	// --- lint ---
	s.AddTool(
		mcp.NewTool("lint",
			mcp.WithDescription("Scan the graph for cleanup candidates: orphans, near-duplicates, dead sources, unlinked memories."),
			mcp.WithBoolean("low_confidence", mcp.Description("Include low-confidence memories section")),
			mcp.WithNumber("low_confidence_threshold", mcp.Description("Confidence threshold 0-1; implies low_confidence=true")),
			mcp.WithString("format", mcp.Description("Output format: json (default) or markdown"), mcp.Enum("json", "markdown")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var opts []cortex.LintOption
			thresholdSet := req.GetFloat("low_confidence_threshold", -1) >= 0
			if thresholdSet {
				th := req.GetFloat("low_confidence_threshold", 0)
				if th < 0 || th > 1 {
					return mcp.NewToolResultError("low_confidence_threshold must be between 0 and 1"), nil
				}
				opts = append(opts, cortex.WithLowConfidenceThreshold(th))
			} else if req.GetBool("low_confidence", false) {
				opts = append(opts, cortex.WithLowConfidence())
			}
			report, err := cx.Lint(ctx, opts...)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			format := req.GetString("format", "json")
			switch format {
			case "json", "":
				return jsonResult(report)
			case "markdown":
				return mcp.NewToolResultText(cortex.RenderLintMarkdown(report)), nil
			default:
				return mcp.NewToolResultError(fmt.Sprintf("unknown format: %s", format)), nil
			}
		},
	)
}

func jsonResult(v any) (*mcp.CallToolResult, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("json marshal error: %v", err)), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}
