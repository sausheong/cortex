# Tier 4 Phase 4a: Memory Edges (supersedes) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give cortex a **facts-on-facts** layer — typed edges *between memories* (research gap G5). Today relationships connect entities only; a memory that supersedes another leaves no queryable trace beyond a flipped `invalid_at`. This phase adds a `memory_edges` table and makes reconciliation, when it applies a supersession, also record an explicit `supersedes` edge (the newer memory → the stale one). The supersession chain becomes auditable and traversable, and the table is the substrate Phase 4b (`derives`/`extends`) and Phase 4d (the `maintain` pass) build on.

**Architecture:** Five tasks. (1) A `memory_edges(id, source_id, target_id, type, source, created_at)` table + indexes, added idempotently in `Open`. (2) Core types `MemoryEdge` + an edge-type vocabulary constant. (3) `PutMemoryEdge` (insert, dedup on `(source_id, target_id, type)`) and read helpers `GetMemoryEdges`/`GetMemoryEdgesByType`. (4) `ApplyReconcile`/`ApplyReconcileReport` record a `supersedes` edge whenever they invalidate a stale memory. (5) `Forget` cleans up edges referencing deleted memories (FK-cascade-style consistency), and the orphan-memory deletion path too.

**Tech Stack:** Go 1.25.1, `modernc.org/sqlite` (pure-Go, no CGo), SQLite. Tests use in-package helpers `openTestDB`, `mockLLM`, `mockExtractor`.

## Global Constraints

- **Module path:** `github.com/sausheong/cortex` (root package is `cortex`).
- **No CGo, single binary.** Only existing dependencies.
- **Backward compatibility:** existing databases must keep working. The `memory_edges` table is created with `CREATE TABLE IF NOT EXISTS` inside `schemaSQL` (and so is auto-created on the next `Open` of any existing DB). No `DROP`, no destructive migration. A DB with no edges behaves exactly as today.
- **Edge direction convention (load-bearing, document it):** an edge `(source_id, target_id, type)` reads **"source_id <type> target_id"**. For supersession: the **newer** memory `supersedes` the **stale** one → `source_id = superseding (newer) memory`, `target_id = stale memory`, `type = "supersedes"`. Phase 4b will follow the same subject-verb-object direction for `derives`/`extends`.
- **Soft-invalidation unchanged:** recording a `supersedes` edge is **additive** to the existing `InvalidateMemory` call — it does not replace soft-invalidation. The stale memory is still soft-invalidated (hidden from default recall, reachable via `as_of`/`include_invalid`); the edge is the durable record of *why/by what*. Never hard-delete a memory here.
- **Edge integrity:** an edge references two memory ids. When a memory is hard-deleted (`Forget`, orphan cleanup), edges referencing it (as source or target) must be deleted too, so no dangling edges accumulate. Use a real FK with `ON DELETE CASCADE` where the deletion goes through SQL that respects FKs (`PRAGMA foreign_keys=ON` is already set, `store.go:21`), AND verify the existing delete paths actually trigger it; add explicit edge-deletion where they don't.
- **Idempotent edge writes:** `PutMemoryEdge` must not create duplicate edges for the same `(source_id, target_id, type)` — a reconcile re-run that re-applies the same supersession must not stack edges. Use a UNIQUE constraint + `INSERT ... ON CONFLICT DO NOTHING` (or pre-check).
- **Confidence invariant, bi-temporal invariants:** unchanged.
- **Commit trailer:** do NOT add a `Co-Authored-By` trailer to commits (user preference for this repo).
- **Test command:** `go test ./...` must pass. Hermetic only. (`llm/openai` live tests fail on HTTP 429 quota — pre-existing, out of scope.)
- **TDD:** write the failing test first, watch it fail, implement minimally, watch it pass, commit.

---

## File Structure

| File | Responsibility | Change |
|------|----------------|--------|
| `store.go` | schema | Add `memory_edges` table + indexes to `schemaSQL` |
| `types.go` | core types | `MemoryEdge` struct; edge-type constants |
| `memory_edge.go` | edge CRUD (new file) | `PutMemoryEdge`, `GetMemoryEdges`, `GetMemoryEdgesByType` |
| `reconcile.go` | apply paths | `ApplyReconcile` + `ApplyReconcileReport` record `supersedes` edges |
| `cortex.go` | forget/orphan cleanup | delete edges referencing deleted memories |
| `*_test.go` | tests | one test file per task |

---

### Task 1: memory_edges schema

