# Confidence Scores Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `confidence` (0.0–1.0) field to entities, relationships, and memories so the LLM extractor can express uncertainty; expose it in Recall results, vault pages, and CLI; default behavior unchanged.

**Architecture:** One `REAL NOT NULL DEFAULT 1.0` column added to three tables via idempotent `ALTER TABLE` at `Open` time (no migrations framework). Go structs gain `Confidence float64`. Put methods coerce zero → 1.0 and clamp to [0, 1] — keeps existing callers byte-identical at the consumer level. LLM extractor prompt updated to request `confidence`; missing values default to 1.0. Recall exposes `Confidence` on `Result` and adds an opt-in `WithMinConfidence` filter (post-RRF, hard `>=` threshold).

**Tech Stack:** Go 1.25.1, SQLite (modernc.org/sqlite), existing prompt format.

**Spec:** `docs/superpowers/specs/2026-05-27-confidence-scores-design.md`

---

## File Structure

**New files:** none.

**Modified files:**

| File | Change |
|---|---|
| `store.go` | `ensureColumn(db, table, col, ddl)` helper + three call sites |
| `store_test.go` (new) | Migration idempotency + legacy-db upgrade test |
| `types.go` | `Confidence float64` on Entity, Relationship, Memory, Result; `WithMinConfidence` option + `minConfidence` on `recallConfig` |
| `entity.go` | `PutEntity` coerces + clamps; `GetEntity`/`FindEntities` SELECT include `confidence` |
| `entity_test.go` | Confidence round-trip + coercion + clamp |
| `relationship.go` | `PutRelationship` coerces + clamps; `GetRelationships` includes `confidence` |
| `relationship_test.go` | Confidence round-trip + coercion + clamp |
| `memory.go` | `PutMemory` coerces + clamps; `GetMemoriesByEntity` + `SearchMemories` include `confidence` |
| `memory_test.go` | Confidence round-trip + coercion + clamp |
| `recall.go` | All four `recall<Strategy>` populate `Result.Confidence`; post-RRF filter |
| `recall_test.go` | Confidence exposed in results; filter works |
| `extractor/llmext/extractor.go` | Updated prompt |
| `llm/openai/llm.go` | `extractionJSON` items gain `Confidence`; struct→cortex conversion copies it |
| `llm/openai/llm_test.go` | Fixture LLM response with confidence parses correctly |
| `llm/anthropic/llm.go` | Same as openai |
| `vault/render.go` | Frontmatter `confidence:` when ≠ 1.0; memory bullet `(conf N%)` when < 1.0 |
| `vault/render_test.go` | New golden test for entity with confidence |
| `vault/testdata/entity_with_confidence.golden.md` (new) | Golden output |
| `cmd/cortex/main.go` | `recall --min-confidence` flag; display confidence in recall + entity get |
| `docs/CORTEX.md` + `cmd/cortex/CORTEX.md.template` | Brief mention of confidence |
| `README.md` | One paragraph in recall/remember section |

---

## Task 1: Schema migration (`ensureColumn` helper + ALTER TABLE)

**Files:**
- Modify: `store.go`
- Create: `store_test.go`

- [ ] **Step 1: Write the failing test**

Create `store_test.go`:

```go
package cortex

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestEnsureColumn_AddsMissingColumn(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE t (id TEXT PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatal(err)
	}

	if err := ensureColumn(db, "t", "confidence", "REAL NOT NULL DEFAULT 1.0"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	// Idempotent — second call is a no-op.
	if err := ensureColumn(db, "t", "confidence", "REAL NOT NULL DEFAULT 1.0"); err != nil {
		t.Fatalf("second call should be no-op: %v", err)
	}

	// Verify column exists and has the right default.
	if _, err := db.Exec(`INSERT INTO t (id, name) VALUES ('x', 'hello')`); err != nil {
		t.Fatal(err)
	}
	var c float64
	if err := db.QueryRow(`SELECT confidence FROM t WHERE id = 'x'`).Scan(&c); err != nil {
		t.Fatal(err)
	}
	if c != 1.0 {
		t.Errorf("default confidence = %v, want 1.0", c)
	}
}

func TestOpen_AddsConfidenceColumnsToLegacyDB(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "legacy.db")

	// Build a "legacy" db with only the pre-confidence schema.
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	legacyDDL := `
		CREATE TABLE entities (id TEXT PRIMARY KEY, type TEXT NOT NULL, name TEXT NOT NULL, attributes TEXT, source TEXT, created_at DATETIME, updated_at DATETIME);
		CREATE TABLE relationships (id TEXT PRIMARY KEY, source_id TEXT, target_id TEXT, type TEXT NOT NULL, attributes TEXT, source TEXT, created_at DATETIME);
		CREATE TABLE memories (id TEXT PRIMARY KEY, content TEXT NOT NULL, source TEXT, created_at DATETIME, updated_at DATETIME);
	`
	if _, err := db.Exec(legacyDDL); err != nil {
		t.Fatal(err)
	}
	// Insert a legacy row.
	if _, err := db.Exec(`INSERT INTO entities (id, type, name) VALUES ('legacy1', 'person', 'Old Alice')`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Open with the new code — should add columns without errors.
	cx, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer cx.Close()

	// Legacy row should now have confidence=1.0.
	e, err := cx.GetEntity(context.Background(), "legacy1")
	if err != nil {
		t.Fatalf("GetEntity: %v", err)
	}
	if e.Confidence != 1.0 {
		t.Errorf("legacy row confidence = %v, want 1.0", e.Confidence)
	}
}
```

Note: the second test depends on Task 2 changes (Entity.Confidence field + GetEntity SELECT). Comment it out for now with a `t.Skip("requires Task 2")`, OR run only `TestEnsureColumn_AddsMissingColumn` until Task 2 lands. Recommendation: keep `TestOpen_AddsConfidenceColumnsToLegacyDB` in the file but `t.Skip` it; remove the Skip in Task 2.

