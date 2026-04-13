## ADDED Requirements

### Requirement: LLM query decomposition
The system SHALL use the configured LLM to decompose natural language queries into structured sub-queries. Each sub-query specifies a search type (memory_lookup, graph_traverse, vector_search, keyword_search) and parameters.

#### Scenario: Decompose a person query
- **WHEN** `Recall(ctx, "What do I know about Alice's work at Stripe?")` is called
- **THEN** the LLM decomposes it into sub-queries: memory_lookup for "Alice Stripe", graph_traverse for entity "Alice" with edge "works_at", and vector_search for "Alice work Stripe"

#### Scenario: Decompose a concept query
- **WHEN** `Recall(ctx, "What are my thoughts on distributed systems?")` is called
- **THEN** the LLM produces sub-queries including vector_search and memory_lookup targeting "distributed systems"

### Requirement: Parallel search execution
The system SHALL execute all sub-queries from query decomposition concurrently. Each search type runs independently against its respective store.

#### Scenario: Sub-queries run in parallel
- **WHEN** a decomposed query produces 3 sub-queries
- **THEN** all 3 execute concurrently (not sequentially)
- **AND** the total latency is dominated by the slowest sub-query, not the sum

### Requirement: Reciprocal rank fusion merging
The system SHALL merge results from multiple search types using reciprocal rank fusion (RRF). Each search type produces a ranked list; RRF combines them into a single ranking without requiring score normalization across different search backends.

#### Scenario: Merge results from multiple search types
- **WHEN** memory_lookup returns 3 results, vector_search returns 5 results, and keyword_search returns 4 results
- **THEN** the final result list is a unified ranking based on RRF scores

#### Scenario: Same item from multiple searches gets boosted
- **WHEN** the same chunk appears in both vector_search and keyword_search results
- **THEN** its RRF score is higher than items appearing in only one search type

### Requirement: Result provenance
The system SHALL include provenance in every result — the source connector, entity links, and metadata — so callers can trace where information came from.

#### Scenario: Result includes source
- **WHEN** a result is returned from Recall
- **THEN** it includes a Source field indicating the origin (e.g., "markdown", "gmail", "conversation")
- **AND** EntityIDs linking to related entities for follow-up traversal

### Requirement: Memory-first ranking
The system SHALL rank memories (distilled facts) higher than raw chunks when both match a query, because memories are higher-signal extractions.

#### Scenario: Memory outranks chunk for same topic
- **WHEN** a query matches both a memory "Alice works at Stripe" and a chunk containing the original text that memory was extracted from
- **THEN** the memory appears higher in the result ranking