**Files:**
- Modify: `store.go` (`schemaSQL`)
- Test: `store_test.go`

**Interfaces:**
- Produces: a `memory_edges` table with a UNIQUE `(source_id, target_id, type)` constraint, FK to `memories(id) ON DELETE CASCADE` on both ends, and indexes on `source_id` and `target_id`. Auto-created on `Open` for new and existing DBs.

- [ ] **Step 1: Write the failing test**

Add to `store_test.go`:

```go
func TestSchema_MemoryEdgesTableExists(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	// The table must exist and accept a row referencing two memories.
	m1 := &Memory{Content: "newer fact"}
	m2 := &Memory{Content: "older fact"}
	if err := c.PutMemory(ctx, m1); err != nil {
		t.Fatal(err)
	}
	if err := c.PutMemory(ctx, m2); err != nil {
		t.Fatal(err)
	}

	_, err := c.db.ExecContext(ctx,
		`INSERT INTO memory_edges (id, source_id, target_id, type, created_at)
		 VALUES ('e1', ?, ?, 'supersedes', datetime('now'))`,
		m1.ID, m2.ID,
	)
	if err != nil {
		t.Fatalf("insert memory edge: %v", err)
	}

	var n int
	if err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_edges`).Scan(&n); err != nil {
		t.Fatalf("count edges: %v", err)
	}
	if n != 1 {
		t.Fatalf("expected 1 edge, got %d", n)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestSchema_MemoryEdgesTableExists -v`
Expected: FAIL — `no such table: memory_edges`.

- [ ] **Step 3: Add the table to schemaSQL**

In `store.go`, add to `schemaSQL` after the `memory_entities` table (around `store.go:83`):

```sql
CREATE TABLE IF NOT EXISTS memory_edges (
	id         TEXT PRIMARY KEY,
	source_id  TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
	target_id  TEXT NOT NULL REFERENCES memories(id) ON DELETE CASCADE,
	type       TEXT NOT NULL,
	source     TEXT,
	created_at DATETIME NOT NULL DEFAULT (datetime('now')),
	UNIQUE (source_id, target_id, type)
);

CREATE INDEX IF NOT EXISTS idx_memory_edges_source ON memory_edges(source_id);
CREATE INDEX IF NOT EXISTS idx_memory_edges_target ON memory_edges(target_id);
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ -run TestSchema_MemoryEdgesTableExists -v`
Expected: PASS.

- [ ] **Step 5: Run the full suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add store.go store_test.go
git commit -m "feat: add memory_edges table for memory-to-memory relationships"
```

---

### Task 2: MemoryEdge type and edge-type vocabulary

**Files:**
- Modify: `types.go`
- Test: covered by Task 3's CRUD tests (this task is type definitions only; no standalone test needed — but a trivial compile-time check is fine).

**Interfaces:**
- Produces: `MemoryEdge` struct mirroring the table; exported edge-type constants `EdgeSupersedes`, `EdgeDerives`, `EdgeExtends` (the last two are used by Phase 4b but define the vocabulary here so it's centralized).

- [ ] **Step 1: Add the type and constants**

In `types.go`, near the other core types (after `Memory`, around `types.go:54`):

```go
// MemoryEdge is a typed directed edge between two memories (facts-on-facts).
// It reads "SourceID <Type> TargetID" — e.g. a supersedes edge has SourceID =
// the newer memory and TargetID = the stale one it replaces. Edges are
// additive metadata: they record relationships discovered during
// reconciliation/maintenance and never change the memories themselves.
type MemoryEdge struct {
	ID        string    `json:"id"`
	SourceID  string    `json:"source_id"`
	TargetID  string    `json:"target_id"`
	Type      string    `json:"type"`
	Source    string    `json:"source,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Memory edge types. SourceID <type> TargetID:
//   - EdgeSupersedes: the newer memory replaces/contradicts the older (target).
//   - EdgeDerives:    the source memory is derived/inferred from the target.
//   - EdgeExtends:    the source memory adds detail to the target without
//     contradicting it.
const (
	EdgeSupersedes = "supersedes"
	EdgeDerives    = "derives"
	EdgeExtends    = "extends"
)
```

(`types.go` already imports `time`.)

- [ ] **Step 2: Build to verify it compiles**

Run: `go build ./...`
Expected: success.

- [ ] **Step 3: Commit**

```bash
git add types.go
git commit -m "feat: MemoryEdge type and edge-type vocabulary"
```

---

### Task 3: PutMemoryEdge and read helpers

**Files:**
- Create: `memory_edge.go`
- Test: `memory_edge_test.go`

**Interfaces:**
- Consumes: the `memory_edges` table (Task 1), `MemoryEdge` (Task 2), `newID()`.
- Produces:
  - `PutMemoryEdge(ctx, e *MemoryEdge) error` — sets `e.ID` (if empty) and `e.CreatedAt`, inserts with `ON CONFLICT (source_id, target_id, type) DO NOTHING` (idempotent). When the conflict path no-ops, the passed struct still carries the generated ID (acceptable; the row that exists may have a different id — document that the return is best-effort identity).
  - `GetMemoryEdges(ctx, memoryID) ([]MemoryEdge, error)` — all edges where the memory is source OR target (its full edge neighborhood).
  - `GetMemoryEdgesByType(ctx, memoryID, edgeType) ([]MemoryEdge, error)` — filtered by type, memory as source OR target.

- [ ] **Step 1: Write the failing test**

Add to `memory_edge_test.go` (new file):

```go
package cortex

import (
	"context"
	"testing"
)

func TestPutAndGetMemoryEdge(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	newer := &Memory{Content: "budget is 10000"}
	older := &Memory{Content: "budget is 5000"}
	if err := c.PutMemory(ctx, newer); err != nil {
		t.Fatal(err)
	}
	if err := c.PutMemory(ctx, older); err != nil {
		t.Fatal(err)
	}

	e := &MemoryEdge{SourceID: newer.ID, TargetID: older.ID, Type: EdgeSupersedes}
	if err := c.PutMemoryEdge(ctx, e); err != nil {
		t.Fatalf("PutMemoryEdge: %v", err)
	}
	if e.ID == "" {
		t.Fatal("expected edge ID to be set")
	}

	// Idempotent: re-inserting the same (source,target,type) does not error or duplicate.
	dup := &MemoryEdge{SourceID: newer.ID, TargetID: older.ID, Type: EdgeSupersedes}
	if err := c.PutMemoryEdge(ctx, dup); err != nil {
		t.Fatalf("PutMemoryEdge (dup): %v", err)
	}

	edges, err := c.GetMemoryEdges(ctx, newer.ID)
	if err != nil {
		t.Fatalf("GetMemoryEdges: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge (idempotent), got %d", len(edges))
	}
	if edges[0].SourceID != newer.ID || edges[0].TargetID != older.ID || edges[0].Type != EdgeSupersedes {
		t.Fatalf("edge mismatch: %+v", edges[0])
	}

	// The older memory sees the same edge (as target).
	asTarget, err := c.GetMemoryEdges(ctx, older.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(asTarget) != 1 {
		t.Fatalf("expected older memory to see 1 edge as target, got %d", len(asTarget))
	}

	// Type filter.
	byType, err := c.GetMemoryEdgesByType(ctx, newer.ID, EdgeSupersedes)
	if err != nil {
		t.Fatal(err)
	}
	if len(byType) != 1 {
		t.Fatalf("expected 1 supersedes edge, got %d", len(byType))
	}
	none, err := c.GetMemoryEdgesByType(ctx, newer.ID, EdgeDerives)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Fatalf("expected 0 derives edges, got %d", len(none))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestPutAndGetMemoryEdge -v`
Expected: FAIL — `PutMemoryEdge`/`GetMemoryEdges`/`GetMemoryEdgesByType` undefined.

- [ ] **Step 3: Implement the CRUD**

Create `memory_edge.go`:

```go
package cortex

import (
	"context"
	"fmt"
	"time"
)

// PutMemoryEdge inserts a typed memory-to-memory edge. It is idempotent on
// (source_id, target_id, type): re-inserting the same edge is a no-op (no
// error, no duplicate). The passed struct's ID (generated if empty) and
// CreatedAt are set; on a conflict no-op the row already in the table is kept
// and its id may differ from e.ID.
func (c *Cortex) PutMemoryEdge(ctx context.Context, e *MemoryEdge) error {
	if e.ID == "" {
		e.ID = newID()
	}
	e.CreatedAt = time.Now().UTC()

	var source any
	if e.Source != "" {
		source = e.Source
	}
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO memory_edges (id, source_id, target_id, type, source, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT (source_id, target_id, type) DO NOTHING`,
		e.ID, e.SourceID, e.TargetID, e.Type, source, e.CreatedAt,
	)
	if err != nil {
		return fmt.Errorf("cortex: insert memory edge: %w", err)
	}
	return nil
}

// GetMemoryEdges returns every edge where memoryID is the source or the
// target — the memory's full edge neighborhood.
func (c *Cortex) GetMemoryEdges(ctx context.Context, memoryID string) ([]MemoryEdge, error) {
	return c.queryMemoryEdges(ctx,
		`SELECT id, source_id, target_id, type, source, created_at
		 FROM memory_edges
		 WHERE source_id = ? OR target_id = ?
		 ORDER BY created_at`,
		memoryID, memoryID,
	)
}

// GetMemoryEdgesByType returns edges of the given type where memoryID is the
// source or the target.
func (c *Cortex) GetMemoryEdgesByType(ctx context.Context, memoryID, edgeType string) ([]MemoryEdge, error) {
	return c.queryMemoryEdges(ctx,
		`SELECT id, source_id, target_id, type, source, created_at
		 FROM memory_edges
		 WHERE (source_id = ? OR target_id = ?) AND type = ?
		 ORDER BY created_at`,
		memoryID, memoryID, edgeType,
	)
}

func (c *Cortex) queryMemoryEdges(ctx context.Context, query string, args ...any) ([]MemoryEdge, error) {
	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("cortex: query memory edges: %w", err)
	}
	defer rows.Close()

	var edges []MemoryEdge
	for rows.Next() {
		var e MemoryEdge
		var source sqlNullString
		if err := rows.Scan(&e.ID, &e.SourceID, &e.TargetID, &e.Type, &source, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("cortex: scan memory edge: %w", err)
		}
		if source.Valid {
			e.Source = source.String
		}
		edges = append(edges, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cortex: iterate memory edges: %w", err)
	}
	return edges, nil
}
```

> **Import note:** use `database/sql`'s `sql.NullString` (the snippet's `sqlNullString` is a placeholder — import `database/sql` and use `sql.NullString`). Match the existing convention in `memory.go`/`search.go`.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ -run TestPutAndGetMemoryEdge -v`
Expected: PASS.

- [ ] **Step 5: Run the full suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add memory_edge.go memory_edge_test.go
git commit -m "feat: PutMemoryEdge and edge read helpers"
```

---

### Task 4: Reconcile apply records supersedes edges

**Files:**
- Modify: `reconcile.go` (`ApplyReconcile`, `ApplyReconcileReport`)
- Test: `reconcile_test.go`

**Interfaces:**
- Consumes: `PutMemoryEdge` (Task 3); the existing apply loops.
- Produces: whenever an apply path soft-invalidates a stale memory, it also records a `supersedes` edge: `source_id = SupersededByID (newer)`, `target_id = StaleID`. Edge write is idempotent, so re-applying is safe. The edge write happens **after** a successful `InvalidateMemory` (so an edge is only recorded for an actually-applied supersession). An edge-write error is wrapped and returned (consistent with the invalidate error handling), but since edge writes are idempotent and additive, consider whether a failed edge write should roll back — for this phase, treat an edge-write failure as a hard error returned to the caller (the supersession is already applied; surfacing the inconsistency is better than hiding it). Document this.

- [ ] **Step 1: Write the failing test**

Add to `reconcile_test.go`:

```go
func TestApplyReconcile_RecordsSupersedesEdge(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	// Two contradicting memories on the same entity, newer supersedes older.
	e := &Entity{Name: "Project X", Type: "project"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	older := &Memory{Content: "Project X budget is 5000", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, older); err != nil {
		t.Fatal(err)
	}
	// Ensure the newer memory has a strictly-later created_at.
	newer := &Memory{Content: "Project X budget is 10000", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, newer); err != nil {
		t.Fatal(err)
	}
	// Backdate the older one so newer.CreatedAt is strictly after.
	if _, err := c.db.ExecContext(ctx,
		`UPDATE memories SET created_at = ? WHERE id = ?`,
		older.CreatedAt.Add(-1*time.Hour), older.ID); err != nil {
		t.Fatal(err)
	}

	c.SetLLM(&mockLLM{
		detectConflictsFn: func(_ context.Context, mems []Memory) ([]ConflictPair, error) {
			return []ConflictPair{{StaleID: older.ID, SupersededByID: newer.ID, Reason: "budget changed"}}, nil
		},
	})

	report, err := c.ApplyReconcile(ctx)
	if err != nil {
		t.Fatalf("ApplyReconcile: %v", err)
	}
	if len(report.Proposed) != 1 {
		t.Fatalf("expected 1 applied supersession, got %d", len(report.Proposed))
	}

	// A supersedes edge must exist: newer -> older.
	edges, err := c.GetMemoryEdgesByType(ctx, newer.ID, EdgeSupersedes)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 supersedes edge, got %d", len(edges))
	}
	if edges[0].SourceID != newer.ID || edges[0].TargetID != older.ID {
		t.Fatalf("edge direction wrong: source=%s target=%s (want source=newer %s, target=older %s)",
			edges[0].SourceID, edges[0].TargetID, newer.ID, older.ID)
	}
}
```

> Confirm `mockLLM` has `detectConflictsFn` (it does, per `recall_test.go:270`) and `"time"` is imported in `reconcile_test.go`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestApplyReconcile_RecordsSupersedesEdge -v`
Expected: FAIL — no edge is recorded (`expected 1 supersedes edge, got 0`).

- [ ] **Step 3: Record the edge in both apply paths**

In `reconcile.go`, `ApplyReconcile` loop (`reconcile.go:118-123`), after the successful `InvalidateMemory`, add the edge write:

```go
	for _, p := range report.Proposed {
		invalidAt := p.InvalidAt
		if err := c.InvalidateMemory(ctx, p.StaleID, &invalidAt); err != nil {
			return report, fmt.Errorf("cortex: apply supersession (stale %s): %w", p.StaleID, err)
		}
		edge := &MemoryEdge{SourceID: p.SupersededByID, TargetID: p.StaleID, Type: EdgeSupersedes}
		if err := c.PutMemoryEdge(ctx, edge); err != nil {
			return report, fmt.Errorf("cortex: record supersedes edge (stale %s): %w", p.StaleID, err)
		}
	}
```

Apply the identical addition in `ApplyReconcileReport` (`reconcile.go:186-190`), after its `InvalidateMemory` and before appending to `out.Proposed`:

```go
		invalidAt := p.InvalidAt
		if err := c.InvalidateMemory(ctx, p.StaleID, &invalidAt); err != nil {
			return out, fmt.Errorf("cortex: apply reviewed supersession (stale %s): %w", p.StaleID, err)
		}
		edge := &MemoryEdge{SourceID: p.SupersededByID, TargetID: p.StaleID, Type: EdgeSupersedes}
		if err := c.PutMemoryEdge(ctx, edge); err != nil {
			return out, fmt.Errorf("cortex: record reviewed supersedes edge (stale %s): %w", p.StaleID, err)
		}
		out.Proposed = append(out.Proposed, p)
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ -run TestApplyReconcile_RecordsSupersedesEdge -v`
Expected: PASS.

- [ ] **Step 5: Run the full suite**

Run: `go test ./...`
Expected: PASS. (Existing reconcile apply tests don't assert edge absence, so they're unaffected; the edge is additive.)

- [ ] **Step 6: Commit**

```bash
git add reconcile.go reconcile_test.go
git commit -m "feat: record supersedes edge when reconcile applies a supersession"
```

---

### Task 5: Edge cleanup on memory deletion

**Files:**
- Modify: `cortex.go` (`deleteOrphanedMemories`, and verify `forgetByEntity`/`forgetBySource` paths)
- Test: `cortex_test.go`

**Interfaces:**
- Consumes: the `memory_edges` FK `ON DELETE CASCADE`.
- Produces: no dangling edges after a memory is hard-deleted. The FK cascade *should* handle this automatically (`PRAGMA foreign_keys=ON` is set), BUT: `modernc.org/sqlite` honors `ON DELETE CASCADE` only when the foreign_keys pragma is active on the connection. Verify cascade actually fires through the existing delete paths; if it does, this task is a **verification test** plus possibly nothing else. If it does NOT (e.g. pragma not applied per-connection), add explicit `DELETE FROM memory_edges WHERE source_id = ? OR target_id = ?` in the orphan-deletion loop.

> **Investigate first:** the schema sets `PRAGMA foreign_keys=ON` in `schemaSQL` (`store.go:21`), but with `database/sql` connection pooling the pragma may not be active on every pooled connection. The existing `memory_entities` table relies on `ON DELETE CASCADE` too — check whether existing Forget tests prove cascade works (e.g. do memory_entities rows vanish when a memory is deleted?). If cascade is proven to work for memory_entities, it works for memory_edges identically and this task is just a confirming test. If not, the existing code already has a latent bug and you should add explicit edge deletion (and note the memory_entities parallel as a pre-existing concern, out of scope to fix here).

- [ ] **Step 1: Write the failing/confirming test**

Add to `cortex_test.go`:

```go
func TestForget_DeletesMemoryEdges(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Name: "Acme", Type: "org"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	m1 := &Memory{Content: "Acme revenue 1M", EntityIDs: []string{e.ID}}
	m2 := &Memory{Content: "Acme revenue 2M", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, m1); err != nil {
		t.Fatal(err)
	}
	if err := c.PutMemory(ctx, m2); err != nil {
		t.Fatal(err)
	}
	if err := c.PutMemoryEdge(ctx, &MemoryEdge{SourceID: m2.ID, TargetID: m1.ID, Type: EdgeSupersedes}); err != nil {
		t.Fatal(err)
	}

	// Forget by entity deletes the entity, its memory links, and (when they
	// become orphaned) the memories — and must leave no dangling edges.
	if err := c.Forget(ctx, Filter{EntityID: e.ID}); err != nil {
		t.Fatalf("Forget: %v", err)
	}

	var n int
	if err := c.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM memory_edges`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("expected 0 edges after forget, got %d (dangling edges)", n)
	}
}
```

- [ ] **Step 2: Run test to verify it fails or passes**

Run: `go test ./ -run TestForget_DeletesMemoryEdges -v`
Expected: If cascade works → PASS already (then this task is the confirming test; skip to Step 5). If it FAILS (`got 1` or more) → cascade isn't firing; proceed to Step 3.

- [ ] **Step 3: (Only if Step 2 failed) Add explicit edge deletion**

In `cortex.go`, `deleteOrphanedMemories` (`cortex.go:312-328`), in the per-orphan delete loop, before `DELETE FROM memories WHERE id = ?`, add:

```go
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM memory_edges WHERE source_id = ? OR target_id = ?`, id, id,
		); err != nil {
			return fmt.Errorf("cortex: delete orphaned memory edges: %w", err)
		}
```

(Placed before the memory row delete, like the existing FTS cleanup.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ -run TestForget_DeletesMemoryEdges -v`
Expected: PASS.

- [ ] **Step 5: Run the full suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add cortex.go cortex_test.go
git commit -m "feat: clean up memory edges when memories are deleted"
```

(If Step 2 passed and no code change was needed, commit just the confirming test: `git add cortex_test.go` with message `test: confirm memory edges cascade-delete with memories`.)

---

## Self-Review

**Spec coverage** (Phase 4a = the `memory_edges` foundation for G5):
- Schema (Task 1), type+vocabulary (Task 2), CRUD (Task 3), supersedes-on-apply (Task 4), deletion integrity (Task 5). ✅

**Placeholder scan:** Task 3 Step 3 flags `sqlNullString` as a placeholder for `sql.NullString` with an explicit import note. Every other step is complete code. ✅

**Type / linkage consistency:**
- `MemoryEdge` (Task 2) used by `PutMemoryEdge`/getters (Task 3), apply paths (Task 4), and the cleanup test (Task 5). ✅
- Edge direction convention (`source supersedes target`, newer→stale) is fixed in the constraints and asserted in Task 4's test. ✅
- Idempotency: UNIQUE constraint (Task 1) + `ON CONFLICT DO NOTHING` (Task 3) → re-apply safe (Task 4). ✅

**Backward compatibility:**
- `CREATE TABLE IF NOT EXISTS` → auto-created on next `Open` for existing DBs; no migration, no DROP. ✅
- Edges are additive — a DB with none behaves exactly as before; reconcile still soft-invalidates as today, now *plus* an edge. ✅

**Known scope edges (documented, not gaps):**
- Edges are not yet surfaced in recall, traverse, vault export, or the CLI/MCP — Phase 4a builds the substrate; surfacing is Phase 4d (`maintain` report) and a possible recall/traverse follow-up.
- `derives`/`extends` constants are defined here but only *written* in Phase 4b.
- If FK cascade proves unreliable for memory_edges, the same latent risk exists for `memory_entities` (pre-existing) — noted, not fixed here.

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-06-23-tier4a-memory-edges.md`.**

This is Phase 4a of Tier 4. After it merges: Phase 4b (LLM-detected `derives`/`extends` edges), Phase 4c (confidence decay + auto-prune), Phase 4d (`cortex maintain` orchestration + cron docs). Execute subagent-driven, then write Phase 4b.
