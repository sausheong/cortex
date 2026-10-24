# Cortex Lint Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `cortex lint` — a pure-read scan of the knowledge graph that surfaces six categories of cleanup candidates (orphans, near-duplicates, etc.) as a markdown report.

**Architecture:** New `Lint(ctx, opts...) (LintReport, error)` method on `*Cortex` runs six SQL queries (one per check), assembles a typed `LintReport` struct. A separate `renderLintMarkdown` function formats the report. CLI command `cortex lint` wires both together with flag parsing. No schema changes, no new dependencies, no LLM/embedder required.

**Tech Stack:** Go 1.25.1, `database/sql`, existing CLI patterns.

**Spec:** `docs/superpowers/specs/2026-05-27-cortex-lint-design.md`

---

## File Structure

**New files:**

| File | Responsibility |
|---|---|
| `lint.go` | `Lint()` orchestrator + six per-check helpers + counts |
| `lint_render.go` | `renderLintMarkdown(r LintReport) string` |
| `lint_test.go` | Integration tests (12 cases) |
| `cmd/cortex/lint.go` | `cmdLint()`, `parseLintArgs`, `lintOptions` |
| `cmd/cortex/lint_test.go` | CLI flag-parsing tests |

**Modified files:**

| File | Change |
|---|---|
| `types.go` | Add `LintReport`, `EntityRef`, `DuplicatePair`, `MemoryRef`, `LintOption`, `lintConfig`, `WithLowConfidence`, `WithLowConfidenceThreshold` |
| `cmd/cortex/main.go` | Add `case "lint": cmdLint()`, usage entry |
| `README.md` | Add `### cortex lint` subsection |
| `docs/CORTEX.md` + `cmd/cortex/CORTEX.md.template` | Brief mention in workflow loop section |

---

## Task 1: Types + `Lint` + six check helpers + tests

**Files:**
- Modify: `types.go`
- Create: `lint.go`
- Create: `lint_test.go`

### Step 1: Add types to `types.go`

Append at the bottom of `types.go`:

```go
// --- Lint ---

// LintReport summarizes the cleanup candidates the lint scan found.
type LintReport struct {
	EntityCount       int
	RelationshipCount int
	MemoryCount       int

	Orphans               []EntityRef
	EntitiesNoMemories    []EntityRef
	NearDuplicates        []DuplicatePair
	DeadSources           []string
	UnlinkedMemories      []MemoryRef
	LowConfidenceMemories []MemoryRef // populated only when WithLowConfidence is set
}

// EntityRef is a minimal entity descriptor for lint findings.
type EntityRef struct {
	ID   string
	Name string
	Type string
}

// DuplicatePair is one pair of entities that share a type and have
// case-insensitively-equal names.
type DuplicatePair struct {
	Type string
	A    EntityRef
	B    EntityRef
}

// MemoryRef is a minimal memory descriptor for lint findings.
// Content is truncated to ~80 chars + "..." if longer.
type MemoryRef struct {
	ID         string
	Content    string
	Source     string
	Confidence float64
}

// LintOption configures Lint behavior.
type LintOption func(*lintConfig)

type lintConfig struct {
	lowConfidence          bool
	lowConfidenceThreshold float64
}

// WithLowConfidence enables the low-confidence memories section
// (skipped by default).
func WithLowConfidence() LintOption {
	return func(c *lintConfig) { c.lowConfidence = true }
}

// WithLowConfidenceThreshold sets the cutoff for "low confidence"
// (default 0.3) and implicitly enables the section.
func WithLowConfidenceThreshold(t float64) LintOption {
	return func(c *lintConfig) {
		c.lowConfidence = true
		c.lowConfidenceThreshold = t
	}
}
```

### Step 2: Write failing tests in `lint_test.go`

Create `lint_test.go`:

