# Cortex Design Spec

A personal knowledge graph, memory system, and digital twin — built in Go as an embeddable library with CLI, MCP, and HTTP interfaces.

Inspired by GBrain (operational model), Cognee (graph structure), and mem0 (memory pipeline).

## Goals

- Store and connect everything you know: people, organizations, concepts, events, documents
- Ingest from multiple sources: markdown files, conversations, Gmail, Google Calendar
- Extract entities and relationships automatically (hybrid deterministic + LLM)
- Queryable by any agent via MCP, HTTP, or direct Go API
- Single binary, single database file, no external dependencies at runtime

## Non-Goals

- Multi-user / multi-tenant (this is a personal tool)
- Real-time streaming ingestion
- Web UI (CLI and agent interfaces are sufficient initially)
- Hosting or cloud deployment (runs locally)

---

## Architecture

```
cortex/
├── core/                    # the heart — graph, search, Remember/Recall/Forget
│   ├── cortex.go            # Cortex struct, Open(), Close()
│   ├── entity.go            # entity types and CRUD
│   ├── relationship.go      # relationship types and CRUD
│   ├── memory.go            # memory types and CRUD
│   ├── chunk.go             # chunk types and CRUD
│   ├── search.go            # vector, keyword, memory search
│   ├── recall.go            # Recall with query decomposition + RRF
│   └── store.go             # SQLite + sqlite-vec setup, migrations, schema
│
├── llm/                     # pluggable LLM/embedder interfaces
│   ├── llm.go               # LLM, Embedder, Extractor interfaces
│   └── openai/              # default OpenAI implementation
│       ├── llm.go
│       └── embedder.go
│
├── extractor/               # entity/relationship extraction
│   ├── deterministic/       # regex, headers, frontmatter, wikilinks
│   ├── llm/                 # LLM-powered extraction with prompts
│   └── hybrid/              # composes deterministic + llm
│
├── connector/               # data source connectors
│   ├── connector.go         # Connector interface
│   ├── markdown/
│   ├── conversation/
│   ├── gmail/
│   └── calendar/
│
├── cmd/                     # executable entry points
│   ├── cortex/              # CLI
│   ├── cortex-mcp/          # MCP stdio server
│   └── cortex-http/         # HTTP/REST server
│
├── go.mod
└── go.sum
```

The design follows **Core Graph + Pluggable Connectors**: the core knows nothing about data sources. Connectors are independent packages that parse source-specific formats and call the core API.

---

## Knowledge Graph Schema

Five SQLite tables model a unified graph where people, organizations, concepts, events, and documents are all first-class node types with typed edges between them.

### entities

| Column     | Type     | Description                                              |
|------------|----------|----------------------------------------------------------|
| id         | TEXT PK  | ULID                                                     |
| type       | TEXT     | "person", "organization", "concept", "event", "document" |
| name       | TEXT     | Display name                                             |
| attributes | TEXT     | JSON blob for type-specific data (email, url, role, etc.)|
| source     | TEXT     | Origin: "markdown", "conversation", "gmail", "calendar"  |
| created_at | DATETIME |                                                          |
| updated_at | DATETIME |                                                          |

### relationships

| Column     | Type     | Description                                              |
|------------|----------|----------------------------------------------------------|
| id         | TEXT PK  | ULID                                                     |
| source_id  | TEXT FK  | References entities.id                                   |
| target_id  | TEXT FK  | References entities.id                                   |
| type       | TEXT     | Open-ended: "works_at", "knows", "discussed_in", etc.    |
| attributes | TEXT     | JSON blob (strength, context, etc.)                      |
| source     | TEXT     | Where this relationship was detected                     |
| created_at | DATETIME |                                                          |

### chunks

| Column     | Type     | Description                                              |
|------------|----------|----------------------------------------------------------|
| id         | TEXT PK  | ULID                                                     |
| entity_id  | TEXT FK  | References entities.id (nullable for standalone chunks)   |
| content    | TEXT     | The actual text fragment                                 |
| metadata   | TEXT     | JSON (file path, line range, message id, etc.)           |
| created_at | DATETIME |                                                          |

### embeddings

sqlite-vec virtual table for vector similarity search.

| Column    | Type       | Description                         |
|-----------|------------|-------------------------------------|
| chunk_id  | TEXT FK    | References chunks.id                |
| embedding | FLOAT[N]   | Vector (dimension depends on model) |

### memories

Distilled facts extracted by the LLM. Higher signal than raw chunks.

| Column     | Type     | Description                                              |
|------------|----------|----------------------------------------------------------|
| id         | TEXT PK  | ULID                                                     |
| content    | TEXT     | "Alice is moving to Berlin in March"                     |
| entity_ids | TEXT     | JSON array of related entity IDs                         |
| source     | TEXT     | Provenance                                               |
| created_at | DATETIME |                                                          |
| updated_at | DATETIME |                                                          |

### memory_entities

Junction table for querying memories by entity. Avoids scanning JSON arrays.

