# Cortex Transport Layers Implementation Plan (Plan 2 of 3)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add MCP stdio server and HTTP/REST server as thin transport wrappers over the cortex core library, making it queryable by any AI agent.

**Architecture:** Both servers import the cortex root package and wire up OpenAI providers (if API key is set). Each tool/endpoint calls one core method and formats the response. No business logic in the transport layer.

**Tech Stack:** `github.com/mark3labs/mcp-go` for MCP, stdlib `net/http` + `encoding/json` for HTTP.

---

## File Structure

```
cortex/
├── cmd/
│   ├── cortex/          # (existing) CLI
│   ├── cortex-mcp/
│   │   └── main.go      # MCP stdio server
│   └── cortex-http/
│       └── main.go      # HTTP/REST server
```

---

### Task 1: MCP Server

**Files:**
- Create: `cmd/cortex-mcp/main.go`

Implement an MCP stdio server exposing these tools:

| Tool | Description | Parameters |
|------|------------|------------|
| `remember` | Ingest and remember content | `content` (string, required), `source` (string, optional) |
| `recall` | Query the knowledge graph | `query` (string, required), `limit` (number, optional) |
| `forget` | Remove knowledge | `entity_id` (string, optional), `source` (string, optional) |
| `get_entity` | Get entity by ID | `id` (string, required) |
| `find_entities` | Search entities | `type` (string, optional), `name` (string, optional), `source` (string, optional) |
| `get_relationships` | Get relationships for entity | `entity_id` (string, required), `type` (string, optional) |
| `traverse` | Walk the graph | `entity_id` (string, required), `depth` (number, optional), `edge_types` (string, optional — comma-separated) |
| `search` | Direct search | `query` (string, required), `mode` (string: "keyword", "vector", "memory"), `limit` (number, optional) |

Each handler:
1. Extracts parameters from `request`
2. Calls the corresponding cortex method
3. Returns result as JSON text via `mcp.NewToolResultText`

Setup:
- Read `CORTEX_DB` env var (default "brain.db") for database path
- Read `OPENAI_API_KEY` — if set, wire up OpenAI LLM + Embedder + hybrid extractor
- Create MCP server with `server.NewMCPServer("cortex", "1.0.0")`
- Add all tools, call `server.ServeStdio(s)`

- [ ] **Step 1: Add mcp-go dependency**

Run: `go get github.com/mark3labs/mcp-go`

- [ ] **Step 2: Write the MCP server**