```go
package cortex

import (
	"context"
	"strings"
	"testing"
)

// --- Test 1: Empty graph ---

func TestLint_EmptyGraph(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	r, err := cx.Lint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r.EntityCount != 0 || r.RelationshipCount != 0 || r.MemoryCount != 0 {
		t.Errorf("expected zero counts, got %+v", r)
	}
	if len(r.Orphans) != 0 || len(r.EntitiesNoMemories) != 0 ||
		len(r.NearDuplicates) != 0 || len(r.DeadSources) != 0 ||
		len(r.UnlinkedMemories) != 0 || len(r.LowConfidenceMemories) != 0 {
		t.Errorf("expected no findings on empty graph: %+v", r)
	}
}

// --- Test 2: Healthy graph has no findings ---

func TestLint_HealthyGraph(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	alice := &Entity{Type: "person", Name: "Alice Healthy"}
	stripe := &Entity{Type: "organization", Name: "Stripe Healthy"}
	if err := cx.PutEntity(ctx, alice); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutEntity(ctx, stripe); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutRelationship(ctx, &Relationship{
		SourceID: alice.ID, TargetID: stripe.ID, Type: "works_at",
	}); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutMemory(ctx, &Memory{
		Content:   "alice works at stripe",
		EntityIDs: []string{alice.ID, stripe.ID},
		Source:    "notes/healthy.md",
	}); err != nil {
		t.Fatal(err)
	}

	r, err := cx.Lint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Orphans) != 0 {
		t.Errorf("Orphans should be empty, got %v", r.Orphans)
	}
	if len(r.EntitiesNoMemories) != 0 {
		t.Errorf("EntitiesNoMemories should be empty, got %v", r.EntitiesNoMemories)
	}
	if len(r.NearDuplicates) != 0 {
		t.Errorf("NearDuplicates should be empty, got %v", r.NearDuplicates)
	}
	if len(r.UnlinkedMemories) != 0 {
		t.Errorf("UnlinkedMemories should be empty, got %v", r.UnlinkedMemories)
	}
}

// --- Test 3: Orphan detection ---

func TestLint_OrphanDetected(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	orphan := &Entity{Type: "concept", Name: "Floating"}
	connected := &Entity{Type: "person", Name: "Connected"}
	other := &Entity{Type: "organization", Name: "Other"}
	for _, e := range []*Entity{orphan, connected, other} {
		if err := cx.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := cx.PutRelationship(ctx, &Relationship{
		SourceID: connected.ID, TargetID: other.ID, Type: "knows",
	}); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutMemory(ctx, &Memory{
		Content: "connected does things", EntityIDs: []string{connected.ID},
	}); err != nil {
		t.Fatal(err)
	}

	r, err := cx.Lint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foundOrphan := false
	for _, o := range r.Orphans {
		if o.ID == orphan.ID {
			foundOrphan = true
		}
		if o.ID == connected.ID {
			t.Errorf("connected entity should NOT be orphan")
		}
	}
	if !foundOrphan {
		t.Errorf("orphan %s not in report: %+v", orphan.ID, r.Orphans)
	}
}

// --- Test 4: EntitiesNoMemories ---

func TestLint_EntityWithoutMemories(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	a := &Entity{Type: "person", Name: "HasRelsNoMems"}
	b := &Entity{Type: "person", Name: "Target"}
	for _, e := range []*Entity{a, b} {
		if err := cx.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := cx.PutRelationship(ctx, &Relationship{
		SourceID: a.ID, TargetID: b.ID, Type: "knows",
	}); err != nil {
		t.Fatal(err)
	}
	// No memory links to either.

	r, err := cx.Lint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foundA := false
	for _, e := range r.EntitiesNoMemories {
		if e.ID == a.ID {
			foundA = true
		}
	}
	if !foundA {
		t.Errorf("entity with rels but no memories not surfaced: %+v", r.EntitiesNoMemories)
	}
	// And NOT in Orphans (it has a relationship).
	for _, o := range r.Orphans {
		if o.ID == a.ID {
			t.Errorf("entity with rels should not be in Orphans")
		}
	}
}

// --- Test 5: Near-duplicate names (case-insensitive, same type) ---

func TestLint_NearDuplicateNames(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	a := &Entity{Type: "person", Name: "Alice Chen"}
	b := &Entity{Type: "person", Name: "alice chen"}
	// Different-type but same name shouldn't pair.
	c := &Entity{Type: "organization", Name: "Alice Chen"}
	for _, e := range []*Entity{a, b, c} {
		if err := cx.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	r, err := cx.Lint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.NearDuplicates) != 1 {
		t.Fatalf("expected 1 near-duplicate pair, got %d: %+v", len(r.NearDuplicates), r.NearDuplicates)
	}
	pair := r.NearDuplicates[0]
	if pair.Type != "person" {
		t.Errorf("pair type = %q, want person", pair.Type)
	}
	// Verify both a and b are in the pair (in some order).
	ids := map[string]bool{pair.A.ID: true, pair.B.ID: true}
	if !ids[a.ID] || !ids[b.ID] {
		t.Errorf("pair should contain both alice entities: got %s and %s", pair.A.ID, pair.B.ID)
	}
}

// --- Test 6: Self-pair guard (no duplicate pairs in reverse order) ---

func TestLint_NearDuplicateNoSelfOrReversePair(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	a := &Entity{Type: "concept", Name: "X"}
	b := &Entity{Type: "concept", Name: "x"}
	if err := cx.PutEntity(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutEntity(ctx, b); err != nil {
		t.Fatal(err)
	}

	r, err := cx.Lint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Should be exactly 1 pair, not 2 (a,b) and (b,a), and not (a,a) or (b,b).
	if len(r.NearDuplicates) != 1 {
		t.Errorf("expected exactly 1 pair, got %d", len(r.NearDuplicates))
	}
}

// --- Test 7: Dead source ---

func TestLint_DeadSource(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	// Create an entity with one source.
	e := &Entity{Type: "person", Name: "Live", Source: "live.md"}
	if err := cx.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	// Insert a memory directly with a source that has no matching entity.
	// We can't use cx.PutMemory and then forget the entity, because Forget
	// cascade-deletes memories. Instead INSERT directly to simulate the
	// orphaned-source state (e.g. from direct SQL access or a bug elsewhere).
	if _, err := cx.db.ExecContext(ctx,
		`INSERT INTO memories (id, content, source) VALUES (?, ?, ?)`,
		"mem_dead", "an orphan memory", "old-import.md"); err != nil {
		t.Fatal(err)
	}

	r, err := cx.Lint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foundDead := false
	for _, s := range r.DeadSources {
		if s == "old-import.md" {
			foundDead = true
		}
		if s == "live.md" {
			t.Errorf("live.md should NOT be in DeadSources")
		}
	}
	if !foundDead {
		t.Errorf("dead source not surfaced: %v", r.DeadSources)
	}
}

// --- Test 8: Unlinked memory ---

func TestLint_UnlinkedMemory(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	// Memory with no EntityIDs.
	m := &Memory{Content: "floating memory", Source: "notes.md"}
	if err := cx.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}

	r, err := cx.Lint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, mr := range r.UnlinkedMemories {
		if mr.ID == m.ID {
			found = true
			if !strings.Contains(mr.Content, "floating memory") {
				t.Errorf("content = %q, want to contain 'floating memory'", mr.Content)
			}
		}
	}
	if !found {
		t.Errorf("unlinked memory not surfaced: %+v", r.UnlinkedMemories)
	}
}

// --- Test 9: Low-confidence default off ---

func TestLint_LowConfidenceDefaultOff(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	m := &Memory{Content: "uncertain claim", Confidence: 0.2}
	if err := cx.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}

	r, err := cx.Lint(ctx) // no options
	if err != nil {
		t.Fatal(err)
	}
	if len(r.LowConfidenceMemories) != 0 {
		t.Errorf("LowConfidenceMemories should be empty by default: %+v", r.LowConfidenceMemories)
	}
}

// --- Test 10: Low-confidence opt-in ---

func TestLint_LowConfidenceOptIn(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	low := &Memory{Content: "uncertain claim", Confidence: 0.2}
	high := &Memory{Content: "solid fact", Confidence: 0.9}
	for _, m := range []*Memory{low, high} {
		if err := cx.PutMemory(ctx, m); err != nil {
			t.Fatal(err)
		}
	}

	r, err := cx.Lint(ctx, WithLowConfidence())
	if err != nil {
		t.Fatal(err)
	}
	foundLow := false
	for _, mr := range r.LowConfidenceMemories {
		if mr.ID == low.ID {
			foundLow = true
		}
		if mr.ID == high.ID {
			t.Errorf("high-confidence memory should NOT surface")
		}
	}
	if !foundLow {
		t.Errorf("low-confidence memory not surfaced: %+v", r.LowConfidenceMemories)
	}
}

// --- Test 11: Custom threshold ---

func TestLint_LowConfidenceCustomThreshold(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	m := &Memory{Content: "mid-confidence", Confidence: 0.5}
	if err := cx.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}

	// Threshold 0.6 → 0.5 is below → surfaces.
	r, err := cx.Lint(ctx, WithLowConfidenceThreshold(0.6))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, mr := range r.LowConfidenceMemories {
		if mr.ID == m.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("memory at 0.5 should surface with threshold 0.6: %+v", r.LowConfidenceMemories)
	}

	// Threshold 0.4 → 0.5 is above → doesn't surface.
	r2, err := cx.Lint(ctx, WithLowConfidenceThreshold(0.4))
	if err != nil {
		t.Fatal(err)
	}
	for _, mr := range r2.LowConfidenceMemories {
		if mr.ID == m.ID {
			t.Errorf("memory at 0.5 should NOT surface with threshold 0.4")
		}
	}
}

// --- Test 12: Counts populated ---

func TestLint_CountsPopulated(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	a := &Entity{Type: "person", Name: "A"}
	b := &Entity{Type: "person", Name: "B"}
	if err := cx.PutEntity(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutEntity(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutRelationship(ctx, &Relationship{
		SourceID: a.ID, TargetID: b.ID, Type: "knows",
	}); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutMemory(ctx, &Memory{Content: "memcontent", EntityIDs: []string{a.ID}}); err != nil {
		t.Fatal(err)
	}

	r, err := cx.Lint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r.EntityCount != 2 {
		t.Errorf("EntityCount = %d, want 2", r.EntityCount)
	}
	if r.RelationshipCount != 1 {
		t.Errorf("RelationshipCount = %d, want 1", r.RelationshipCount)
	}
	if r.MemoryCount != 1 {
		t.Errorf("MemoryCount = %d, want 1", r.MemoryCount)
	}
}
```