Adjusted Step 1:

```go
func TestOpen_AddsConfidenceColumnsToLegacyDB(t *testing.T) {
	t.Skip("requires Task 2 (Entity.Confidence field)")
	// ... body as above
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./... -run "TestEnsureColumn|TestOpen_AddsConfidence" -v`
Expected: build error — undefined `ensureColumn`.

- [ ] **Step 3: Implement `ensureColumn` in `store.go`**

Add this helper above the `Open` function:

```go
// ensureColumn adds column to table if it does not already exist.
// Idempotent — safe to call on every Open. Used in lieu of a migrations
// framework for single-column additions. ddl is the column definition
// (everything after the column name in ALTER TABLE), e.g.
// "REAL NOT NULL DEFAULT 1.0".
func ensureColumn(db *sql.DB, table, column, ddl string) error {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return fmt.Errorf("cortex: pragma_table_info(%s): %w", table, err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if name == column {
			return nil // already present
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, ddl)
	if _, err := db.Exec(stmt); err != nil {
		return fmt.Errorf("cortex: %s: %w", stmt, err)
	}
	return nil
}
```

- [ ] **Step 4: Call ensureColumn from Open for all three tables**

In `store.go`, inside `Open`, immediately after the existing `db.Exec(schemaSQL)` succeeds (around line 113), add:

```go
for _, t := range []string{"entities", "relationships", "memories"} {
	if err := ensureColumn(db, t, "confidence", "REAL NOT NULL DEFAULT 1.0"); err != nil {
		db.Close()
		return nil, fmt.Errorf("cortex: migrate %s.confidence: %w", t, err)
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test -run TestEnsureColumn -v ./...`
Expected: `TestEnsureColumn_AddsMissingColumn` PASS. The `TestOpen_AddsConfidenceColumnsToLegacyDB` is skipped.

Also run full suite to confirm nothing else broke:
Run: `go test ./...`
Expected: existing tests still pass (modulo any pre-existing live-network failures).

- [ ] **Step 6: Commit**

```bash
git add store.go store_test.go
git commit -m "$(cat <<'EOF'
feat(cortex): add ensureColumn helper for idempotent schema migrations

Adds confidence column (REAL NOT NULL DEFAULT 1.0) to entities,
relationships, and memories on every Open. Existing rows get 1.0.
No migrations framework — single helper, idempotent.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Confidence field on claim types + Put coercion + Get SELECTs

**Files:**
- Modify: `types.go`
- Modify: `entity.go`, `relationship.go`, `memory.go`
- Modify: `entity_test.go`, `relationship_test.go`, `memory_test.go`
- Modify: `store_test.go` (un-skip the legacy-db test)

- [ ] **Step 1: Add `Confidence float64` to the four types in `types.go`**

In `types.go`, modify the three claim structs and the `Result` struct:

```go
type Entity struct {
	ID         string         `json:"id"`
	Type       string         `json:"type"`
	Name       string         `json:"name"`
	Attributes map[string]any `json:"attributes,omitempty"`
	Source     string         `json:"source,omitempty"`
	Confidence float64        `json:"confidence"`
	CreatedAt  time.Time      `json:"created_at"`
	UpdatedAt  time.Time      `json:"updated_at"`
}

type Relationship struct {
	ID         string         `json:"id"`
	SourceID   string         `json:"source_id"`
	TargetID   string         `json:"target_id"`
	Type       string         `json:"type"`
	Attributes map[string]any `json:"attributes,omitempty"`
	Source     string         `json:"source,omitempty"`
	Confidence float64        `json:"confidence"`
	CreatedAt  time.Time      `json:"created_at"`
}