| Column     | Type     | Description                                              |
|------------|----------|----------------------------------------------------------|
| memory_id  | TEXT FK  | References memories.id                                   |
| entity_id  | TEXT FK  | References entities.id                                   |

Primary key is `(memory_id, entity_id)`. Indexed on `entity_id` for "what memories mention Alice?" lookups.

### Embedding coverage

The `embeddings` virtual table stores vectors for both `chunks` and `memories`. The `chunk_id` column references either table — a `type` column distinguishes them:

| Column    | Type       | Description                            |
|-----------|------------|----------------------------------------|
| row_id    | TEXT FK    | References chunks.id or memories.id    |
| row_type  | TEXT       | "chunk" or "memory"                    |
| embedding | FLOAT[N]   | Vector (dimension depends on model)    |

This allows vector search to return both raw chunks and distilled memories in one query.

### Notes

Relationship types are open-ended strings, not an enum. This keeps the schema flexible as new connectors introduce new relationship types.

---

## Core Library API

### Entry Point

```go
type Cortex struct { ... }

func Open(dbPath string, opts ...Option) (*Cortex, error)
func (c *Cortex) Close() error
```

### High-Level Memory Operations

```go
// Remember ingests content, extracts entities/relationships/memories, stores everything.
func (c *Cortex) Remember(ctx context.Context, content string, opts ...RememberOption) error

// Recall answers a query using decomposition + parallel search + RRF merge.
func (c *Cortex) Recall(ctx context.Context, query string, opts ...RecallOption) ([]Result, error)

// Forget removes entities, relationships, and memories matching the filter.
func (c *Cortex) Forget(ctx context.Context, filter Filter) error
```

### Structured Graph Operations

```go
// Entity CRUD
func (c *Cortex) PutEntity(ctx context.Context, entity Entity) error
func (c *Cortex) GetEntity(ctx context.Context, id string) (*Entity, error)
func (c *Cortex) FindEntities(ctx context.Context, filter EntityFilter) ([]Entity, error)

// Relationship CRUD
func (c *Cortex) PutRelationship(ctx context.Context, rel Relationship) error
func (c *Cortex) GetRelationships(ctx context.Context, entityID string, opts ...RelFilter) ([]Relationship, error)

// Graph traversal
func (c *Cortex) Traverse(ctx context.Context, startID string, opts ...TraverseOption) (*Graph, error)

// Search primitives
func (c *Cortex) SearchVector(ctx context.Context, query string, limit int) ([]Chunk, error)
func (c *Cortex) SearchKeyword(ctx context.Context, query string, limit int) ([]Chunk, error)
func (c *Cortex) SearchMemories(ctx context.Context, query string, limit int) ([]Memory, error)
```

### Result Type

```go
type Result struct {
    Type      string         // "memory", "entity", "chunk", "relationship"
    Content   string         // displayable text
    Score     float64        // RRF score
    EntityIDs []string       // related entities for follow-up traversal
    Source    string         // provenance
    Metadata  map[string]any
}
```

### Initialization Options

```go
cortex.WithLLM(llm LLM)
cortex.WithEmbedder(embedder Embedder)
cortex.WithExtractor(extractor Extractor)
cortex.WithSource(source string)
cortex.WithUserID(userID string)
```

---

## Pluggable Interfaces

### LLM

```go
type LLM interface {
    Extract(ctx context.Context, text string, prompt string) (ExtractionResult, error)
    Decompose(ctx context.Context, query string) ([]StructuredQuery, error)
    Summarize(ctx context.Context, texts []string) (string, error)
}
```

### Embedder

```go
type Embedder interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
    Dimensions() int
}
```

### Extractor

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

### Shipped Implementations

| Interface | Package          | Description                                        |
|-----------|------------------|----------------------------------------------------|
| LLM       | `llm/openai`     | OpenAI GPT-4.1-mini for extraction/decomposition   |
| Embedder  | `llm/openai`     | OpenAI text-embedding-3-small                      |
| Extractor | `extractor/hybrid`| Composes deterministic + LLM extraction            |
| Extractor | `extractor/deterministic` | Regex, headers, frontmatter, wikilinks    |
| Extractor | `extractor/llm`  | LLM-powered with extraction-specific prompts       |

Users wire these up at initialization:

```go
c, err := cortex.Open("brain.db",
    cortex.WithLLM(openai.NewLLM(apiKey)),
    cortex.WithEmbedder(openai.NewEmbedder(apiKey)),
    cortex.WithExtractor(hybrid.New(deterministic.New(), llmextractor.New(llm))),
)
```

---

## Connectors

### Interface

```go
type Connector interface {
    Sync(ctx context.Context, c *cortex.Cortex) error
}
```

Each connector manages its own sync state by storing a metadata record in cortex (last sync timestamp, cursor/history ID), so `Sync()` is always safe to re-run.

### Markdown Connector (Priority 1)