### Step 3: Run tests to verify they fail

Run: `go test -run TestLint -v .`
Expected: compile error — `Lint` undefined.

### Step 4: Implement `lint.go`

Create `lint.go`:

```go
package cortex

import (
	"context"
	"database/sql"
	"fmt"
)

const defaultLowConfidenceThreshold = 0.3

// Lint scans the graph and returns a structured report of cleanup
// candidates. Pure read operation — never mutates the graph.
func (c *Cortex) Lint(ctx context.Context, opts ...LintOption) (LintReport, error) {
	cfg := &lintConfig{lowConfidenceThreshold: defaultLowConfidenceThreshold}
	for _, o := range opts {
		o(cfg)
	}

	var r LintReport
	var err error

	if r.EntityCount, err = countRows(ctx, c.db, "entities"); err != nil {
		return r, fmt.Errorf("cortex: lint: count entities: %w", err)
	}
	if r.RelationshipCount, err = countRows(ctx, c.db, "relationships"); err != nil {
		return r, fmt.Errorf("cortex: lint: count relationships: %w", err)
	}
	if r.MemoryCount, err = countRows(ctx, c.db, "memories"); err != nil {
		return r, fmt.Errorf("cortex: lint: count memories: %w", err)
	}

	if r.Orphans, err = findOrphans(ctx, c.db); err != nil {
		return r, fmt.Errorf("cortex: lint: orphans: %w", err)
	}
	if r.EntitiesNoMemories, err = findEntitiesNoMemories(ctx, c.db); err != nil {
		return r, fmt.Errorf("cortex: lint: entities-no-memories: %w", err)
	}
	if r.NearDuplicates, err = findNearDuplicates(ctx, c.db); err != nil {
		return r, fmt.Errorf("cortex: lint: near-duplicates: %w", err)
	}
	if r.DeadSources, err = findDeadSources(ctx, c.db); err != nil {
		return r, fmt.Errorf("cortex: lint: dead-sources: %w", err)
	}
	if r.UnlinkedMemories, err = findUnlinkedMemories(ctx, c.db); err != nil {
		return r, fmt.Errorf("cortex: lint: unlinked-memories: %w", err)
	}
	if cfg.lowConfidence {
		if r.LowConfidenceMemories, err = findLowConfidenceMemories(ctx, c.db, cfg.lowConfidenceThreshold); err != nil {
			return r, fmt.Errorf("cortex: lint: low-confidence: %w", err)
		}
	}

	return r, nil
}

func countRows(ctx context.Context, db *sql.DB, table string) (int, error) {
	var n int
	// Table name is hard-coded by caller — safe to interpolate.
	err := db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&n)
	return n, err
}

func findOrphans(ctx context.Context, db *sql.DB) ([]EntityRef, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT e.id, e.name, e.type FROM entities e
		WHERE NOT EXISTS (SELECT 1 FROM relationships WHERE source_id = e.id OR target_id = e.id)
		  AND NOT EXISTS (SELECT 1 FROM memory_entities WHERE entity_id = e.id)
		ORDER BY e.type, e.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EntityRef
	for rows.Next() {
		var e EntityRef
		if err := rows.Scan(&e.ID, &e.Name, &e.Type); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func findEntitiesNoMemories(ctx context.Context, db *sql.DB) ([]EntityRef, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT e.id, e.name, e.type FROM entities e
		WHERE NOT EXISTS (SELECT 1 FROM memory_entities WHERE entity_id = e.id)
		  AND EXISTS (SELECT 1 FROM relationships WHERE source_id = e.id OR target_id = e.id)
		ORDER BY e.type, e.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EntityRef
	for rows.Next() {
		var e EntityRef
		if err := rows.Scan(&e.ID, &e.Name, &e.Type); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func findNearDuplicates(ctx context.Context, db *sql.DB) ([]DuplicatePair, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT a.id, a.name, a.type, b.id, b.name, b.type
		FROM entities a
		JOIN entities b
		  ON a.type = b.type
		 AND LOWER(a.name) = LOWER(b.name)
		 AND a.id < b.id
		ORDER BY a.type, LOWER(a.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DuplicatePair
	for rows.Next() {
		var p DuplicatePair
		if err := rows.Scan(&p.A.ID, &p.A.Name, &p.A.Type, &p.B.ID, &p.B.Name, &p.B.Type); err != nil {
			return nil, err
		}
		p.Type = p.A.Type
		out = append(out, p)
	}
	return out, rows.Err()
}

func findDeadSources(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT source FROM memories
		WHERE source IS NOT NULL AND source != ''
		  AND source NOT IN (
			SELECT DISTINCT source FROM entities
			WHERE source IS NOT NULL AND source != ''
		  )
		ORDER BY source`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func findUnlinkedMemories(ctx context.Context, db *sql.DB) ([]MemoryRef, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, content, source, confidence FROM memories m
		WHERE NOT EXISTS (SELECT 1 FROM memory_entities WHERE memory_id = m.id)
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMemoryRefs(rows)
}

