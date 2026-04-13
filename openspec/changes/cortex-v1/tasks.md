# Tasks

## 1. Project Setup

- [ ] 1.1 Initialize Go module (`github.com/sausheong/cortex`)
- [ ] 1.2 Set up project directory structure (core/, llm/, extractor/, connector/, cmd/)
- [ ] 1.3 Add core dependencies (SQLite driver, ULID, go-openai)
- [ ] 1.4 Evaluate and select sqlite-vec Go binding

## 2. Core Schema and Storage

- [ ] 2.1 Implement SQLite database initialization with WAL mode
- [ ] 2.2 Create entities table with indexes on (type), (name, type)
- [ ] 2.3 Create relationships table with indexes on (source_id), (target_id), (type)
- [ ] 2.4 Create chunks table with FTS5 virtual table for keyword search
- [ ] 2.5 Create memories table and memory_entities junction table
- [ ] 2.6 Set up sqlite-vec virtual table for embeddings
- [ ] 2.7 Implement schema migration system

## 3. Entity and Relationship CRUD

- [ ] 3.1 Implement PutEntity with upsert logic (merge by name + type)
- [ ] 3.2 Implement GetEntity by ID
- [ ] 3.3 Implement FindEntities with type, name pattern, and source filters
- [ ] 3.4 Implement PutRelationship
- [ ] 3.5 Implement GetRelationships with entity ID and type filters
- [ ] 3.6 Implement Traverse with configurable depth and edge type filtering

## 4. Pluggable LLM and Embedder Interfaces

- [ ] 4.1 Define LLM interface (Extract, Decompose, Summarize)
- [ ] 4.2 Define Embedder interface (Embed, Dimensions)
- [ ] 4.3 Define Extractor interface and Extraction result types
- [ ] 4.4 Implement OpenAI LLM provider (GPT-4.1-mini)
- [ ] 4.5 Implement OpenAI Embedder provider (text-embedding-3-small)

## 5. Extraction Pipeline

- [ ] 5.1 Implement deterministic extractor (YAML frontmatter, wikilinks, email headers)
- [ ] 5.2 Implement LLM extractor with structured output prompts
- [ ] 5.3 Implement hybrid extractor (composes deterministic + LLM)

## 6. Chunk and Embedding Storage

- [ ] 6.1 Implement chunk creation and entity linking
- [ ] 6.2 Implement embedding generation and sqlite-vec storage
- [ ] 6.3 Implement vector similarity search via sqlite-vec
- [ ] 6.4 Implement FTS5 keyword search on chunks

## 7. Memory Storage and Search

- [ ] 7.1 Implement memory creation with entity linking (junction table)
- [ ] 7.2 Implement memory embedding (reuse embedder)
- [ ] 7.3 Implement memory search (keyword + vector)
- [ ] 7.4 Implement memory update and re-embedding

## 8. Remember Pipeline

- [ ] 8.1 Implement Remember method: content → extractor → store entities/relationships/memories/chunks → embed
- [ ] 8.2 Implement RememberOption types (WithSource, WithUserID, etc.)
- [ ] 8.3 Implement entity deduplication during Remember (match by name + type)

## 9. Recall Pipeline

- [ ] 9.1 Implement LLM query decomposition (natural language → structured sub-queries)
- [ ] 9.2 Implement parallel sub-query execution (goroutines)
- [ ] 9.3 Implement reciprocal rank fusion merging
- [ ] 9.4 Implement Recall method wiring decomposition → execution → merge
- [ ] 9.5 Implement RecallOption types (limit, source filter, etc.)

## 10. Forget

- [ ] 10.1 Implement Forget with cascading deletes (entity → relationships, chunks, memories, embeddings)
- [ ] 10.2 Implement Filter types (by entity ID, by source, by type)

## 11. Markdown Connector

- [ ] 11.1 Implement recursive .md file discovery with glob patterns
- [ ] 11.2 Implement YAML frontmatter parsing
- [ ] 11.3 Implement wikilink detection and relationship creation
- [ ] 11.4 Implement body text chunking
- [ ] 11.5 Implement incremental sync via file modification time tracking
- [ ] 11.6 Implement Sync method orchestrating parse → extract → store

## 12. CLI

- [ ] 12.1 Implement `cortex init` command
- [ ] 12.2 Implement `cortex remember` command
- [ ] 12.3 Implement `cortex recall` command with formatted output
- [ ] 12.4 Implement `cortex sync markdown` command
- [ ] 12.5 Implement `cortex entity list` and `cortex entity get` commands
- [ ] 12.6 Implement `cortex forget` command

## 13. MCP Server

- [ ] 13.1 Set up MCP stdio server using mcp-go
- [ ] 13.2 Implement `remember` tool
- [ ] 13.3 Implement `recall` tool
- [ ] 13.4 Implement `forget` tool
- [ ] 13.5 Implement `get_entity`, `find_entities` tools
- [ ] 13.6 Implement `get_relationships`, `traverse` tools
- [ ] 13.7 Implement `search` tool (vector, keyword, memory)

## 14. HTTP Server

- [ ] 14.1 Set up HTTP server with router
- [ ] 14.2 Implement POST /remember endpoint
- [ ] 14.3 Implement GET /recall endpoint
- [ ] 14.4 Implement DELETE /forget endpoint
- [ ] 14.5 Implement entity and relationship REST endpoints
- [ ] 14.6 Implement search endpoints

## 15. Conversation Connector

- [ ] 15.1 Implement message ingestion with LLM extraction
- [ ] 15.2 Implement entity merging across messages
- [ ] 15.3 Implement Ingest method (inline, not batch Sync)

## 16. Gmail Connector

- [ ] 16.1 Implement Gmail API OAuth2 authentication flow
- [ ] 16.2 Implement deterministic extraction from email headers (From/To/Cc)
- [ ] 16.3 Implement LLM extraction on email bodies
- [ ] 16.4 Implement incremental sync via Gmail history ID

## 17. Calendar Connector

- [ ] 17.1 Implement Google Calendar API OAuth2 authentication flow
- [ ] 17.2 Implement deterministic extraction of attendees and events
- [ ] 17.3 Implement "attended" relationship creation
- [ ] 17.4 Implement LLM extraction on event descriptions
