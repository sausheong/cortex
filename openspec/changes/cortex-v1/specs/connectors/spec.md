## ADDED Requirements

### Requirement: Connector interface
The system SHALL define a `Connector` interface with a single `Sync` method. Each connector manages its own sync state (last sync time, cursor) by storing metadata in cortex, making `Sync` safe to re-run (idempotent incremental sync).

#### Scenario: Sync is idempotent
- **WHEN** `connector.Sync(ctx, c)` is called twice with no source changes between calls
- **THEN** no duplicate entities, relationships, or chunks are created

### Requirement: Markdown connector
The system SHALL provide a markdown connector that reads `.md` files from a directory, parses YAML frontmatter for structured metadata, detects wikilinks as relationships, splits body text into chunks, and runs LLM extraction on unstructured prose.

#### Scenario: Ingest a markdown file with frontmatter
- **WHEN** a file with frontmatter `type: person` and `name: Alice` is synced
- **THEN** an entity of type "person" named "Alice" is created with source "markdown"

#### Scenario: Detect wikilinks as relationships
- **WHEN** a markdown file for "Alice" contains `[[Stripe]]` in its body
- **THEN** a relationship is created between the "Alice" entity and the "Stripe" entity (created if it doesn't exist)

#### Scenario: Incremental sync by modification time
- **WHEN** `Sync` is called and only 2 of 100 files have changed since the last sync
- **THEN** only the 2 changed files are re-processed

#### Scenario: Chunk splitting
- **WHEN** a markdown file with 2000 words of body text is synced
- **THEN** the body is split into multiple chunks of reasonable size for embedding

### Requirement: Conversation connector
The system SHALL provide a conversation connector that accepts chat messages (role + content pairs), runs LLM extraction on each message, and stores extracted entities, relationships, and memories. This connector is designed for inline use from agents, not batch sync.

#### Scenario: Ingest a conversation message
- **WHEN** `conv.Ingest(ctx, c, messages)` is called with `"Had lunch with Alice, she's joining Stripe next month"`
- **THEN** entities "Alice" (person) and "Stripe" (organization) are created or merged
- **AND** a relationship "works_at" between Alice and Stripe is created
- **AND** a memory "Alice joining Stripe next month" is stored

#### Scenario: Multiple messages in sequence
- **WHEN** multiple messages are ingested in sequence
- **THEN** entities mentioned across messages are merged (not duplicated)

### Requirement: Gmail connector
The system SHALL provide a Gmail connector that syncs emails via the Gmail API using OAuth2. It performs deterministic extraction on headers (From/To/Cc → person entities) and LLM extraction on email bodies for relationships and memories. Incremental sync uses Gmail history IDs.

#### Scenario: Extract people from email headers
- **WHEN** an email with From: "Alice <alice@example.com>" and To: "Bob <bob@example.com>" is synced
- **THEN** person entities for Alice and Bob are created or merged with email attributes

#### Scenario: Incremental sync via history ID
- **WHEN** `Sync` is called after a previous sync
- **THEN** only emails received since the last history ID are processed

### Requirement: Calendar connector
The system SHALL provide a calendar connector that syncs events via the Google Calendar API using OAuth2. It performs deterministic extraction on attendees (→ person entities) and events (→ event entities), links attendees to events via "attended" relationships, and runs LLM extraction on event descriptions.

#### Scenario: Extract attendees from calendar event
- **WHEN** a calendar event with attendees "Alice" and "Bob" is synced
- **THEN** person entities for Alice and Bob are created or merged
- **AND** an event entity is created
- **AND** "attended" relationships link each person to the event

#### Scenario: Extract context from event description
- **WHEN** an event has description "Discuss Series A fundraising with Alice"
- **THEN** the LLM extractor identifies relevant concepts and relationships from the description