func findLowConfidenceMemories(ctx context.Context, db *sql.DB, threshold float64) ([]MemoryRef, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, content, source, confidence FROM memories
		WHERE confidence < ?
		ORDER BY confidence ASC`, threshold)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanMemoryRefs(rows)
}

func scanMemoryRefs(rows *sql.Rows) ([]MemoryRef, error) {
	var out []MemoryRef
	for rows.Next() {
		var m MemoryRef
		if err := rows.Scan(&m.ID, &m.Content, &m.Source, &m.Confidence); err != nil {
			return nil, err
		}
		m.Content = truncate(m.Content, 80)
		out = append(out, m)
	}
	return out, rows.Err()
}

// truncate returns s if shorter than n, else s[:n] + "...".
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
```

### Step 5: Run tests to verify they pass

Run: `go test -run TestLint -v -count=1 .`
Expected: all 12 tests PASS.

Run: `go test ./... -count=1`
Expected: full hermetic suite passes. Pre-existing `llm/openai` 429 failures unrelated.

### Step 6: Commit

```bash
git add types.go lint.go lint_test.go
git commit -m "$(cat <<'EOF'
feat(cortex): Lint — read-only scan for graph cleanup candidates

Six checks: orphan entities, entities with no memories, near-duplicate
names (case-insensitive within type), dead sources, unlinked memories,
and low-confidence memories (opt-in via WithLowConfidence).

Pure SQL queries; no LLM or embedder required. Returns a typed
LintReport for easy testing and downstream formatting.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Markdown rendering

**Files:**
- Create: `lint_render.go`
- Modify: `lint_test.go` (add render test)

### Step 1: Write failing render test

Append to `lint_test.go`:

```go
// --- Test 13: Render markdown ---

func TestLint_RenderMarkdown(t *testing.T) {
	r := LintReport{
		EntityCount:       10,
		RelationshipCount: 5,
		MemoryCount:       7,
		Orphans: []EntityRef{
			{ID: "ent_01", Name: "Floating", Type: "concept"},
		},
		NearDuplicates: []DuplicatePair{
			{Type: "person", A: EntityRef{ID: "ent_02", Name: "Alice Chen", Type: "person"}, B: EntityRef{ID: "ent_03", Name: "alice chen", Type: "person"}},
		},
		UnlinkedMemories: []MemoryRef{
			{ID: "mem_01", Content: "loose memory", Source: "slack-export", Confidence: 1.0},
		},
	}
	got := renderLintMarkdown(r)

	// Header summary.
	if !strings.Contains(got, "Scanned 10 entities, 5 relationships, 7 memories.") {
		t.Errorf("missing header summary, got:\n%s", got)
	}
	// Orphan section heading + count.
	if !strings.Contains(got, "## Orphan entities (1)") {
		t.Errorf("missing orphans heading, got:\n%s", got)
	}
	if !strings.Contains(got, "ent_01") {
		t.Errorf("missing orphan ID, got:\n%s", got)
	}
	// Near-duplicate pair line.
	if !strings.Contains(got, "## Near-duplicate entity names (1 pair") {
		t.Errorf("missing near-duplicates heading, got:\n%s", got)
	}
	if !strings.Contains(got, "ent_02") || !strings.Contains(got, "ent_03") {
		t.Errorf("missing duplicate IDs, got:\n%s", got)
	}
	// Unlinked memory.
	if !strings.Contains(got, "## Memories with no entity links (1)") {
		t.Errorf("missing unlinked memories heading, got:\n%s", got)
	}
	if !strings.Contains(got, "loose memory") {
		t.Errorf("missing memory content, got:\n%s", got)
	}
	// Empty sections should NOT appear.
	if strings.Contains(got, "## Dead sources") {
		t.Errorf("empty section should be omitted, got:\n%s", got)
	}
	if strings.Contains(got, "## Entities without memories") {
		t.Errorf("empty section should be omitted, got:\n%s", got)
	}
}

func TestLint_RenderMarkdown_HealthyGraph(t *testing.T) {
	r := LintReport{EntityCount: 100, RelationshipCount: 50, MemoryCount: 200}
	got := renderLintMarkdown(r)

	if !strings.Contains(got, "Scanned 100 entities") {
		t.Errorf("missing summary: %s", got)
	}
	// No findings = no section headings at all (other than top-level).
	if strings.Contains(got, "## Orphan") || strings.Contains(got, "## Near-duplicate") ||
		strings.Contains(got, "## Memories") || strings.Contains(got, "## Dead") {
		t.Errorf("healthy graph should have no finding sections, got:\n%s", got)
	}
}

func TestLint_RenderMarkdown_LowConfidenceSkippedNote(t *testing.T) {
	// When LowConfidenceMemories is nil (default — option not set),
	// a "skipped" note appears.
	r := LintReport{EntityCount: 10, RelationshipCount: 5, MemoryCount: 7}
	got := renderLintMarkdown(r)
	if !strings.Contains(got, "Low-confidence memories (skipped") {
		t.Errorf("expected skipped note, got:\n%s", got)
	}
}

func TestLint_RenderMarkdown_LowConfidenceWithFindings(t *testing.T) {
	r := LintReport{
		EntityCount:       10,
		RelationshipCount: 5,
		MemoryCount:       7,
		LowConfidenceMemories: []MemoryRef{
			{ID: "mem_lc", Content: "shaky claim", Source: "slack", Confidence: 0.2},
		},
	}
	got := renderLintMarkdown(r)
	if !strings.Contains(got, "## Low-confidence memories (1)") {
		t.Errorf("missing low-confidence section, got:\n%s", got)
	}
	if !strings.Contains(got, "mem_lc") {
		t.Errorf("missing memory id, got:\n%s", got)
	}
	if strings.Contains(got, "skipped") {
		t.Errorf("should not show skipped note when section populated, got:\n%s", got)
	}
}
```

The "skipped" vs "section populated" distinction depends on whether `LowConfidenceMemories` is nil vs empty/populated. In Go, an unset field on a struct literal is the nil slice; after the lint runs without `WithLowConfidence`, the slice is also nil (never assigned). After lint runs WITH the option but finding zero items, it's a non-nil empty slice. The renderer can use `r.LowConfidenceMemories == nil` to detect "skipped." This is a subtle but useful distinction.

### Step 2: Run tests to verify they fail

Run: `go test -run "TestLint_RenderMarkdown" -v .`
Expected: compile error — `renderLintMarkdown` undefined.

### Step 3: Implement `lint_render.go`

Create `lint_render.go`:

```go
package cortex

import (
	"fmt"
	"strings"
)

// renderLintMarkdown formats a LintReport as a human-readable markdown
// document. Empty finding sections are omitted entirely. If
// LowConfidenceMemories is nil (i.e. the option was not passed), a
// "skipped" note appears in its place.
func renderLintMarkdown(r LintReport) string {
	var b strings.Builder
	b.WriteString("# Cortex Lint Report\n\n")
	fmt.Fprintf(&b, "Scanned %d entities, %d relationships, %d memories.\n",
		r.EntityCount, r.RelationshipCount, r.MemoryCount)

	if len(r.Orphans) > 0 {
		fmt.Fprintf(&b, "\n## Orphan entities (%d)\n\n", len(r.Orphans))
		b.WriteString("Entities with no relationships and no memory links — likely noise.\n\n")
		for _, e := range r.Orphans {
			fmt.Fprintf(&b, "- `%s` — %q (%s)\n", e.ID, e.Name, e.Type)
		}
	}

	if len(r.EntitiesNoMemories) > 0 {
		fmt.Fprintf(&b, "\n## Entities without memories (%d)\n\n", len(r.EntitiesNoMemories))
		b.WriteString("Entities with relationships but no memory links.\n\n")
		for _, e := range r.EntitiesNoMemories {
			fmt.Fprintf(&b, "- `%s` — %q (%s)\n", e.ID, e.Name, e.Type)
		}
	}

	if len(r.NearDuplicates) > 0 {
		word := "pairs"
		if len(r.NearDuplicates) == 1 {
			word = "pair"
		}
		fmt.Fprintf(&b, "\n## Near-duplicate entity names (%d %s)\n\n", len(r.NearDuplicates), word)
		b.WriteString("Same type + case-insensitively-equal name. Consider `cortex merge`.\n\n")
		for _, p := range r.NearDuplicates {
			fmt.Fprintf(&b, "- %q / %q (%s): `%s` / `%s`\n",
				p.A.Name, p.B.Name, p.Type, p.A.ID, p.B.ID)
		}
	}

	if len(r.DeadSources) > 0 {
		fmt.Fprintf(&b, "\n## Dead sources (%d)\n\n", len(r.DeadSources))
		b.WriteString("Source values present on memory rows but no live entity carries them.\n\n")
		for _, s := range r.DeadSources {
			fmt.Fprintf(&b, "- `%s`\n", s)
		}
	}

	if len(r.UnlinkedMemories) > 0 {
		fmt.Fprintf(&b, "\n## Memories with no entity links (%d)\n\n", len(r.UnlinkedMemories))
		b.WriteString("These memories are findable via search but not via graph traversal.\n\n")
		for _, m := range r.UnlinkedMemories {
			if m.Source != "" {
				fmt.Fprintf(&b, "- `%s` — %q (source: %s)\n", m.ID, m.Content, m.Source)
			} else {
				fmt.Fprintf(&b, "- `%s` — %q\n", m.ID, m.Content)
			}
		}
	}

	if r.LowConfidenceMemories == nil {
		b.WriteString("\n## Low-confidence memories (skipped — pass --low-confidence to include)\n")
	} else if len(r.LowConfidenceMemories) > 0 {
		fmt.Fprintf(&b, "\n## Low-confidence memories (%d)\n\n", len(r.LowConfidenceMemories))
		b.WriteString("Memories the LLM was uncertain about. Worth reviewing.\n\n")
		for _, m := range r.LowConfidenceMemories {
			fmt.Fprintf(&b, "- `%s` (conf %.0f%%) — %q\n", m.ID, m.Confidence*100, m.Content)
		}
	}
	// If LowConfidenceMemories is a non-nil empty slice (option was passed,
	// no findings), section is silently omitted — same as other clean sections.

	return b.String()
}
```

### Step 4: Run tests

Run: `go test -run TestLint -v -count=1 .`
Expected: all 16 tests PASS (12 from Task 1 + 4 new render tests).

### Step 5: Commit

```bash
git add lint_render.go lint_test.go
git commit -m "$(cat <<'EOF'
feat(cortex): renderLintMarkdown — format LintReport as markdown

Empty finding sections are omitted entirely. LowConfidenceMemories
distinguishes "skipped" (nil slice — option not passed) from
"no findings" (non-nil empty slice).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: CLI command + flag parsing

**Files:**
- Create: `cmd/cortex/lint.go`
- Create: `cmd/cortex/lint_test.go`
- Modify: `cmd/cortex/main.go`

### Step 1: Write failing CLI flag-parsing tests

Create `cmd/cortex/lint_test.go`:

```go
package main

import "testing"

func TestParseLintArgs_Defaults(t *testing.T) {
	opts, err := parseLintArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.LowConfidence {
		t.Error("LowConfidence should default to false")
	}
	if opts.LowConfidenceThreshold != 0 {
		t.Errorf("LowConfidenceThreshold default = %v, want 0 (cortex applies internal default)",
			opts.LowConfidenceThreshold)
	}
	if opts.OutPath != "" {
		t.Errorf("OutPath default = %q, want empty", opts.OutPath)
	}
}

func TestParseLintArgs_LowConfidence(t *testing.T) {
	opts, err := parseLintArgs([]string{"--low-confidence"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.LowConfidence {
		t.Error("LowConfidence should be true")
	}
}

func TestParseLintArgs_ThresholdImpliesEnable(t *testing.T) {
	opts, err := parseLintArgs([]string{"--low-confidence-threshold", "0.5"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.LowConfidence {
		t.Error("--low-confidence-threshold should imply --low-confidence")
	}
	if opts.LowConfidenceThreshold != 0.5 {
		t.Errorf("LowConfidenceThreshold = %v, want 0.5", opts.LowConfidenceThreshold)
	}
}

func TestParseLintArgs_OutPath(t *testing.T) {
	opts, err := parseLintArgs([]string{"--out", "/tmp/lint.md"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.OutPath != "/tmp/lint.md" {
		t.Errorf("OutPath = %q, want /tmp/lint.md", opts.OutPath)
	}
}

func TestParseLintArgs_UnknownFlag(t *testing.T) {
	_, err := parseLintArgs([]string{"--bogus"})
	if err == nil {
		t.Error("expected error for unknown flag")
	}
}

func TestParseLintArgs_ThresholdRequiresValue(t *testing.T) {
	_, err := parseLintArgs([]string{"--low-confidence-threshold"})
	if err == nil {
		t.Error("expected error for missing value")
	}
}

func TestParseLintArgs_InvalidThreshold(t *testing.T) {
	_, err := parseLintArgs([]string{"--low-confidence-threshold", "notanumber"})
	if err == nil {
		t.Error("expected error for non-numeric threshold")
	}
}
```

### Step 2: Run failing tests

Run: `go test -run TestParseLintArgs -v ./cmd/cortex/...`
Expected: build error — `parseLintArgs` undefined.

### Step 3: Implement `cmd/cortex/lint.go`

Create:

```go
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/sausheong/cortex"
)

type lintOptions struct {
	LowConfidence          bool
	LowConfidenceThreshold float64 // 0 = use cortex default
	OutPath                string  // "" = stdout
}

func parseLintArgs(args []string) (lintOptions, error) {
	var opts lintOptions
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--low-confidence":
			opts.LowConfidence = true
		case "--low-confidence-threshold":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--low-confidence-threshold requires a value")
			}
			v, err := strconv.ParseFloat(args[i+1], 64)
			if err != nil {
				return opts, fmt.Errorf("invalid --low-confidence-threshold: %w", err)
			}
			opts.LowConfidenceThreshold = v
			opts.LowConfidence = true // implies enable
			i++
		case "--out":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--out requires a path argument")
			}
			opts.OutPath = args[i+1]
			i++
		default:
			return opts, fmt.Errorf("unknown flag: %s", args[i])
		}
	}
	return opts, nil
}

func cmdLint() {
	opts, err := parseLintArgs(os.Args[2:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		fmt.Fprintln(os.Stderr, "usage: cortex lint [--low-confidence] [--low-confidence-threshold <0-1>] [--out <file>]")
		os.Exit(1)
	}

	cx := openCortex()
	defer cx.Close()
	ctx := context.Background()

	var lintOpts []cortex.LintOption
	if opts.LowConfidenceThreshold > 0 {
		lintOpts = append(lintOpts, cortex.WithLowConfidenceThreshold(opts.LowConfidenceThreshold))
	} else if opts.LowConfidence {
		lintOpts = append(lintOpts, cortex.WithLowConfidence())
	}

	report, err := cx.Lint(ctx, lintOpts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	markdown := cortex.RenderLintMarkdown(report)

	if opts.OutPath != "" {
		if err := os.WriteFile(opts.OutPath, []byte(markdown), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "error: write %s: %v\n", opts.OutPath, err)
			os.Exit(1)
		}
		fmt.Printf("Wrote %s\n", opts.OutPath)
	} else {
		fmt.Print(markdown)
	}
}
```

The CLI references `cortex.RenderLintMarkdown` (exported) — but Task 2 created `renderLintMarkdown` (unexported). The CLI lives in package `main` so it needs an exported wrapper. Add this to `lint_render.go` (in the `cortex` package):

```go
// RenderLintMarkdown formats a LintReport as a human-readable markdown
// document. Exported wrapper for CLI consumption.
func RenderLintMarkdown(r LintReport) string {
	return renderLintMarkdown(r)
}
```

(The render tests in `lint_test.go` call `renderLintMarkdown` directly because they're in the same package. The CLI calls the exported wrapper.)

### Step 4: Wire `cmdLint` into the command switch

In `cmd/cortex/main.go`, find the existing switch (alongside `init`, `remember`, `recall`, `sync`, `entity`, `forget`, `config`, `export`, `init-schema`, `merge`). Add before `default:`:

```go
	case "lint":
		cmdLint()
```

### Step 5: Update `printUsage`

Add a line:

```
  lint [--low-confidence] [--low-confidence-threshold <0-1>] [--out <file>]
                                 Scan the graph for cleanup candidates (orphans, near-duplicates, etc.)
```

Match the indentation of nearby entries.

### Step 6: Run tests + build

Run: `go test -run "TestParseLintArgs|TestLint" -v -count=1 ./...`
Expected: all 16 + 7 tests PASS.

Run: `go build ./...`
Expected: clean build.

Smoke test (optional):

```bash
go build -o /tmp/cortex-test ./cmd/cortex
echo -e "Test\n\n" | /tmp/cortex-test --db /tmp/lint-smoke.db init
/tmp/cortex-test --db /tmp/lint-smoke.db lint
# Expected: a small markdown report, "Scanned 1 entities, 0 relationships, 0 memories." plus possibly an Orphan section for the owner entity.
rm /tmp/lint-smoke.db /tmp/cortex-test
```

### Step 7: Commit

```bash
git add cmd/cortex/lint.go cmd/cortex/lint_test.go cmd/cortex/main.go lint_render.go
git commit -m "$(cat <<'EOF'
feat(cli): add cortex lint command

CLI takes optional --low-confidence, --low-confidence-threshold,
--out flags. Prints markdown report to stdout (or --out file).
Threshold flag implies enable. RenderLintMarkdown is the exported
wrapper consumed by the CLI; internal renderer stays unexported.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Documentation

**Files:**
- Modify: `README.md`
- Modify: `docs/CORTEX.md`
- Modify: `cmd/cortex/CORTEX.md.template`

### Step 1: Add `### cortex lint` to README

Find the existing `### cortex merge` subsection in README.md's CLI Reference section. Add the new `### cortex lint` subsection nearby (place it after `cortex merge` since they pair naturally — lint surfaces what to merge):

```markdown
### `cortex lint`

Scan the graph for cleanup candidates and print a markdown report.

```bash
cortex lint [--low-confidence] [--low-confidence-threshold <0-1>] [--out <file>]
```

Pure read operation — never modifies the graph. Six checks run on every invocation:

- **Orphan entities** — no relationships, no memory links. Often extraction noise.
- **Entities without memories** — have relationships but nothing said about them.
- **Near-duplicate entity names** — same type + case-insensitively-equal name. Direct candidates for `cortex merge`.
- **Dead sources** — source strings on memory rows with no matching live entity.
- **Unlinked memories** — memories not joined to any entity; findable via search but invisible to graph traversal.
- **Low-confidence memories** — opt-in via `--low-confidence`. Default threshold 0.3; override with `--low-confidence-threshold <n>` (which also enables the section).

Output is markdown by default — empty sections are omitted so a healthy graph shows just the count summary. Use `--out <path>` to write the report to a file.

Lint is informational. Even when findings are present, exit code is 0.
```

### Step 2: Add a paragraph to `docs/CORTEX.md`

Add to the Workflow loop section (the "Lint" step in the loop is already mentioned at a conceptual level). Find a sensible spot — likely after the "Cleaning up duplicates" section added by the merge feature, since lint surfaces candidates for merge:

```markdown
### Linting the graph

`cortex lint` runs a fast read-only scan and prints a markdown report of cleanup candidates: orphan entities, near-duplicate names, unlinked memories, and more. No LLM required. Run it periodically — weekly is a reasonable cadence for an actively-used brain.

```
cortex lint                       # full report to stdout
cortex lint --out lint-report.md  # write to file
cortex lint --low-confidence      # also surface memories the extractor was unsure about
```

The report's near-duplicate pairs are direct inputs to `cortex merge`. Other findings (orphans, unlinked memories) usually mean `cortex forget` or just leaving them alone.
```

### Step 3: Mirror to the embedded template

```bash
head -5 cmd/cortex/CORTEX.md.template > /tmp/header.md
cat /tmp/header.md > cmd/cortex/CORTEX.md.template
echo "" >> cmd/cortex/CORTEX.md.template
cat docs/CORTEX.md >> cmd/cortex/CORTEX.md.template
rm /tmp/header.md
```

Verify with `diff <(tail -n +7 cmd/cortex/CORTEX.md.template) docs/CORTEX.md` (should be empty; adjust `+N` if the header isn't 5 lines + 1 blank).

### Step 4: Verify

Run: `go test ./...`
Expected: hermetic tests still pass.

### Step 5: Commit

```bash
git add README.md docs/CORTEX.md cmd/cortex/CORTEX.md.template
git commit -m "$(cat <<'EOF'
docs: document cortex lint command

README gains a `### cortex lint` subsection.
CORTEX.md template gains a "Linting the graph" paragraph alongside
the existing "Cleaning up duplicates" section.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Done

After Task 4, callers can:

```go
report, _ := cx.Lint(ctx)
md := cortex.RenderLintMarkdown(report)
// or:
report, _ := cx.Lint(ctx, cortex.WithLowConfidence())
```

And via CLI:

```bash
cortex lint
cortex lint --low-confidence --out lint.md
cortex lint --low-confidence-threshold 0.5
```