```go
// cmd/cortex-mcp/main.go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/extractor/deterministic"
	"github.com/sausheong/cortex/extractor/hybrid"
	llmext "github.com/sausheong/cortex/extractor/llmext"
	oaillm "github.com/sausheong/cortex/llm/openai"
)

func main() {
	dbPath := os.Getenv("CORTEX_DB")
	if dbPath == "" {
		dbPath = "brain.db"
	}

	c := openCortex(dbPath)
	defer c.Close()

	s := server.NewMCPServer(
		"cortex",
		"1.0.0",
		server.WithToolCapabilities(false),
	)

	addRememberTool(s, c)
	addRecallTool(s, c)
	addForgetTool(s, c)
	addGetEntityTool(s, c)
	addFindEntitiesTool(s, c)
	addGetRelationshipsTool(s, c)
	addTraverseTool(s, c)
	addSearchTool(s, c)

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "cortex-mcp: %v\n", err)
		os.Exit(1)
	}
}

func openCortex(dbPath string) *cortex.Cortex {
	apiKey := os.Getenv("OPENAI_API_KEY")
	var opts []cortex.Option

	if apiKey != "" {
		llm := oaillm.NewLLM(apiKey)
		emb := oaillm.NewEmbedder(apiKey)
		det := deterministic.New()
		llmE := llmext.New(llm)
		ext := hybrid.New(det, llmE)
		opts = append(opts,
			cortex.WithLLM(llm),
			cortex.WithEmbedder(emb),
			cortex.WithExtractor(ext),
		)
	} else {
		det := deterministic.New()
		opts = append(opts, cortex.WithExtractor(det))
	}

	c, err := cortex.Open(dbPath, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cortex-mcp: open db: %v\n", err)
		os.Exit(1)
	}
	return c
}

func toJSON(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func addRememberTool(s *server.MCPServer, c *cortex.Cortex) {
	tool := mcp.NewTool("remember",
		mcp.WithDescription("Ingest and remember content — extracts entities, relationships, and memories"),
		mcp.WithString("content", mcp.Required(), mcp.Description("Text content to remember")),
		mcp.WithString("source", mcp.Description("Source provenance (e.g. 'markdown', 'conversation')")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		content, err := req.RequireString("content")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		var opts []cortex.RememberOption
		if src, err := req.RequireString("source"); err == nil && src != "" {
			opts = append(opts, cortex.WithSource(src))
		}
		if err := c.Remember(ctx, content, opts...); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("Remembered."), nil
	})
}

func addRecallTool(s *server.MCPServer, c *cortex.Cortex) {
	tool := mcp.NewTool("recall",
		mcp.WithDescription("Query the knowledge graph with natural language"),
		mcp.WithString("query", mcp.Required(), mcp.Description("Natural language query")),
		mcp.WithNumber("limit", mcp.Description("Max results (default 10)")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		limit := 10
		if l, err := req.RequireFloat("limit"); err == nil {
			limit = int(l)
		}
		results, err := c.Recall(ctx, query, cortex.WithLimit(limit))
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(toJSON(results)), nil
	})
}

func addForgetTool(s *server.MCPServer, c *cortex.Cortex) {
	tool := mcp.NewTool("forget",
		mcp.WithDescription("Remove knowledge from the graph"),
		mcp.WithString("entity_id", mcp.Description("Entity ID to forget")),
		mcp.WithString("source", mcp.Description("Source to forget (e.g. 'gmail')")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		f := cortex.Filter{}
		if id, err := req.RequireString("entity_id"); err == nil {
			f.EntityID = id
		}
		if src, err := req.RequireString("source"); err == nil {
			f.Source = src
		}
		if f.EntityID == "" && f.Source == "" {
			return mcp.NewToolResultError("entity_id or source required"), nil
		}
		if err := c.Forget(ctx, f); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("Forgotten."), nil
	})
}

func addGetEntityTool(s *server.MCPServer, c *cortex.Cortex) {
	tool := mcp.NewTool("get_entity",
		mcp.WithDescription("Get an entity by ID"),
		mcp.WithString("id", mcp.Required(), mcp.Description("Entity ID")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, err := req.RequireString("id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		e, err := c.GetEntity(ctx, id)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(toJSON(e)), nil
	})
}

func addFindEntitiesTool(s *server.MCPServer, c *cortex.Cortex) {
	tool := mcp.NewTool("find_entities",
		mcp.WithDescription("Search for entities by type, name, or source"),
		mcp.WithString("type", mcp.Description("Entity type (person, organization, concept, event, document)")),
		mcp.WithString("name", mcp.Description("Name pattern (supports SQL LIKE with %)")),
		mcp.WithString("source", mcp.Description("Source filter")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		f := cortex.EntityFilter{}
		if t, err := req.RequireString("type"); err == nil {
			f.Type = t
		}
		if n, err := req.RequireString("name"); err == nil {
			f.NameLike = n
		}
		if s, err := req.RequireString("source"); err == nil {
			f.Source = s
		}
		entities, err := c.FindEntities(ctx, f)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(toJSON(entities)), nil
	})
}

func addGetRelationshipsTool(s *server.MCPServer, c *cortex.Cortex) {
	tool := mcp.NewTool("get_relationships",
		mcp.WithDescription("Get relationships for an entity"),
		mcp.WithString("entity_id", mcp.Required(), mcp.Description("Entity ID")),
		mcp.WithString("type", mcp.Description("Relationship type filter")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		eid, err := req.RequireString("entity_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		var opts []cortex.RelFilter
		if t, err := req.RequireString("type"); err == nil && t != "" {
			opts = append(opts, cortex.RelTypeFilter(t))
		}
		rels, err := c.GetRelationships(ctx, eid, opts...)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(toJSON(rels)), nil
	})
}

func addTraverseTool(s *server.MCPServer, c *cortex.Cortex) {
	tool := mcp.NewTool("traverse",
		mcp.WithDescription("Walk the knowledge graph from an entity"),
		mcp.WithString("entity_id", mcp.Required(), mcp.Description("Start entity ID")),
		mcp.WithNumber("depth", mcp.Description("Traversal depth (default 1)")),
		mcp.WithString("edge_types", mcp.Description("Comma-separated edge types to follow")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		eid, err := req.RequireString("entity_id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		var opts []cortex.TraverseOption
		if d, err := req.RequireFloat("depth"); err == nil {
			opts = append(opts, cortex.WithDepth(int(d)))
		}
		if et, err := req.RequireString("edge_types"); err == nil && et != "" {
			types := strings.Split(et, ",")
			for i := range types {
				types[i] = strings.TrimSpace(types[i])
			}
			opts = append(opts, cortex.WithEdgeTypes(types...))
		}
		g, err := c.Traverse(ctx, eid, opts...)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText(toJSON(g)), nil
	})
}

func addSearchTool(s *server.MCPServer, c *cortex.Cortex) {
	tool := mcp.NewTool("search",
		mcp.WithDescription("Direct search — keyword, vector, or memory"),
		mcp.WithString("query", mcp.Required(), mcp.Description("Search query")),
		mcp.WithString("mode", mcp.Required(), mcp.Description("Search mode: keyword, vector, or memory"),
			mcp.Enum("keyword", "vector", "memory")),
		mcp.WithNumber("limit", mcp.Description("Max results (default 10)")),
	)
	s.AddTool(tool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		query, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		mode, err := req.RequireString("mode")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		limit := 10
		if l, err := req.RequireFloat("limit"); err == nil {
			limit = int(l)
		}
		switch mode {
		case "keyword":
			chunks, err := c.SearchKeyword(ctx, query, limit)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(toJSON(chunks)), nil
		case "vector":
			chunks, err := c.SearchVector(ctx, query, limit)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(toJSON(chunks)), nil
		case "memory":
			mems, err := c.SearchMemories(ctx, query, limit)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText(toJSON(mems)), nil
		default:
			return mcp.NewToolResultError("mode must be keyword, vector, or memory"), nil
		}
	})
}

// strconv import is used if needed for parsing, keeping it available
var _ = strconv.Itoa
```