type Memory struct {
	ID         string    `json:"id"`
	Content    string    `json:"content"`
	EntityIDs  []string  `json:"entity_ids,omitempty"`
	Source     string    `json:"source,omitempty"`
	Confidence float64   `json:"confidence"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Result struct {
	Type       string         `json:"type"`
	Content    string         `json:"content"`
	Score      float64        `json:"score"`
	Confidence float64        `json:"confidence"`
	EntityIDs  []string       `json:"entity_ids,omitempty"`
	Source     string         `json:"source,omitempty"`
	Metadata   map[string]any `json:"metadata,omitempty"`
}
```

- [ ] **Step 2: Add a shared coercion helper to `types.go`**

At the bottom of `types.go`:

```go
// coerceConfidence enforces the [0, 1] invariant for confidence values.
// Zero (the Go zero value, indistinguishable from "unset") is coerced to
// 1.0 — this is what preserves backward compatibility: callers that did
// not specify confidence (deterministic extractor, manual API use, legacy
// tests) get the pre-feature behavior of "treat all data as fully
// confident." Out-of-range values are clamped silently rather than
// errored: failing an entire ingest because of one bad number from an LLM
// is worse UX than clamping and continuing.
func coerceConfidence(c float64) float64 {
	if c == 0 {
		return 1.0
	}
	if c < 0 {
		return 0
	}
	if c > 1 {
		return 1
	}
	return c
}
```

- [ ] **Step 3: Write failing tests for coercion + round-trip**

Append to `entity_test.go`:

```go
func TestPutEntity_ConfidenceDefaultsToOne(t *testing.T) {
	cx := openTestCortex(t)
	defer cx.Close()
	e := &Entity{Type: "person", Name: "NoConfidenceSet"}
	if err := cx.PutEntity(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	got, err := cx.GetEntity(context.Background(), e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Confidence != 1.0 {
		t.Errorf("Confidence = %v, want 1.0 (zero coerces to 1)", got.Confidence)
	}
}

func TestPutEntity_ConfidenceRoundTrip(t *testing.T) {
	cx := openTestCortex(t)
	defer cx.Close()
	e := &Entity{Type: "person", Name: "PartialConfidence", Confidence: 0.42}
	if err := cx.PutEntity(context.Background(), e); err != nil {
		t.Fatal(err)
	}
	got, err := cx.GetEntity(context.Background(), e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Confidence != 0.42 {
		t.Errorf("Confidence = %v, want 0.42", got.Confidence)
	}
}

func TestPutEntity_ConfidenceClamped(t *testing.T) {
	cx := openTestCortex(t)
	defer cx.Close()
	tests := []struct {
		in, want float64
	}{
		{1.5, 1.0},   // over → 1
		{-0.1, 0.0},  // under → 0
		{2.0, 1.0},
	}
	for _, tt := range tests {
		e := &Entity{Type: "person", Name: fmt.Sprintf("Clamp-%v", tt.in), Confidence: tt.in}
		if err := cx.PutEntity(context.Background(), e); err != nil {
			t.Fatal(err)
		}
		got, err := cx.GetEntity(context.Background(), e.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Confidence != tt.want {
			t.Errorf("input %v: Confidence = %v, want %v", tt.in, got.Confidence, tt.want)
		}
	}
}
```

`openTestCortex(t)` may already exist as a helper in the test file. If not, create it at the top:

```go
func openTestCortex(t *testing.T) *Cortex {
	t.Helper()
	dir := t.TempDir()
	cx, err := Open(filepath.Join(dir, "brain.db"))
	if err != nil {
		t.Fatal(err)
	}
	return cx
}
```

Add equivalent test functions to `relationship_test.go` and `memory_test.go` — same shape, just substitute the type-specific construction.

For relationship_test.go:

```go
func TestPutRelationship_ConfidenceDefaultsToOne(t *testing.T) {
	cx := openTestCortex(t)
	defer cx.Close()
	ctx := context.Background()
	a := &Entity{Type: "person", Name: "RelA"}
	b := &Entity{Type: "person", Name: "RelB"}
	if err := cx.PutEntity(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutEntity(ctx, b); err != nil {
		t.Fatal(err)
	}
	r := &Relationship{SourceID: a.ID, TargetID: b.ID, Type: "knows"}
	if err := cx.PutRelationship(ctx, r); err != nil {
		t.Fatal(err)
	}
	rels, err := cx.GetRelationships(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) == 0 {
		t.Fatal("no relationships returned")
	}
	if rels[0].Confidence != 1.0 {
		t.Errorf("Confidence = %v, want 1.0", rels[0].Confidence)
	}
}

func TestPutRelationship_ConfidenceClamped(t *testing.T) {
	cx := openTestCortex(t)
	defer cx.Close()
	ctx := context.Background()
	a := &Entity{Type: "person", Name: "RelClampA"}
	b := &Entity{Type: "person", Name: "RelClampB"}
	if err := cx.PutEntity(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutEntity(ctx, b); err != nil {
		t.Fatal(err)
	}
	r := &Relationship{SourceID: a.ID, TargetID: b.ID, Type: "knows", Confidence: 1.5}
	if err := cx.PutRelationship(ctx, r); err != nil {
		t.Fatal(err)
	}
	rels, err := cx.GetRelationships(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rels[0].Confidence != 1.0 {
		t.Errorf("Confidence = %v, want 1.0 (clamped from 1.5)", rels[0].Confidence)
	}
}
```

For memory_test.go:

```go
func TestPutMemory_ConfidenceDefaultsToOne(t *testing.T) {
	cx := openTestCortex(t)
	defer cx.Close()
	ctx := context.Background()
	m := &Memory{Content: "alice did X"}
	if err := cx.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	// SearchMemories returns memories; pull and check confidence.
	results, err := cx.SearchMemories(ctx, "alice", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("no memories")
	}
	if results[0].Confidence != 1.0 {
		t.Errorf("Confidence = %v, want 1.0", results[0].Confidence)
	}
}

func TestPutMemory_ConfidenceClamped(t *testing.T) {
	cx := openTestCortex(t)
	defer cx.Close()
	ctx := context.Background()
	m := &Memory{Content: "alice did Y", Confidence: -0.5}
	if err := cx.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	results, err := cx.SearchMemories(ctx, "alice", 10)
	if err != nil {
		t.Fatal(err)
	}
	if results[0].Confidence != 0.0 {
		t.Errorf("Confidence = %v, want 0.0 (clamped)", results[0].Confidence)
	}
}
```

- [ ] **Step 4: Run tests to verify they fail**

Run: `go test -run "Confidence" ./..."` (in single quotes if needed)
Expected: compile errors (Confidence field doesn't exist on structs yet — Step 1 should fix that) or test failures (Put doesn't coerce, Get doesn't SELECT confidence).

After Step 1's struct changes compile, the failures should be:
- All tests pass `Confidence: 0.42` etc. but Get returns `Confidence: 0.0` (because SELECT doesn't include the column yet).

- [ ] **Step 5: Update `entity.go`**

In `PutEntity`, at the top after coercing inputs (around line 24, before the existing attributes marshal), add:

```go
e.Confidence = coerceConfidence(e.Confidence)
```

Add `confidence` to both the UPDATE and INSERT SQL:

```go
// UPDATE branch (around line 33):
_, err = c.db.ExecContext(ctx,
	`UPDATE entities SET attributes = ?, source = ?, confidence = ?, updated_at = ? WHERE id = ?`,
	string(attrsJSON), e.Source, e.Confidence, now, existingID,
)
```

```go
// INSERT branch (around line 53):
_, err = c.db.ExecContext(ctx,
	`INSERT INTO entities (id, type, name, attributes, source, confidence, created_at, updated_at)
	 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
	e.ID, e.Type, e.Name, string(attrsJSON), e.Source, e.Confidence, e.CreatedAt, e.UpdatedAt,
)
```

In `GetEntity`, add `confidence` to the SELECT and Scan:

```go
err := c.db.QueryRowContext(ctx,
	`SELECT id, type, name, attributes, source, confidence, created_at, updated_at
	 FROM entities WHERE id = ?`, id,
).Scan(&e.ID, &e.Type, &e.Name, &attrsJSON, &e.Source, &e.Confidence, &e.CreatedAt, &e.UpdatedAt)
```

In `FindEntities`, do the same — the SELECT at the top of the function and the Scan call mid-function (around line 121 in current file):

```go
query := `SELECT id, type, name, attributes, source, confidence, created_at, updated_at FROM entities`
// ...
if err := rows.Scan(&e.ID, &e.Type, &e.Name, &attrsJSON, &e.Source, &e.Confidence, &e.CreatedAt, &e.UpdatedAt); err != nil {
```

- [ ] **Step 6: Update `relationship.go`**

Same pattern. In `PutRelationship`, after the existing setup:

```go
r.Confidence = coerceConfidence(r.Confidence)
```

INSERT SQL:

```go
_, err = c.db.ExecContext(ctx,
	`INSERT INTO relationships (id, source_id, target_id, type, attributes, source, confidence, created_at)
	 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
	r.ID, r.SourceID, r.TargetID, r.Type, string(attrsJSON), r.Source, r.Confidence, r.CreatedAt,
)
```

`GetRelationships` SELECT + Scan:

```go
query := `SELECT id, source_id, target_id, type, attributes, source, confidence, created_at
	FROM relationships
	WHERE (source_id = ? OR target_id = ?)`
// ...
if err := rows.Scan(&r.ID, &r.SourceID, &r.TargetID, &r.Type, &attrsJSON, &r.Source, &r.Confidence, &r.CreatedAt); err != nil {
```

- [ ] **Step 7: Update `memory.go`**

In `PutMemory`:

```go
m.Confidence = coerceConfidence(m.Confidence)
```

INSERT statement gets `confidence` column added (find the existing INSERT and add the column + value).

`GetMemoriesByEntity` SELECT + Scan (around line 105):

```go
`SELECT m.id, m.content, m.source, m.confidence, m.created_at, m.updated_at
 FROM memories m
 JOIN memory_entities me ON m.id = me.memory_id
 WHERE me.entity_id = ?`
// ...
if err := rows.Scan(&m.ID, &m.Content, &m.Source, &m.Confidence, &m.CreatedAt, &m.UpdatedAt); err != nil {
```

`SearchMemories` (find the function and apply the same change).

- [ ] **Step 8: Un-skip the legacy-DB test in store_test.go**

Remove the `t.Skip("requires Task 2 ...")` line from `TestOpen_AddsConfidenceColumnsToLegacyDB`.

- [ ] **Step 9: Run all tests**

Run: `go test ./... -count=1`
Expected:
- All new Confidence tests PASS
- `TestOpen_AddsConfidenceColumnsToLegacyDB` PASS
- All pre-existing tests still pass

If any pre-existing test fails because of the new Confidence column in JSON output, that's expected — fix the test by either accepting the new field or asserting on specific fields only.

- [ ] **Step 10: Commit**

```bash
git add types.go entity.go entity_test.go relationship.go relationship_test.go memory.go memory_test.go store_test.go
git commit -m "$(cat <<'EOF'
feat(cortex): add Confidence to Entity, Relationship, Memory, Result

Put methods coerce zero (Go zero value, indistinguishable from "unset")
to 1.0 and clamp [0,1]. Existing callers and legacy DB rows behave
identically to before — they all read back as 1.0.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Recall populates Confidence + WithMinConfidence filter

**Files:**
- Modify: `types.go` (add `WithMinConfidence` option + `minConfidence` on `recallConfig`)
- Modify: `recall.go`
- Modify: `recall_test.go`

- [ ] **Step 1: Write failing tests**

Append to `recall_test.go`:

```go
func TestRecall_ResultIncludesConfidence(t *testing.T) {
	cx := openTestCortex(t)
	defer cx.Close()
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "Alice Recall", Confidence: 0.6}
	if err := cx.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	m := &Memory{Content: "alice recall test memory", EntityIDs: []string{e.ID}, Confidence: 0.4}
	if err := cx.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}

	results, err := cx.Recall(ctx, "alice recall")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("no results")
	}
	// At least one result should have non-default confidence.
	found := false
	for _, r := range results {
		if r.Confidence == 0.4 || r.Confidence == 0.6 {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no result with expected confidence values; got %+v", results)
	}
}

func TestRecall_WithMinConfidence_FiltersBelow(t *testing.T) {
	cx := openTestCortex(t)
	defer cx.Close()
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "FilterAlice"}
	if err := cx.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	low := &Memory{Content: "filteralice rumor", EntityIDs: []string{e.ID}, Confidence: 0.2}
	high := &Memory{Content: "filteralice fact", EntityIDs: []string{e.ID}, Confidence: 0.9}
	if err := cx.PutMemory(ctx, low); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutMemory(ctx, high); err != nil {
		t.Fatal(err)
	}

	// Without filter: both should appear.
	all, err := cx.Recall(ctx, "filteralice")
	if err != nil {
		t.Fatal(err)
	}
	hasLow := false
	for _, r := range all {
		if r.Confidence == 0.2 {
			hasLow = true
		}
	}
	if !hasLow {
		t.Error("expected low-confidence result without filter")
	}

	// With filter at 0.5: only high should appear.
	filtered, err := cx.Recall(ctx, "filteralice", WithMinConfidence(0.5))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range filtered {
		if r.Confidence < 0.5 {
			t.Errorf("WithMinConfidence(0.5) returned r.Confidence=%v", r.Confidence)
		}
	}
}
```

- [ ] **Step 2: Add `WithMinConfidence` option to `types.go`**

In `types.go`, find the existing `RecallOption` block (`recallConfig` struct and `WithLimit`/`WithSourceFilter`):

```go
type recallConfig struct {
	limit         int
	source        string
	minConfidence float64
}

// WithMinConfidence filters out recall results below the given threshold.
// Default 0.0 (no filtering). Applied as a hard >= threshold after RRF
// merge, before the limit cap.
func WithMinConfidence(c float64) RecallOption {
	return func(cfg *recallConfig) { cfg.minConfidence = c }
}
```

- [ ] **Step 3: Populate Confidence in each recall strategy**

In `recall.go`:

`recallMemories` (around line 112) — `Memory` already has `.Confidence` after Task 2:

```go
results[key] = Result{
	Type:       "memory",
	Content:    m.Content,
	Confidence: m.Confidence,
	EntityIDs:  m.EntityIDs,
	Source:     m.Source,
}
```

`recallGraph` (around line 177) — when building results from entities:

```go
results[key] = Result{
	Type:       "entity",
	Content:    content,
	Confidence: e.Confidence,
	// ... other fields as currently set
}
```

(Adjust `Result{...}` to match the existing struct literal in that function — just add the `Confidence:` line.)

`recallKeyword` and `recallVector` — chunks have no confidence. If `ch.EntityID` is non-empty, fetch the parent entity's confidence; else default to 1.0:

```go
for i, ch := range chunks {
	key := "chunk:" + ch.ID
	items[i] = rankedItem{id: key, rank: i}
	conf := 1.0
	if ch.EntityID != "" {
		if e, err := c.GetEntity(ctx, ch.EntityID); err == nil {
			conf = e.Confidence
		}
	}
	results[key] = Result{
		Type:       "chunk",
		Content:    ch.Content,
		Confidence: conf,
		Metadata:   ch.Metadata,
	}
}
```

Apply that same `conf := 1.0; if ch.EntityID != "" { ... }` block in both `recallKeyword` and `recallVector`.

- [ ] **Step 4: Apply min-confidence filter post-RRF in `Recall`**

In `recall.go` around line 53 (after the RRF merge, before the limit cap):

```go
// Merge via reciprocal rank fusion.
merged := rrfMerge(lists, 60)

// Build final results from merged ranked items.
final := make([]Result, 0, len(merged))
for _, item := range merged {
	if r, ok := resultMap[item.id]; ok {
		r.Score = item.score
		final = append(final, r)
	}
}

// Apply min-confidence filter (post-RRF, pre-limit).
if cfg.minConfidence > 0 {
	filtered := final[:0]
	for _, r := range final {
		if r.Confidence >= cfg.minConfidence {
			filtered = append(filtered, r)
		}
	}
	final = filtered
}

// Apply limit.
if len(final) > cfg.limit {
	final = final[:cfg.limit]
}
```

- [ ] **Step 5: Run tests**

Run: `go test -run "TestRecall_ResultIncludesConfidence|TestRecall_WithMinConfidence" -v ./...`
Expected: both PASS.

Run: `go test ./...`
Expected: all tests still pass.

- [ ] **Step 6: Commit**

```bash
git add types.go recall.go recall_test.go
git commit -m "$(cat <<'EOF'
feat(cortex): expose Confidence on Recall Result + WithMinConfidence filter

Each recall strategy populates Result.Confidence from the underlying
row. Chunks (which have no confidence column) inherit from their parent
entity; chunks with no entity_id default to 1.0. New WithMinConfidence
option filters post-RRF as a hard >= threshold. Default 0.0 = no
filtering, preserving prior behavior.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: LLM extractor prompt + JSON parsers (openai + anthropic)

**Files:**
- Modify: `extractor/llmext/extractor.go`
- Modify: `llm/openai/llm.go`
- Modify: `llm/anthropic/llm.go`
- Modify: `llm/openai/llm_test.go`
- Modify: `extractor/llmext/extractor_test.go`

- [ ] **Step 1: Write failing parser test in `llm/openai/llm_test.go`**

Append:

```go
func TestParseExtractionJSON_PreservesConfidence(t *testing.T) {
	raw := `{
		"entities": [
			{"name": "Alice", "type": "person", "confidence": 0.9}
		],
		"relationships": [
			{"source": "Alice", "target": "Stripe", "type": "works_at", "confidence": 0.6}
		],
		"memories": [
			{"content": "alice joined stripe", "confidence": 0.4}
		]
	}`
	ext, err := parseExtractionJSON(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ext.Entities) != 1 || ext.Entities[0].Confidence != 0.9 {
		t.Errorf("entity confidence = %v, want 0.9", ext.Entities[0].Confidence)
	}
	if len(ext.Relationships) != 1 || ext.Relationships[0].Confidence != 0.6 {
		t.Errorf("rel confidence = %v, want 0.6", ext.Relationships[0].Confidence)
	}
	if len(ext.Memories) != 1 || ext.Memories[0].Confidence != 0.4 {
		t.Errorf("memory confidence = %v, want 0.4", ext.Memories[0].Confidence)
	}
}

func TestParseExtractionJSON_OmittedConfidenceIsZero(t *testing.T) {
	// Pre-feature LLM response — no confidence field anywhere.
	raw := `{
		"entities": [{"name": "Bob", "type": "person"}],
		"relationships": [],
		"memories": [{"content": "bob exists"}]
	}`
	ext, err := parseExtractionJSON(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Parser returns zero — Put layer will coerce to 1.0.
	if ext.Entities[0].Confidence != 0 {
		t.Errorf("entity confidence = %v, want 0 (zero, to be coerced upstream)", ext.Entities[0].Confidence)
	}
	if ext.Memories[0].Confidence != 0 {
		t.Errorf("memory confidence = %v, want 0", ext.Memories[0].Confidence)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -run "TestParseExtractionJSON_PreservesConfidence|TestParseExtractionJSON_OmittedConfidenceIsZero" -v ./llm/openai/...`
Expected: first test FAIL (Confidence is always 0); second test should already PASS (zero is the existing behavior).

- [ ] **Step 3: Update `extractionJSON` struct + assembly in `llm/openai/llm.go`**

Around line 179, change the struct:

```go
type extractionJSON struct {
	Entities []struct {
		Type       string  `json:"type"`
		Name       string  `json:"name"`
		Confidence float64 `json:"confidence"`
	} `json:"entities"`
	Relationships []struct {
		Source     string  `json:"source"`
		Target     string  `json:"target"`
		Type       string  `json:"type"`
		Confidence float64 `json:"confidence"`
	} `json:"relationships"`
	Memories []json.RawMessage `json:"memories"`
}
```

Update the assembly loops:

```go
for _, e := range ej.Entities {
	extraction.Entities = append(extraction.Entities, cortex.Entity{
		Type:       e.Type,
		Name:       e.Name,
		Confidence: e.Confidence,
	})
}

for _, r := range ej.Relationships {
	extraction.Relationships = append(extraction.Relationships, cortex.Relationship{
		SourceID:   r.Source,
		TargetID:   r.Target,
		Type:       r.Type,
		Confidence: r.Confidence,
	})
}
```

For memories, the existing code already handles both `{"content": "..."}` and bare string forms. Extend the object form:

```go
for _, m := range ej.Memories {
	var memObj struct {
		Content    string  `json:"content"`
		Confidence float64 `json:"confidence"`
	}
	if err := json.Unmarshal(m, &memObj); err == nil && memObj.Content != "" {
		extraction.Memories = append(extraction.Memories, cortex.Memory{
			Content:    memObj.Content,
			Confidence: memObj.Confidence,
		})
		continue
	}
	// Handle case where memory is just a string.
	var memStr string
	if err := json.Unmarshal(m, &memStr); err == nil && memStr != "" {
		extraction.Memories = append(extraction.Memories, cortex.Memory{
			Content: memStr,
		})
	}
}
```

(The string form leaves Confidence as 0, which is correct — the Put layer will coerce to 1.0.)

- [ ] **Step 4: Apply identical changes to `llm/anthropic/llm.go`**

The anthropic provider's `extractionJSON` and parsing logic mirror openai's. Apply the same struct fields and assembly changes. Files are around the same line numbers (struct at ~180, parsing at ~210-240).

- [ ] **Step 5: Update the extractor prompt in `extractor/llmext/extractor.go`**

Replace the `extractionPrompt` constant with:

```go
const extractionPrompt = `Analyze the following text and extract structured knowledge.
Return a JSON object with the following fields:
- "entities": array of objects with "name", "type", optional "attributes", and "confidence"
- "relationships": array of objects with "source" (entity name), "target" (entity name), "type", optional "attributes", and "confidence"
- "memories": array of objects with "content" (a concise factual statement), optional "entity_ids", and "confidence"

For each item, "confidence" is a float between 0.0 and 1.0 expressing how
certain you are about that specific extracted claim:
- 1.0  = directly stated in the text, unambiguous
- 0.7  = strongly implied or paraphrased
- 0.4  = inferred or interpretive
- 0.2  = speculative or weakly supported

Be honest. It is better to mark something low-confidence than to omit it
or to claim certainty you don't have.

Extract all people, organizations, places, concepts, and other notable entities.
Identify relationships between entities (e.g., works_at, knows, located_in).
Create memories for key facts and statements.

Return ONLY valid JSON, no markdown formatting.`
```

- [ ] **Step 6: Add a test for the prompt-extracted-confidence path in `extractor/llmext/extractor_test.go`**

The existing test uses a fake LLM. Add (or extend) a test where the fake LLM returns extraction JSON containing confidence and assert it survives the Extract call:

```go
func TestExtractor_PreservesConfidence(t *testing.T) {
	fake := &fakeLLM{
		extractResult: cortex.ExtractionResult{
			Parsed: &cortex.Extraction{
				Entities:      []cortex.Entity{{Type: "person", Name: "Alice", Confidence: 0.7}},
				Relationships: []cortex.Relationship{{SourceID: "Alice", TargetID: "Stripe", Type: "works_at", Confidence: 0.5}},
				Memories:      []cortex.Memory{{Content: "alice did X", Confidence: 0.3}},
			},
		},
	}
	ex := New(fake)
	got, err := ex.Extract(context.Background(), "some text", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Entities[0].Confidence != 0.7 {
		t.Errorf("entity confidence = %v, want 0.7", got.Entities[0].Confidence)
	}
	if got.Relationships[0].Confidence != 0.5 {
		t.Errorf("rel confidence = %v, want 0.5", got.Relationships[0].Confidence)
	}
	if got.Memories[0].Confidence != 0.3 {
		t.Errorf("memory confidence = %v, want 0.3", got.Memories[0].Confidence)
	}
}
```

You may need to check what the existing `fakeLLM` looks like and adapt the test to match its API; the principle is: the LLM-side Extractor is just a passthrough, so this test verifies that nothing strips confidence en route.

- [ ] **Step 7: Run tests**

Run: `go test ./extractor/llmext/... ./llm/openai/... ./llm/anthropic/... -count=1 -v`
Expected: all new tests PASS. (The pre-existing `llm/openai` live-network 429s remain unrelated.)

- [ ] **Step 8: Commit**

```bash
git add extractor/llmext/extractor.go extractor/llmext/extractor_test.go llm/openai/llm.go llm/openai/llm_test.go llm/anthropic/llm.go
git commit -m "$(cat <<'EOF'
feat(extractor): request and parse confidence from LLM extraction

Updated prompt asks for a confidence float per item with four anchor
values (1.0 / 0.7 / 0.4 / 0.2). Provider parsers (openai + anthropic)
preserve the field through to cortex.Extraction structs. Missing
confidence falls through as zero — coerced to 1.0 by the Put layer
for backward compatibility.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Vault render — frontmatter + memory bullet confidence

**Files:**
- Modify: `vault/render.go`
- Modify: `vault/render_test.go`
- Create: `vault/testdata/entity_with_confidence.golden.md`

- [ ] **Step 1: Write the golden + test**

Create `vault/testdata/entity_with_confidence.golden.md`:

```markdown
---
cortex_id: ent_01HUNSURE
type: person
name: Uncertain Ulrika
created_at: 2026-05-01T00:00:00Z
updated_at: 2026-05-01T00:00:00Z
confidence: 0.7
exported_at: 2026-05-26T14:32:10Z
---

# Uncertain Ulrika

## Memories

- She may join Stripe (conf 40%) — `slack-rumor`
- She has a brother
```

Append to `vault/render_test.go`:

```go
func TestRenderEntity_WithConfidence(t *testing.T) {
	ent := cortex.Entity{
		ID:         "ent_01HUNSURE",
		Type:       "person",
		Name:       "Uncertain Ulrika",
		Confidence: 0.7,
		CreatedAt:  time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
	}
	memories := []cortex.Memory{
		{Content: "She may join Stripe", Source: "slack-rumor", Confidence: 0.4},
		{Content: "She has a brother", Confidence: 1.0},
	}
	got := renderEntity(ent, memories, nil, nil, nil, fixedExportTime)
	assertGolden(t, "entity_with_confidence.golden.md", got)
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test -run TestRenderEntity_WithConfidence -v ./vault/...`
Expected: golden mismatch (current renderer doesn't emit confidence).

- [ ] **Step 3: Update `renderEntity` in `vault/render.go`**

In the frontmatter assembly, after `updated_at` and before `exported_at`:

```go
if e.Confidence != 0 && e.Confidence != 1.0 {
	fmt.Fprintf(&b, "confidence: %g\n", e.Confidence)
}
```

(The `!= 0` guard handles legacy paths where Confidence might still be zero somehow; `!= 1.0` is the spec-mandated "only emit when not the default.")

In the memory-bullet rendering inside the `## Memories` section:

```go
for _, m := range memories {
	var confSuffix string
	if m.Confidence > 0 && m.Confidence < 1.0 {
		confSuffix = fmt.Sprintf(" (conf %d%%)", int(m.Confidence*100))
	}
	if m.Source != "" {
		fmt.Fprintf(&b, "- %s%s — `%s`\n", m.Content, confSuffix, m.Source)
	} else {
		fmt.Fprintf(&b, "- %s%s\n", m.Content, confSuffix)
	}
}
```

Whole-percent display via `int(m.Confidence*100)`. Suffix omitted at 1.0 (default) or 0 (unset, treated as default upstream).

- [ ] **Step 4: Run all vault tests**

Run: `go test ./vault/... -v -count=1`
Expected: new test PASS. All prior golden tests still PASS (Confidence=0 / 1.0 on those entities means no suffix and no frontmatter line — same output as before).

- [ ] **Step 5: Commit**

```bash
git add vault/render.go vault/render_test.go vault/testdata/entity_with_confidence.golden.md
git commit -m "$(cat <<'EOF'
feat(vault): render confidence in frontmatter and memory bullets

Entity frontmatter gains `confidence:` line when value is not 1.0.
Memory bullets gain `(conf N%)` inline suffix when value is < 1.0.
Default 1.0 entities/memories render identically to before — existing
goldens unchanged.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: CLI — `--min-confidence` flag + display in recall + entity get

**Files:**
- Modify: `cmd/cortex/main.go`

- [ ] **Step 1: Reference: current `cmdRecall` (lines 254-282 of `cmd/cortex/main.go`)**

For reference, the current function is:

```go
func cmdRecall() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: cortex recall <query>")
		os.Exit(1)
	}

	query := strings.Join(os.Args[2:], " ")
	cx := openCortex()
	defer cx.Close()

	ctx := context.Background()
	results, err := cx.Recall(ctx, query)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if len(results) == 0 {
		fmt.Println("No results found.")
		return
	}

	for i, r := range results {
		fmt.Printf("[%d] (%s, score=%.4f) %s\n", i+1, r.Type, r.Score, r.Content)
		if r.Source != "" {
			fmt.Printf("    source: %s\n", r.Source)
		}
	}
}
```

- [ ] **Step 2: Rewrite `cmdRecall` to parse `--min-confidence` and pass it through**

Replace the function body. The new version splits `os.Args[2:]` into query tokens vs flags, builds an options slice, and calls `Recall` with it:

```go
func cmdRecall() {
	args := os.Args[2:]
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cortex recall <query> [--min-confidence <0-1>]")
		os.Exit(1)
	}

	var minConf float64
	var queryParts []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--min-confidence":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --min-confidence requires a value")
				os.Exit(1)
			}
			v, err := strconv.ParseFloat(args[i+1], 64)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: invalid --min-confidence: %v\n", err)
				os.Exit(1)
			}
			minConf = v
			i++
		default:
			queryParts = append(queryParts, args[i])
		}
	}
	if len(queryParts) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cortex recall <query> [--min-confidence <0-1>]")
		os.Exit(1)
	}
	query := strings.Join(queryParts, " ")

	cx := openCortex()
	defer cx.Close()

	ctx := context.Background()
	var opts []cortex.RecallOption
	if minConf > 0 {
		opts = append(opts, cortex.WithMinConfidence(minConf))
	}
	results, err := cx.Recall(ctx, query, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if len(results) == 0 {
		fmt.Println("No results found.")
		return
	}

	for i, r := range results {
		fmt.Printf("[%d] (%s, score=%.4f, conf=%d%%) %s\n",
			i+1, r.Type, r.Score, int(r.Confidence*100), r.Content)
		if r.Source != "" {
			fmt.Printf("    source: %s\n", r.Source)
		}
	}
}
```

`strconv` must be imported — check the top of `main.go`; it's already imported (used by the existing `EMBEDDING_DIMS` parsing).

- [ ] **Step 3: (merged into Step 2)** — the result-printing format is part of the rewrite in Step 2; no separate edit needed.

- [ ] **Step 4: Add confidence display to `cmdEntity get`**

Find the existing per-entity display lines in `cmdEntity` (the `get` subcommand). Add one new line:

```go
fmt.Printf("Confidence: %.0f%%\n", e.Confidence*100)
```

Place it near the other entity fields (Type, Name, Source, etc.).

- [ ] **Step 5: Update usage string in `printUsage`**

Find the `recall` entry in the usage text and update:

```
  recall <query> [--min-confidence <0-1>]
                                 Recall and print results
