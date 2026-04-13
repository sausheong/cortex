## Context

Existing personal knowledge systems each solve part of the problem: GBrain provides a compounding read-write loop with markdown as source of truth, Cognee offers graph-structured knowledge with hybrid search, and mem0 delivers a clean memory pipeline with multi-level storage. None combines all three in an embeddable, single-binary Go library.

Cortex is a greenfield Go project. There is no existing codebase — all architecture decisions are forward-looking. The primary user is the developer themselves; this is a personal tool, not a multi-tenant service.

## Goals / Non-Goals

**Goals:**
- Single Go library with a clean API (`Remember`/`Recall`/`Forget`) that any Go program can import
- Embedded storage (SQLite + sqlite-vec) — no external database server
- Hybrid extraction: deterministic parsing for structured data, LLM for unstructured prose
- Swappable LLM/embedding providers via Go interfaces
- Four data connectors (markdown, conversation, Gmail, calendar), each independent
- Multi-strategy search: vector + keyword + graph traversal + memory lookup, merged via RRF
- Three thin transport wrappers: CLI, MCP, HTTP

**Non-Goals:**
- Multi-user or multi-tenant support
- Real-time streaming ingestion
- Web UI
- Cloud deployment or hosting
- Supporting non-Go languages (no FFI/bindings)

## Decisions

### Decision: SQLite + sqlite-vec over Postgres
Using embedded SQLite with sqlite-vec extension for vector search instead of embedded Postgres or a separate vector database.

**Rationale:** SQLite gives a true single-file, zero-dependency deployment. sqlite-vec adds vector similarity search as a virtual table. The alternative (embedded Postgres via PGlite) is heavier, WASM-based, and harder to embed in a Go binary. A separate vector store (Qdrant, Pinecone) adds operational complexity inappropriate for a personal tool.

**Trade-off:** sqlite-vec is less mature than pgvector. If Go bindings for sqlite-vec require CGo, we may need to use `mattn/go-sqlite3` instead of the pure-Go `modernc.org/sqlite`. If neither provides good sqlite-vec support, we'll implement a minimal brute-force vector search in pure Go (acceptable for personal-scale data, <100K vectors).

### Decision: Core graph + pluggable connectors architecture
The core package knows nothing about data sources. Each connector is a standalone package that parses source-specific formats and calls the core API.

**Rationale:** Connectors are independent — you can build and test the markdown connector without thinking about Gmail. Adding a new source is a new package, not surgery on the core. Alternatives: (A) linear pipeline of stages — too rigid for adding sources 3 and 4; (C) event-sourced graph — complexity overkill for a single-user tool.

### Decision: Hybrid extraction (deterministic + LLM)
Structured sources (email headers, calendar events, frontmatter, wikilinks) use deterministic regex/parser extraction. Unstructured content (prose, freeform notes) falls back to LLM extraction.

**Rationale:** Deterministic extraction is free, fast, and reliable for structured data. LLM extraction is expensive and slow but necessary for natural language. The hybrid approach runs deterministic first, then fills in gaps with LLM — minimizing API costs while maintaining extraction quality.

### Decision: Reciprocal rank fusion for search merging
Using RRF to merge results from vector search, keyword search, graph traversal, and memory lookup.

**Rationale:** RRF combines ranked lists without requiring score normalization across heterogeneous search backends. Vector similarity scores, FTS5 ranks, and graph traversal depths are not comparable — RRF avoids the normalization problem entirely. Simple to implement (formula: `1 / (k + rank)` for each result, sum across lists).

### Decision: ULID for entity IDs
Using ULIDs instead of UUIDs or auto-increment integers.

**Rationale:** ULIDs are sortable by creation time (useful for "most recent" queries), globally unique (no coordination needed), and URL-safe. Auto-increment integers leak information and break if data is merged from multiple sources. UUIDs are not time-sortable.

### Decision: Open-ended relationship types
Relationship types are free-form strings ("works_at", "knows", "discussed_in"), not an enum.

**Rationale:** New connectors introduce new relationship types organically. An enum would require schema changes each time. Graph queries filter by type string — the caller decides which types matter.

## Risks / Trade-offs

**sqlite-vec Go ecosystem maturity** — sqlite-vec is relatively new and Go bindings may be immature or require CGo.
→ Mitigation: Fall back to brute-force cosine similarity over stored float arrays if needed. Personal-scale data (<100K vectors) makes this viable.

**LLM extraction quality** — Entity and relationship extraction from prose is inherently noisy. The LLM may miss entities, hallucinate relationships, or fail to deduplicate.
→ Mitigation: Entity merging by (name, type) tuple. Human can always edit the graph directly via structured API. Over time, extraction prompts can be refined.

**Entity deduplication** — "Alice", "Alice Smith", "alice@example.com" may all refer to the same person but create separate entities.
→ Mitigation: Start with exact (name, type) matching. Add fuzzy matching / LLM-assisted merge as a future improvement. The structured API allows manual merging.

**Single-file SQLite under concurrent access** — If multiple agents or CLI commands access brain.db simultaneously, SQLite's write-lock may cause contention.
→ Mitigation: Use WAL mode for concurrent reads. For writes, serialize through the `Cortex` struct (single writer). Personal usage patterns make heavy write contention unlikely.

## Open Questions

- What is the best Go binding for sqlite-vec? Need to evaluate `nicois/sqlite-vec`, `AkhilSharma90/go-sqlite-vec`, or building directly against `mattn/go-sqlite3` with the sqlite-vec C extension loaded.
- Should memories support confidence scores or decay over time (older memories weighted lower)?
- Should the conversation connector support session IDs to separate memory by conversation context?
