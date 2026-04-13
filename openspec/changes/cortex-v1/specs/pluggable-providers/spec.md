## ADDED Requirements

### Requirement: Pluggable LLM interface
The system SHALL define an `LLM` interface with methods for entity extraction, query decomposition, and summarization. Implementations are swappable at initialization time.

#### Scenario: Use OpenAI LLM implementation
- **WHEN** `cortex.Open("brain.db", WithLLM(openai.NewLLM(apiKey)))` is called
- **THEN** all LLM operations (extraction, decomposition, summarization) use the OpenAI API

#### Scenario: Use a custom LLM implementation
- **WHEN** a user provides a struct implementing the `LLM` interface
- **THEN** cortex uses that implementation for all LLM operations

### Requirement: Pluggable Embedder interface
The system SHALL define an `Embedder` interface with methods for generating vector embeddings and reporting vector dimensions. The embedder is separate from the LLM because embeddings often come from a different model or provider.

#### Scenario: Use OpenAI embedder
- **WHEN** `cortex.Open("brain.db", WithEmbedder(openai.NewEmbedder(apiKey)))` is called
- **THEN** all embedding operations use OpenAI text-embedding-3-small

#### Scenario: Embedder reports dimensions
- **WHEN** `embedder.Dimensions()` is called
- **THEN** the correct vector dimension for the model is returned (e.g., 1536 for text-embedding-3-small)

### Requirement: Pluggable Extractor interface
The system SHALL define an `Extractor` interface that produces entities, relationships, and memories from content. The system ships three implementations: deterministic, LLM-powered, and hybrid (which composes both).

#### Scenario: Deterministic extraction from structured content
- **WHEN** content with YAML frontmatter or wikilinks is processed by the deterministic extractor
- **THEN** entities and relationships are extracted without any LLM calls

#### Scenario: LLM extraction from unstructured content
- **WHEN** freeform prose is processed by the LLM extractor
- **THEN** entities, relationships, and memories are extracted using the configured LLM

#### Scenario: Hybrid extraction tries deterministic first
- **WHEN** content is processed by the hybrid extractor
- **THEN** the deterministic extractor runs first
- **AND** the LLM extractor processes any remaining unstructured content

### Requirement: Shipped OpenAI implementation
The system SHALL ship a default OpenAI implementation covering both the `LLM` and `Embedder` interfaces, using GPT-4.1-mini for extraction/decomposition and text-embedding-3-small for embeddings.

#### Scenario: OpenAI LLM extracts entities
- **WHEN** `llm.Extract(ctx, text, prompt)` is called with the OpenAI implementation
- **THEN** the OpenAI API is called and the response is parsed into structured entities, relationships, and memories

#### Scenario: OpenAI embedder batches requests
- **WHEN** `embedder.Embed(ctx, texts)` is called with multiple texts
- **THEN** the texts are sent to OpenAI in a single batch request