```

- [ ] **Step 6: Build and smoke test**

Run: `go build ./...`
Expected: clean build.

Smoke (optional; pure formatting verification):

```bash
go build -o /tmp/cortex-test ./cmd/cortex
/tmp/cortex-test --db /tmp/conf-smoke.db init <<EOF
TestUser


EOF
/tmp/cortex-test --db /tmp/conf-smoke.db recall test
# Should show "conf=100%" in output
/tmp/cortex-test --db /tmp/conf-smoke.db recall test --min-confidence 0.5
# Should still show results (everything is conf=100%)
rm -rf /tmp/conf-smoke.db /tmp/cortex-test
```

- [ ] **Step 7: Commit**

```bash
git add cmd/cortex/main.go
git commit -m "$(cat <<'EOF'
feat(cli): show confidence in recall + entity get; --min-confidence flag

`cortex recall` accepts --min-confidence <0-1>. Recall output now
includes "conf=N%" alongside score. `cortex entity get <id>` shows
the entity's confidence on its own line.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: Documentation

**Files:**
- Modify: `docs/CORTEX.md`
- Modify: `cmd/cortex/CORTEX.md.template`
- Modify: `README.md`

- [ ] **Step 1: Update `docs/CORTEX.md`**

In the existing "When to remember" section, add a final bullet:

