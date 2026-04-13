## ADDED Requirements

### Requirement: CLI interface
The system SHALL provide a CLI (`cmd/cortex`) exposing core operations as shell commands: `init`, `remember`, `recall`, `sync`, `entity list`, `entity get`, and `forget`.

#### Scenario: Initialize a new brain
- **WHEN** `cortex init` is run in a directory without a brain.db
- **THEN** a new database is created with all tables and indexes

#### Scenario: Remember from CLI
- **WHEN** `cortex remember "Alice works at Stripe"` is run
- **THEN** the text is processed through the Remember pipeline and stored

#### Scenario: Recall from CLI
- **WHEN** `cortex recall "who works at Stripe"` is run
- **THEN** ranked results are printed to stdout in a human-readable format

#### Scenario: Sync markdown from CLI
- **WHEN** `cortex sync markdown /path/to/notes` is run
- **THEN** the markdown connector syncs all .md files from the directory

### Requirement: MCP stdio server
The system SHALL provide an MCP server (`cmd/cortex-mcp`) that exposes core operations as MCP tools via stdio. Tools include: `remember`, `recall`, `forget`, `get_entity`, `find_entities`, `get_relationships`, `traverse`, `search`.

#### Scenario: Agent calls remember via MCP
- **WHEN** an MCP client sends a `remember` tool call with content
- **THEN** the content is processed through the Remember pipeline
- **AND** a success response is returned

#### Scenario: Agent calls recall via MCP
- **WHEN** an MCP client sends a `recall` tool call with a query
- **THEN** ranked results are returned as structured JSON

### Requirement: HTTP/REST server
The system SHALL provide an HTTP server (`cmd/cortex-http`) exposing the same operations as REST endpoints for non-MCP agents, webhooks, and external tools.

#### Scenario: POST /remember
- **WHEN** a POST request is sent to `/remember` with JSON body `{"content": "Alice works at Stripe"}`
- **THEN** the content is processed and a 200 response is returned

#### Scenario: GET /recall
- **WHEN** a GET request is sent to `/recall?q=who+works+at+Stripe`
- **THEN** ranked results are returned as JSON

### Requirement: Transport layers are thin wrappers
The system SHALL keep all business logic in the core library. Transport layers (CLI, MCP, HTTP) only parse input, call core methods, and format output. No extraction, search, or graph logic exists in the transport layer.

#### Scenario: CLI and MCP produce equivalent results
- **WHEN** the same query is sent via CLI and MCP
- **THEN** both return the same results (modulo formatting differences)
