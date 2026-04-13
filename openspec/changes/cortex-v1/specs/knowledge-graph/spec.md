## ADDED Requirements

### Requirement: Unified entity storage
The system SHALL store all knowledge as typed entities in a single SQLite database. Entity types include "person", "organization", "concept", "event", and "document". Each entity has an ID (ULID), name, type, JSON attributes, source provenance, and timestamps.

#### Scenario: Create a person entity
- **WHEN** a connector or API call creates an entity with type "person" and name "Alice"
- **THEN** the entity is persisted with a generated ULID, type "person", and created_at timestamp
- **AND** the entity is retrievable by ID

#### Scenario: Create an entity with attributes
- **WHEN** an entity is created with attributes `{"email": "alice@example.com", "role": "engineer"}`
- **THEN** the attributes are stored as a JSON blob and returned on retrieval

#### Scenario: Entity type validation
- **WHEN** an entity is created with an unrecognized type (e.g., "widget")
- **THEN** the entity is still stored (types are open-ended strings, not an enum)

### Requirement: Typed relationships between entities
The system SHALL store directed, typed relationships between entities. Each relationship has a source entity, target entity, type string, optional JSON attributes, source provenance, and timestamp.

#### Scenario: Create a relationship
- **WHEN** a relationship of type "works_at" is created between entity "Alice" and entity "Stripe"
- **THEN** the relationship is persisted and queryable from either entity

#### Scenario: Multiple relationships between same entities
- **WHEN** two relationships of different types ("works_at" and "knows") exist between the same entity pair
- **THEN** both relationships are returned when querying relationships for either entity

#### Scenario: Relationship with attributes
- **WHEN** a relationship is created with attributes `{"since": "2024-01", "role": "staff engineer"}`
- **THEN** the attributes are stored and returned on retrieval

### Requirement: Text chunk storage
The system SHALL store text chunks linked to entities for retrieval and embedding. Each chunk has content, optional entity link, JSON metadata (file path, line range, message ID), and timestamp.

#### Scenario: Store a chunk linked to an entity
- **WHEN** a text chunk is stored with entity_id referencing entity "Alice"
- **THEN** the chunk is retrievable by entity ID and by content search

#### Scenario: Store a standalone chunk
- **WHEN** a text chunk is stored without an entity_id
- **THEN** the chunk is still searchable via keyword and vector search

### Requirement: Vector embedding storage
The system SHALL store vector embeddings for both chunks and memories using sqlite-vec. Each embedding record tracks the source row ID, row type ("chunk" or "memory"), and the float vector.

#### Scenario: Embed a chunk
- **WHEN** a chunk is stored and the embedder is configured
- **THEN** a vector embedding is generated and stored in the sqlite-vec virtual table

#### Scenario: Embed a memory
- **WHEN** a memory is stored and the embedder is configured
- **THEN** a vector embedding is generated for the memory content

### Requirement: Distilled memory storage
The system SHALL store distilled facts (memories) separately from raw text chunks. Memories are high-signal facts extracted by the LLM (e.g., "Alice is moving to Berlin in March"). Each memory links to related entities via a junction table.

#### Scenario: Store a memory with entity links
- **WHEN** a memory "Alice is joining Stripe next month" is stored with entity links to "Alice" and "Stripe"
- **THEN** the memory is retrievable by searching for "Alice" or "Stripe"
- **AND** the memory_entities junction table contains both links

#### Scenario: Update a memory
- **WHEN** a memory's content is updated (e.g., corrected date)
- **THEN** the updated_at timestamp is refreshed and the new content is re-embedded

### Requirement: Embedded single-file database
The system SHALL use embedded SQLite with sqlite-vec as the sole storage backend. No external database server is required. The entire knowledge graph, including vectors, is stored in a single database file.

#### Scenario: Open a new database
- **WHEN** `cortex.Open("brain.db")` is called and the file does not exist
- **THEN** the database is created with all tables and the sqlite-vec virtual table

#### Scenario: Open an existing database
- **WHEN** `cortex.Open("brain.db")` is called and the file exists
- **THEN** the existing data is preserved and any pending schema migrations are applied
