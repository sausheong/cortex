## ADDED Requirements

### Requirement: Remember ingests and extracts knowledge
The system SHALL provide a `Remember` method that accepts raw text content, runs extraction (entities, relationships, memories), generates embeddings, and stores everything in the graph.

#### Scenario: Remember a simple fact
- **WHEN** `Remember(ctx, "Alice works at Stripe")` is called
- **THEN** entities "Alice" (person) and "Stripe" (organization) are created or merged
- **AND** a relationship "works_at" is created between them
- **AND** a memory "Alice works at Stripe" is stored
- **AND** embeddings are generated for the memory

#### Scenario: Remember with source option
- **WHEN** `Remember(ctx, content, WithSource("markdown"))` is called
- **THEN** all created entities, relationships, and memories have source set to "markdown"

#### Scenario: Remember merges duplicate entities
- **WHEN** an entity "Alice" already exists and new content mentions "Alice"
- **THEN** the existing entity is reused (matched by name and type), not duplicated

### Requirement: Recall answers natural language queries
The system SHALL provide a `Recall` method that accepts a natural language query, decomposes it into structured sub-queries via the LLM, executes them in parallel, and returns ranked results via reciprocal rank fusion.

#### Scenario: Recall a person query
- **WHEN** `Recall(ctx, "What do I know about Alice?")` is called
- **THEN** results include memories mentioning Alice, the Alice entity with attributes, chunks related to Alice, and relationships from Alice to other entities
- **AND** results are ranked by RRF score

#### Scenario: Recall with no results
- **WHEN** `Recall(ctx, "What do I know about Zebadiah?")` is called and no matching data exists
- **THEN** an empty result slice is returned (no error)

### Requirement: Forget removes knowledge
The system SHALL provide a `Forget` method that removes entities, relationships, memories, chunks, and embeddings matching a filter.

#### Scenario: Forget by entity ID
- **WHEN** `Forget(ctx, Filter{EntityID: "alice-id"})` is called
- **THEN** the entity, all its relationships, linked chunks, linked memories, and associated embeddings are deleted

#### Scenario: Forget by source
- **WHEN** `Forget(ctx, Filter{Source: "gmail"})` is called
- **THEN** all entities, relationships, memories, and chunks with source "gmail" are deleted

### Requirement: Entity CRUD operations
The system SHALL provide `PutEntity`, `GetEntity`, and `FindEntities` methods for direct entity manipulation.

#### Scenario: Put and get an entity
- **WHEN** `PutEntity(ctx, Entity{Name: "Alice", Type: "person"})` is called
- **THEN** `GetEntity(ctx, id)` returns the entity with all fields populated

#### Scenario: Find entities by type
- **WHEN** `FindEntities(ctx, EntityFilter{Type: "person"})` is called
- **THEN** all person entities are returned

#### Scenario: Find entities by name pattern
- **WHEN** `FindEntities(ctx, EntityFilter{NameLike: "Ali%"})` is called
- **THEN** entities with names matching the pattern are returned

### Requirement: Relationship CRUD operations
The system SHALL provide `PutRelationship` and `GetRelationships` methods for direct relationship manipulation.

#### Scenario: Get relationships for an entity
- **WHEN** `GetRelationships(ctx, aliceID)` is called
- **THEN** all relationships where Alice is either source or target are returned

#### Scenario: Get relationships filtered by type
- **WHEN** `GetRelationships(ctx, aliceID, RelTypeFilter("works_at"))` is called
- **THEN** only "works_at" relationships involving Alice are returned

### Requirement: Graph traversal
The system SHALL provide a `Traverse` method that walks the graph from a starting entity, following relationships up to a configurable depth.

#### Scenario: Traverse one level deep
- **WHEN** `Traverse(ctx, aliceID, WithDepth(1))` is called
- **THEN** a graph is returned containing Alice and all entities directly connected to her

#### Scenario: Traverse with edge type filter
- **WHEN** `Traverse(ctx, aliceID, WithDepth(2), WithEdgeTypes("works_at", "knows"))` is called
- **THEN** only "works_at" and "knows" edges are followed during traversal

### Requirement: Direct search primitives
The system SHALL provide `SearchVector`, `SearchKeyword`, and `SearchMemories` methods that search the store directly without query decomposition.

#### Scenario: Vector search returns ranked chunks
- **WHEN** `SearchVector(ctx, "distributed systems", 10)` is called
- **THEN** up to 10 chunks are returned, ranked by cosine similarity to the query embedding

#### Scenario: Keyword search uses FTS5
- **WHEN** `SearchKeyword(ctx, "Stripe", 10)` is called
- **THEN** chunks matching the keyword via SQLite FTS5 are returned

#### Scenario: Memory search
- **WHEN** `SearchMemories(ctx, "joining Stripe", 5)` is called
- **THEN** memories matching by keyword and vector similarity are returned