```markdown
- When you're not certain about a fact, let the extractor mark it low-confidence rather than dropping it. Better to record a hesitation than to lose it.
```

Add a new short section after "What NOT to remember":

```markdown
## Confidence

Every extracted entity, relationship, and memory carries a confidence score (0.0–1.0). The LLM extractor sets it: 1.0 for things directly stated, lower for things inferred, paraphrased, or speculative.

Use it when deciding whether to assert a fact: confidence below ~0.5 usually means "the user said something like this but you might be misreading it." Cite the memory's source if you act on it. If a recall returns mostly low-confidence results, that's a signal to ask the user a clarifying question rather than guessing.

You can filter recalls to high-confidence only:

```
cortex recall "..." --min-confidence 0.7
```

But the default (no filter) is right for most cases — uncertain knowledge is still knowledge.
```

- [ ] **Step 2: Mirror to the embedded template**

Copy `docs/CORTEX.md` to `cmd/cortex/CORTEX.md.template`, preserving the existing comment header at the top:

```bash
# Preserve header, replace body:
head -5 cmd/cortex/CORTEX.md.template > /tmp/header.md
cat /tmp/header.md > cmd/cortex/CORTEX.md.template
echo "" >> cmd/cortex/CORTEX.md.template
cat docs/CORTEX.md >> cmd/cortex/CORTEX.md.template
rm /tmp/header.md
```