- Reads a directory of `.md` files recursively
- Parses YAML frontmatter for structured metadata (type, tags, related entities)
- Detects wikilinks (`[[Alice]]`) as relationships deterministically
- Splits body into chunks for embedding
- Sends unstructured prose through the LLM extractor for entity/relationship discovery
- Tracks file modification times for incremental sync

```go
md := markdown.New("/path/to/brain", markdown.WithGlob("**/*.md"))
md.Sync(ctx, c)
```

### Conversation Connector (Priority 2)

- Accepts chat messages (role + content pairs)
- Runs every message through the LLM extractor for entities, relationships, and memories
- Implements the compounding loop: read from cortex before responding, write back after
- Designed for inline use from an agent, not batch sync

```go
conv := conversation.New()
conv.Ingest(ctx, c, []conversation.Message{
    {Role: "user", Content: "Had lunch with Alice, she's joining Stripe next month"},
})
```

### Gmail Connector (Priority 3)

- Uses Gmail API with OAuth2
- Deterministic extraction: From/To/Cc headers → person entities, subject + body → chunks
- LLM extraction on body for relationships and memories
- Incremental sync via Gmail history ID

### Calendar Connector (Priority 4)

- Uses Google Calendar API with OAuth2
- Deterministic extraction: attendees → person entities, event → event entity
- Links attendees to events via `attended` relationships
- LLM extraction on event description/notes for additional context

---

## Search & Recall Strategy

### Query Flow

1. **Decomposition**: LLM breaks natural language query into structured sub-queries
2. **Parallel execution**: All sub-queries run concurrently
3. **Merge & rank**: Reciprocal Rank Fusion (RRF) combines results across search types
4. **Return**: Ranked `[]Result` with provenance and entity links

### Search Types

| Type            | Source              | Best For                              |
|-----------------|---------------------|---------------------------------------|
| Memory lookup   | `memories` table    | Distilled facts, highest signal       |
| Graph traversal | `entities` + `relationships` | Relationship queries, "who knows who" |
| Vector search   | `embeddings` via sqlite-vec | Semantic similarity                   |
| Keyword search  | FTS5 on `chunks`    | Exact matches, names, identifiers     |

### Example Decomposition

```
Query: "What do I know about Alice's work at Stripe?"

Sub-queries:
  1. memory_lookup:   {query: "Alice Stripe"}
  2. graph_traverse:  {entity: "Alice", edge: "works_at"}
  3. vector_search:   {query: "Alice work Stripe"}
```

Results from all sub-queries are merged via RRF into a single ranked list.

---

## Transport Layers

### CLI (`cmd/cortex`)

```
cortex init                            # create brain.db
cortex remember "Alice works at Stripe"
cortex recall "who works at Stripe"
cortex sync markdown /path/to/notes
cortex sync gmail
cortex entity list --type person
cortex entity get <id>
```

### MCP Server (`cmd/cortex-mcp`)

Exposes core operations as MCP tools via stdio: `remember`, `recall`, `forget`, `get_entity`, `find_entities`, `get_relationships`, `traverse`, `search`.

### HTTP Server (`cmd/cortex-http`)

Same operations as REST endpoints for non-MCP agents, webhooks, and external tools.

Both wrappers are thin — parse input, call core library, format output. No business logic in the transport layer.

---

## Key Dependencies

| Dependency                | Purpose                                  |
|---------------------------|------------------------------------------|
| `modernc.org/sqlite`      | Pure Go SQLite (no CGo)                  |
| `github.com/nicois/sqlite-vec` or equivalent | sqlite-vec bindings for Go |
| `github.com/oklog/ulid`   | ULID generation for entity IDs           |
| `github.com/sashabaranov/go-openai` | OpenAI API client             |
| `github.com/mark3labs/mcp-go` | MCP server implementation             |

Note: sqlite-vec Go bindings may require CGo depending on available packages. If a pure-Go sqlite-vec binding doesn't exist, we'll use CGo bindings with `mattn/go-sqlite3` + sqlite-vec extension, or evaluate alternatives like building a minimal vector search on top of pure-Go SQLite.

---

## Implementation Order

1. Core schema + SQLite setup (`core/store.go`)
2. Entity and relationship CRUD (`core/entity.go`, `core/relationship.go`)
3. LLM and Embedder interfaces + OpenAI implementation (`llm/`)
4. Extractor interfaces + deterministic extractor (`extractor/`)
5. Chunk storage + embedding + vector search (`core/chunk.go`, `core/search.go`)
6. Memory storage + search (`core/memory.go`)
7. `Remember` flow — wire extraction → storage (`core/cortex.go`)
8. `Recall` flow — query decomposition → parallel search → RRF (`core/recall.go`)
9. Markdown connector (`connector/markdown/`)
10. CLI (`cmd/cortex/`)
11. MCP server (`cmd/cortex-mcp/`)
12. HTTP server (`cmd/cortex-http/`)
13. Conversation connector (`connector/conversation/`)
14. Gmail connector (`connector/gmail/`)
15. Calendar connector (`connector/calendar/`)
