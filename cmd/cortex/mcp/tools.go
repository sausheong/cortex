package mcp

import (
	"context"
	"encoding/json"
	"errors"
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
	RegisterToolsFor(s, cx, "")
}

// RegisterToolsFor attaches the tool set. Each call uses the handle the HTTP
// layer put in the request context; fallback is used only when the context
// carries none (stdio and the legacy single-user transport). publicURL
// builds the "set up your AI provider" message.
func RegisterToolsFor(s *server.MCPServer, fallback *cortex.Cortex, publicURL string) {
	h := handles{fallback: fallback, publicURL: publicURL}
	// --- context ---
	s.AddTool(
		mcp.NewTool("context",
			mcp.WithDescription("Call at the start of every conversation and before answering anything that depends on the user's preferences, how they would do things, or people and things they know. Returns who the user is plus relevant memories."),
			mcp.WithString("question", mcp.Required(), mcp.Description("What the conversation is about or what you need to know")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cx, bad := h.get(ctx)
			if bad != nil {
				return bad, nil
			}
			question, err := req.RequireString("question")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			out := contextResult{Memories: []cortex.Result{}, Entities: []cortex.Result{}}
			if owner, ok := ownerID(ctx, cx); ok {
				p, err := cx.Profile(ctx, owner)
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				out.Profile = contextProfile{Static: nz(p.Static), Dynamic: nz(p.Dynamic)}
			} else {
				out.Profile = contextProfile{Static: []string{}, Dynamic: []string{}}
			}
			rr, err := cx.RecallWithStrength(ctx, question, cortex.WithLimit(8))
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			out.Abstain = rr.Abstain
			for _, r := range rr.Results {
				if r.Type == "entity" {
					out.Entities = append(out.Entities, r)
				} else {
					out.Memories = append(out.Memories, r)
				}
			}
			return jsonResult(out)
		},
	)

	// --- remember ---
	s.AddTool(
		mcp.NewTool("remember",
			mcp.WithDescription("Call whenever the conversation reveals something new about the user: a preference, a connection (person or organisation), a capability, background, or an identity fact. Don't store small talk or secrets."),
			mcp.WithString("content", mcp.Required(), mcp.Description("The text content to remember")),
			mcp.WithString("source", mcp.Description("Optional source label for the content")),
			mcp.WithString("speaker", mcp.Description("Who asserted this (e.g. \"user\", \"assistant\", a document name). Optional provenance label.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cx, bad := h.get(ctx)
			if bad != nil {
				return bad, nil
			}
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
				// Stored in full; only some vectors or some entity and
				// fact extraction are missing.
				var ep *cortex.EmbedPartialError
				var xp *cortex.ExtractPartialError
				var notes []string
				if errors.As(err, &xp) {
					notes = append(notes, fmt.Sprintf("%d of %d section(s) could not be analysed for entities and facts, but their text is saved and searchable", xp.Failed, xp.Total))
				}
				if errors.As(err, &ep) {
					notes = append(notes, fmt.Sprintf("keyword search works now; %d item(s) will be added to semantic search by tonight's maintenance", ep.Count))
				}
				if len(notes) > 0 {
					return mcp.NewToolResultText("remembered (" + strings.Join(notes, "; ") + ")"), nil
				}
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
			mcp.WithBoolean("rerank", mcp.Description("Diversity-rerank results (default false). Set true to spread out near-duplicate hits for browse/explore; leave off for best fact-recall accuracy.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cx, bad := h.get(ctx)
			if bad != nil {
				return bad, nil
			}
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
			if req.GetBool("rerank", false) {
				opts = append(opts, cortex.WithRerank(true))
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
			mcp.WithDescription("Remove knowledge from the graph by entity ID or source. Deletion is recoverable for 30 days."),
			mcp.WithString("entity_id", mcp.Description("Entity ID to forget")),
			mcp.WithString("source", mcp.Description("Source label to forget")),
			mcp.WithString("memory_id", mcp.Description("Memory ID to forget (the id of a memory result from recall)")),
			mcp.WithString("chunk_id", mcp.Description("Chunk ID to forget (the id of a chunk result from recall)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cx, bad := h.get(ctx)
			if bad != nil {
				return bad, nil
			}
			entityID := req.GetString("entity_id", "")
			source := req.GetString("source", "")
			memoryID := req.GetString("memory_id", "")
			chunkID := req.GetString("chunk_id", "")
			if entityID == "" && source == "" && memoryID == "" && chunkID == "" {
				return mcp.NewToolResultError("one of entity_id, source, memory_id or chunk_id is required"), nil
			}
			if memoryID != "" {
				if _, _, err := cx.SoftDeleteMemories(ctx, cortex.DeleteFilter{IDs: []string{memoryID}}, "user"); err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
			}
			if chunkID != "" {
				if _, _, err := cx.SoftDeleteChunks(ctx, []string{chunkID}, "user"); err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
			}
			if entityID == "" && source == "" {
				return mcp.NewToolResultText("forgotten (recoverable for 30 days)"), nil
			}
			filter := cortex.Filter{EntityID: entityID, Source: source}
			if err := cx.Forget(ctx, filter); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText("forgotten (recoverable for 30 days)"), nil
		},
	)

	// --- get_entity ---
	s.AddTool(
		mcp.NewTool("get_entity",
			mcp.WithDescription("Retrieve an entity by its ID."),
			mcp.WithString("id", mcp.Required(), mcp.Description("The entity ID")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cx, bad := h.get(ctx)
			if bad != nil {
				return bad, nil
			}
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
			cx, bad := h.get(ctx)
			if bad != nil {
				return bad, nil
			}
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
			cx, bad := h.get(ctx)
			if bad != nil {
				return bad, nil
			}
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
			cx, bad := h.get(ctx)
			if bad != nil {
				return bad, nil
			}
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
			cx, bad := h.get(ctx)
			if bad != nil {
				return bad, nil
			}
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
			mcp.WithDescription("Merge one entity into another, re-targeting all relationships, memory links, and chunks. Use to clean up duplicate entities surfaced by lint. The merge is recoverable for 30 days."),
			mcp.WithString("keep_id", mcp.Required(), mcp.Description("Entity ID to keep")),
			mcp.WithString("drop_id", mcp.Required(), mcp.Description("Entity ID to merge into keep and delete")),
			mcp.WithBoolean("dry_run", mcp.Description("Simulate the merge and roll back; returns the stats without modifying the graph")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cx, bad := h.get(ctx)
			if bad != nil {
				return bad, nil
			}
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
			res, err := jsonResult(stats)
			if err == nil && !res.IsError && !dryRun {
				res.Content = append(res.Content, mcp.NewTextContent("The dropped entity is recoverable for 30 days; the merge can be undone."))
			}
			return res, err
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
			cx, bad := h.get(ctx)
			if bad != nil {
				return bad, nil
			}
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

	// --- profile ---
	s.AddTool(
		mcp.NewTool("profile",
			mcp.WithDescription("Get a cached context digest for an entity — who they are (static) plus recent context (dynamic). Call this FIRST to load baseline context about the user (omit entity_id for the owner) before answering, instead of guessing what to recall. Returns static[] and dynamic[] fact lists."),
			mcp.WithString("entity_id", mcp.Description("Entity ID to profile. Omit for the owner entity.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			cx, bad := h.get(ctx)
			if bad != nil {
				return bad, nil
			}
			entityID := req.GetString("entity_id", "")
			if entityID == "" {
				owner, ok := ownerID(ctx, cx)
				if !ok {
					return mcp.NewToolResultError("no owner configured"), nil
				}
				entityID = owner
			}
			p, err := cx.Profile(ctx, entityID)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(p)
		},
	)
}

type contextProfile struct {
	Static  []string `json:"static"`
	Dynamic []string `json:"dynamic"`
}

type contextResult struct {
	Profile  contextProfile  `json:"profile"`
	Memories []cortex.Result `json:"memories"`
	Entities []cortex.Result `json:"entities"`
	Abstain  bool            `json:"abstain"`
}

func nz(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func jsonResult(v any) (*mcp.CallToolResult, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("json marshal error: %v", err)), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}

// ownerID is the user's owner entity: users.owner_entity_id, else (legacy
// single-user databases) the first person entity with source "owner".
func ownerID(ctx context.Context, cx *cortex.Cortex) (string, bool) {
	if id, err := cx.OwnerEntityID(ctx); err == nil {
		return id, true
	}
	owners, err := cx.FindEntities(ctx, cortex.EntityFilter{Type: "person", Source: "owner"})
	if err != nil || len(owners) == 0 {
		return "", false
	}
	return owners[0].ID, true
}
