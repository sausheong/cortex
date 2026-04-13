# Cortex

A personal knowledge graph and memory system in Go. Cortex stores everything you know — people, organizations, concepts, events, documents — as a unified graph with typed relationships. It extracts entities and facts from your notes, conversations, emails, and calendar, then lets any AI agent query the graph via MCP, HTTP, or the Go API.

Think of it as a digital twin that compounds knowledge over time. Agents read from cortex before responding and write back after every conversation. The more you use it, the smarter it gets.

Inspired by [GBrain](https://github.com/garrytan/gbrain) (operational model), [Cognee](https://github.com/topoteretes/cognee) (graph structure), and [mem0](https://github.com/mem0ai/mem0) (memory pipeline).

## Features

- **Unified knowledge graph** — people, organizations, concepts, events, documents as first-class nodes with typed relationships
- **Remember / Recall / Forget** — simple high-level API inspired by mem0
- **Hybrid extraction** — deterministic parsing (frontmatter, wikilinks) + LLM-powered entity/relationship discovery
- **Multi-strategy search** — keyword (FTS5), vector (cosine similarity), graph traversal, and memory lookup merged via reciprocal rank fusion
- **Four data connectors** — markdown files, conversations, Gmail, Google Calendar
- **Three interfaces** — CLI, MCP stdio server, HTTP/REST API
- **Single binary, single file** — embedded SQLite, no external database
- **Pluggable providers** — swap OpenAI for Anthropic, Ollama, or any custom implementation

## Prerequisites

- **Go 1.22+** (uses `net/http` method-based routing from Go 1.22)
- **OpenAI API key** (optional but recommended) — enables LLM extraction and semantic search

Without an OpenAI key, cortex still works but is limited to deterministic extraction (YAML frontmatter + wikilinks) and keyword search.

## Installation

### From Source

```bash
git clone https://github.com/sausheong/cortex.git
cd cortex

# Build all three binaries
go build -o cortex ./cmd/cortex/
go build -o cortex-mcp ./cmd/cortex-mcp/
go build -o cortex-http ./cmd/cortex-http/

# Move to your PATH (optional)
mv cortex cortex-mcp cortex-http /usr/local/bin/
```

### As a Go Library

```bash
go get github.com/sausheong/cortex
```

## Setup

### 1. Initialize the Brain

```bash
cortex init
```

This creates `brain.db` in the current directory — a single SQLite file containing the entire knowledge graph, embeddings, and search indexes.

### 2. Set Your OpenAI API Key (Recommended)

```bash
export OPENAI_API_KEY=sk-...
```

With an API key, cortex will:
- Use GPT-4o-mini to extract entities, relationships, and distilled facts from any text
- Use text-embedding-3-small to generate vector embeddings for semantic search
- Decompose natural language queries into multi-strategy sub-queries

Without it, cortex falls back to deterministic extraction only (frontmatter + wikilinks).

### 3. Start Ingesting

```bash
# Sync your markdown notes
cortex sync markdown ~/notes

# Remember ad-hoc facts
cortex remember "Alice works at Stripe as a staff engineer"
cortex remember "Bob and Alice went to Stanford together"

# Query the graph
cortex recall "who works at Stripe"
cortex recall "what do I know about Alice"

# List what's been stored
cortex entity list --type person
cortex entity get <entity-id>
```

## CLI Reference

```
cortex init                           Create brain.db
cortex remember <text>                Ingest text, extract entities/relationships/memories
cortex recall <query>                 Natural language query with multi-strategy search
cortex sync markdown <dir>            Sync .md files from a directory (incremental)
cortex sync gmail                     Sync Gmail (requires OAuth2 — see below)
cortex sync calendar                  Sync Google Calendar (requires OAuth2 — see below)
cortex entity list [--type <type>]    List entities, optionally filtered by type
cortex entity get <id>                Show entity details, attributes, and relationships
cortex forget --source <src>          Remove all knowledge from a source
cortex forget --entity <id>           Remove a specific entity and all linked data
```

## Using as an MCP Server

Connect cortex to Claude Code, Cursor, Windsurf, or any MCP-compatible AI tool.

```bash
go build -o cortex-mcp ./cmd/cortex-mcp/
```

### Claude Code

Add to `~/.claude/server.json`:

```json
{
  "mcpServers": {
    "cortex": {
      "command": "/path/to/cortex-mcp",
      "env": {
        "OPENAI_API_KEY": "sk-...",
        "CORTEX_DB": "/path/to/brain.db"
      }
    }
  }
}
```

### Cursor / Windsurf

Add to your MCP server configuration:

```json
{
  "cortex": {
    "command": "/path/to/cortex-mcp",
    "args": [],
    "env": {
      "OPENAI_API_KEY": "sk-...",
      "CORTEX_DB": "/path/to/brain.db"
    }
  }
}
```

### MCP Tools

| Tool | Description | Parameters |
|------|-------------|------------|
| `remember` | Store content in the knowledge graph | `content` (required), `source` |
| `recall` | Natural language query | `query` (required), `limit` |
| `forget` | Remove knowledge | `entity_id` or `source` |
| `get_entity` | Get entity by ID | `id` (required) |
| `find_entities` | Search entities | `type`, `name`, `source` |
| `get_relationships` | Get relationships for entity | `entity_id` (required), `type` |
| `traverse` | Walk the knowledge graph | `start_id` (required), `depth`, `edge_types` |
| `search` | Direct keyword/vector/memory search | `query` (required), `mode` (required), `limit` |

## Using as an HTTP/REST Server

```bash
go build -o cortex-http ./cmd/cortex-http/

OPENAI_API_KEY=sk-... CORTEX_DB=brain.db cortex-http
# Listening on :8080
```

### Endpoints

| Method | Endpoint | Description | Example |
|--------|----------|-------------|---------|
| POST | `/remember` | Ingest content | `curl -X POST -d '{"content":"Alice works at Stripe"}' localhost:8080/remember` |
| GET | `/recall` | Natural language query | `curl 'localhost:8080/recall?q=who+works+at+Stripe&limit=5'` |
| DELETE | `/forget` | Remove knowledge | `curl -X DELETE 'localhost:8080/forget?source=gmail'` |
| GET | `/entity/{id}` | Get entity by ID | `curl localhost:8080/entity/01HXY...` |
| GET | `/entities` | Search entities | `curl 'localhost:8080/entities?type=person'` |
| GET | `/relationships/{entity_id}` | Get relationships | `curl 'localhost:8080/relationships/01HXY...?type=works_at'` |
| GET | `/traverse/{entity_id}` | Walk graph | `curl 'localhost:8080/traverse/01HXY...?depth=2'` |
| GET | `/search` | Direct search | `curl 'localhost:8080/search?q=Stripe&mode=keyword&limit=10'` |

All endpoints return JSON. Error responses use `{"error": "message"}` with appropriate HTTP status codes.

## Using as a Go Library

```go
package main

import (
    "context"
    "fmt"

    "github.com/sausheong/cortex"
    "github.com/sausheong/cortex/extractor/deterministic"
    "github.com/sausheong/cortex/extractor/hybrid"
    llmext "github.com/sausheong/cortex/extractor/llmext"
    oaillm "github.com/sausheong/cortex/llm/openai"
)

func main() {
    // Set up providers
    llm := oaillm.NewLLM("sk-...")
    emb := oaillm.NewEmbedder("sk-...")
    ext := hybrid.New(deterministic.New(), llmext.New(llm))

    // Open the knowledge graph
    c, _ := cortex.Open("brain.db",
        cortex.WithLLM(llm),
        cortex.WithEmbedder(emb),
        cortex.WithExtractor(ext),
    )
    defer c.Close()

    ctx := context.Background()

    // Remember — extracts entities, relationships, and memories automatically
    c.Remember(ctx, "Alice works at Stripe as a staff engineer")
    c.Remember(ctx, "Bob and Alice went to Stanford together")

    // Recall — multi-strategy search with RRF merging
    results, _ := c.Recall(ctx, "who works at Stripe")
    for _, r := range results {
        fmt.Printf("[%s] %s (score: %.3f)\n", r.Type, r.Content, r.Score)
    }

    // Structured graph queries
    entities, _ := c.FindEntities(ctx, cortex.EntityFilter{Type: "person"})
    for _, e := range entities {
        fmt.Printf("%s (%s)\n", e.Name, e.Type)
    }

    rels, _ := c.GetRelationships(ctx, entities[0].ID)
    for _, r := range rels {
        fmt.Printf("  --%s--> %s\n", r.Type, r.TargetID)
    }

    // Graph traversal — BFS from an entity
    graph, _ := c.Traverse(ctx, entities[0].ID, cortex.WithDepth(2))
    fmt.Printf("Found %d entities, %d relationships\n",
        len(graph.Entities), len(graph.Relationships))

    // Forget — remove by entity or source
    c.Forget(ctx, cortex.Filter{Source: "gmail"})
}
```

### Using Without OpenAI

For environments without an API key, use only the deterministic extractor:

```go
c, _ := cortex.Open("brain.db",
    cortex.WithExtractor(deterministic.New()),
)
```

This handles YAML frontmatter and wikilinks but won't extract entities from freeform prose.

## Architecture

```
cortex/
├── *.go                     # Core library — graph, search, Remember/Recall/Forget
├── llm/openai/              # Pluggable OpenAI LLM + Embedder
├── extractor/
│   ├── deterministic/       # Regex, frontmatter, wikilink extraction
│   ├── llmext/              # LLM-powered extraction
│   └── hybrid/              # Composes deterministic + LLM
├── connector/
│   ├── markdown/            # Markdown directory connector
│   ├── conversation/        # Conversation message connector
│   ├── gmail/               # Gmail connector (OAuth2)
│   └── calendar/            # Google Calendar connector (OAuth2)
├── cmd/
│   ├── cortex/              # CLI
│   ├── cortex-mcp/          # MCP stdio server
│   └── cortex-http/         # HTTP/REST server
└── internal/testutil/       # Test mocks and helpers
```

**Core Graph + Pluggable Connectors**: the core knows nothing about data sources. Connectors are independent packages that parse source-specific formats and call the core API. Adding a new data source means writing a new connector package — no changes to the core.

## Core API Reference

### High-Level Memory Operations

| Method | Description |
|--------|-------------|
| `Remember(ctx, content, ...RememberOption)` | Ingest text, extract entities/relationships/memories, generate embeddings |
| `Recall(ctx, query, ...RecallOption)` | Natural language query with decomposition + parallel search + RRF merge |
| `Forget(ctx, Filter)` | Remove knowledge by entity ID or source, with cascading deletes |

### Structured Graph Operations

| Method | Description |
|--------|-------------|
| `PutEntity(ctx, *Entity)` | Create or update entity (upsert by name + type) |
| `GetEntity(ctx, id)` | Get entity by ID |
| `FindEntities(ctx, EntityFilter)` | Search by type, name pattern (SQL LIKE), source |
| `PutRelationship(ctx, *Relationship)` | Create a typed directed relationship between two entities |
| `GetRelationships(ctx, entityID, ...RelFilter)` | Get relationships involving an entity (as source or target) |
| `Traverse(ctx, startID, ...TraverseOption)` | BFS graph walk with configurable depth and edge type filtering |

### Search Primitives

| Method | Description |
|--------|-------------|
| `SearchKeyword(ctx, query, limit)` | FTS5 full-text search on text chunks |
| `SearchVector(ctx, query, limit)` | Cosine similarity search on vector embeddings |
| `SearchMemories(ctx, query, limit)` | Keyword search on distilled memory facts |

### Options

```go
// Remember options
cortex.WithSource("markdown")       // Set source provenance
cortex.WithContentType("markdown")  // Hint content type for extraction

// Recall options
cortex.WithLimit(10)                // Max results (default 20)
cortex.WithSourceFilter("gmail")    // Filter results by source

// Relationship filter
cortex.RelTypeFilter("works_at")    // Filter by relationship type

// Traverse options
cortex.WithDepth(2)                          // Traversal depth (default 1)
cortex.WithEdgeTypes("works_at", "knows")    // Only follow these edge types

// Provider options
cortex.WithLLM(myLLM)              // Set LLM provider
cortex.WithEmbedder(myEmbedder)    // Set embedding provider
cortex.WithExtractor(myExtractor)  // Set extraction provider
```

## Knowledge Model

### Entities

All node types are first-class. Every entity has a type, name, optional JSON attributes, and source provenance.

| Type | Examples |
|------|---------|
| `person` | Alice, Bob, your contacts |
| `organization` | Stripe, Google, your company |
| `concept` | distributed systems, machine learning |
| `event` | meetings, conferences, milestones |
| `document` | notes, articles, emails |

### Relationships

Typed directed edges between entities. Types are open-ended strings — new connectors introduce new types organically.

Common types: `works_at`, `knows`, `attended`, `discussed_in`, `related_to`, `created`, `part_of`

### Chunks

Raw text fragments linked to entities. Indexed for both FTS5 keyword search and vector similarity search. Each chunk carries JSON metadata (file path, line range, message ID, etc.).

### Memories

Distilled facts extracted by the LLM. Higher signal than raw chunks. Examples:
- "Alice is joining Stripe next month"
- "Bob and Alice went to Stanford together"
- "The project deadline is March 15"

Memories are linked to related entities via a junction table and are searched first during `Recall` because they're denser and more useful than raw text.

## Extraction Pipeline

### How It Works

When you call `Remember`, cortex runs a hybrid extraction pipeline:

1. **Deterministic extraction** (free, fast, reliable):
   - YAML frontmatter: `type: person`, `name: Alice` becomes an entity
   - Wikilinks: `[[Stripe]]` becomes a document entity and a relationship
   - Email headers: From/To/Cc become person entities with email attributes
   - Calendar events: attendees become person entities with "attended" relationships

2. **LLM extraction** (powerful, costs API calls):
   - Sends unstructured prose to OpenAI with a structured extraction prompt
   - Returns entities, relationships, and distilled memory facts as JSON
   - Only runs on content the deterministic extractor can't handle

The hybrid extractor runs deterministic first, then LLM, and merges results.

### Custom Extractors

Implement the `Extractor` interface to add your own extraction logic:

```go
type Extractor interface {
    Extract(ctx context.Context, content string, contentType string) (*Extraction, error)
}

type Extraction struct {
    Entities      []Entity
    Relationships []Relationship
    Memories      []Memory
}
```

## Search Strategy

`Recall` uses a multi-step process to answer natural language queries:

### 1. Query Decomposition

The LLM breaks the query into structured sub-queries:

```
"What do I know about Alice's work at Stripe?"
  -> memory_lookup:   {query: "Alice Stripe"}
  -> graph_traverse:  {entity: "Alice", edge: "works_at"}
  -> vector_search:   {query: "Alice work Stripe"}
```

### 2. Parallel Execution

All sub-queries run concurrently:

| Strategy | Source | Best For |
|----------|--------|----------|
| Memory lookup | `memories` table | Distilled facts, highest signal |
| Graph traversal | `entities` + `relationships` | Relationship queries, "who knows who" |
| Vector search | `embeddings` (cosine similarity) | Semantic similarity |
| Keyword search | FTS5 on `chunks` | Exact matches, names, identifiers |

### 3. Reciprocal Rank Fusion

Results from all strategies are merged using RRF — a ranking algorithm that combines ranked lists without requiring score normalization across different search backends. Items appearing in multiple search results get boosted.

## Connectors

### Markdown

Syncs a directory of `.md` files with incremental change detection.

```bash
cortex sync markdown ~/notes
```

- Parses YAML frontmatter for entity type and name
- Detects `[[wikilinks]]` as relationships
- Splits body into chunks for search
- Tracks file modification times — only re-processes changed files

**Frontmatter format:**
```yaml
---
type: person
name: Alice
email: alice@example.com
tags: engineering, leadership
---
Alice is a staff engineer at [[Stripe]].
She knows [[Bob]] from their time at [[Google]].
```

### Conversation

Inline ingestion from chat messages — designed for agent integration, not batch sync.

```go
import "github.com/sausheong/cortex/connector/conversation"

conv := conversation.New()
conv.Ingest(ctx, c, []conversation.Message{
    {Role: "user", Content: "Had lunch with Alice, she's joining Stripe next month"},
    {Role: "assistant", Content: "I'll remember that Alice is joining Stripe."},
})
```

### Gmail

Syncs emails via the Gmail API with OAuth2. Deterministic extraction on headers (From/To/Cc become person entities), LLM extraction on email bodies.

### Google Calendar

Syncs events via the Google Calendar API with OAuth2. Attendees become person entities with "attended" relationships to event entities.

### Google OAuth2 Setup (Gmail & Calendar)

The Gmail and Calendar connectors require a pre-built Google API service with OAuth2 credentials:

1. Create a project in [Google Cloud Console](https://console.cloud.google.com)
2. Enable the Gmail API and/or Google Calendar API
3. Create OAuth2 credentials (Desktop application type)
4. Download the credentials JSON file

Then use the Go API to set up the OAuth2 flow and create the connector:

```go
import (
    "context"
    "golang.org/x/oauth2/google"
    "google.golang.org/api/gmail/v1"
    "google.golang.org/api/calendar/v3"
    "google.golang.org/api/option"

    gmailconn "github.com/sausheong/cortex/connector/gmail"
    calconn "github.com/sausheong/cortex/connector/calendar"
)

// Create OAuth2 config from credentials file
b, _ := os.ReadFile("credentials.json")
config, _ := google.ConfigFromJSON(b,
    gmail.GmailReadonlyScope,
    calendar.CalendarReadonlyScope,
)

// Obtain token (implement your own token exchange flow)
token := getTokenFromUser(config) // see Google's OAuth2 docs

// Create service clients
tokenSource := config.TokenSource(ctx, token)
gmailService, _ := gmail.NewService(ctx, option.WithTokenSource(tokenSource))
calService, _ := calendar.NewService(ctx, option.WithTokenSource(tokenSource))

// Sync
gmailConn := gmailconn.New(gmailService)
gmailConn.Sync(ctx, cx)

calConn := calconn.New(calService)
calConn.Sync(ctx, cx)
```

## Storage

Cortex uses embedded SQLite (via `modernc.org/sqlite`, pure Go — no CGo required) with a single database file.

### Schema

| Table | Purpose |
|-------|---------|
| `entities` | Graph nodes with type, name, JSON attributes, source |
| `relationships` | Directed typed edges between entities |
| `chunks` | Text fragments linked to entities |
| `chunks_fts` | FTS5 virtual table for keyword search |
| `memories` | Distilled facts extracted by the LLM |
| `memory_entities` | Junction table linking memories to entities |
| `embeddings` | Vector embeddings stored as BLOBs |
| `sync_state` | Connector sync state (last sync time, cursor, etc.) |

### Design Choices

- **WAL mode** for concurrent reads while writing
- **FTS5** for fast full-text keyword search
- **Brute-force cosine similarity** for vector search (stored as BLOBs, computed in Go). Performant for personal-scale data (<100K vectors). Can be swapped for sqlite-vec if Go bindings mature.
- **ULIDs** for entity IDs — time-sortable, globally unique, URL-safe
- **Open-ended relationship types** — no enum, connectors introduce new types organically

## Pluggable Providers

| Interface | Methods | Shipped Implementation |
|-----------|---------|----------------------|
| `LLM` | `Extract`, `Decompose`, `Summarize` | OpenAI GPT-4o-mini |
| `Embedder` | `Embed`, `Dimensions` | OpenAI text-embedding-3-small (1536 dims) |
| `Extractor` | `Extract` | Hybrid (deterministic + LLM) |

### Implementing a Custom Provider

```go
// Example: custom LLM provider
type MyLLM struct { /* ... */ }

func (m *MyLLM) Extract(ctx context.Context, text, prompt string) (cortex.ExtractionResult, error) {
    // Call your model, parse response into Extraction struct
}

func (m *MyLLM) Decompose(ctx context.Context, query string) ([]cortex.StructuredQuery, error) {
    // Break query into sub-queries
}

func (m *MyLLM) Summarize(ctx context.Context, texts []string) (string, error) {
    // Summarize texts
}

// Use it
c, _ := cortex.Open("brain.db", cortex.WithLLM(&MyLLM{}))
```

## Environment Variables

| Variable | Default | Used By | Description |
|----------|---------|---------|-------------|
| `OPENAI_API_KEY` | (none) | CLI, MCP, HTTP | Enables LLM extraction + semantic search |
| `CORTEX_DB` | `brain.db` | MCP, HTTP | Database file path |
| `CORTEX_PORT` | `8080` | HTTP | HTTP server port |

## Running Tests

```bash
# Run all tests (no API key needed — uses mocks)
go test ./...

# Run with verbose output
go test ./... -v

# Run OpenAI integration tests (requires API key)
OPENAI_API_KEY=sk-... go test ./llm/openai/ -v
```

## License

MIT