(Verify by `head -10 cmd/cortex/CORTEX.md.template` afterward — comment header should be intact, body should match docs/CORTEX.md.)

- [ ] **Step 3: Update `README.md`**

Find the section that describes `recall` (likely under CLI Reference). Add a short paragraph at the end of that section:

```markdown
Recall results now carry a `confidence` score (0–1). The LLM extractor sets the value at ingest time — 1.0 for facts directly stated in the source, lower for inferences or speculation. Filter to high-confidence-only with `--min-confidence`:

```bash
cortex recall "did alice join stripe" --min-confidence 0.7
```

Default behavior is unchanged: all results surface, ranked by Reciprocal Rank Fusion, with confidence reported but not used for ranking.
```

Find the section that describes `remember` (or the Extraction Pipeline section). Add:

```markdown
The LLM extractor now requests a `confidence` field on every extracted item. Existing brain.db files migrate transparently on next `Open` — old rows get `confidence=1.0`.
```

- [ ] **Step 4: Verify docs and full suite**

Run: `go test ./...`
Expected: still passing (docs-only change should not affect tests, but it's cheap insurance).

Visual check: `head -100 docs/CORTEX.md` and `head -100 cmd/cortex/CORTEX.md.template` — bodies should match modulo the template's `<!-- ... -->` header.

- [ ] **Step 5: Commit**

```bash
git add docs/CORTEX.md cmd/cortex/CORTEX.md.template README.md
git commit -m "$(cat <<'EOF'
docs: document confidence scores in CORTEX.md and README

CORTEX.md gains a Confidence section explaining the score, when to
factor it into agent reasoning, and how to filter recalls.
README's recall and remember sections gain short paragraphs on the
new field and the transparent migration.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Done

After Task 7, callers can:

```go
// Manual ingest with explicit confidence:
cx.PutMemory(ctx, &cortex.Memory{
    Content:    "Alice might be leaving Stripe",
    Confidence: 0.3,
})

// Filtered recall:
results, _ := cx.Recall(ctx, "alice", cortex.WithMinConfidence(0.5))
for _, r := range results {
    fmt.Printf("%s (%.0f%% confident)\n", r.Content, r.Confidence*100)
}
```

And via CLI:

```bash
cortex recall "what about alice" --min-confidence 0.7
cortex entity get ent_01H... # shows Confidence: 80%
cortex export --vault ./vault --full # picks up new frontmatter+bullets
```

LLM-extracted ingests via `cortex remember` and `cortex sync` automatically get LLM-graded confidences.
