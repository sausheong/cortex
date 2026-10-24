# Cortex Core Library Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a working Go library + CLI that can ingest markdown files, extract entities/relationships/memories, and answer natural language queries — the core of a personal knowledge graph.

**Architecture:** Root package `cortex` exposes `Open`, `Remember`, `Recall`, `Forget`, and structured graph CRUD. Embedded SQLite stores entities, relationships, chunks, and memories. Embeddings stored as BLOBs with brute-force cosine similarity (swap in sqlite-vec later). Pluggable LLM/Embedder interfaces with OpenAI default. Hybrid extractor (deterministic + LLM). Markdown connector and CLI as first consumers.

**Tech Stack:** Go 1.22+, `modernc.org/sqlite` (pure Go SQLite), `github.com/oklog/ulid/v2`, `github.com/sashabaranov/go-openai`

---

## File Structure

```
cortex/
├── cortex.go              # Cortex struct, Open(), Close(), Remember(), Recall(), Forget()
├── cortex_test.go         # Integration tests for Remember/Recall/Forget
├── entity.go              # Entity type, PutEntity, GetEntity, FindEntities
├── entity_test.go
├── relationship.go        # Relationship type, PutRelationship, GetRelationships
├── relationship_test.go
├── memory.go              # Memory type, memory CRUD
├── memory_test.go
├── chunk.go               # Chunk type, chunk CRUD
├── chunk_test.go
├── search.go              # SearchVector, SearchKeyword, SearchMemories
├── search_test.go
├── recall.go              # Recall: decomposition → parallel search → RRF
├── recall_test.go
├── traverse.go            # Graph traversal
├── traverse_test.go
├── store.go               # SQLite setup, schema DDL, migrations
├── store_test.go
├── types.go               # Result, Filter, Option types, shared constants
├── math.go                # Cosine similarity, RRF helpers
├── math_test.go
│
├── llm/
│   └── openai/
│       ├── llm.go         # OpenAI LLM implementation
│       ├── llm_test.go
│       ├── embedder.go    # OpenAI Embedder implementation
│       └── embedder_test.go
│
├── extractor/
│   ├── deterministic/
│   │   ├── deterministic.go   # Frontmatter, wikilink, header extraction
│   │   └── deterministic_test.go
│   ├── llmext/
│   │   ├── extractor.go       # LLM-powered extraction
│   │   └── extractor_test.go
│   └── hybrid/
│       ├── hybrid.go          # Composes deterministic + LLM
│       └── hybrid_test.go
│
├── connector/
│   ├── connector.go           # Connector interface
│   └── markdown/
│       ├── markdown.go        # Markdown directory connector
│       └── markdown_test.go
│
├── internal/
│   └── testutil/
│       └── testutil.go        # Mock LLM, mock Embedder, test DB helper
│
├── cmd/
│   └── cortex/
│       └── main.go            # CLI entry point
│
├── go.mod
└── go.sum
```

---

### Task 1: Project Setup

**Files:**
- Create: `go.mod`
- Create: `cortex.go` (stub)
- Create: `cortex_test.go` (stub)

- [ ] **Step 1: Initialize Go module**

Run:
```bash
cd /Users/sausheong/projects/cortex
go mod init github.com/sausheong/cortex
```
Expected: `go.mod` created with module path

- [ ] **Step 2: Add dependencies**

Run:
```bash
go get modernc.org/sqlite
go get github.com/oklog/ulid/v2
go get github.com/sashabaranov/go-openai
```

- [ ] **Step 3: Create directory structure**

Run:
```bash
mkdir -p llm/openai extractor/deterministic extractor/llmext extractor/hybrid connector/markdown internal/testutil cmd/cortex
```

- [ ] **Step 4: Create stub cortex.go to verify build**

```go
// cortex.go
package cortex
```

- [ ] **Step 5: Create stub test to verify test runner**

```go
// cortex_test.go
package cortex

import "testing"

func TestPlaceholder(t *testing.T) {
	// Removed once real tests exist
}
```

- [ ] **Step 6: Run tests**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add go.mod go.sum cortex.go cortex_test.go llm/ extractor/ connector/ internal/ cmd/
git commit -m "chore: initialize Go module and project structure"
```

---

### Task 2: Core Types

**Files:**
- Create: `types.go`
- Create: `llm/interfaces.go`

- [ ] **Step 1: Write types.go with all shared types**

```go
// types.go
package cortex

import "time"

// Entity represents a node in the knowledge graph.
type Entity struct {
	ID         string            `json:"id"`
	Type       string            `json:"type"` // "person", "organization", "concept", "event", "document"
	Name       string            `json:"name"`
	Attributes map[string]any    `json:"attributes,omitempty"`
	Source     string            `json:"source,omitempty"`
	CreatedAt  time.Time         `json:"created_at"`
	UpdatedAt  time.Time         `json:"updated_at"`
}

// Relationship represents a directed edge between two entities.
type Relationship struct {
	ID         string         `json:"id"`
	SourceID   string         `json:"source_id"`
	TargetID   string         `json:"target_id"`
	Type       string         `json:"type"` // "works_at", "knows", "discussed_in", etc.
	Attributes map[string]any `json:"attributes,omitempty"`
	Source     string         `json:"source,omitempty"`
	CreatedAt  time.Time      `json:"created_at"`
}

// Chunk represents a text fragment linked to an entity.
type Chunk struct {
	ID        string         `json:"id"`
	EntityID  string         `json:"entity_id,omitempty"`
	Content   string         `json:"content"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

// Memory represents a distilled fact extracted by the LLM.
type Memory struct {
	ID        string    `json:"id"`
	Content   string    `json:"content"`
	EntityIDs []string  `json:"entity_ids,omitempty"`
	Source    string    `json:"source,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Result is returned by Recall and search methods.
type Result struct {
	Type      string         `json:"type"`    // "memory", "entity", "chunk", "relationship"
	Content   string         `json:"content"`
	Score     float64        `json:"score"`
	EntityIDs []string       `json:"entity_ids,omitempty"`
	Source    string         `json:"source,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

// Filter specifies what to delete in Forget.
type Filter struct {
	EntityID string
	Source   string
	Type     string
}

// EntityFilter specifies criteria for FindEntities.
type EntityFilter struct {
	Type     string
	NameLike string
	Source   string
}

// Graph is returned by Traverse.
type Graph struct {
	Entities      []Entity       `json:"entities"`
	Relationships []Relationship `json:"relationships"`
}

// Option configures a Cortex instance.
type Option func(*config)

type config struct {
	llm       LLM
	embedder  Embedder
	extractor Extractor
}

// LLM handles text understanding.
type LLM interface {
	Extract(ctx context.Context, text string, prompt string) (ExtractionResult, error)
	Decompose(ctx context.Context, query string) ([]StructuredQuery, error)
	Summarize(ctx context.Context, texts []string) (string, error)
}

// Embedder generates vector embeddings.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	Dimensions() int
}

// Extractor pulls entities and relationships from content.
type Extractor interface {
	Extract(ctx context.Context, content string, contentType string) (*Extraction, error)
}

// Extraction holds the result of entity/relationship extraction.
type Extraction struct {
	Entities      []Entity
	Relationships []Relationship
	Memories      []Memory
}

// ExtractionResult wraps raw LLM output and parsed extraction.
type ExtractionResult struct {
	Raw    string
	Parsed *Extraction
}

// StructuredQuery is a decomposed sub-query for Recall.
type StructuredQuery struct {
	Type   string         `json:"type"` // "vector_search", "graph_traverse", "memory_lookup", "keyword_search"
	Params map[string]any `json:"params"`
}

// RememberOption configures a Remember call.
type RememberOption func(*rememberConfig)

type rememberConfig struct {
	source      string
	contentType string
}

// WithSource sets the source provenance for Remember.
func WithSource(source string) RememberOption {
	return func(c *rememberConfig) { c.source = source }
}

// WithContentType hints the content type for extraction.
func WithContentType(ct string) RememberOption {
	return func(c *rememberConfig) { c.contentType = ct }
}

// RecallOption configures a Recall call.
type RecallOption func(*recallConfig)

type recallConfig struct {
	limit  int
	source string
}

// WithLimit sets the max number of results.
func WithLimit(n int) RecallOption {
	return func(c *recallConfig) { c.limit = n }
}

// WithSourceFilter filters results by source.
func WithSourceFilter(source string) RecallOption {
	return func(c *recallConfig) { c.source = source }
}

// RelFilter filters relationships by type.
type RelFilter func(*relFilterConfig)

type relFilterConfig struct {
	relType string
}

// RelTypeFilter filters relationships to a specific type.
func RelTypeFilter(t string) RelFilter {
	return func(c *relFilterConfig) { c.relType = t }
}

// TraverseOption configures a Traverse call.
type TraverseOption func(*traverseConfig)

type traverseConfig struct {
	depth     int
	edgeTypes []string
}

// WithDepth sets traversal depth.
func WithDepth(d int) TraverseOption {
	return func(c *traverseConfig) { c.depth = d }
}

// WithEdgeTypes filters which edge types to follow.
func WithEdgeTypes(types ...string) TraverseOption {
	return func(c *traverseConfig) { c.edgeTypes = types }
}

// WithLLM sets the LLM provider.
func WithLLM(l LLM) Option {
	return func(c *config) { c.llm = l }
}

// WithEmbedder sets the embedding provider.
func WithEmbedder(e Embedder) Option {
	return func(c *config) { c.embedder = e }
}

// WithExtractor sets the extraction provider.
func WithExtractor(e Extractor) Option {
	return func(c *config) { c.extractor = e }
}
```

Note: add `"context"` to the import block (needed by the interfaces).

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add types.go
git commit -m "feat: add core types — Entity, Relationship, Memory, Chunk, Result, interfaces"
```

---

### Task 3: SQLite Store Setup

**Files:**
- Create: `store.go`
- Create: `store_test.go`

- [ ] **Step 1: Write test for store initialization**

```go
// store_test.go
package cortex

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenCreatesDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	c, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer c.Close()

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		t.Fatal("database file was not created")
	}
}

func TestOpenCreatesTablesAndIndexes(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer c.Close()

	tables := []string{"entities", "relationships", "chunks", "memories", "memory_entities", "embeddings"}
	for _, table := range tables {
		var name string
		err := c.db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", table, err)
		}
	}
}

func TestOpenExistingDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	c1, err := Open(dbPath)
	if err != nil {
		t.Fatalf("first Open() error: %v", err)
	}
	c1.Close()

	c2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("second Open() error: %v", err)
	}
	defer c2.Close()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./... -run TestOpen -v`
Expected: FAIL — `Open` not defined

- [ ] **Step 3: Implement store.go**

```go
// store.go
package cortex

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS entities (
	id         TEXT PRIMARY KEY,
	type       TEXT NOT NULL,
	name       TEXT NOT NULL,
	attributes TEXT DEFAULT '{}',
	source     TEXT DEFAULT '',
	created_at DATETIME DEFAULT (datetime('now')),
	updated_at DATETIME DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_entities_type ON entities(type);
CREATE INDEX IF NOT EXISTS idx_entities_name_type ON entities(name, type);

CREATE TABLE IF NOT EXISTS relationships (
	id         TEXT PRIMARY KEY,
	source_id  TEXT NOT NULL REFERENCES entities(id),
	target_id  TEXT NOT NULL REFERENCES entities(id),
	type       TEXT NOT NULL,
	attributes TEXT DEFAULT '{}',
	source     TEXT DEFAULT '',
	created_at DATETIME DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_relationships_source_id ON relationships(source_id);
CREATE INDEX IF NOT EXISTS idx_relationships_target_id ON relationships(target_id);
CREATE INDEX IF NOT EXISTS idx_relationships_type ON relationships(type);

CREATE TABLE IF NOT EXISTS chunks (
	id         TEXT PRIMARY KEY,
	entity_id  TEXT REFERENCES entities(id),
	content    TEXT NOT NULL,
	metadata   TEXT DEFAULT '{}',
	created_at DATETIME DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_chunks_entity_id ON chunks(entity_id);

CREATE VIRTUAL TABLE IF NOT EXISTS chunks_fts USING fts5(content, content_rowid='rowid');

CREATE TABLE IF NOT EXISTS memories (
	id         TEXT PRIMARY KEY,
	content    TEXT NOT NULL,
	source     TEXT DEFAULT '',
	created_at DATETIME DEFAULT (datetime('now')),
	updated_at DATETIME DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS memory_entities (
	memory_id  TEXT NOT NULL REFERENCES memories(id),
	entity_id  TEXT NOT NULL REFERENCES entities(id),
	PRIMARY KEY (memory_id, entity_id)
);

CREATE INDEX IF NOT EXISTS idx_memory_entities_entity_id ON memory_entities(entity_id);

CREATE TABLE IF NOT EXISTS embeddings (
	id        TEXT PRIMARY KEY,
	row_id    TEXT NOT NULL,
	row_type  TEXT NOT NULL,
	embedding BLOB NOT NULL,
	created_at DATETIME DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_embeddings_row ON embeddings(row_id, row_type);

CREATE TABLE IF NOT EXISTS sync_state (
	connector TEXT PRIMARY KEY,
	state     TEXT DEFAULT '{}',
	updated_at DATETIME DEFAULT (datetime('now'))
);
`

// Cortex is the main entry point for the knowledge graph.
type Cortex struct {
	db        *sql.DB
	cfg       config
}

// Open creates or opens a cortex database at the given path.
func Open(dbPath string, opts ...Option) (*Cortex, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	// Enable WAL mode for concurrent reads
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	// Enable foreign keys
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	// Create schema
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}

	c := &Cortex{db: db}
	for _, opt := range opts {
		opt(&c.cfg)
	}

	return c, nil
}

// Close closes the database connection.
func (c *Cortex) Close() error {
	return c.db.Close()
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./... -run TestOpen -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add store.go store_test.go
git commit -m "feat: add SQLite store setup with schema, WAL mode, and FTS5"
```

---

### Task 4: Test Utilities

**Files:**
- Create: `internal/testutil/testutil.go`

- [ ] **Step 1: Create mock LLM, Embedder, and test DB helper**

```go
// internal/testutil/testutil.go
package testutil

import (
	"context"
	"math"
	"path/filepath"
	"testing"

	"github.com/sausheong/cortex"
)

// MockLLM returns canned responses for testing.
type MockLLM struct {
	ExtractFn   func(ctx context.Context, text, prompt string) (cortex.ExtractionResult, error)
	DecomposeFn func(ctx context.Context, query string) ([]cortex.StructuredQuery, error)
	SummarizeFn func(ctx context.Context, texts []string) (string, error)
}

func (m *MockLLM) Extract(ctx context.Context, text, prompt string) (cortex.ExtractionResult, error) {
	if m.ExtractFn != nil {
		return m.ExtractFn(ctx, text, prompt)
	}
	return cortex.ExtractionResult{Parsed: &cortex.Extraction{}}, nil
}

func (m *MockLLM) Decompose(ctx context.Context, query string) ([]cortex.StructuredQuery, error) {
	if m.DecomposeFn != nil {
		return m.DecomposeFn(ctx, query)
	}
	return []cortex.StructuredQuery{
		{Type: "keyword_search", Params: map[string]any{"query": query}},
	}, nil
}

func (m *MockLLM) Summarize(ctx context.Context, texts []string) (string, error) {
	if m.SummarizeFn != nil {
		return m.SummarizeFn(ctx, texts)
	}
	return "summary", nil
}

// MockEmbedder returns deterministic embeddings for testing.
type MockEmbedder struct {
	Dims int
}

func (m *MockEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	result := make([][]float32, len(texts))
	dims := m.Dims
	if dims == 0 {
		dims = 8
	}
	for i, text := range texts {
		vec := make([]float32, dims)
		for j := 0; j < dims && j < len(text); j++ {
			vec[j] = float32(text[j]) / 255.0
		}
		// Normalize
		var norm float64
		for _, v := range vec {
			norm += float64(v) * float64(v)
		}
		norm = math.Sqrt(norm)
		if norm > 0 {
			for j := range vec {
				vec[j] = float32(float64(vec[j]) / norm)
			}
		}
		result[i] = vec
	}
	return result, nil
}

func (m *MockEmbedder) Dimensions() int {
	if m.Dims == 0 {
		return 8
	}
	return m.Dims
}

// MockExtractor returns a fixed extraction for testing.
type MockExtractor struct {
	ExtractFn func(ctx context.Context, content, contentType string) (*cortex.Extraction, error)
}

func (m *MockExtractor) Extract(ctx context.Context, content, contentType string) (*cortex.Extraction, error) {
	if m.ExtractFn != nil {
		return m.ExtractFn(ctx, content, contentType)
	}
	return &cortex.Extraction{}, nil
}

// OpenTestDB creates a Cortex instance with an in-temp-dir database and mock providers.
func OpenTestDB(t *testing.T) *cortex.Cortex {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	mockLLM := &MockLLM{}
	mockEmb := &MockEmbedder{}
	mockExt := &MockExtractor{}
	c, err := cortex.Open(dbPath,
		cortex.WithLLM(mockLLM),
		cortex.WithEmbedder(mockEmb),
		cortex.WithExtractor(mockExt),
	)
	if err != nil {
		t.Fatalf("OpenTestDB: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add internal/testutil/testutil.go
git commit -m "feat: add test utilities — mock LLM, Embedder, Extractor, and test DB helper"
```

---

### Task 5: ULID Generation Helper

**Files:**
- Create: `id.go`

- [ ] **Step 1: Create ULID helper**

```go
// id.go
package cortex

import (
	"math/rand"
	"time"

	"github.com/oklog/ulid/v2"
)

var entropy = rand.New(rand.NewSource(time.Now().UnixNano()))

// newID generates a new ULID.
func newID() string {
	return ulid.MustNew(ulid.Timestamp(time.Now()), entropy).String()
}
```

- [ ] **Step 2: Verify build**

Run: `go build ./...`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add id.go
git commit -m "feat: add ULID generation helper"
```

---

### Task 6: Entity CRUD

**Files:**
- Create: `entity.go`
- Create: `entity_test.go`

- [ ] **Step 1: Write tests for PutEntity and GetEntity**

```go
// entity_test.go
package cortex

import (
	"context"
	"testing"

	"github.com/sausheong/cortex/internal/testutil"
)

func TestPutAndGetEntity(t *testing.T) {
	c := testutil.OpenTestDB(t)
	ctx := context.Background()

	e := Entity{
		Type:       "person",
		Name:       "Alice",
		Attributes: map[string]any{"email": "alice@example.com"},
		Source:     "test",
	}

	err := c.PutEntity(ctx, &e)
	if err != nil {
		t.Fatalf("PutEntity: %v", err)
	}
	if e.ID == "" {
		t.Fatal("PutEntity did not set ID")
	}

	got, err := c.GetEntity(ctx, e.ID)
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if got.Name != "Alice" {
		t.Errorf("Name = %q, want %q", got.Name, "Alice")
	}
	if got.Type != "person" {
		t.Errorf("Type = %q, want %q", got.Type, "person")
	}
	if got.Attributes["email"] != "alice@example.com" {
		t.Errorf("Attributes[email] = %v, want alice@example.com", got.Attributes["email"])
	}
}

func TestPutEntityUpsertsByNameAndType(t *testing.T) {
	c := testutil.OpenTestDB(t)
	ctx := context.Background()

	e1 := Entity{Type: "person", Name: "Alice", Source: "test"}
	if err := c.PutEntity(ctx, &e1); err != nil {
		t.Fatal(err)
	}

	e2 := Entity{Type: "person", Name: "Alice", Attributes: map[string]any{"role": "engineer"}, Source: "test"}
	if err := c.PutEntity(ctx, &e2); err != nil {
		t.Fatal(err)
	}

	if e2.ID != e1.ID {
		t.Errorf("expected upsert to reuse ID %s, got %s", e1.ID, e2.ID)
	}

	got, _ := c.GetEntity(ctx, e1.ID)
	if got.Attributes["role"] != "engineer" {
		t.Errorf("upsert did not update attributes")
	}
}

func TestGetEntityNotFound(t *testing.T) {
	c := testutil.OpenTestDB(t)
	ctx := context.Background()

	_, err := c.GetEntity(ctx, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent entity")
	}
}

func TestFindEntitiesByType(t *testing.T) {
	c := testutil.OpenTestDB(t)
	ctx := context.Background()

	c.PutEntity(ctx, &Entity{Type: "person", Name: "Alice"})
	c.PutEntity(ctx, &Entity{Type: "person", Name: "Bob"})
	c.PutEntity(ctx, &Entity{Type: "organization", Name: "Stripe"})

	people, err := c.FindEntities(ctx, EntityFilter{Type: "person"})
	if err != nil {
		t.Fatal(err)
	}
	if len(people) != 2 {
		t.Errorf("got %d people, want 2", len(people))
	}
}

func TestFindEntitiesByNameLike(t *testing.T) {
	c := testutil.OpenTestDB(t)
	ctx := context.Background()

	c.PutEntity(ctx, &Entity{Type: "person", Name: "Alice"})
	c.PutEntity(ctx, &Entity{Type: "person", Name: "Alberto"})
	c.PutEntity(ctx, &Entity{Type: "person", Name: "Bob"})

	matches, err := c.FindEntities(ctx, EntityFilter{NameLike: "Al%"})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Errorf("got %d matches, want 2", len(matches))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run TestPut -v`
Expected: FAIL — `PutEntity` not defined

- [ ] **Step 3: Implement entity.go**

```go
// entity.go
package cortex

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"
)

// PutEntity creates or updates an entity. If an entity with the same (name, type)
// exists, it is updated (upsert). The entity's ID is set on the struct.
func (c *Cortex) PutEntity(ctx context.Context, e *Entity) error {
	// Check for existing entity with same name and type
	var existingID string
	err := c.db.QueryRowContext(ctx,
		"SELECT id FROM entities WHERE name = ? AND type = ?",
		e.Name, e.Type,
	).Scan(&existingID)

	attrs, err2 := json.Marshal(e.Attributes)
	if err2 != nil {
		return fmt.Errorf("marshal attributes: %w", err2)
	}

	now := time.Now().UTC()

	if err == nil {
		// Update existing
		e.ID = existingID
		e.UpdatedAt = now
		_, err = c.db.ExecContext(ctx,
			"UPDATE entities SET attributes = ?, source = ?, updated_at = ? WHERE id = ?",
			string(attrs), e.Source, now, existingID,
		)
		return err
	}

	// Insert new
	if e.ID == "" {
		e.ID = newID()
	}
	e.CreatedAt = now
	e.UpdatedAt = now
	_, err = c.db.ExecContext(ctx,
		"INSERT INTO entities (id, type, name, attributes, source, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		e.ID, e.Type, e.Name, string(attrs), e.Source, e.CreatedAt, e.UpdatedAt,
	)
	return err
}

// GetEntity retrieves an entity by ID.
func (c *Cortex) GetEntity(ctx context.Context, id string) (*Entity, error) {
	var e Entity
	var attrs string
	err := c.db.QueryRowContext(ctx,
		"SELECT id, type, name, attributes, source, created_at, updated_at FROM entities WHERE id = ?", id,
	).Scan(&e.ID, &e.Type, &e.Name, &attrs, &e.Source, &e.CreatedAt, &e.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("entity %q not found", id)
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(attrs), &e.Attributes); err != nil {
		e.Attributes = map[string]any{}
	}
	return &e, nil
}

// FindEntities returns entities matching the filter.
func (c *Cortex) FindEntities(ctx context.Context, f EntityFilter) ([]Entity, error) {
	query := "SELECT id, type, name, attributes, source, created_at, updated_at FROM entities WHERE 1=1"
	var args []any

	if f.Type != "" {
		query += " AND type = ?"
		args = append(args, f.Type)
	}
	if f.NameLike != "" {
		query += " AND name LIKE ?"
		args = append(args, f.NameLike)
	}
	if f.Source != "" {
		query += " AND source = ?"
		args = append(args, f.Source)
	}

	query += " ORDER BY name"

	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entities []Entity
	for rows.Next() {
		var e Entity
		var attrs string
		if err := rows.Scan(&e.ID, &e.Type, &e.Name, &attrs, &e.Source, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(attrs), &e.Attributes); err != nil {
			e.Attributes = map[string]any{}
		}
		entities = append(entities, e)
	}
	return entities, rows.Err()
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./... -run "TestPut|TestGet|TestFind" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add entity.go entity_test.go
git commit -m "feat: add entity CRUD — PutEntity with upsert, GetEntity, FindEntities"
```

---

### Task 7: Relationship CRUD

**Files:**
- Create: `relationship.go`
- Create: `relationship_test.go`

- [ ] **Step 1: Write tests**

```go
// relationship_test.go
package cortex

import (
	"context"
	"testing"

	"github.com/sausheong/cortex/internal/testutil"
)

func TestPutAndGetRelationships(t *testing.T) {
	c := testutil.OpenTestDB(t)
	ctx := context.Background()

	alice := Entity{Type: "person", Name: "Alice"}
	stripe := Entity{Type: "organization", Name: "Stripe"}
	c.PutEntity(ctx, &alice)
	c.PutEntity(ctx, &stripe)

	rel := Relationship{
		SourceID:   alice.ID,
		TargetID:   stripe.ID,
		Type:       "works_at",
		Attributes: map[string]any{"role": "engineer"},
		Source:     "test",
	}
	if err := c.PutRelationship(ctx, &rel); err != nil {
		t.Fatalf("PutRelationship: %v", err)
	}
	if rel.ID == "" {
		t.Fatal("PutRelationship did not set ID")
	}

	rels, err := c.GetRelationships(ctx, alice.ID)
	if err != nil {
		t.Fatalf("GetRelationships: %v", err)
	}
	if len(rels) != 1 {
		t.Fatalf("got %d relationships, want 1", len(rels))
	}
	if rels[0].Type != "works_at" {
		t.Errorf("Type = %q, want %q", rels[0].Type, "works_at")
	}
}

func TestGetRelationshipsFilterByType(t *testing.T) {
	c := testutil.OpenTestDB(t)
	ctx := context.Background()

	alice := Entity{Type: "person", Name: "Alice"}
	bob := Entity{Type: "person", Name: "Bob"}
	stripe := Entity{Type: "organization", Name: "Stripe"}
	c.PutEntity(ctx, &alice)
	c.PutEntity(ctx, &bob)
	c.PutEntity(ctx, &stripe)

	c.PutRelationship(ctx, &Relationship{SourceID: alice.ID, TargetID: stripe.ID, Type: "works_at"})
	c.PutRelationship(ctx, &Relationship{SourceID: alice.ID, TargetID: bob.ID, Type: "knows"})

	rels, _ := c.GetRelationships(ctx, alice.ID, RelTypeFilter("works_at"))
	if len(rels) != 1 {
		t.Errorf("got %d relationships, want 1", len(rels))
	}
}

func TestGetRelationshipsFromEitherDirection(t *testing.T) {
	c := testutil.OpenTestDB(t)
	ctx := context.Background()

	alice := Entity{Type: "person", Name: "Alice"}
	stripe := Entity{Type: "organization", Name: "Stripe"}
	c.PutEntity(ctx, &alice)
	c.PutEntity(ctx, &stripe)

	c.PutRelationship(ctx, &Relationship{SourceID: alice.ID, TargetID: stripe.ID, Type: "works_at"})

	// Query from target side
	rels, _ := c.GetRelationships(ctx, stripe.ID)
	if len(rels) != 1 {
		t.Errorf("got %d relationships from target side, want 1", len(rels))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run TestPutAndGetRelationships -v`
Expected: FAIL

- [ ] **Step 3: Implement relationship.go**

```go
// relationship.go
package cortex

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// PutRelationship creates a relationship between two entities.
func (c *Cortex) PutRelationship(ctx context.Context, r *Relationship) error {
	if r.ID == "" {
		r.ID = newID()
	}
	r.CreatedAt = time.Now().UTC()

	attrs, err := json.Marshal(r.Attributes)
	if err != nil {
		return fmt.Errorf("marshal attributes: %w", err)
	}

	_, err = c.db.ExecContext(ctx,
		"INSERT INTO relationships (id, source_id, target_id, type, attributes, source, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		r.ID, r.SourceID, r.TargetID, r.Type, string(attrs), r.Source, r.CreatedAt,
	)
	return err
}

// GetRelationships returns relationships involving the given entity (as source or target).
func (c *Cortex) GetRelationships(ctx context.Context, entityID string, opts ...RelFilter) ([]Relationship, error) {
	var cfg relFilterConfig
	for _, opt := range opts {
		opt(&cfg)
	}

	query := "SELECT id, source_id, target_id, type, attributes, source, created_at FROM relationships WHERE (source_id = ? OR target_id = ?)"
	args := []any{entityID, entityID}

	if cfg.relType != "" {
		query += " AND type = ?"
		args = append(args, cfg.relType)
	}

	query += " ORDER BY created_at"

	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rels []Relationship
	for rows.Next() {
		var r Relationship
		var attrs string
		if err := rows.Scan(&r.ID, &r.SourceID, &r.TargetID, &r.Type, &attrs, &r.Source, &r.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(attrs), &r.Attributes); err != nil {
			r.Attributes = map[string]any{}
		}
		rels = append(rels, r)
	}
	return rels, rows.Err()
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./... -run "TestPutAndGetRelationships|TestGetRelationships" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add relationship.go relationship_test.go
git commit -m "feat: add relationship CRUD — PutRelationship, GetRelationships with type filter"
```

---

### Task 8: Chunk Storage and FTS5 Keyword Search

**Files:**
- Create: `chunk.go`
- Create: `chunk_test.go`

- [ ] **Step 1: Write tests**

```go
// chunk_test.go
package cortex

import (
	"context"
	"testing"

	"github.com/sausheong/cortex/internal/testutil"
)

func TestPutChunk(t *testing.T) {
	c := testutil.OpenTestDB(t)
	ctx := context.Background()

	alice := Entity{Type: "person", Name: "Alice"}
	c.PutEntity(ctx, &alice)

	ch := Chunk{
		EntityID: alice.ID,
		Content:  "Alice is a software engineer working on distributed systems",
		Metadata: map[string]any{"file": "alice.md"},
	}
	if err := c.PutChunk(ctx, &ch); err != nil {
		t.Fatalf("PutChunk: %v", err)
	}
	if ch.ID == "" {
		t.Fatal("PutChunk did not set ID")
	}
}

func TestSearchKeyword(t *testing.T) {
	c := testutil.OpenTestDB(t)
	ctx := context.Background()

	c.PutChunk(ctx, &Chunk{Content: "Alice works on distributed systems at Stripe"})
	c.PutChunk(ctx, &Chunk{Content: "Bob prefers functional programming"})
	c.PutChunk(ctx, &Chunk{Content: "Stripe is a payments company"})

	results, err := c.SearchKeyword(ctx, "Stripe", 10)
	if err != nil {
		t.Fatalf("SearchKeyword: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("got %d results, want 2", len(results))
	}
}

func TestSearchKeywordNoResults(t *testing.T) {
	c := testutil.OpenTestDB(t)
	ctx := context.Background()

	c.PutChunk(ctx, &Chunk{Content: "Alice works at Stripe"})

	results, err := c.SearchKeyword(ctx, "Zebadiah", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results, want 0", len(results))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run "TestPutChunk|TestSearchKeyword" -v`
Expected: FAIL

- [ ] **Step 3: Implement chunk.go**

```go
// chunk.go
package cortex

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// PutChunk stores a text chunk and indexes it for FTS5 keyword search.
func (c *Cortex) PutChunk(ctx context.Context, ch *Chunk) error {
	if ch.ID == "" {
		ch.ID = newID()
	}
	ch.CreatedAt = time.Now().UTC()

	meta, err := json.Marshal(ch.Metadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Insert into chunks table
	res, err := tx.ExecContext(ctx,
		"INSERT INTO chunks (id, entity_id, content, metadata, created_at) VALUES (?, ?, ?, ?, ?)",
		ch.ID, ch.EntityID, ch.Content, string(meta), ch.CreatedAt,
	)
	if err != nil {
		return err
	}

	// Get the rowid for FTS5
	rowid, err := res.LastInsertId()
	if err != nil {
		return err
	}

	// Insert into FTS5 index
	_, err = tx.ExecContext(ctx,
		"INSERT INTO chunks_fts (rowid, content) VALUES (?, ?)",
		rowid, ch.Content,
	)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// SearchKeyword performs FTS5 full-text search on chunks.
func (c *Cortex) SearchKeyword(ctx context.Context, query string, limit int) ([]Chunk, error) {
	if limit <= 0 {
		limit = 10
	}

	rows, err := c.db.QueryContext(ctx,
		`SELECT c.id, c.entity_id, c.content, c.metadata, c.created_at
		 FROM chunks c
		 JOIN chunks_fts f ON c.rowid = f.rowid
		 WHERE chunks_fts MATCH ?
		 ORDER BY rank
		 LIMIT ?`,
		query, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("keyword search: %w", err)
	}
	defer rows.Close()

	var chunks []Chunk
	for rows.Next() {
		var ch Chunk
		var meta string
		var entityID *string
		if err := rows.Scan(&ch.ID, &entityID, &ch.Content, &meta, &ch.CreatedAt); err != nil {
			return nil, err
		}
		if entityID != nil {
			ch.EntityID = *entityID
		}
		if err := json.Unmarshal([]byte(meta), &ch.Metadata); err != nil {
			ch.Metadata = map[string]any{}
		}
		chunks = append(chunks, ch)
	}
	return chunks, rows.Err()
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./... -run "TestPutChunk|TestSearchKeyword" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add chunk.go chunk_test.go
git commit -m "feat: add chunk storage with FTS5 keyword search"
```

---

### Task 9: Math Helpers — Cosine Similarity and RRF

**Files:**
- Create: `math.go`
- Create: `math_test.go`

- [ ] **Step 1: Write tests**

```go
// math_test.go
package cortex

import (
	"math"
	"testing"
)

func TestCosineSimilarity(t *testing.T) {
	a := []float32{1, 0, 0}
	b := []float32{1, 0, 0}
	if got := cosineSimilarity(a, b); math.Abs(float64(got)-1.0) > 0.001 {
		t.Errorf("identical vectors: got %f, want 1.0", got)
	}

	c := []float32{0, 1, 0}
	if got := cosineSimilarity(a, c); math.Abs(float64(got)) > 0.001 {
		t.Errorf("orthogonal vectors: got %f, want 0.0", got)
	}
}

func TestCosineSimilarityZeroVector(t *testing.T) {
	a := []float32{1, 0, 0}
	zero := []float32{0, 0, 0}
	if got := cosineSimilarity(a, zero); got != 0 {
		t.Errorf("zero vector: got %f, want 0", got)
	}
}

func TestRRFMerge(t *testing.T) {
	lists := [][]rankedItem{
		{{id: "a", rank: 0}, {id: "b", rank: 1}},
		{{id: "b", rank: 0}, {id: "c", rank: 1}},
	}
	merged := rrfMerge(lists, 60)
	if len(merged) != 3 {
		t.Fatalf("got %d items, want 3", len(merged))
	}
	// "b" appears in both lists so should have the highest score
	if merged[0].id != "b" {
		t.Errorf("top result = %q, want %q", merged[0].id, "b")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run "TestCosine|TestRRF" -v`
Expected: FAIL

- [ ] **Step 3: Implement math.go**

```go
// math.go
package cortex

import (
	"math"
	"sort"
)

// cosineSimilarity computes cosine similarity between two vectors.
func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		normA += float64(a[i]) * float64(a[i])
		normB += float64(b[i]) * float64(b[i])
	}
	normA = math.Sqrt(normA)
	normB = math.Sqrt(normB)
	if normA == 0 || normB == 0 {
		return 0
	}
	return float32(dot / (normA * normB))
}

// encodeFloat32s encodes a float32 slice to bytes for SQLite BLOB storage.
func encodeFloat32s(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		bits := math.Float32bits(f)
		buf[i*4] = byte(bits)
		buf[i*4+1] = byte(bits >> 8)
		buf[i*4+2] = byte(bits >> 16)
		buf[i*4+3] = byte(bits >> 24)
	}
	return buf
}

// decodeFloat32s decodes bytes from SQLite BLOB to float32 slice.
func decodeFloat32s(b []byte) []float32 {
	v := make([]float32, len(b)/4)
	for i := range v {
		bits := uint32(b[i*4]) | uint32(b[i*4+1])<<8 | uint32(b[i*4+2])<<16 | uint32(b[i*4+3])<<24
		v[i] = math.Float32frombits(bits)
	}
	return v
}

type rankedItem struct {
	id    string
	rank  int
	score float64
}

// rrfMerge performs Reciprocal Rank Fusion across multiple ranked lists.
// k is the RRF constant (typically 60).
func rrfMerge(lists [][]rankedItem, k int) []rankedItem {
	scores := map[string]float64{}
	for _, list := range lists {
		for _, item := range list {
			scores[item.id] += 1.0 / float64(k+item.rank+1)
		}
	}

	result := make([]rankedItem, 0, len(scores))
	for id, score := range scores {
		result = append(result, rankedItem{id: id, score: score})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].score > result[j].score
	})
	return result
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./... -run "TestCosine|TestRRF" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add math.go math_test.go
git commit -m "feat: add cosine similarity, float32 encoding, and RRF merge"
```

---

### Task 10: Embedding Storage and Vector Search

**Files:**
- Create: `search.go`
- Create: `search_test.go`

- [ ] **Step 1: Write tests**

```go
// search_test.go
package cortex

import (
	"context"
	"testing"

	"github.com/sausheong/cortex/internal/testutil"
)

func TestStoreAndSearchEmbedding(t *testing.T) {
	c := testutil.OpenTestDB(t)
	ctx := context.Background()

	// Store chunks with embeddings
	ch1 := Chunk{Content: "Alice works on distributed systems"}
	c.PutChunk(ctx, &ch1)
	emb1 := []float32{0.9, 0.1, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0}
	c.putEmbedding(ctx, ch1.ID, "chunk", emb1)

	ch2 := Chunk{Content: "Bob likes functional programming"}
	c.PutChunk(ctx, &ch2)
	emb2 := []float32{0.0, 0.0, 0.9, 0.1, 0.0, 0.0, 0.0, 0.0}
	c.putEmbedding(ctx, ch2.ID, "chunk", emb2)

	// Search with query vector similar to ch1
	queryVec := []float32{0.85, 0.15, 0.0, 0.0, 0.0, 0.0, 0.0, 0.0}
	results, err := c.searchVectorRaw(ctx, queryVec, "chunk", 10)
	if err != nil {
		t.Fatalf("searchVectorRaw: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	// ch1 should be first (most similar)
	if results[0].rowID != ch1.ID {
		t.Errorf("top result = %q, want %q", results[0].rowID, ch1.ID)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run TestStoreAndSearchEmbedding -v`
Expected: FAIL

- [ ] **Step 3: Implement search.go**

```go
// search.go
package cortex

import (
	"context"
	"fmt"
	"sort"
)

type vectorResult struct {
	rowID      string
	similarity float32
}

// putEmbedding stores a vector embedding for a chunk or memory.
func (c *Cortex) putEmbedding(ctx context.Context, rowID, rowType string, embedding []float32) error {
	id := newID()
	blob := encodeFloat32s(embedding)
	_, err := c.db.ExecContext(ctx,
		"INSERT INTO embeddings (id, row_id, row_type, embedding, created_at) VALUES (?, ?, ?, ?, datetime('now'))",
		id, rowID, rowType, blob,
	)
	return err
}

// searchVectorRaw performs brute-force cosine similarity search over embeddings.
func (c *Cortex) searchVectorRaw(ctx context.Context, queryVec []float32, rowType string, limit int) ([]vectorResult, error) {
	rows, err := c.db.QueryContext(ctx,
		"SELECT row_id, embedding FROM embeddings WHERE row_type = ?",
		rowType,
	)
	if err != nil {
		return nil, fmt.Errorf("vector search query: %w", err)
	}
	defer rows.Close()

	var results []vectorResult
	for rows.Next() {
		var rowID string
		var blob []byte
		if err := rows.Scan(&rowID, &blob); err != nil {
			return nil, err
		}
		emb := decodeFloat32s(blob)
		sim := cosineSimilarity(queryVec, emb)
		results = append(results, vectorResult{rowID: rowID, similarity: sim})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].similarity > results[j].similarity
	})

	if limit > 0 && len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}

// SearchVector embeds the query text and searches for similar chunks.
func (c *Cortex) SearchVector(ctx context.Context, query string, limit int) ([]Chunk, error) {
	if c.cfg.embedder == nil {
		return nil, fmt.Errorf("no embedder configured")
	}

	vecs, err := c.cfg.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(vecs) == 0 {
		return nil, nil
	}

	results, err := c.searchVectorRaw(ctx, vecs[0], "chunk", limit)
	if err != nil {
		return nil, err
	}

	chunks := make([]Chunk, 0, len(results))
	for _, r := range results {
		var ch Chunk
		var meta string
		var entityID *string
		err := c.db.QueryRowContext(ctx,
			"SELECT id, entity_id, content, metadata, created_at FROM chunks WHERE id = ?",
			r.rowID,
		).Scan(&ch.ID, &entityID, &ch.Content, &meta, &ch.CreatedAt)
		if err != nil {
			continue
		}
		if entityID != nil {
			ch.EntityID = *entityID
		}
		chunks = append(chunks, ch)
	}
	return chunks, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./... -run TestStoreAndSearchEmbedding -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add search.go search_test.go
git commit -m "feat: add embedding storage and brute-force vector search"
```

---

### Task 11: Memory Storage and Search

**Files:**
- Create: `memory.go`
- Create: `memory_test.go`

- [ ] **Step 1: Write tests**

```go
// memory_test.go
package cortex

import (
	"context"
	"testing"

	"github.com/sausheong/cortex/internal/testutil"
)

func TestPutAndSearchMemory(t *testing.T) {
	c := testutil.OpenTestDB(t)
	ctx := context.Background()

	alice := Entity{Type: "person", Name: "Alice"}
	stripe := Entity{Type: "organization", Name: "Stripe"}
	c.PutEntity(ctx, &alice)
	c.PutEntity(ctx, &stripe)

	m := Memory{
		Content:   "Alice is joining Stripe next month",
		EntityIDs: []string{alice.ID, stripe.ID},
		Source:    "conversation",
	}
	if err := c.PutMemory(ctx, &m); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}
	if m.ID == "" {
		t.Fatal("PutMemory did not set ID")
	}

	// Search by keyword
	results, err := c.SearchMemories(ctx, "Stripe", 10)
	if err != nil {
		t.Fatalf("SearchMemories: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	if results[0].Content != m.Content {
		t.Errorf("Content = %q, want %q", results[0].Content, m.Content)
	}
}

func TestGetMemoriesByEntity(t *testing.T) {
	c := testutil.OpenTestDB(t)
	ctx := context.Background()

	alice := Entity{Type: "person", Name: "Alice"}
	c.PutEntity(ctx, &alice)

	m := Memory{Content: "Alice prefers Go", EntityIDs: []string{alice.ID}}
	c.PutMemory(ctx, &m)

	memories, err := c.GetMemoriesByEntity(ctx, alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 1 {
		t.Errorf("got %d memories, want 1", len(memories))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run "TestPutAndSearchMemory|TestGetMemoriesByEntity" -v`
Expected: FAIL

- [ ] **Step 3: Implement memory.go**

```go
// memory.go
package cortex

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// PutMemory stores a distilled fact and links it to entities.
func (c *Cortex) PutMemory(ctx context.Context, m *Memory) error {
	if m.ID == "" {
		m.ID = newID()
	}
	now := time.Now().UTC()
	m.CreatedAt = now
	m.UpdatedAt = now

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(ctx,
		"INSERT INTO memories (id, content, source, created_at, updated_at) VALUES (?, ?, ?, ?, ?)",
		m.ID, m.Content, m.Source, m.CreatedAt, m.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("insert memory: %w", err)
	}

	for _, eid := range m.EntityIDs {
		_, err = tx.ExecContext(ctx,
			"INSERT OR IGNORE INTO memory_entities (memory_id, entity_id) VALUES (?, ?)",
			m.ID, eid,
		)
		if err != nil {
			return fmt.Errorf("link memory to entity: %w", err)
		}
	}

	return tx.Commit()
}

// SearchMemories searches memories by keyword (simple LIKE match on content).
func (c *Cortex) SearchMemories(ctx context.Context, query string, limit int) ([]Memory, error) {
	if limit <= 0 {
		limit = 10
	}

	// Split query into words for flexible matching
	words := strings.Fields(query)
	if len(words) == 0 {
		return nil, nil
	}

	whereClauses := make([]string, len(words))
	args := make([]any, len(words))
	for i, w := range words {
		whereClauses[i] = "content LIKE ?"
		args[i] = "%" + w + "%"
	}
	args = append(args, limit)

	q := fmt.Sprintf(
		"SELECT id, content, source, created_at, updated_at FROM memories WHERE %s ORDER BY updated_at DESC LIMIT ?",
		strings.Join(whereClauses, " OR "),
	)

	rows, err := c.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var memories []Memory
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.Content, &m.Source, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		// Load entity links
		erows, err := c.db.QueryContext(ctx, "SELECT entity_id FROM memory_entities WHERE memory_id = ?", m.ID)
		if err != nil {
			return nil, err
		}
		for erows.Next() {
			var eid string
			erows.Scan(&eid)
			m.EntityIDs = append(m.EntityIDs, eid)
		}
		erows.Close()
		memories = append(memories, m)
	}
	return memories, rows.Err()
}

// GetMemoriesByEntity returns all memories linked to an entity.
func (c *Cortex) GetMemoriesByEntity(ctx context.Context, entityID string) ([]Memory, error) {
	rows, err := c.db.QueryContext(ctx,
		`SELECT m.id, m.content, m.source, m.created_at, m.updated_at
		 FROM memories m
		 JOIN memory_entities me ON m.id = me.memory_id
		 WHERE me.entity_id = ?
		 ORDER BY m.updated_at DESC`,
		entityID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var memories []Memory
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.Content, &m.Source, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, err
		}
		m.EntityIDs = []string{entityID}
		memories = append(memories, m)
	}
	return memories, rows.Err()
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./... -run "TestPutAndSearchMemory|TestGetMemoriesByEntity" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add memory.go memory_test.go
git commit -m "feat: add memory storage, keyword search, and entity-linked queries"
```

---

### Task 12: Graph Traversal

**Files:**
- Create: `traverse.go`
- Create: `traverse_test.go`

- [ ] **Step 1: Write tests**

```go
// traverse_test.go
package cortex

import (
	"context"
	"testing"

	"github.com/sausheong/cortex/internal/testutil"
)

func TestTraverseOneLevel(t *testing.T) {
	c := testutil.OpenTestDB(t)
	ctx := context.Background()

	alice := Entity{Type: "person", Name: "Alice"}
	bob := Entity{Type: "person", Name: "Bob"}
	stripe := Entity{Type: "organization", Name: "Stripe"}
	c.PutEntity(ctx, &alice)
	c.PutEntity(ctx, &bob)
	c.PutEntity(ctx, &stripe)
	c.PutRelationship(ctx, &Relationship{SourceID: alice.ID, TargetID: stripe.ID, Type: "works_at"})
	c.PutRelationship(ctx, &Relationship{SourceID: alice.ID, TargetID: bob.ID, Type: "knows"})

	g, err := c.Traverse(ctx, alice.ID, WithDepth(1))
	if err != nil {
		t.Fatalf("Traverse: %v", err)
	}
	if len(g.Entities) != 3 {
		t.Errorf("got %d entities, want 3 (alice + bob + stripe)", len(g.Entities))
	}
	if len(g.Relationships) != 2 {
		t.Errorf("got %d relationships, want 2", len(g.Relationships))
	}
}

func TestTraverseWithEdgeTypeFilter(t *testing.T) {
	c := testutil.OpenTestDB(t)
	ctx := context.Background()

	alice := Entity{Type: "person", Name: "Alice"}
	bob := Entity{Type: "person", Name: "Bob"}
	stripe := Entity{Type: "organization", Name: "Stripe"}
	c.PutEntity(ctx, &alice)
	c.PutEntity(ctx, &bob)
	c.PutEntity(ctx, &stripe)
	c.PutRelationship(ctx, &Relationship{SourceID: alice.ID, TargetID: stripe.ID, Type: "works_at"})
	c.PutRelationship(ctx, &Relationship{SourceID: alice.ID, TargetID: bob.ID, Type: "knows"})

	g, err := c.Traverse(ctx, alice.ID, WithDepth(1), WithEdgeTypes("works_at"))
	if err != nil {
		t.Fatal(err)
	}
	// Should only follow "works_at" edges: alice + stripe
	if len(g.Entities) != 2 {
		t.Errorf("got %d entities, want 2", len(g.Entities))
	}
}

func TestTraverseDepthZero(t *testing.T) {
	c := testutil.OpenTestDB(t)
	ctx := context.Background()

	alice := Entity{Type: "person", Name: "Alice"}
	c.PutEntity(ctx, &alice)

	g, err := c.Traverse(ctx, alice.ID, WithDepth(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(g.Entities) != 1 {
		t.Errorf("got %d entities, want 1", len(g.Entities))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run TestTraverse -v`
Expected: FAIL

- [ ] **Step 3: Implement traverse.go**

```go
// traverse.go
package cortex

import "context"

// Traverse walks the graph from startID, following relationships up to the given depth.
func (c *Cortex) Traverse(ctx context.Context, startID string, opts ...TraverseOption) (*Graph, error) {
	cfg := traverseConfig{depth: 1}
	for _, opt := range opts {
		opt(&cfg)
	}

	visited := map[string]bool{}
	var allEntities []Entity
	var allRels []Relationship

	startEntity, err := c.GetEntity(ctx, startID)
	if err != nil {
		return nil, err
	}
	visited[startID] = true
	allEntities = append(allEntities, *startEntity)

	frontier := []string{startID}

	for d := 0; d < cfg.depth; d++ {
		var nextFrontier []string
		for _, entityID := range frontier {
			rels, err := c.GetRelationships(ctx, entityID)
			if err != nil {
				return nil, err
			}
			for _, rel := range rels {
				// Filter by edge type if specified
				if len(cfg.edgeTypes) > 0 && !contains(cfg.edgeTypes, rel.Type) {
					continue
				}
				allRels = append(allRels, rel)

				// Find the other end of the relationship
				neighborID := rel.TargetID
				if neighborID == entityID {
					neighborID = rel.SourceID
				}

				if !visited[neighborID] {
					visited[neighborID] = true
					neighbor, err := c.GetEntity(ctx, neighborID)
					if err != nil {
						continue
					}
					allEntities = append(allEntities, *neighbor)
					nextFrontier = append(nextFrontier, neighborID)
				}
			}
		}
		frontier = nextFrontier
	}

	return &Graph{Entities: allEntities, Relationships: allRels}, nil
}

func contains(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./... -run TestTraverse -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add traverse.go traverse_test.go
git commit -m "feat: add graph traversal with configurable depth and edge type filtering"
```

---

### Task 13: Deterministic Extractor

**Files:**
- Create: `extractor/deterministic/deterministic.go`
- Create: `extractor/deterministic/deterministic_test.go`

- [ ] **Step 1: Write tests**

```go
// extractor/deterministic/deterministic_test.go
package deterministic

import (
	"context"
	"testing"
)

func TestExtractFrontmatter(t *testing.T) {
	content := `---
type: person
name: Alice
tags: [engineering, leadership]
---
Alice is a staff engineer at Stripe.`

	ext := New()
	result, err := ext.Extract(context.Background(), content, "markdown")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entities) == 0 {
		t.Fatal("expected at least one entity from frontmatter")
	}
	if result.Entities[0].Name != "Alice" {
		t.Errorf("Name = %q, want Alice", result.Entities[0].Name)
	}
	if result.Entities[0].Type != "person" {
		t.Errorf("Type = %q, want person", result.Entities[0].Type)
	}
}

func TestExtractWikilinks(t *testing.T) {
	content := `Alice works at [[Stripe]] and knows [[Bob]].`

	ext := New()
	result, err := ext.Extract(context.Background(), content, "markdown")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entities) != 2 {
		t.Fatalf("got %d entities, want 2 (Stripe, Bob)", len(result.Entities))
	}
}

func TestExtractNoMarkdownContent(t *testing.T) {
	content := "Just some plain text with no special markers."

	ext := New()
	result, err := ext.Extract(context.Background(), content, "markdown")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entities) != 0 {
		t.Errorf("got %d entities, want 0", len(result.Entities))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./extractor/deterministic/ -v`
Expected: FAIL

- [ ] **Step 3: Implement deterministic extractor**

```go
// extractor/deterministic/deterministic.go
package deterministic

import (
	"context"
	"regexp"
	"strings"

	"github.com/sausheong/cortex"
)

var wikilinkRe = regexp.MustCompile(`\[\[([^\]]+)\]\]`)

// Extractor performs deterministic extraction from structured content.
type Extractor struct{}

// New creates a new deterministic extractor.
func New() *Extractor {
	return &Extractor{}
}

// Extract pulls entities and relationships from content using deterministic rules.
func (e *Extractor) Extract(_ context.Context, content string, contentType string) (*cortex.Extraction, error) {
	result := &cortex.Extraction{}

	if contentType == "markdown" || contentType == "" {
		e.extractFrontmatter(content, result)
		e.extractWikilinks(content, result)
	}

	return result, nil
}

func (e *Extractor) extractFrontmatter(content string, result *cortex.Extraction) {
	if !strings.HasPrefix(strings.TrimSpace(content), "---") {
		return
	}

	parts := strings.SplitN(content, "---", 3)
	if len(parts) < 3 {
		return
	}

	fm := parts[1]
	attrs := map[string]any{}
	var name, typ string

	for _, line := range strings.Split(fm, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		kv := strings.SplitN(line, ":", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(kv[1])

		switch key {
		case "name":
			name = val
		case "type":
			typ = val
		default:
			attrs[key] = val
		}
	}

	if name != "" {
		if typ == "" {
			typ = "document"
		}
		result.Entities = append(result.Entities, cortex.Entity{
			Type:       typ,
			Name:       name,
			Attributes: attrs,
			Source:     "markdown",
		})
	}
}

func (e *Extractor) extractWikilinks(content string, result *cortex.Extraction) {
	matches := wikilinkRe.FindAllStringSubmatch(content, -1)
	seen := map[string]bool{}
	for _, m := range matches {
		name := strings.TrimSpace(m[1])
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		result.Entities = append(result.Entities, cortex.Entity{
			Type:   "document", // default type for wikilinks; will be merged if entity exists
			Name:   name,
			Source: "markdown",
		})
	}
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./extractor/deterministic/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add extractor/deterministic/
git commit -m "feat: add deterministic extractor — YAML frontmatter and wikilink parsing"
```

---

### Task 14: LLM Extractor

**Files:**
- Create: `extractor/llmext/extractor.go`
- Create: `extractor/llmext/extractor_test.go`

- [ ] **Step 1: Write tests (using mock LLM)**

```go
// extractor/llmext/extractor_test.go
package llmext

import (
	"context"
	"testing"

	"github.com/sausheong/cortex"
)

type mockLLM struct{}

func (m *mockLLM) Extract(_ context.Context, text, prompt string) (cortex.ExtractionResult, error) {
	return cortex.ExtractionResult{
		Parsed: &cortex.Extraction{
			Entities: []cortex.Entity{
				{Type: "person", Name: "Alice"},
				{Type: "organization", Name: "Stripe"},
			},
			Relationships: []cortex.Relationship{
				{Type: "works_at"},
			},
			Memories: []cortex.Memory{
				{Content: "Alice works at Stripe"},
			},
		},
	}, nil
}

func (m *mockLLM) Decompose(_ context.Context, q string) ([]cortex.StructuredQuery, error) {
	return nil, nil
}

func (m *mockLLM) Summarize(_ context.Context, texts []string) (string, error) {
	return "", nil
}

func TestLLMExtractor(t *testing.T) {
	ext := New(&mockLLM{})
	result, err := ext.Extract(context.Background(), "Alice works at Stripe as an engineer.", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entities) != 2 {
		t.Errorf("got %d entities, want 2", len(result.Entities))
	}
	if len(result.Relationships) != 1 {
		t.Errorf("got %d relationships, want 1", len(result.Relationships))
	}
	if len(result.Memories) != 1 {
		t.Errorf("got %d memories, want 1", len(result.Memories))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./extractor/llmext/ -v`
Expected: FAIL

- [ ] **Step 3: Implement LLM extractor**

```go
// extractor/llmext/extractor.go
package llmext

import (
	"context"
	"fmt"

	"github.com/sausheong/cortex"
)

const extractionPrompt = `Extract entities, relationships, and key facts from the following text.

Return a JSON object with this structure:
{
  "entities": [{"type": "person|organization|concept|event|document", "name": "..."}],
  "relationships": [{"source": "entity name", "target": "entity name", "type": "works_at|knows|related_to|discussed_in|attended|..."}],
  "memories": [{"content": "A concise distilled fact"}]
}

Rules:
- Entity types: person, organization, concept, event, document
- Relationship types are open-ended but prefer: works_at, knows, related_to, discussed_in, attended, created, part_of
- Memories should be concise, standalone facts that would be useful to recall later
- Only extract entities and relationships that are clearly stated or strongly implied
- Return valid JSON only`

// Extractor uses an LLM to extract entities, relationships, and memories from text.
type Extractor struct {
	llm cortex.LLM
}

// New creates a new LLM-powered extractor.
func New(llm cortex.LLM) *Extractor {
	return &Extractor{llm: llm}
}

// Extract sends content to the LLM for entity/relationship/memory extraction.
func (e *Extractor) Extract(ctx context.Context, content string, contentType string) (*cortex.Extraction, error) {
	result, err := e.llm.Extract(ctx, content, extractionPrompt)
	if err != nil {
		return nil, fmt.Errorf("llm extraction: %w", err)
	}
	if result.Parsed == nil {
		return &cortex.Extraction{}, nil
	}
	return result.Parsed, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./extractor/llmext/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add extractor/llmext/
git commit -m "feat: add LLM-powered extractor with structured extraction prompt"
```

---

### Task 15: Hybrid Extractor

**Files:**
- Create: `extractor/hybrid/hybrid.go`
- Create: `extractor/hybrid/hybrid_test.go`

- [ ] **Step 1: Write test**

```go
// extractor/hybrid/hybrid_test.go
package hybrid

import (
	"context"
	"testing"

	"github.com/sausheong/cortex"
)

type stubExtractor struct {
	entities []cortex.Entity
}

func (s *stubExtractor) Extract(_ context.Context, _ string, _ string) (*cortex.Extraction, error) {
	return &cortex.Extraction{Entities: s.entities}, nil
}

func TestHybridMergesBothExtractors(t *testing.T) {
	det := &stubExtractor{entities: []cortex.Entity{{Type: "person", Name: "Alice"}}}
	llm := &stubExtractor{entities: []cortex.Entity{{Type: "organization", Name: "Stripe"}}}

	ext := New(det, llm)
	result, err := ext.Extract(context.Background(), "Alice works at Stripe", "markdown")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Entities) != 2 {
		t.Errorf("got %d entities, want 2", len(result.Entities))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./extractor/hybrid/ -v`
Expected: FAIL

- [ ] **Step 3: Implement hybrid extractor**

```go
// extractor/hybrid/hybrid.go
package hybrid

import (
	"context"

	"github.com/sausheong/cortex"
)

// Extractor composes a deterministic extractor and an LLM extractor.
// Deterministic runs first, LLM fills in gaps.
type Extractor struct {
	deterministic cortex.Extractor
	llm           cortex.Extractor
}

// New creates a hybrid extractor.
func New(deterministic, llm cortex.Extractor) *Extractor {
	return &Extractor{deterministic: deterministic, llm: llm}
}

// Extract runs deterministic extraction first, then LLM, and merges results.
func (e *Extractor) Extract(ctx context.Context, content string, contentType string) (*cortex.Extraction, error) {
	result := &cortex.Extraction{}

	// Run deterministic extractor
	if e.deterministic != nil {
		det, err := e.deterministic.Extract(ctx, content, contentType)
		if err == nil && det != nil {
			result.Entities = append(result.Entities, det.Entities...)
			result.Relationships = append(result.Relationships, det.Relationships...)
			result.Memories = append(result.Memories, det.Memories...)
		}
	}

	// Run LLM extractor
	if e.llm != nil {
		llm, err := e.llm.Extract(ctx, content, contentType)
		if err == nil && llm != nil {
			result.Entities = append(result.Entities, llm.Entities...)
			result.Relationships = append(result.Relationships, llm.Relationships...)
			result.Memories = append(result.Memories, llm.Memories...)
		}
	}

	return result, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./extractor/hybrid/ -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add extractor/hybrid/
git commit -m "feat: add hybrid extractor — composes deterministic and LLM extraction"
```

---

### Task 16: Remember Pipeline

**Files:**
- Modify: `cortex.go`
- Create: `cortex_test.go` (replace stub)

- [ ] **Step 1: Write test for Remember**

```go
// cortex_test.go
package cortex

import (
	"context"
	"testing"

	"github.com/sausheong/cortex/internal/testutil"
)

func TestRemember(t *testing.T) {
	c := testutil.OpenTestDB(t)
	ctx := context.Background()

	// Configure mock extractor to return known entities
	c.SetExtractor(&testutil.MockExtractor{
		ExtractFn: func(_ context.Context, content, _ string) (*Extraction, error) {
			return &Extraction{
				Entities: []Entity{
					{Type: "person", Name: "Alice"},
					{Type: "organization", Name: "Stripe"},
				},
				Relationships: []Relationship{
					{Type: "works_at"},
				},
				Memories: []Memory{
					{Content: "Alice works at Stripe"},
				},
			}, nil
		},
	})

	err := c.Remember(ctx, "Alice works at Stripe as an engineer", WithSource("test"))
	if err != nil {
		t.Fatalf("Remember: %v", err)
	}

	// Verify entities were created
	people, _ := c.FindEntities(ctx, EntityFilter{Type: "person"})
	if len(people) != 1 || people[0].Name != "Alice" {
		t.Errorf("expected person Alice, got %v", people)
	}

	orgs, _ := c.FindEntities(ctx, EntityFilter{Type: "organization"})
	if len(orgs) != 1 || orgs[0].Name != "Stripe" {
		t.Errorf("expected org Stripe, got %v", orgs)
	}

	// Verify memory was created
	mems, _ := c.SearchMemories(ctx, "Stripe", 10)
	if len(mems) != 1 {
		t.Errorf("expected 1 memory, got %d", len(mems))
	}
}
```

Note: `SetExtractor` needs to be added as a method on `Cortex` to allow tests to swap the extractor after construction. Add to `cortex.go`:

```go
func (c *Cortex) SetExtractor(e Extractor) { c.cfg.extractor = e }
func (c *Cortex) SetLLM(l LLM)             { c.cfg.llm = l }
func (c *Cortex) SetEmbedder(e Embedder)   { c.cfg.embedder = e }
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./... -run TestRemember -v`
Expected: FAIL — `Remember` not defined

- [ ] **Step 3: Implement Remember in cortex.go**

```go
// cortex.go
package cortex

import (
	"context"
	"fmt"
)

// Remember ingests content, extracts entities/relationships/memories,
// generates embeddings, and stores everything.
func (c *Cortex) Remember(ctx context.Context, content string, opts ...RememberOption) error {
	cfg := rememberConfig{contentType: "text"}
	for _, opt := range opts {
		opt(&cfg)
	}

	if c.cfg.extractor == nil {
		return fmt.Errorf("no extractor configured")
	}

	// Extract entities, relationships, and memories
	extraction, err := c.cfg.extractor.Extract(ctx, content, cfg.contentType)
	if err != nil {
		return fmt.Errorf("extraction: %w", err)
	}

	// Store entities (upsert by name+type)
	entityIDsByName := map[string]string{}
	for i := range extraction.Entities {
		e := &extraction.Entities[i]
		if cfg.source != "" {
			e.Source = cfg.source
		}
		if err := c.PutEntity(ctx, e); err != nil {
			return fmt.Errorf("put entity %q: %w", e.Name, err)
		}
		entityIDsByName[e.Name] = e.ID
	}

	// Store relationships (resolve source/target by name)
	for i := range extraction.Relationships {
		r := &extraction.Relationships[i]
		if cfg.source != "" {
			r.Source = cfg.source
		}
		// If SourceID/TargetID not set, they will be empty — skip
		// The LLM extractor returns names in SourceID/TargetID fields as a convention
		if id, ok := entityIDsByName[r.SourceID]; ok {
			r.SourceID = id
		}
		if id, ok := entityIDsByName[r.TargetID]; ok {
			r.TargetID = id
		}
		// Only store if both ends are resolved
		if r.SourceID != "" && r.TargetID != "" {
			c.PutRelationship(ctx, r)
		}
	}

	// Store the raw content as a chunk
	ch := &Chunk{Content: content}
	if cfg.source != "" {
		ch.Metadata = map[string]any{"source": cfg.source}
	}
	if err := c.PutChunk(ctx, ch); err != nil {
		return fmt.Errorf("put chunk: %w", err)
	}

	// Embed the chunk
	if c.cfg.embedder != nil {
		vecs, err := c.cfg.embedder.Embed(ctx, []string{content})
		if err == nil && len(vecs) > 0 {
			c.putEmbedding(ctx, ch.ID, "chunk", vecs[0])
		}
	}

	// Store memories
	for i := range extraction.Memories {
		m := &extraction.Memories[i]
		if cfg.source != "" {
			m.Source = cfg.source
		}
		// Link memory to all extracted entities
		for _, eid := range entityIDsByName {
			m.EntityIDs = append(m.EntityIDs, eid)
		}
		if err := c.PutMemory(ctx, m); err != nil {
			return fmt.Errorf("put memory: %w", err)
		}
		// Embed the memory
		if c.cfg.embedder != nil {
			vecs, err := c.cfg.embedder.Embed(ctx, []string{m.Content})
			if err == nil && len(vecs) > 0 {
				c.putEmbedding(ctx, m.ID, "memory", vecs[0])
			}
		}
	}

	return nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./... -run TestRemember -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cortex.go cortex_test.go
git commit -m "feat: add Remember pipeline — extract, store entities/relationships/memories, embed"
```

---

### Task 17: Recall Pipeline

**Files:**
- Create: `recall.go`
- Create: `recall_test.go`

- [ ] **Step 1: Write tests**

```go
// recall_test.go
package cortex

import (
	"context"
	"testing"

	"github.com/sausheong/cortex/internal/testutil"
)

func TestRecallFindsMemories(t *testing.T) {
	c := testutil.OpenTestDB(t)
	ctx := context.Background()

	// Seed data directly
	alice := Entity{Type: "person", Name: "Alice"}
	c.PutEntity(ctx, &alice)
	c.PutMemory(ctx, &Memory{Content: "Alice is joining Stripe next month", EntityIDs: []string{alice.ID}})

	// Mock LLM to decompose into keyword search
	c.SetLLM(&testutil.MockLLM{
		DecomposeFn: func(_ context.Context, query string) ([]StructuredQuery, error) {
			return []StructuredQuery{
				{Type: "memory_lookup", Params: map[string]any{"query": "Alice Stripe"}},
				{Type: "keyword_search", Params: map[string]any{"query": "Alice"}},
			}, nil
		},
	})

	results, err := c.Recall(ctx, "What do I know about Alice?")
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one result")
	}

	found := false
	for _, r := range results {
		if r.Type == "memory" {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected a memory result")
	}
}

func TestRecallNoResults(t *testing.T) {
	c := testutil.OpenTestDB(t)
	ctx := context.Background()

	c.SetLLM(&testutil.MockLLM{})

	results, err := c.Recall(ctx, "What do I know about Zebadiah?")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("got %d results, want 0", len(results))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run TestRecall -v`
Expected: FAIL

- [ ] **Step 3: Implement recall.go**

```go
// recall.go
package cortex

import (
	"context"
	"fmt"
	"sync"
)

// Recall answers a natural language query by decomposing it into sub-queries,
// executing them in parallel, and merging results via RRF.
func (c *Cortex) Recall(ctx context.Context, query string, opts ...RecallOption) ([]Result, error) {
	cfg := recallConfig{limit: 20}
	for _, opt := range opts {
		opt(&cfg)
	}

	// Decompose query into sub-queries
	var subQueries []StructuredQuery
	if c.cfg.llm != nil {
		var err error
		subQueries, err = c.cfg.llm.Decompose(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("decompose query: %w", err)
		}
	}

	// Fallback: if no LLM or no sub-queries, do a basic keyword + memory search
	if len(subQueries) == 0 {
		subQueries = []StructuredQuery{
			{Type: "keyword_search", Params: map[string]any{"query": query}},
			{Type: "memory_lookup", Params: map[string]any{"query": query}},
		}
	}

	// Execute sub-queries in parallel
	type subResult struct {
		items []rankedItem
		results []Result
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	var rankedLists [][]rankedItem
	allResults := map[string]Result{}

	for _, sq := range subQueries {
		wg.Add(1)
		go func(sq StructuredQuery) {
			defer wg.Done()

			q, _ := sq.Params["query"].(string)
			if q == "" {
				q = query
			}

			var items []rankedItem
			var results []Result

			switch sq.Type {
			case "memory_lookup":
				mems, err := c.SearchMemories(ctx, q, cfg.limit)
				if err != nil {
					return
				}
				for rank, m := range mems {
					items = append(items, rankedItem{id: "mem:" + m.ID, rank: rank})
					results = append(results, Result{
						Type:      "memory",
						Content:   m.Content,
						EntityIDs: m.EntityIDs,
						Source:    m.Source,
					})
				}

			case "keyword_search":
				chunks, err := c.SearchKeyword(ctx, q, cfg.limit)
				if err != nil {
					return
				}
				for rank, ch := range chunks {
					items = append(items, rankedItem{id: "chunk:" + ch.ID, rank: rank})
					r := Result{
						Type:    "chunk",
						Content: ch.Content,
						Source:  "",
					}
					if ch.EntityID != "" {
						r.EntityIDs = []string{ch.EntityID}
					}
					results = append(results, r)
				}

			case "vector_search":
				chunks, err := c.SearchVector(ctx, q, cfg.limit)
				if err != nil {
					return
				}
				for rank, ch := range chunks {
					items = append(items, rankedItem{id: "chunk:" + ch.ID, rank: rank})
					r := Result{
						Type:    "chunk",
						Content: ch.Content,
					}
					if ch.EntityID != "" {
						r.EntityIDs = []string{ch.EntityID}
					}
					results = append(results, r)
				}

			case "graph_traverse":
				entityName, _ := sq.Params["entity"].(string)
				if entityName == "" {
					return
				}
				entities, err := c.FindEntities(ctx, EntityFilter{NameLike: entityName + "%"})
				if err != nil || len(entities) == 0 {
					return
				}
				g, err := c.Traverse(ctx, entities[0].ID, WithDepth(1))
				if err != nil {
					return
				}
				for rank, e := range g.Entities {
					items = append(items, rankedItem{id: "entity:" + e.ID, rank: rank})
					results = append(results, Result{
						Type:      "entity",
						Content:   fmt.Sprintf("%s (%s)", e.Name, e.Type),
						EntityIDs: []string{e.ID},
						Source:    e.Source,
					})
				}
			}

			mu.Lock()
			rankedLists = append(rankedLists, items)
			for i, item := range items {
				if _, exists := allResults[item.id]; !exists {
					allResults[item.id] = results[i]
				}
			}
			mu.Unlock()
		}(sq)
	}

	wg.Wait()

	// Merge via RRF
	merged := rrfMerge(rankedLists, 60)

	// Build final result list
	var finalResults []Result
	for _, item := range merged {
		if r, ok := allResults[item.id]; ok {
			r.Score = item.score
			finalResults = append(finalResults, r)
		}
	}

	if cfg.limit > 0 && len(finalResults) > cfg.limit {
		finalResults = finalResults[:cfg.limit]
	}

	return finalResults, nil
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./... -run TestRecall -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add recall.go recall_test.go
git commit -m "feat: add Recall pipeline — query decomposition, parallel search, RRF merge"
```

---

### Task 18: Forget

**Files:**
- Modify: `cortex.go` (add Forget method)
- Add tests to: `cortex_test.go`

- [ ] **Step 1: Write tests**

Add to `cortex_test.go`:

```go
func TestForgetByEntityID(t *testing.T) {
	c := testutil.OpenTestDB(t)
	ctx := context.Background()

	alice := Entity{Type: "person", Name: "Alice"}
	c.PutEntity(ctx, &alice)
	c.PutChunk(ctx, &Chunk{EntityID: alice.ID, Content: "Alice chunk"})
	c.PutMemory(ctx, &Memory{Content: "Alice fact", EntityIDs: []string{alice.ID}})

	err := c.Forget(ctx, Filter{EntityID: alice.ID})
	if err != nil {
		t.Fatalf("Forget: %v", err)
	}

	_, err = c.GetEntity(ctx, alice.ID)
	if err == nil {
		t.Error("expected entity to be deleted")
	}
}

func TestForgetBySource(t *testing.T) {
	c := testutil.OpenTestDB(t)
	ctx := context.Background()

	c.PutEntity(ctx, &Entity{Type: "person", Name: "Alice", Source: "gmail"})
	c.PutEntity(ctx, &Entity{Type: "person", Name: "Bob", Source: "markdown"})

	c.Forget(ctx, Filter{Source: "gmail"})

	all, _ := c.FindEntities(ctx, EntityFilter{})
	if len(all) != 1 || all[0].Name != "Bob" {
		t.Errorf("expected only Bob to remain, got %v", all)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run TestForget -v`
Expected: FAIL

- [ ] **Step 3: Implement Forget in cortex.go**

Add to `cortex.go`:

```go
// Forget removes entities, relationships, memories, chunks, and embeddings
// matching the filter.
func (c *Cortex) Forget(ctx context.Context, f Filter) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if f.EntityID != "" {
		// Delete embeddings for chunks linked to this entity
		tx.ExecContext(ctx, "DELETE FROM embeddings WHERE row_id IN (SELECT id FROM chunks WHERE entity_id = ?)", f.EntityID)
		// Delete chunks linked to this entity
		tx.ExecContext(ctx, "DELETE FROM chunks WHERE entity_id = ?", f.EntityID)
		// Delete memory_entities links
		tx.ExecContext(ctx, "DELETE FROM memory_entities WHERE entity_id = ?", f.EntityID)
		// Delete orphaned memories (no remaining entity links)
		tx.ExecContext(ctx, "DELETE FROM memories WHERE id NOT IN (SELECT DISTINCT memory_id FROM memory_entities)")
		// Delete embeddings for deleted memories
		tx.ExecContext(ctx, "DELETE FROM embeddings WHERE row_type = 'memory' AND row_id NOT IN (SELECT id FROM memories)")
		// Delete relationships
		tx.ExecContext(ctx, "DELETE FROM relationships WHERE source_id = ? OR target_id = ?", f.EntityID, f.EntityID)
		// Delete entity
		tx.ExecContext(ctx, "DELETE FROM entities WHERE id = ?", f.EntityID)
	}

	if f.Source != "" {
		// Get entity IDs for this source
		rows, err := tx.QueryContext(ctx, "SELECT id FROM entities WHERE source = ?", f.Source)
		if err != nil {
			return err
		}
		var ids []string
		for rows.Next() {
			var id string
			rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()

		for _, id := range ids {
			tx.ExecContext(ctx, "DELETE FROM embeddings WHERE row_id IN (SELECT id FROM chunks WHERE entity_id = ?)", id)
			tx.ExecContext(ctx, "DELETE FROM chunks WHERE entity_id = ?", id)
			tx.ExecContext(ctx, "DELETE FROM memory_entities WHERE entity_id = ?", id)
			tx.ExecContext(ctx, "DELETE FROM relationships WHERE source_id = ? OR target_id = ?", id, id)
		}
		tx.ExecContext(ctx, "DELETE FROM entities WHERE source = ?", f.Source)
		tx.ExecContext(ctx, "DELETE FROM memories WHERE id NOT IN (SELECT DISTINCT memory_id FROM memory_entities)")
		tx.ExecContext(ctx, "DELETE FROM embeddings WHERE row_type = 'memory' AND row_id NOT IN (SELECT id FROM memories)")
	}

	return tx.Commit()
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./... -run TestForget -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cortex.go cortex_test.go
git commit -m "feat: add Forget with cascading deletes by entity ID and source"
```

---

### Task 19: Markdown Connector

**Files:**
- Create: `connector/connector.go`
- Create: `connector/markdown/markdown.go`
- Create: `connector/markdown/markdown_test.go`

- [ ] **Step 1: Create connector interface**

```go
// connector/connector.go
package connector

import (
	"context"

	"github.com/sausheong/cortex"
)

// Connector reads from an external source and ingests into cortex.
type Connector interface {
	Sync(ctx context.Context, c *cortex.Cortex) error
}
```

- [ ] **Step 2: Write markdown connector tests**

```go
// connector/markdown/markdown_test.go
package markdown

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/testutil"
)

func TestSyncMarkdownFiles(t *testing.T) {
	c := testutil.OpenTestDB(t)
	ctx := context.Background()

	// Create test markdown files
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "alice.md"), []byte(`---
type: person
name: Alice
---
Alice is an engineer at [[Stripe]].
`), 0644)

	os.WriteFile(filepath.Join(dir, "bob.md"), []byte(`---
type: person
name: Bob
---
Bob knows [[Alice]].
`), 0644)

	conn := New(dir)
	if err := conn.Sync(ctx, c); err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// Verify entities were created
	people, _ := c.FindEntities(ctx, cortex.EntityFilter{Type: "person"})
	if len(people) < 2 {
		t.Errorf("got %d person entities, want at least 2", len(people))
	}
}

func TestSyncIsIncremental(t *testing.T) {
	c := testutil.OpenTestDB(t)
	ctx := context.Background()

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "alice.md"), []byte(`---
type: person
name: Alice
---
Alice is an engineer.
`), 0644)

	conn := New(dir)
	conn.Sync(ctx, c)
	conn.Sync(ctx, c) // Second sync should not duplicate

	people, _ := c.FindEntities(ctx, cortex.EntityFilter{NameLike: "Alice"})
	if len(people) != 1 {
		t.Errorf("got %d Alice entities after double sync, want 1", len(people))
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./connector/markdown/ -v`
Expected: FAIL

- [ ] **Step 4: Implement markdown connector**

```go
// connector/markdown/markdown.go
package markdown

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sausheong/cortex"
)

// Connector syncs markdown files from a directory into cortex.
type Connector struct {
	dir  string
	glob string
}

// New creates a markdown connector for the given directory.
func New(dir string, opts ...Option) *Connector {
	c := &Connector{dir: dir, glob: "**/*.md"}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// Option configures the markdown connector.
type Option func(*Connector)

// WithGlob sets the glob pattern for file discovery.
func WithGlob(pattern string) Option {
	return func(c *Connector) { c.glob = pattern }
}

// Sync reads markdown files and ingests them into cortex.
func (c *Connector) Sync(ctx context.Context, cx *cortex.Cortex) error {
	// Load sync state
	lastSync := c.loadSyncState(ctx, cx)

	// Find markdown files
	files, err := c.findFiles()
	if err != nil {
		return fmt.Errorf("find files: %w", err)
	}

	for _, path := range files {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}

		// Skip files not modified since last sync
		if !lastSync.IsZero() && info.ModTime().Before(lastSync) {
			continue
		}

		content, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		relPath, _ := filepath.Rel(c.dir, path)
		err = cx.Remember(ctx, string(content),
			cortex.WithSource("markdown"),
			cortex.WithContentType("markdown"),
		)
		if err != nil {
			return fmt.Errorf("remember %s: %w", relPath, err)
		}
	}

	// Save sync state
	c.saveSyncState(ctx, cx)

	return nil
}

func (c *Connector) findFiles() ([]string, error) {
	var files []string
	err := filepath.Walk(c.dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, ".md") {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

func (c *Connector) loadSyncState(ctx context.Context, cx *cortex.Cortex) time.Time {
	state, err := cx.GetSyncState(ctx, "markdown:"+c.dir)
	if err != nil {
		return time.Time{}
	}
	var s struct {
		LastSync time.Time `json:"last_sync"`
	}
	json.Unmarshal([]byte(state), &s)
	return s.LastSync
}

func (c *Connector) saveSyncState(ctx context.Context, cx *cortex.Cortex) {
	state, _ := json.Marshal(map[string]any{"last_sync": time.Now().UTC()})
	cx.SetSyncState(ctx, "markdown:"+c.dir, string(state))
}
```

Note: This requires adding `GetSyncState` and `SetSyncState` methods to `cortex.go`:

```go
// GetSyncState retrieves sync state for a connector.
func (c *Cortex) GetSyncState(ctx context.Context, connector string) (string, error) {
	var state string
	err := c.db.QueryRowContext(ctx, "SELECT state FROM sync_state WHERE connector = ?", connector).Scan(&state)
	if err != nil {
		return "", err
	}
	return state, nil
}

// SetSyncState saves sync state for a connector.
func (c *Cortex) SetSyncState(ctx context.Context, connector, state string) error {
	_, err := c.db.ExecContext(ctx,
		"INSERT INTO sync_state (connector, state, updated_at) VALUES (?, ?, datetime('now')) ON CONFLICT(connector) DO UPDATE SET state = excluded.state, updated_at = excluded.updated_at",
		connector, state,
	)
	return err
}
```

- [ ] **Step 5: Run tests**

Run: `go test ./connector/markdown/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add connector/ cortex.go
git commit -m "feat: add markdown connector with incremental sync and sync state"
```

---

### Task 20: OpenAI LLM Implementation

**Files:**
- Create: `llm/openai/llm.go`
- Create: `llm/openai/llm_test.go`

- [ ] **Step 1: Write test (skipped without API key)**

```go
// llm/openai/llm_test.go
package openai

import (
	"context"
	"os"
	"testing"
)

func TestExtractIntegration(t *testing.T) {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	llm := NewLLM(key)
	result, err := llm.Extract(context.Background(), "Alice works at Stripe as a staff engineer. She knows Bob from their time at Google.", "")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if result.Parsed == nil {
		t.Fatal("expected parsed extraction")
	}
	if len(result.Parsed.Entities) == 0 {
		t.Error("expected at least one entity")
	}
}
```

- [ ] **Step 2: Implement OpenAI LLM provider**

```go
// llm/openai/llm.go
package openai

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/sausheong/cortex"
	oai "github.com/sashabaranov/go-openai"
)

// LLM implements cortex.LLM using OpenAI models.
type LLM struct {
	client *oai.Client
	model  string
}

// NewLLM creates a new OpenAI LLM provider.
func NewLLM(apiKey string, opts ...LLMOption) *LLM {
	l := &LLM{
		client: oai.NewClient(apiKey),
		model:  oai.GPT4oMini,
	}
	for _, opt := range opts {
		opt(l)
	}
	return l
}

// LLMOption configures the OpenAI LLM.
type LLMOption func(*LLM)

// WithModel sets the model to use.
func WithModel(model string) LLMOption {
	return func(l *LLM) { l.model = model }
}

// Extract sends text to the LLM with an extraction prompt and parses the JSON response.
func (l *LLM) Extract(ctx context.Context, text string, prompt string) (cortex.ExtractionResult, error) {
	if prompt == "" {
		prompt = defaultExtractionPrompt
	}

	resp, err := l.client.CreateChatCompletion(ctx, oai.ChatCompletionRequest{
		Model: l.model,
		Messages: []oai.ChatCompletionMessage{
			{Role: oai.ChatMessageRoleSystem, Content: prompt},
			{Role: oai.ChatMessageRoleUser, Content: text},
		},
		Temperature: 0.0,
		ResponseFormat: &oai.ChatCompletionResponseFormat{
			Type: oai.ChatCompletionResponseFormatTypeJSONObject,
		},
	})
	if err != nil {
		return cortex.ExtractionResult{}, fmt.Errorf("openai extract: %w", err)
	}

	raw := resp.Choices[0].Message.Content
	var parsed struct {
		Entities      []struct{ Type, Name string }                 `json:"entities"`
		Relationships []struct{ Source, Target, Type string }       `json:"relationships"`
		Memories      []struct{ Content string }                    `json:"memories"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return cortex.ExtractionResult{Raw: raw}, fmt.Errorf("parse extraction JSON: %w", err)
	}

	extraction := &cortex.Extraction{}
	for _, e := range parsed.Entities {
		extraction.Entities = append(extraction.Entities, cortex.Entity{Type: e.Type, Name: e.Name})
	}
	for _, r := range parsed.Relationships {
		extraction.Relationships = append(extraction.Relationships, cortex.Relationship{
			SourceID: r.Source,
			TargetID: r.Target,
			Type:     r.Type,
		})
	}
	for _, m := range parsed.Memories {
		extraction.Memories = append(extraction.Memories, cortex.Memory{Content: m.Content})
	}

	return cortex.ExtractionResult{Raw: raw, Parsed: extraction}, nil
}

// Decompose uses the LLM to break a natural language query into structured sub-queries.
func (l *LLM) Decompose(ctx context.Context, query string) ([]cortex.StructuredQuery, error) {
	resp, err := l.client.CreateChatCompletion(ctx, oai.ChatCompletionRequest{
		Model: l.model,
		Messages: []oai.ChatCompletionMessage{
			{Role: oai.ChatMessageRoleSystem, Content: decomposePrompt},
			{Role: oai.ChatMessageRoleUser, Content: query},
		},
		Temperature: 0.0,
		ResponseFormat: &oai.ChatCompletionResponseFormat{
			Type: oai.ChatCompletionResponseFormatTypeJSONObject,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("openai decompose: %w", err)
	}

	var result struct {
		Queries []cortex.StructuredQuery `json:"queries"`
	}
	if err := json.Unmarshal([]byte(resp.Choices[0].Message.Content), &result); err != nil {
		return nil, fmt.Errorf("parse decompose JSON: %w", err)
	}
	return result.Queries, nil
}

// Summarize produces a summary of the given texts.
func (l *LLM) Summarize(ctx context.Context, texts []string) (string, error) {
	combined := ""
	for _, t := range texts {
		combined += t + "\n\n"
	}
	resp, err := l.client.CreateChatCompletion(ctx, oai.ChatCompletionRequest{
		Model: l.model,
		Messages: []oai.ChatCompletionMessage{
			{Role: oai.ChatMessageRoleSystem, Content: "Summarize the following texts into a concise summary. Return only the summary text."},
			{Role: oai.ChatMessageRoleUser, Content: combined},
		},
		Temperature: 0.0,
	})
	if err != nil {
		return "", err
	}
	return resp.Choices[0].Message.Content, nil
}

const defaultExtractionPrompt = `Extract entities, relationships, and key facts from the following text.

Return a JSON object with this structure:
{
  "entities": [{"type": "person|organization|concept|event|document", "name": "..."}],
  "relationships": [{"source": "entity name", "target": "entity name", "type": "works_at|knows|related_to|discussed_in|attended|created|part_of"}],
  "memories": [{"content": "A concise distilled fact"}]
}

Rules:
- Entity types: person, organization, concept, event, document
- Relationship types are open-ended but prefer: works_at, knows, related_to, discussed_in, attended, created, part_of
- Memories should be concise, standalone facts useful to recall later
- Only extract clearly stated or strongly implied information
- Return valid JSON only`

const decomposePrompt = `You are a query decomposer for a personal knowledge graph. Break the user's natural language query into structured sub-queries.

Return a JSON object:
{
  "queries": [
    {"type": "memory_lookup", "params": {"query": "search terms"}},
    {"type": "keyword_search", "params": {"query": "search terms"}},
    {"type": "vector_search", "params": {"query": "semantic search text"}},
    {"type": "graph_traverse", "params": {"entity": "entity name", "edge": "relationship type"}}
  ]
}

Available query types:
- memory_lookup: Search distilled facts/memories
- keyword_search: FTS5 keyword search on text chunks
- vector_search: Semantic similarity search on text chunks
- graph_traverse: Walk relationships from an entity

Choose 2-4 sub-queries that together would answer the user's question.`
```

- [ ] **Step 3: Verify build**

Run: `go build ./...`
Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add llm/openai/llm.go llm/openai/llm_test.go
git commit -m "feat: add OpenAI LLM provider — extraction, decomposition, summarization"
```

---

### Task 21: OpenAI Embedder Implementation

**Files:**
- Create: `llm/openai/embedder.go`
- Create: `llm/openai/embedder_test.go`

- [ ] **Step 1: Write test (skipped without API key)**

```go
// llm/openai/embedder_test.go
package openai

import (
	"context"
	"os"
	"testing"
)

func TestEmbedIntegration(t *testing.T) {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	emb := NewEmbedder(key)
	vecs, err := emb.Embed(context.Background(), []string{"hello world", "goodbye world"})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vecs) != 2 {
		t.Fatalf("got %d vectors, want 2", len(vecs))
	}
	if len(vecs[0]) != emb.Dimensions() {
		t.Errorf("vector dimension = %d, want %d", len(vecs[0]), emb.Dimensions())
	}
}
```

- [ ] **Step 2: Implement embedder**

```go
// llm/openai/embedder.go
package openai

import (
	"context"
	"fmt"

	oai "github.com/sashabaranov/go-openai"
)

// Embedder implements cortex.Embedder using OpenAI embeddings.
type Embedder struct {
	client *oai.Client
	model  oai.EmbeddingModel
	dims   int
}

// NewEmbedder creates a new OpenAI embedding provider.
func NewEmbedder(apiKey string) *Embedder {
	return &Embedder{
		client: oai.NewClient(apiKey),
		model:  oai.SmallEmbedding3,
		dims:   1536,
	}
}

// Embed generates embeddings for the given texts.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	resp, err := e.client.CreateEmbeddings(ctx, oai.EmbeddingRequest{
		Input: texts,
		Model: e.model,
	})
	if err != nil {
		return nil, fmt.Errorf("openai embed: %w", err)
	}

	result := make([][]float32, len(resp.Data))
	for _, d := range resp.Data {
		result[d.Index] = d.Embedding
	}
	return result, nil
}

// Dimensions returns the embedding vector dimension.
func (e *Embedder) Dimensions() int {
	return e.dims
}
```

- [ ] **Step 3: Verify build**

Run: `go build ./...`
Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add llm/openai/embedder.go llm/openai/embedder_test.go
git commit -m "feat: add OpenAI embedder — text-embedding-3-small"
```

---

### Task 22: CLI

**Files:**
- Create: `cmd/cortex/main.go`

- [ ] **Step 1: Implement CLI with init, remember, recall, sync, entity, forget commands**

```go
// cmd/cortex/main.go
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/connector/markdown"
	"github.com/sausheong/cortex/extractor/deterministic"
	"github.com/sausheong/cortex/extractor/hybrid"
	llmext "github.com/sausheong/cortex/extractor/llmext"
	oaillm "github.com/sausheong/cortex/llm/openai"
)

const defaultDB = "brain.db"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "init":
		cmdInit()
	case "remember":
		cmdRemember(args)
	case "recall":
		cmdRecall(args)
	case "sync":
		cmdSync(args)
	case "entity":
		cmdEntity(args)
	case "forget":
		cmdForget(args)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `Usage: cortex <command> [args]

Commands:
  init                         Create a new brain database
  remember <text>              Ingest and remember text
  recall <query>               Query the knowledge graph
  sync markdown <dir>          Sync markdown files from a directory
  entity list [--type <type>]  List entities
  entity get <id>              Get entity by ID
  forget --source <source>     Forget by source
  forget --entity <id>         Forget by entity ID`)
}

func openCortex() *cortex.Cortex {
	apiKey := os.Getenv("OPENAI_API_KEY")
	var opts []cortex.Option

	if apiKey != "" {
		llm := oaillm.NewLLM(apiKey)
		emb := oaillm.NewEmbedder(apiKey)
		det := deterministic.New()
		llmE := llmext.New(llm)
		ext := hybrid.New(det, llmE)
		opts = append(opts,
			cortex.WithLLM(llm),
			cortex.WithEmbedder(emb),
			cortex.WithExtractor(ext),
		)
	} else {
		det := deterministic.New()
		opts = append(opts, cortex.WithExtractor(det))
	}

	c, err := cortex.Open(defaultDB, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	return c
}

func cmdInit() {
	c, err := cortex.Open(defaultDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	c.Close()
	fmt.Println("Brain initialized at", defaultDB)
}

func cmdRemember(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cortex remember <text>")
		os.Exit(1)
	}
	c := openCortex()
	defer c.Close()

	text := strings.Join(args, " ")
	if err := c.Remember(context.Background(), text, cortex.WithSource("cli")); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Remembered.")
}

func cmdRecall(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cortex recall <query>")
		os.Exit(1)
	}
	c := openCortex()
	defer c.Close()

	query := strings.Join(args, " ")
	results, err := c.Recall(context.Background(), query, cortex.WithLimit(10))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if len(results) == 0 {
		fmt.Println("No results found.")
		return
	}

	for i, r := range results {
		fmt.Printf("[%d] (%s, score: %.4f) %s\n", i+1, r.Type, r.Score, r.Content)
		if r.Source != "" {
			fmt.Printf("    source: %s\n", r.Source)
		}
	}
}

func cmdSync(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: cortex sync markdown <dir>")
		os.Exit(1)
	}

	connType := args[0]
	switch connType {
	case "markdown":
		dir := args[1]
		c := openCortex()
		defer c.Close()

		conn := markdown.New(dir)
		if err := conn.Sync(context.Background(), c); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Synced markdown files from %s\n", dir)
	default:
		fmt.Fprintf(os.Stderr, "unknown connector: %s\n", connType)
		os.Exit(1)
	}
}

func cmdEntity(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cortex entity list|get ...")
		os.Exit(1)
	}

	c := openCortex()
	defer c.Close()
	ctx := context.Background()

	switch args[0] {
	case "list":
		filter := cortex.EntityFilter{}
		for i := 1; i < len(args)-1; i++ {
			if args[i] == "--type" {
				filter.Type = args[i+1]
			}
		}
		entities, err := c.FindEntities(ctx, filter)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		for _, e := range entities {
			fmt.Printf("%s  %-15s  %s\n", e.ID, e.Type, e.Name)
		}

	case "get":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: cortex entity get <id>")
			os.Exit(1)
		}
		e, err := c.GetEntity(ctx, args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("ID:         %s\n", e.ID)
		fmt.Printf("Type:       %s\n", e.Type)
		fmt.Printf("Name:       %s\n", e.Name)
		fmt.Printf("Source:     %s\n", e.Source)
		fmt.Printf("Created:    %s\n", e.CreatedAt)
		if len(e.Attributes) > 0 {
			fmt.Printf("Attributes: %v\n", e.Attributes)
		}

		// Show relationships
		rels, _ := c.GetRelationships(ctx, e.ID)
		if len(rels) > 0 {
			fmt.Println("Relationships:")
			for _, r := range rels {
				other := r.TargetID
				if other == e.ID {
					other = r.SourceID
				}
				otherEntity, _ := c.GetEntity(ctx, other)
				otherName := other
				if otherEntity != nil {
					otherName = otherEntity.Name
				}
				fmt.Printf("  --%s--> %s\n", r.Type, otherName)
			}
		}
	}
}

func cmdForget(args []string) {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: cortex forget --source <source> | --entity <id>")
		os.Exit(1)
	}

	c := openCortex()
	defer c.Close()

	f := cortex.Filter{}
	for i := 0; i < len(args)-1; i++ {
		switch args[i] {
		case "--source":
			f.Source = args[i+1]
		case "--entity":
			f.EntityID = args[i+1]
		}
	}

	if err := c.Forget(context.Background(), f); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Forgotten.")
}
```

- [ ] **Step 2: Build the CLI**

Run: `go build -o cortex-bin ./cmd/cortex/`
Expected: binary created successfully

- [ ] **Step 3: Test CLI manually**

Run:
```bash
./cortex-bin init
./cortex-bin entity list
```
Expected: "Brain initialized at brain.db", empty entity list

- [ ] **Step 4: Clean up test artifact**

Run: `rm -f brain.db cortex-bin`

- [ ] **Step 5: Commit**

```bash
git add cmd/cortex/main.go
git commit -m "feat: add CLI — init, remember, recall, sync, entity, forget commands"
```

---

### Task 23: Run Full Test Suite

- [ ] **Step 1: Run all tests**

Run: `go test ./... -v`
Expected: All tests PASS

- [ ] **Step 2: Run build for all targets**

Run: `go build ./...`
Expected: no errors

- [ ] **Step 3: Commit any fixes if needed**

If any tests fail, fix them and commit:
```bash
git add -A
git commit -m "fix: resolve test failures from integration"
```