Remove the unused `strconv` import (it was in the plan template but isn't needed). The code above should compile without it.

- [ ] **Step 3: Build and verify**

Run: `go build ./cmd/cortex-mcp/`
Expected: binary builds successfully

- [ ] **Step 4: Commit**

```bash
git add cmd/cortex-mcp/ go.mod go.sum
git commit -m "feat: add MCP stdio server exposing 8 cortex tools"
```

---

### Task 2: HTTP/REST Server

**Files:**
- Create: `cmd/cortex-http/main.go`

Implement an HTTP server with these endpoints:

| Method | Path | Description |
|--------|------|-------------|
| POST | `/remember` | `{"content": "...", "source": "..."}` |
| GET | `/recall?q=...&limit=10` | Query the knowledge graph |
| DELETE | `/forget?entity_id=...` or `?source=...` | Remove knowledge |
| GET | `/entity/:id` | Get entity by ID |
| GET | `/entities?type=...&name=...&source=...` | Search entities |
| GET | `/relationships/:entity_id?type=...` | Get relationships |
| GET | `/traverse/:entity_id?depth=1&edge_types=...` | Walk graph |
| GET | `/search?q=...&mode=keyword&limit=10` | Direct search |

Use stdlib `net/http` with `http.ServeMux`. Parse path parameters manually (split on `/`).

Default port: `8080`, configurable via `CORTEX_PORT` env var.

Same openCortex() setup as MCP server.

- [ ] **Step 1: Write the HTTP server**

Full implementation using `net/http.ServeMux`. Each handler:
1. Parses query params or JSON body
2. Calls cortex method
3. Returns JSON response

Error responses: `{"error": "message"}` with appropriate HTTP status.

- [ ] **Step 2: Build and verify**

Run: `go build ./cmd/cortex-http/`
Expected: binary builds successfully

- [ ] **Step 3: Commit**

```bash
git add cmd/cortex-http/
git commit -m "feat: add HTTP/REST server with 8 endpoints"
```

---

### Task 3: Run Full Test Suite and Tidy

- [ ] **Step 1: Run all tests**

Run: `go test ./... -count=1`
Expected: all pass, no regressions

- [ ] **Step 2: Tidy modules**

Run: `go mod tidy`

- [ ] **Step 3: Verify all binaries build**

Run: `go build ./cmd/cortex/ && go build ./cmd/cortex-mcp/ && go build ./cmd/cortex-http/`

- [ ] **Step 4: Commit if needed**

```bash
git add go.mod go.sum
git commit -m "chore: go mod tidy for transport layer dependencies"
```
