# Cortex

A personal knowledge graph and memory system in Go. Cortex stores everything you know — people, organizations, concepts, events, documents — as a unified graph with typed relationships. It extracts entities and facts from your notes and conversations, then lets any AI agent query the graph via MCP, HTTP, or the Go API.

Think of it as a digital twin that compounds knowledge over time. Agents read from cortex before responding and write back after every conversation. The more you use it, the smarter it gets.

Inspired by [GBrain](https://github.com/garrytan/gbrain) (operational model), [Cognee](https://github.com/topoteretes/cognee) (graph structure), and [mem0](https://github.com/mem0ai/mem0) (memory pipeline).

## How It Works

```
Ingest
  Signal arrives (note, conversation)
    -> Hybrid extraction (deterministic + LLM)
       -> Entities: Alice (person), Stripe (org), distributed systems (concept)
       -> Relationships: Alice --works_at--> Stripe
       -> Memories: "Alice is joining Stripe next month" (confidence: 0.9)
    -> Store in unified graph (SQLite)
    -> Embed for semantic search

Query
  ("What do I know about Alice?")
    -> LLM decomposes into sub-queries
    -> Parallel search: memories + keywords + vectors + graph traversal
    -> Reciprocal Rank Fusion merges results
    -> Ranked results with provenance + confidence

Maintain
  Project   -> cortex export --vault ./vault   (browsable Obsidian-compatible projection)
  Tidy      -> cortex lint                     (orphans, near-duplicates, dead sources)
  Reconcile -> cortex merge <keep-id> <drop-id>   (atomic dedup with provenance)
  Brief     -> cortex init-schema              (drops CORTEX.md agent contract for AI clients)
```

Every cycle through ingest/query adds knowledge. The agent enriches a person page after a meeting. Next time that person comes up, the agent already has context. The maintain loop (export, lint, merge) is what keeps the graph healthy as it grows — run periodically, ideally by an agent reading the lint report and acting on its suggestions. You never start from zero.

## Features

- **Unified knowledge graph** — people, organizations, concepts, events, documents as first-class nodes with typed relationships
- **Remember / Recall / Forget** — simple high-level API inspired by mem0
- **Hybrid extraction** — deterministic parsing (frontmatter, wikilinks) + LLM-powered entity/relationship discovery
- **Confidence scores** — every extracted entity, relationship, and memory carries a 0–1 confidence value the LLM assigns at ingest. Filter results with `--min-confidence`; lower-confidence claims show inline in vault pages and recall output
- **Multi-strategy search** — keyword (FTS5), vector (cosine similarity), graph traversal, and memory lookup merged via reciprocal rank fusion
- **Two ingestion paths** — `remember` (ad-hoc text) and `sync` (directory of text files: `.md`, `.csv`, `.yaml`, `.json`, `.txt`, `.tsv`, `.xml`, `.toml`). File format is auto-detected and the LLM extracts knowledge accordingly
- **Obsidian-compatible vault export** — `cortex export` projects the graph as a browsable markdown vault with frontmatter, wikilinks, and backlinks. Incremental change detection via a hidden manifest
- **Agent contract** — `cortex init-schema` drops a `CORTEX.md` into a project that tells any LLM-based agent how to use cortex as the knowledge layer (when to recall, when to remember, what not to store)
- **Operational primitives** — `cortex merge` atomically combines duplicate entities (re-targets every reference, dedups, records provenance). `cortex lint` scans the graph for orphans, near-duplicates, and other cleanup candidates as a markdown report
- **Two interfaces** — CLI and MCP server (stdio or streamable HTTP)
- **Single binary, single file** — embedded SQLite with pure Go driver, no external database, no CGo
- **Pluggable providers** — swap OpenAI for Anthropic, Ollama, or any custom implementation

## Table of Contents

- [Prerequisites](#prerequisites)
- [Installation](#installation)
- [Setup](#setup)
- [CLI Reference](#cli-reference)
- [MCP Server](#using-as-an-mcp-server)
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
make build          # cortex, cortex-mcp -> bin/

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

During initialization, cortex will prompt for your identity:
- **Name** (required)
- **Nickname** (optional)
- **Email addresses** (zero or more)

This stores you as the owner entity in the graph so that queries like "who am I" work out of the box. You can update your identity later with `cortex config`.

### Step 2: Set Your OpenAI API Key (Recommended)

```bash
export OPENAI_API_KEY=sk-...
```

Add this to your `~/.zshrc` or `~/.bashrc` to persist across sessions.

With an API key, cortex will:
- Use **GPT-5.4-mini** to extract entities, relationships, and distilled facts from any text
- Use **text-embedding-3-small** to generate 1536-dimensional vector embeddings for semantic search
- Use LLM-powered **query decomposition** to break natural language queries into multi-strategy sub-queries

Without it, cortex falls back to deterministic extraction only (YAML frontmatter + wikilinks) and keyword search.

### Step 3: Ingest Your Data

**Sync your files:**
```bash
cortex sync ~/notes
```

Cortex will recursively find all supported text files (`.md`, `.csv`, `.yaml`, `.json`, `.txt`, etc.), auto-detect the format, parse frontmatter and wikilinks from markdown, and run LLM extraction on all content (if API key is set). Incremental — only re-processes files that changed since the last sync.

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
Usage: cortex [--db <path>] <command> [arguments]

Global options:
  --db <path>                    Path to brain.db (default: brain.db, or CORTEX_DB env var)

Commands:
  init                           Create brain.db and set up owner identity
  remember <text>                Ingest text, extract entities/relationships/memories
  recall <query>                 Natural language query with multi-strategy search
  sync <dir>                     Sync text files from a directory (incremental, auto-detects format)
  entity list [--type <type>]    List entities, optionally filtered by type
  entity get <id>                Show entity details, attributes, and relationships
  forget --source <src>          Remove all knowledge from a source
  forget --entity <id>           Remove a specific entity and all linked data
  merge <keep-id> <drop-id> [--dry-run]
                                 Merge drop entity into keep entity; re-target all references
  lint [--low-confidence] [--low-confidence-threshold <0-1>] [--out <file>]
                                 Scan the graph for cleanup candidates; print markdown report
  config                         Show owner identity
  config --name <name>           Update owner name
  config --nickname <nick>       Update owner nickname
  config --email <email>         Add an email address
  export [--vault <dir>] [--full] [--dry-run]
                                 Export the knowledge graph as an Obsidian-compatible markdown vault
  init-schema [<dir>] [--force]  Write CORTEX.md (generic agent contract) to <dir> (default: cwd)
```

### `cortex recall`

Recall results now carry a `confidence` score (0–1). The LLM extractor sets the value at ingest time — 1.0 for facts directly stated in the source, lower for inferences or speculation. Filter to high-confidence-only with `--min-confidence`:

```bash
cortex recall "did alice join stripe" --min-confidence 0.7
```

Default behavior is unchanged: all results surface, ranked by Reciprocal Rank Fusion, with confidence reported but not used for ranking.

### `cortex export`

Project the knowledge graph as an Obsidian-compatible markdown vault.

```bash
cortex export [--vault <dir>] [--full] [--dry-run]
```

- `--vault <dir>` — output directory (default: `./vault`).
- `--full` — rewrite every page regardless of whether content has changed.
- `--dry-run` — compute what would change and report stats; write nothing.

The vault is laid out as `vault/people/`, `vault/organizations/`, `vault/concepts/`, etc., with one markdown page per entity. An `index.md` catalog and a chronological `log.md` live at the vault root. A hidden `.cortex-export.json` manifest tracks content hashes so subsequent runs only rewrite pages whose content has actually changed. Entities deleted from the graph are moved to `vault/_archive/<timestamp>/`.

Each page carries YAML frontmatter (including the entity's full `cortex_id`) and renders memories, outbound relationships, backlinks, and source attribution. Wikilinks use explicit paths (`[[people/alice-chen|Alice Chen]]`), so the vault is browsable in Obsidian, in a plain editor, or via `grep`.

### `cortex merge`

Merge a duplicate entity into the canonical one.

```bash
cortex merge <keep-id> <drop-id> [--dry-run]
```

Re-targets every reference to the drop entity onto the keep entity: relationships, memory links, chunks, and the entity's vector embedding. Duplicate relationships and memory links that would arise from the re-target are collapsed. Self-loops (an edge from drop pointing at keep, which would become keep→keep after merge) are removed. The drop entity is then deleted.

Attributes from the drop entity are unioned into the keep entity. **Keep wins** on duplicate keys — its existing attribute values are preserved. A `merged_from` record (containing the drop entity's name, type, source, and full attribute snapshot at merge time) is appended to the keep entity's attributes for provenance. Multiple merges into the same entity append to the same `merged_from` array.

The whole operation runs in a single SQLite transaction. On any error, all changes are rolled back. `--dry-run` runs the same algorithm and reports identical stats, but always rolls back — useful for previewing a merge before committing to it.

```bash
$ cortex merge ent_01HKEEP ent_01HDROP --dry-run
Dry-run: would merge ent_01HDROP → ent_01HKEEP (Alice Chen, person)
  4 relationships would be re-targeted (1 duplicates dropped)
  ...
No changes written.
```

Errors out (exit 1) if either entity is missing, the IDs are identical, or the entities have different types (e.g. trying to merge a `person` into an `organization`).

### `cortex lint`

Scan the graph for cleanup candidates and print a markdown report.

```bash
cortex lint [--low-confidence] [--low-confidence-threshold <0-1>] [--out <file>]
```

Pure read operation — never modifies the graph. Six checks run on every invocation:

- **Orphan entities** — no relationships, no memory links. Often extraction noise.
- **Entities without memories** — have relationships but nothing said about them.
- **Near-duplicate entity names** — same type + case-insensitively-equal name. Direct candidates for `cortex merge`.
- **Dead sources** — source strings on memory rows with no matching live entity.
- **Unlinked memories** — memories not joined to any entity; findable via search but invisible to graph traversal.
- **Low-confidence memories** — opt-in via `--low-confidence`. Default threshold 0.3; override with `--low-confidence-threshold <n>` (which also enables the section).

Output is markdown by default — empty sections are omitted so a healthy graph shows just the count summary. Use `--out <path>` to write the report to a file.

Lint is informational. Even when findings are present, exit code is 0.

### `cortex init-schema`

Write a generic agent contract (`CORTEX.md`) into a project directory.

```bash
cortex init-schema [<dir>] [--force]
```

The template explains to any LLM-based agent how to use cortex as the knowledge layer for the project: when to `recall`, when to `remember`, what not to store, and how the vault fits in. Refuses to overwrite an existing `CORTEX.md` unless `--force` is passed. After running, edit the file to add project-specific conventions.

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

### HTTP transport

Cortex can also serve MCP over streamable HTTP for clients that can't or won't drive a subprocess:

```bash
# Default: listens on 127.0.0.1:8080, MCP endpoint at /mcp
bin/cortex-mcp --transport http

# Bind elsewhere
bin/cortex-mcp --transport http --addr 0.0.0.0:9000

# With bearer-token auth (required if you expose the port beyond localhost)
CORTEX_AUTH_TOKEN=$(openssl rand -hex 32) bin/cortex-mcp --transport http
```

Health check:

```bash
curl http://127.0.0.1:8080/healthz
# {"status":"ok"}
```

Env-var equivalents: `CORTEX_TRANSPORT`, `CORTEX_ADDR`, `CORTEX_AUTH_TOKEN`. Flags win over env. Stdio remains the default — `cortex-mcp` with no arguments behaves exactly as before.

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

## Using as a Go Library

### Full Example

```go
package main

import (
    "context"
    "fmt"

    "github.com/sausheong/cortex"
    "github.com/sausheong/cortex/connector/conversation"
    "github.com/sausheong/cortex/connector/files"
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

    // Sync files (markdown, CSV, YAML, JSON, etc.)
    f := files.New("/path/to/notes")
    f.Sync(ctx, c)

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
    c.Forget(ctx, cortex.Filter{Source: "markdown"})

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
                           │    Ingestion     │
                           │                  │
                           │  Remember(text)  │
                           │  Sync(markdown/) │
                           └────────┬─────────┘
                                    │
                           ┌────────▼─────────┐
                           │  Hybrid Extractor│
                           │                  │
                           │  Deterministic:  │
                           │  frontmatter,    │
                           │  wikilinks       │
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
│   ├── files/               # File directory connector (md, csv, yaml, json, txt, etc.)
│   └── conversation/        # Conversation message connector
├── cmd/
│   ├── cortex/              # CLI
│   └── cortex-mcp/          # MCP server (stdio + streamable HTTP)
└── internal/testutil/       # Test mocks and helpers
```

**Simple ingestion model**: cortex accepts text via `Remember` and text files via `Sync`. The file connector auto-detects format by extension and lets the LLM extract knowledge from any supported text format. No external API connectors — keeps the core simple and dependency-free.

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
cortex.WithSourceFilter("markdown")    // Filter results by source

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
    Source     string            // Origin: "markdown", "conversation"
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
**Step 2 — LLM extraction** (powerful, costs API calls):
- Sends unstructured prose to OpenAI with a structured extraction prompt
- Receives entities, relationships, and distilled memory facts as JSON
- Processes content that the deterministic extractor can't handle

**Step 3 — Store and embed**:
- Entities are upserted (merged by name + type — no duplicates)
- Relationships are created between resolved entities
- Raw text is stored as chunks and indexed for FTS5 + vector search
- Memories are stored with entity links

The LLM extractor now requests a `confidence` field on every extracted item. Existing brain.db files migrate transparently on next `Open` — old rows get `confidence=1.0`.

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

### File Sync

Syncs a directory of text files with incremental change detection and automatic format detection.

```bash
cortex sync ~/notes
```

**Supported formats:** `.md`, `.csv`, `.tsv`, `.yaml`, `.yml`, `.json`, `.txt`, `.xml`, `.toml`, `.log`

- Recursively finds all supported text files
- Auto-detects content type from file extension
- For markdown: parses YAML frontmatter and `[[wikilinks]]` deterministically
- For all formats: the LLM extracts entities, relationships, and memories from the content
- Tracks file modification times — only re-processes changed files on subsequent syncs

**Markdown frontmatter format:**
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

**CSV / YAML / JSON** are ingested as-is — the LLM reads the tabular or structured data and extracts entities and relationships from it.

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

- **WAL mode** — enables concurrent reads while writing; important when the MCP server handles queries during ingestion
- **FTS5** — SQLite's full-text search extension for fast keyword matching
- **Brute-force cosine similarity** — vector embeddings stored as BLOBs, similarity computed in Go. Performant for personal-scale data (<100K vectors). Can be swapped for [sqlite-vec](https://github.com/asg017/sqlite-vec) when mature Go bindings are available.
- **ULIDs** — time-sortable, globally unique, URL-safe identifiers for all entities
- **Open-ended types** — entity types and relationship types are plain strings, not enums. New connectors introduce new types without schema changes.

---

## Pluggable Providers

| Interface | Methods | Shipped Implementations |
|-----------|---------|------------------------|
| `LLM` | `Extract`, `Decompose`, `Summarize` | OpenAI (GPT-5.4-mini), Anthropic (Claude Sonnet 4.5) |
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
| `LLM_MODEL` | (provider default) | Override the LLM model (e.g., `gpt-5.4-mini`, `claude-haiku-4-5`) |
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
| `CORTEX_DB` | `brain.db` | Database file path (CLI, MCP) |
| `CORTEX_TRANSPORT` | `stdio` | MCP transport: `stdio` or `http` |
| `CORTEX_ADDR` | `127.0.0.1:8080` | Listen address when transport is `http` |
| `CORTEX_AUTH_TOKEN` | _(unset)_ | Bearer token required for HTTP transport on non-loopback binds |

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
make run-mcp      Build and run MCP server (stdio)
make run-mcp-http Build and run MCP server (streamable HTTP)
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
go test ./connector/files/ -v

# Run OpenAI integration tests (requires API key)
OPENAI_API_KEY=sk-... go test ./llm/openai/ -v

# Run Anthropic integration tests (requires API key)
ANTHROPIC_API_KEY=sk-ant-... go test ./llm/anthropic/ -v
```
