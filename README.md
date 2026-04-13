# Cortex

A personal knowledge graph and memory system in Go. Cortex stores everything you know — people, organizations, concepts, events, documents — as a unified graph with typed relationships. It extracts entities and facts from your notes, conversations, emails, and calendar, then lets any AI agent query the graph via MCP, HTTP, or the Go API.

Think of it as a digital twin that compounds knowledge over time. Agents read from cortex before responding and write back after every conversation. The more you use it, the smarter it gets.

Inspired by [GBrain](https://github.com/garrytan/gbrain) (operational model), [Cognee](https://github.com/topoteretes/cognee) (graph structure), and [mem0](https://github.com/mem0ai/mem0) (memory pipeline).

## How It Works

```
Signal arrives (note, email, meeting, conversation)
  -> Hybrid extraction (deterministic + LLM)
     -> Entities: Alice (person), Stripe (org), distributed systems (concept)
     -> Relationships: Alice --works_at--> Stripe
     -> Memories: "Alice is joining Stripe next month"
  -> Store in unified graph (SQLite)
  -> Embed for semantic search

Query arrives ("What do I know about Alice?")
  -> LLM decomposes into sub-queries
  -> Parallel search: memories + keywords + vectors + graph traversal
  -> Reciprocal Rank Fusion merges results
  -> Ranked results with provenance
```

Every cycle through this loop adds knowledge. The agent enriches a person page after a meeting. Next time that person comes up, the agent already has context. You never start from zero.

## Features

- **Unified knowledge graph** — people, organizations, concepts, events, documents as first-class nodes with typed relationships
- **Remember / Recall / Forget** — simple high-level API inspired by mem0
- **Hybrid extraction** — deterministic parsing (frontmatter, wikilinks, email headers) + LLM-powered entity/relationship discovery
- **Multi-strategy search** — keyword (FTS5), vector (cosine similarity), graph traversal, and memory lookup merged via reciprocal rank fusion
- **Four data connectors** — markdown files, conversations, Gmail, Google Calendar
- **Three interfaces** — CLI, MCP stdio server, HTTP/REST API
- **Single binary, single file** — embedded SQLite with pure Go driver, no external database, no CGo
- **Pluggable providers** — swap OpenAI for Anthropic, Ollama, or any custom implementation

## Table of Contents

- [Prerequisites](#prerequisites)
- [Installation](#installation)
- [Setup](#setup)
- [CLI Reference](#cli-reference)
- [MCP Server](#using-as-an-mcp-server)
- [HTTP/REST Server](#using-as-an-httprest-server)
- [Go Library](#using-as-a-go-library)
- [Architecture](#architecture)
- [API Reference](#core-api-reference)
- [Knowledge Model](#knowledge-model)
- [Extraction Pipeline](#extraction-pipeline)
- [Search Strategy](#search-strategy)
- [Connectors](#connectors)
- [Storage](#storage)
- [Pluggable Providers](#pluggable-providers)
- [Testing](#running-tests)

---

## Prerequisites

- **Go 1.22+** (uses `net/http` method-based routing from Go 1.22)
- **OpenAI API key** (optional but recommended) — enables LLM extraction and semantic search

Without an OpenAI key, cortex still works but is limited to deterministic extraction (YAML frontmatter + wikilinks) and keyword search only. No semantic search, no entity extraction from prose.

## Installation

### From Source

```bash
git clone https://github.com/sausheong/cortex.git
cd cortex

# Build all three binaries to bin/
make

# Or build individually
make build          # cortex, cortex-mcp, cortex-http -> bin/

# Optional: install to /usr/local/bin
sudo make install
```

### As a Go Module

```bash
go get github.com/sausheong/cortex
```

This gives you the core library, all connectors, and all provider implementations as importable packages.

---

## Setup

### Step 1: Initialize the Brain

```bash
cortex init
```

This creates `brain.db` in the current directory — a single SQLite file containing the entire knowledge graph, vector embeddings, FTS5 search indexes, and sync state. Everything lives in this one file.

### Step 2: Set Your OpenAI API Key (Recommended)

```bash
export OPENAI_API_KEY=sk-...
```

Add this to your `~/.zshrc` or `~/.bashrc` to persist across sessions.

With an API key, cortex will:
- Use **GPT-4o-mini** to extract entities, relationships, and distilled facts from any text
- Use **text-embedding-3-small** to generate 1536-dimensional vector embeddings for semantic search
- Use LLM-powered **query decomposition** to break natural language queries into multi-strategy sub-queries

Without it, cortex falls back to deterministic extraction only (YAML frontmatter + wikilinks) and keyword search.

### Step 3: Ingest Your Data

**Sync your markdown notes:**
```bash
cortex sync markdown ~/notes
```

Cortex will recursively find all `.md` files, parse frontmatter and wikilinks deterministically, run LLM extraction on the body text (if API key is set), and store everything in the graph. Incremental — only re-processes files that changed since the last sync.

**Remember ad-hoc facts:**
```bash
cortex remember "Alice works at Stripe as a staff engineer"
cortex remember "Bob and Alice went to Stanford together"
cortex remember "Meeting with Carol next Tuesday to discuss the Series A"
```

Each call runs the full extraction pipeline: entities are created or merged, relationships are discovered, and distilled memory facts are stored.

### Step 4: Query

```bash
# Natural language queries
cortex recall "who works at Stripe"
cortex recall "what do I know about Alice"
cortex recall "who should I invite to dinner who knows both Alice and Bob"

# Browse the graph
cortex entity list --type person
cortex entity get <entity-id>
```

Example output from `cortex recall "who works at Stripe"`:
```
[1] (memory, score=0.0323) Alice works at Stripe as a staff engineer
    source: cli
[2] (chunk, score=0.0161) Alice works at Stripe as a staff engineer
[3] (entity, score=0.0161) Alice (person)
[4] (entity, score=0.0161) Stripe (organization)
```

---

## CLI Reference

```
cortex init                           Create brain.db in the current directory
cortex remember <text>                Ingest text, extract entities/relationships/memories
cortex recall <query>                 Natural language query with multi-strategy search
cortex sync markdown <dir>            Sync .md files from a directory (incremental)
cortex sync gmail                     Sync Gmail (requires OAuth2 — see Connectors)
cortex sync calendar                  Sync Google Calendar (requires OAuth2 — see Connectors)
cortex entity list [--type <type>]    List entities, optionally filtered by type
cortex entity get <id>                Show entity details, attributes, and relationships
cortex forget --source <src>          Remove all knowledge from a source
cortex forget --entity <id>           Remove a specific entity and all linked data
```

---

## Using as an MCP Server

Connect cortex to Claude Code, Cursor, Windsurf, or any MCP-compatible AI tool. The MCP server exposes 8 tools over stdio.

### Build

```bash
make build    # builds all binaries to bin/
```

### Claude Code Setup

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

### Cursor / Windsurf Setup

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

### Available MCP Tools

| Tool | Description | Required Params | Optional Params |
|------|-------------|-----------------|-----------------|
| `remember` | Store content in the knowledge graph | `content` | `source` |
| `recall` | Natural language query with multi-strategy search | `query` | `limit` |
| `forget` | Remove knowledge from the graph | `entity_id` or `source` | |
| `get_entity` | Retrieve an entity by ID | `id` | |
| `find_entities` | Search for entities | | `type`, `name`, `source` |
| `get_relationships` | Get relationships for an entity | `entity_id` | `type` |
| `traverse` | BFS walk of the knowledge graph | `start_id` | `depth`, `edge_types` |
| `search` | Direct keyword, vector, or memory search | `query`, `mode` | `limit` |

The `search` tool's `mode` parameter accepts `keyword`, `vector`, or `memory` to select the search strategy directly, bypassing query decomposition.

The `traverse` tool's `edge_types` parameter accepts a comma-separated list of relationship types to follow (e.g., `"works_at,knows"`).

---

## Using as an HTTP/REST Server

### Build and Run

```bash
make build

OPENAI_API_KEY=sk-... CORTEX_DB=brain.db bin/cortex-http
# cortex-http listening on :8080

# Or use the make shortcut:
make run-http
```

### Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| POST | `/remember` | Ingest content |
| GET | `/recall` | Natural language query |
| DELETE | `/forget` | Remove knowledge |
| GET | `/entity/{id}` | Get entity by ID |
| GET | `/entities` | Search entities |
| GET | `/relationships/{entity_id}` | Get relationships |
| GET | `/traverse/{entity_id}` | Walk graph |
| GET | `/search` | Direct search |

### Examples

**Remember:**
```bash
curl -X POST localhost:8080/remember \
  -H "Content-Type: application/json" \
  -d '{"content": "Alice works at Stripe as a staff engineer", "source": "manual"}'

# Response: {"status": "remembered"}
```

**Recall:**
```bash
curl 'localhost:8080/recall?q=who+works+at+Stripe&limit=5'

# Response: [{"type":"memory","content":"Alice works at Stripe...","score":0.032,...}, ...]
```

**Find entities:**
```bash
curl 'localhost:8080/entities?type=person'

# Response: [{"id":"01J...","type":"person","name":"Alice",...}, ...]
```

**Traverse the graph:**
```bash
curl 'localhost:8080/traverse/01JXYZ...?depth=2&edge_types=works_at,knows'

# Response: {"entities":[...],"relationships":[...]}
```

**Direct search:**
```bash
curl 'localhost:8080/search?q=distributed+systems&mode=vector&limit=5'

# Response: [{"id":"...","content":"Alice works on distributed systems...",...}, ...]
```

**Forget:**
```bash
curl -X DELETE 'localhost:8080/forget?source=gmail'

# Response: {"status": "forgotten"}
```

All endpoints return JSON. Errors use `{"error": "message"}` with appropriate HTTP status codes (400 for bad input, 404 for not found, 500 for internal errors).

---

## Using as a Go Library

### Full Example

```go
package main

import (
    "context"
    "fmt"

    "github.com/sausheong/cortex"
    "github.com/sausheong/cortex/connector/conversation"
    "github.com/sausheong/cortex/connector/markdown"
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

    // --- Ingestion ---

    // Remember ad-hoc text
    c.Remember(ctx, "Alice works at Stripe as a staff engineer")

    // Sync markdown notes
    md := markdown.New("/path/to/notes")
    md.Sync(ctx, c)

    // Ingest conversation messages
    conv := conversation.New()
    conv.Ingest(ctx, c, []conversation.Message{
        {Role: "user", Content: "Had lunch with Bob, he's leaving Google"},
        {Role: "assistant", Content: "Noted — Bob is leaving Google."},
    })

    // --- Querying ---

    // Natural language recall (multi-strategy search + RRF)
    results, _ := c.Recall(ctx, "who works at Stripe")
    for _, r := range results {
        fmt.Printf("[%s] %s (score: %.3f)\n", r.Type, r.Content, r.Score)
    }

    // Structured graph queries
    people, _ := c.FindEntities(ctx, cortex.EntityFilter{Type: "person"})
    for _, p := range people {
        fmt.Printf("%s: %s\n", p.Name, p.ID)

        // Get their relationships
        rels, _ := c.GetRelationships(ctx, p.ID)
        for _, r := range rels {
            fmt.Printf("  --%s--> %s\n", r.Type, r.TargetID)
        }
    }

    // Graph traversal — BFS from an entity
    graph, _ := c.Traverse(ctx, people[0].ID,
        cortex.WithDepth(2),
        cortex.WithEdgeTypes("works_at", "knows"),
    )
    fmt.Printf("Traversal found %d entities, %d relationships\n",
        len(graph.Entities), len(graph.Relationships))

    // Direct search primitives
    chunks, _ := c.SearchKeyword(ctx, "distributed systems", 5)
    vectors, _ := c.SearchVector(ctx, "machine learning research", 5)
    memories, _ := c.SearchMemories(ctx, "Stripe", 5)

    // --- Cleanup ---

    // Forget all knowledge from a source
    c.Forget(ctx, cortex.Filter{Source: "gmail"})

    // Forget a specific entity (cascades to relationships, chunks, memories)
    c.Forget(ctx, cortex.Filter{EntityID: people[0].ID})
}
```

### Minimal Setup (No OpenAI)

```go
c, _ := cortex.Open("brain.db",
    cortex.WithExtractor(deterministic.New()),
)
defer c.Close()

// Works with frontmatter + wikilinks only
c.Remember(ctx, "---\ntype: person\nname: Alice\n---\nAlice works at [[Stripe]].")
```

---

## Architecture

```
                           ┌─────────────────┐
                           │   Data Sources   │
                           │                  │
                           │  Markdown files  │
                           │  Conversations   │
                           │  Gmail (OAuth2)  │
                           │  Calendar(OAuth2)│
                           └────────┬─────────┘
                                    │
                           ┌────────▼─────────┐
                           │   Connectors     │
                           │                  │
                           │  Parse source-   │
                           │  specific format │
                           │  Call Remember() │
                           └────────┬─────────┘
                                    │
                           ┌────────▼─────────┐
                           │  Hybrid Extractor│
                           │                  │
                           │  Deterministic:  │
                           │  frontmatter,    │
                           │  wikilinks,      │
                           │  email headers   │
                           │       +          │
                           │  LLM: prose →    │
                           │  entities, rels, │
                           │  memories        │
                           └────────┬─────────┘
                                    │
                    ┌───────────────┼───────────────┐
                    │               │               │
             ┌──────▼──────┐ ┌─────▼──────┐ ┌──────▼──────┐
             │  Entities   │ │   Chunks   │ │  Memories   │
             │  & Rels     │ │ + FTS5     │ │  + Links    │
             │             │ │ + Vectors  │ │  + Vectors  │
             └──────┬──────┘ └─────┬──────┘ └──────┬──────┘
                    │              │               │
                    └──────────────┼───────────────┘
                                   │
                          ┌────────▼─────────┐
                          │  SQLite (brain.db)│
                          │  Single file,    │
                          │  WAL mode        │
                          └────────┬─────────┘
                                   │
                    ┌──────────────┼──────────────┐
                    │              │              │
             ┌──────▼──────┐ ┌────▼─────┐ ┌─────▼─────┐
             │    CLI      │ │   MCP    │ │   HTTP    │
             │  cortex ... │ │  stdio   │ │  :8080    │
             └─────────────┘ └──────────┘ └───────────┘
```

### Package Structure

```
cortex/
├── *.go                     # Core library — graph, search, Remember/Recall/Forget
├── llm/
│   ├── openai/              # OpenAI + OpenAI-compatible LLM + Embedder
│   └── anthropic/           # Anthropic Claude LLM
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

---

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

// Provider options (used in Open)
cortex.WithLLM(myLLM)              // Set LLM provider
cortex.WithEmbedder(myEmbedder)    // Set embedding provider
cortex.WithExtractor(myExtractor)  // Set extraction provider
```

### Types

```go
type Entity struct {
    ID         string            // ULID, auto-generated
    Type       string            // "person", "organization", "concept", "event", "document"
    Name       string            // Display name
    Attributes map[string]any    // Type-specific data (email, url, role, etc.)
    Source     string            // Origin: "markdown", "conversation", "gmail", "calendar"
    CreatedAt  time.Time
    UpdatedAt  time.Time
}

type Relationship struct {
    ID         string            // ULID
    SourceID   string            // Entity ID
    TargetID   string            // Entity ID
    Type       string            // "works_at", "knows", "attended", etc.
    Attributes map[string]any
    Source     string
    CreatedAt  time.Time
}

type Memory struct {
    ID        string             // ULID
    Content   string             // "Alice is joining Stripe next month"
    EntityIDs []string           // Linked entity IDs
    Source    string
    CreatedAt time.Time
    UpdatedAt time.Time
}

type Result struct {
    Type      string             // "memory", "entity", "chunk", "relationship"
    Content   string             // Displayable text
    Score     float64            // RRF score
    EntityIDs []string           // Related entities for follow-up traversal
    Source    string             // Provenance
    Metadata  map[string]any
}
```

---

## Knowledge Model

Cortex uses a unified graph where all node types are first-class:

### Entities (Nodes)

| Type | Description | Examples |
|------|-------------|---------|
| `person` | People in your network | Alice, Bob, your contacts |
| `organization` | Companies and institutions | Stripe, Google, Stanford |
| `concept` | Ideas and topics | distributed systems, machine learning |
| `event` | Meetings, conferences, milestones | "Q4 planning meeting", "GopherCon 2024" |
| `document` | Notes, articles, emails | Linked from wikilinks or file ingestion |

Entity types are open-ended strings. You can introduce any type — the five above are conventions, not constraints.

### Relationships (Edges)

Typed directed edges between entities. Types are also open-ended — connectors introduce new types organically.

| Type | Example |
|------|---------|
| `works_at` | Alice --works_at--> Stripe |
| `knows` | Alice --knows--> Bob |
| `attended` | Alice --attended--> Q4 Planning |
| `discussed_in` | Series A --discussed_in--> Q4 Planning |
| `related_to` | Machine Learning --related_to--> AI |
| `part_of` | Alice --part_of--> Engineering Team |

### Chunks

Raw text fragments linked to entities. Indexed for both FTS5 keyword search and vector similarity search. Each chunk carries JSON metadata (file path, line range, message ID, etc.).

### Memories

Distilled facts extracted by the LLM. Higher signal density than raw chunks.

Examples:
- "Alice is joining Stripe next month"
- "Bob and Alice went to Stanford together"
- "The project deadline is March 15"
- "Carol prefers async communication"

Memories are linked to related entities via a junction table and are searched first during `Recall` because they're denser and more useful than raw text.

---

## Extraction Pipeline

### How It Works

When you call `Remember`, cortex runs a hybrid extraction pipeline:

**Step 1 — Deterministic extraction** (free, fast, reliable):
- **YAML frontmatter**: `type: person`, `name: Alice` becomes an entity
- **Wikilinks**: `[[Stripe]]` becomes a document entity
- **Email headers** (Gmail connector): From/To/Cc become person entities with email attributes
- **Calendar attendees**: become person entities with "attended" relationships

**Step 2 — LLM extraction** (powerful, costs API calls):
- Sends unstructured prose to OpenAI with a structured extraction prompt
- Receives entities, relationships, and distilled memory facts as JSON
- Processes content that the deterministic extractor can't handle

**Step 3 — Store and embed**:
- Entities are upserted (merged by name + type — no duplicates)
- Relationships are created between resolved entities
- Raw text is stored as chunks and indexed for FTS5 + vector search
- Memories are stored with entity links

### Custom Extractors

Implement the `Extractor` interface to add your own extraction logic:

```go
type Extractor interface {
    Extract(ctx context.Context, content string, contentType string) (*Extraction, error)
}
```

For example, you could write an extractor that handles Slack messages, Notion pages, or any domain-specific format.

---

## Search Strategy

`Recall` uses a multi-step process to answer natural language queries:

### Step 1: Query Decomposition

The LLM breaks the query into structured sub-queries:

```
Query: "What do I know about Alice's work at Stripe?"

Sub-queries:
  1. memory_lookup:   {query: "Alice Stripe"}
  2. graph_traverse:  {entity: "Alice", edge: "works_at"}
  3. vector_search:   {query: "Alice work Stripe"}
```

Without an LLM, cortex falls back to keyword search + memory lookup on the raw query text.

### Step 2: Parallel Execution

All sub-queries run concurrently using goroutines:

| Strategy | Source | Best For |
|----------|--------|----------|
| Memory lookup | `memories` table | Distilled facts — highest signal |
| Keyword search | FTS5 on `chunks` | Exact matches, names, identifiers |
| Vector search | Embeddings (cosine similarity) | Semantic similarity, fuzzy matching |
| Graph traversal | `entities` + `relationships` | Relationship queries ("who knows who") |

### Step 3: Reciprocal Rank Fusion (RRF)

Results from all strategies are merged using RRF — a ranking algorithm that combines ranked lists without requiring score normalization across different search backends.

Formula: `score(item) = sum(1 / (k + rank_in_list))` across all lists where the item appears.

Items appearing in multiple search results get boosted. For example, if "Alice works at Stripe" appears as both a memory match and a keyword match, it ranks higher than items from only one source.

---

## Connectors

### Markdown

Syncs a directory of `.md` files with incremental change detection.

```bash
cortex sync markdown ~/notes
```

- Recursively finds all `.md` files
- Parses YAML frontmatter for entity type, name, and attributes
- Detects `[[wikilinks]]` as relationships to other entities
- Splits body into chunks for search indexing
- Tracks file modification times — only re-processes changed files on subsequent syncs

**Frontmatter format:**
```yaml
---
type: person
name: Alice
email: alice@example.com
role: Staff Engineer
---
Alice is a staff engineer at [[Stripe]].
She knows [[Bob]] from their time at [[Google]].
She's interested in [[distributed systems]] and [[machine learning]].
```

### Conversation

Inline ingestion from chat messages — designed for agent integration, not batch sync.

```go
import "github.com/sausheong/cortex/connector/conversation"

conv := conversation.New()
err := conv.Ingest(ctx, c, []conversation.Message{
    {Role: "user", Content: "Had lunch with Alice, she's joining Stripe next month"},
    {Role: "assistant", Content: "I'll remember that Alice is joining Stripe."},
})
```

Messages are concatenated with role prefixes and passed through `Remember` with source `"conversation"`. The LLM extractor identifies entities and relationships from the conversation text.

Use this in your agent's post-response hook to build the compounding knowledge loop: read from cortex before responding, write back after.

### Gmail

Syncs emails via the Gmail API with OAuth2. Requires a pre-built `*gmail.Service`.

- **Deterministic extraction**: From/To/Cc headers become person entities with email attributes
- **LLM extraction**: email bodies are processed for relationship and memory discovery
- **Incremental sync**: uses Gmail history IDs — only fetches new emails

```go
import (
    gmailconn "github.com/sausheong/cortex/connector/gmail"
    gm "google.golang.org/api/gmail/v1"
)

conn := gmailconn.New(gmailService)
conn.Sync(ctx, c)                            // Sync last 50 emails (first run)
conn.Sync(ctx, c)                            // Only new emails (subsequent runs)
```

Options: `gmailconn.WithUserID("me")`, `gmailconn.WithMaxResults(100)`

### Google Calendar

Syncs events from Google Calendar with OAuth2. Requires a pre-built `*calendar.Service`.

- **Deterministic extraction**: attendees become person entities, events become event entities
- **Relationships**: "attended" edges link each attendee to each event
- **LLM extraction**: event descriptions are processed for additional context
- **Incremental sync**: uses Calendar sync tokens

```go
import (
    calconn "github.com/sausheong/cortex/connector/calendar"
    cal "google.golang.org/api/calendar/v3"
)

conn := calconn.New(calService)
conn.Sync(ctx, c)                            // Sync last 30 days (first run)
conn.Sync(ctx, c)                            // Only changed events (subsequent runs)
```

Options: `calconn.WithCalendarID("primary")`

### Google OAuth2 Setup (Gmail & Calendar)

Both Google connectors require OAuth2 credentials. The CLI cannot handle the interactive OAuth2 flow — use the Go API directly.

**1. Create credentials:**
- Go to [Google Cloud Console](https://console.cloud.google.com)
- Create a project (or select existing)
- Enable the **Gmail API** and/or **Google Calendar API**
- Go to Credentials > Create Credentials > OAuth Client ID
- Select "Desktop application" as the application type
- Download the credentials JSON file

**2. Set up the OAuth2 flow in your code:**

```go
import (
    "context"
    "encoding/json"
    "fmt"
    "os"

    "golang.org/x/oauth2"
    "golang.org/x/oauth2/google"
    "google.golang.org/api/gmail/v1"
    "google.golang.org/api/calendar/v3"
    "google.golang.org/api/option"

    gmailconn "github.com/sausheong/cortex/connector/gmail"
    calconn "github.com/sausheong/cortex/connector/calendar"
)

func main() {
    ctx := context.Background()

    // Load credentials
    b, _ := os.ReadFile("credentials.json")
    config, _ := google.ConfigFromJSON(b,
        gmail.GmailReadonlyScope,
        calendar.CalendarReadonlyScope,
    )

    // Get token (first time: interactive flow; thereafter: from saved file)
    token := getToken(config)

    // Create service clients
    ts := config.TokenSource(ctx, token)
    gmailService, _ := gmail.NewService(ctx, option.WithTokenSource(ts))
    calService, _ := calendar.NewService(ctx, option.WithTokenSource(ts))

    // Open cortex and sync
    c, _ := cortex.Open("brain.db", /* ... providers ... */)
    defer c.Close()

    gmailconn.New(gmailService).Sync(ctx, c)
    calconn.New(calService).Sync(ctx, c)
}

func getToken(config *oauth2.Config) *oauth2.Token {
    // Try loading from file
    f, err := os.Open("token.json")
    if err == nil {
        defer f.Close()
        var tok oauth2.Token
        json.NewDecoder(f).Decode(&tok)
        return &tok
    }

    // Interactive: print URL, get code from user
    url := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
    fmt.Printf("Visit this URL to authorize:\n%s\n\nEnter code: ", url)
    var code string
    fmt.Scan(&code)

    tok, _ := config.Exchange(context.Background(), code)

    // Save for next time
    f, _ = os.Create("token.json")
    json.NewEncoder(f).Encode(tok)
    f.Close()

    return tok
}
```

---

## Storage

Cortex uses embedded SQLite via [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite) — a pure Go implementation with no CGo dependency. Everything lives in a single `brain.db` file.

### Schema

| Table | Purpose |
|-------|---------|
| `entities` | Graph nodes — type, name, JSON attributes, source, timestamps |
| `relationships` | Directed typed edges between entities with JSON attributes |
| `chunks` | Text fragments linked to entities with JSON metadata |
| `chunks_fts` | FTS5 virtual table for full-text keyword search |
| `memories` | Distilled facts extracted by the LLM |
| `memory_entities` | Junction table linking memories to related entities |
| `embeddings` | Vector embeddings stored as BLOBs (float32 arrays) |
| `sync_state` | Per-connector sync state (timestamps, cursors, history IDs) |

### Design Choices

- **WAL mode** — enables concurrent reads while writing; important when the MCP/HTTP server handles queries during ingestion
- **FTS5** — SQLite's full-text search extension for fast keyword matching
- **Brute-force cosine similarity** — vector embeddings stored as BLOBs, similarity computed in Go. Performant for personal-scale data (<100K vectors). Can be swapped for [sqlite-vec](https://github.com/asg017/sqlite-vec) when mature Go bindings are available.
- **ULIDs** — time-sortable, globally unique, URL-safe identifiers for all entities
- **Open-ended types** — entity types and relationship types are plain strings, not enums. New connectors introduce new types without schema changes.

---

## Pluggable Providers

| Interface | Methods | Shipped Implementations |
|-----------|---------|------------------------|
| `LLM` | `Extract`, `Decompose`, `Summarize` | OpenAI (GPT-4o-mini), Anthropic (Claude Sonnet 4.5) |
| `Embedder` | `Embed`, `Dimensions` | OpenAI (text-embedding-3-small, 1536 dims) |
| `Extractor` | `Extract` | Deterministic, LLM, Hybrid (deterministic + LLM) |

### OpenAI Provider

```go
import oaillm "github.com/sausheong/cortex/llm/openai"

// Default (OpenAI API)
llm := oaillm.NewLLM("sk-...")
emb := oaillm.NewEmbedder("sk-...")

// With a different model
llm := oaillm.NewLLM("sk-...", oaillm.WithModel("gpt-4o"))

// OpenAI-compatible API (Ollama, vLLM, LM Studio, Together AI, Groq, etc.)
llm := oaillm.NewLLM("ollama",
    oaillm.WithBaseURL("http://localhost:11434/v1"),
    oaillm.WithModel("llama3"),
)
emb := oaillm.NewEmbedder("ollama",
    oaillm.WithEmbedderBaseURL("http://localhost:11434/v1"),
    oaillm.WithEmbeddingModel(oai.EmbeddingModel("nomic-embed-text"), 768),
)
```

Or set `OPENAI_BASE_URL` as an environment variable — all three binaries (CLI, MCP, HTTP) pick it up automatically.

### Anthropic Provider

Anthropic does not offer an embeddings API, so you always pair the Anthropic LLM with a separate `Embedder` (e.g., OpenAI, Ollama).

```go
import (
    anthropicllm "github.com/sausheong/cortex/llm/anthropic"
    oaillm "github.com/sausheong/cortex/llm/openai"
)

// Default (Anthropic API, Claude Sonnet 4.5)
llm := anthropicllm.NewLLM("sk-ant-...")

// With a different model
llm := anthropicllm.NewLLM("sk-ant-...",
    anthropicllm.WithModel("claude-haiku-4-5"),
)

// Anthropic-compatible API (custom proxy, AWS Bedrock adapter, etc.)
llm := anthropicllm.NewLLM("key",
    anthropicllm.WithBaseURL("https://your-proxy.example.com"),
)

// Wire up with OpenAI embedder (Anthropic has no embeddings)
emb := oaillm.NewEmbedder("sk-...")
ext := hybrid.New(deterministic.New(), llmext.New(llm))

c, _ := cortex.Open("brain.db",
    cortex.WithLLM(llm),
    cortex.WithEmbedder(emb),
    cortex.WithExtractor(ext),
)
```

### Implementing a Custom Provider

Implement the `LLM` and/or `Embedder` interfaces to add any provider:

```go
type LLM interface {
    Extract(ctx context.Context, text, prompt string) (ExtractionResult, error)
    Decompose(ctx context.Context, query string) ([]StructuredQuery, error)
    Summarize(ctx context.Context, texts []string) (string, error)
}

type Embedder interface {
    Embed(ctx context.Context, texts []string) ([][]float32, error)
    Dimensions() int
}
```

You can mix providers freely — for example, Anthropic for extraction, OpenAI for embeddings, and Ollama for query decomposition.

---

## Environment Variables

**LLM Provider:**

| Variable | Default | Description |
|----------|---------|-------------|
| `LLM_PROVIDER` | `openai` | LLM provider: `openai` or `anthropic` |
| `LLM_MODEL` | (provider default) | Override the LLM model (e.g., `gpt-4o`, `claude-haiku-4-5`) |
| `OPENAI_API_KEY` | (none) | OpenAI API key |
| `OPENAI_BASE_URL` | (none) | Custom base URL for OpenAI-compatible APIs (Ollama, vLLM, Together AI, Groq) |
| `ANTHROPIC_API_KEY` | (none) | Anthropic API key (required when `LLM_PROVIDER=anthropic`) |
| `ANTHROPIC_BASE_URL` | (none) | Custom base URL for Anthropic-compatible APIs |

**Embeddings:**

| Variable | Default | Description |
|----------|---------|-------------|
| `EMBEDDING_API_KEY` | falls back to `OPENAI_API_KEY` | API key for the embedding provider |
| `EMBEDDING_BASE_URL` | falls back to `OPENAI_BASE_URL` | Base URL for the embedding provider |
| `EMBEDDING_MODEL` | `text-embedding-3-small` | Embedding model name |
| `EMBEDDING_DIMS` | `1536` | Embedding vector dimensions |

**Server:**

| Variable | Default | Description |
|----------|---------|-------------|
| `CORTEX_DB` | `brain.db` | Database file path (MCP, HTTP) |
| `CORTEX_PORT` | `8080` | HTTP server listen port |

### Example Configurations

**OpenAI (default):**
```bash
export OPENAI_API_KEY=sk-...
cortex recall "who works at Stripe"
```

**Anthropic Claude + OpenAI embeddings:**
```bash
export LLM_PROVIDER=anthropic
export ANTHROPIC_API_KEY=sk-ant-...
export OPENAI_API_KEY=sk-...           # for embeddings
cortex recall "who works at Stripe"
```

**Ollama (fully local, no API keys):**
```bash
export OPENAI_API_KEY=ollama
export OPENAI_BASE_URL=http://localhost:11434/v1
export LLM_MODEL=llama3
export EMBEDDING_MODEL=nomic-embed-text
export EMBEDDING_DIMS=768
cortex recall "who works at Stripe"
```

**Anthropic LLM + Ollama embeddings:**
```bash
export LLM_PROVIDER=anthropic
export ANTHROPIC_API_KEY=sk-ant-...
export EMBEDDING_API_KEY=ollama
export EMBEDDING_BASE_URL=http://localhost:11434/v1
export EMBEDDING_MODEL=nomic-embed-text
export EMBEDDING_DIMS=768
cortex recall "who works at Stripe"
```

---

## Make Targets

```
make              Build all binaries to bin/
make test         Run all tests
make test-v       Run tests with verbose output
make test-cover   Run tests with HTML coverage report
make vet          Run go vet
make tidy         Run go mod tidy
make clean        Remove bin/ and coverage files
make install      Copy binaries to /usr/local/bin
make run-http     Build and run HTTP server
make run-mcp      Build and run MCP server
```

## Running Tests

```bash
# Run all tests (no API key needed — uses mocks)
make test

# Run with verbose output
make test-v

# Run with coverage report
make test-cover    # generates coverage.html

# Run a specific package
go test ./connector/markdown/ -v

# Run OpenAI integration tests (requires API key)
OPENAI_API_KEY=sk-... go test ./llm/openai/ -v

# Run Anthropic integration tests (requires API key)
ANTHROPIC_API_KEY=sk-ant-... go test ./llm/anthropic/ -v
```

---

## Project Stats

| Metric | Value |
|--------|-------|
| Go packages | 16 |
| Lines of Go | ~6,750 |
| Tests | 83 |
| Test packages | 10 |
| Binaries | 3 |
| External DB required | No |
| CGo required | No |

---

## License

MIT
