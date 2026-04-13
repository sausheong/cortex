## Why

AI agents are smart but know nothing about you. Every conversation starts from zero — no memory of who you know, what you've discussed, or what matters to you. Existing solutions (GBrain, Cognee, mem0) each solve part of this problem but none combine a unified knowledge graph, hybrid extraction pipeline, and agent-queryable interface in a single embeddable Go library.

Cortex fixes this by building a personal knowledge graph and memory system — a digital twin that compounds knowledge over time. Agents read from it before responding and write back after every conversation. The more you use it, the smarter it gets.

## What Changes

- New Go library (`core/`) providing a unified knowledge graph with entity-centric storage, typed relationships, and distilled memories
- Hybrid extraction pipeline: deterministic parsing for structured data (email headers, calendar events, frontmatter), LLM-powered extraction for unstructured prose
- Pluggable LLM and embedding provider interfaces with OpenAI as the default implementation
- Four data source connectors: markdown files, conversations, Gmail, Google Calendar
- Multi-strategy search combining vector similarity, keyword (FTS5), graph traversal, and memory lookup via reciprocal rank fusion
- Three transport layers: CLI, MCP stdio server, HTTP/REST server — all thin wrappers over the core library
- Embedded SQLite + sqlite-vec for single-file, zero-dependency storage

## Capabilities

### New Capabilities
- `knowledge-graph`: Unified entity graph schema with entities, relationships, chunks, embeddings, and memories stored in embedded SQLite + sqlite-vec
- `core-api`: High-level Remember/Recall/Forget API and structured graph CRUD operations (entity, relationship, traversal, search)
- `pluggable-providers`: Swappable LLM, Embedder, and Extractor interfaces with shipped OpenAI and hybrid implementations
- `connectors`: Data source connectors (markdown, conversation, Gmail, calendar) that independently ingest and sync into the core
- `search-recall`: Multi-strategy search with LLM query decomposition, parallel execution, and reciprocal rank fusion ranking
- `transport-layers`: CLI, MCP stdio server, and HTTP/REST server as thin wrappers over the core library

### Modified Capabilities

(None — greenfield project)

## Impact

- **New project**: Entire codebase is new — Go module at `github.com/sausheong/cortex`
- **Dependencies**: `modernc.org/sqlite` or `mattn/go-sqlite3`, sqlite-vec, `sashabaranov/go-openai`, `mark3labs/mcp-go`, `oklog/ulid`
- **External APIs**: OpenAI API for embeddings and LLM extraction (requires API key), Gmail API and Google Calendar API for connectors 3-4 (requires OAuth2)
- **Storage**: Single SQLite database file (`brain.db`), no external database server
