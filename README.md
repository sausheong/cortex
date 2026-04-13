# Cortex

A personal knowledge graph and memory system in Go. Cortex stores everything you know — people, organizations, concepts, events, documents — as a unified graph with typed relationships. It extracts entities and facts from your notes, conversations, and other sources, then lets any AI agent query the graph via MCP, HTTP, or the Go API.

Inspired by [GBrain](https://github.com/garrytan/gbrain) (operational model), [Cognee](https://github.com/topoteretes/cognee) (graph structure), and [mem0](https://github.com/mem0ai/mem0) (memory pipeline).

## Quick Start

```bash
# Build
go build -o cortex ./cmd/cortex/

# Initialize a brain
./cortex init

# Remember something
./cortex remember "Alice works at Stripe as a staff engineer"

# Query the knowledge graph
./cortex recall "who works at Stripe"

# Sync a directory of markdown notes
./cortex sync markdown /path/to/notes

# List entities
./cortex entity list --type person

# Forget by source
./cortex forget --source gmail
```

Set `OPENAI_API_KEY` for LLM-powered extraction (entity/relationship discovery from prose) and semantic search. Without it, only deterministic extraction (YAML frontmatter + wikilinks) and keyword search are available.

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
│   └── markdown/            # Markdown directory connector
├── cmd/
│   ├── cortex/              # CLI
│   ├── cortex-mcp/          # MCP stdio server
│   └── cortex-http/         # HTTP/REST server
└── internal/testutil/       # Test mocks and helpers
```

**Core Graph + Pluggable Connectors**: the core knows nothing about data sources. Connectors are independent packages that parse source-specific formats and call the core API.

## Use as a Go Library

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

    // Remember
    c.Remember(ctx, "Alice works at Stripe as a staff engineer")

    // Recall
    results, _ := c.Recall(ctx, "who works at Stripe")
    for _, r := range results {
        fmt.Printf("[%s] %s (score: %.3f)\n", r.Type, r.Content, r.Score)
    }

    // Structured queries
    entities, _ := c.FindEntities(ctx, cortex.EntityFilter{Type: "person"})
    rels, _ := c.GetRelationships(ctx, entities[0].ID)
    graph, _ := c.Traverse(ctx, entities[0].ID, cortex.WithDepth(2))
}
```

## API

### High-Level (mem0-style)

| Method | Description |
|--------|-------------|
| `Remember(ctx, content, ...RememberOption)` | Ingest text, extract entities/relationships/memories, embed |
| `Recall(ctx, query, ...RecallOption)` | Natural language query with multi-strategy search + RRF |
| `Forget(ctx, Filter)` | Remove knowledge by entity ID or source |

### Structured Graph

| Method | Description |
|--------|-------------|
| `PutEntity(ctx, *Entity)` | Create or update entity (upsert by name+type) |
| `GetEntity(ctx, id)` | Get entity by ID |
| `FindEntities(ctx, EntityFilter)` | Search by type, name pattern, source |
| `PutRelationship(ctx, *Relationship)` | Create a relationship |
| `GetRelationships(ctx, entityID, ...RelFilter)` | Get relationships for entity |
| `Traverse(ctx, startID, ...TraverseOption)` | BFS graph walk with depth/edge filters |

### Search Primitives

| Method | Description |
|--------|-------------|
| `SearchKeyword(ctx, query, limit)` | FTS5 full-text search on chunks |
| `SearchVector(ctx, query, limit)` | Cosine similarity on embeddings |
| `SearchMemories(ctx, query, limit)` | Keyword search on distilled memories |

## MCP Server

Connect cortex to Claude Code, Cursor, Windsurf, or any MCP client.

```bash
go build -o cortex-mcp ./cmd/cortex-mcp/
```

**Claude Code** (`~/.claude/server.json`):
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

**Exposed tools:** `remember`, `recall`, `forget`, `get_entity`, `find_entities`, `get_relationships`, `traverse`, `search`

## HTTP/REST Server

```bash
go build -o cortex-http ./cmd/cortex-http/

OPENAI_API_KEY=sk-... CORTEX_DB=brain.db ./cortex-http
# Listening on :8080
```

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/remember` | `{"content": "...", "source": "..."}` |
| GET | `/recall?q=...&limit=10` | Natural language query |
| DELETE | `/forget?entity_id=...` | Remove knowledge |
| GET | `/entity/{id}` | Get entity |
| GET | `/entities?type=person` | Search entities |
| GET | `/relationships/{entity_id}` | Get relationships |
| GET | `/traverse/{entity_id}?depth=2` | Walk graph |
| GET | `/search?q=...&mode=keyword` | Direct search |

## Knowledge Model

Cortex uses a unified graph where all node types are first-class:

- **Entities**: person, organization, concept, event, document
- **Relationships**: typed directed edges (works_at, knows, discussed_in, etc.)
- **Chunks**: text fragments linked to entities, indexed for keyword + vector search
- **Memories**: distilled facts extracted by the LLM, higher signal than raw chunks

## Extraction Pipeline

**Hybrid extraction** — deterministic first, LLM fills in gaps:

1. **Deterministic**: YAML frontmatter (`type: person`, `name: Alice`), wikilinks (`[[Stripe]]`)
2. **LLM**: Sends unstructured prose to OpenAI, extracts entities, relationships, and memories as structured JSON

The extraction pipeline is pluggable. Implement the `Extractor` interface to add custom extraction logic.

## Search Strategy

`Recall` uses multi-strategy search with query decomposition:

1. **Decomposition**: LLM breaks natural language query into structured sub-queries
2. **Parallel execution**: memory lookup, keyword search, vector search, graph traversal run concurrently
3. **Reciprocal Rank Fusion**: merges results from different search types without score normalization

## Storage

Embedded SQLite (via `modernc.org/sqlite`, pure Go) with a single database file:

- WAL mode for concurrent reads
- FTS5 for keyword search
- Vector embeddings stored as BLOBs with brute-force cosine similarity
- No external database server required

## Pluggable Providers

| Interface | Purpose | Shipped Implementation |
|-----------|---------|----------------------|
| `LLM` | Extraction, query decomposition, summarization | OpenAI GPT-4o-mini |
| `Embedder` | Vector embeddings | OpenAI text-embedding-3-small |
| `Extractor` | Entity/relationship extraction | Hybrid (deterministic + LLM) |

Implement the interface to swap in Anthropic, Ollama, or any other provider.

## Connectors

| Connector | Status | Description |
|-----------|--------|-------------|
| Markdown | Available | Sync a directory of .md files with incremental change detection |
| Conversation | Planned | Inline ingestion from chat messages |
| Gmail | Planned | Email sync via Gmail API |
| Calendar | Planned | Event sync via Google Calendar API |

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `OPENAI_API_KEY` | (none) | Enables LLM extraction + semantic search |
| `CORTEX_DB` | `brain.db` | Database file path (MCP/HTTP servers) |
| `CORTEX_PORT` | `8080` | HTTP server port |

## License

MIT
