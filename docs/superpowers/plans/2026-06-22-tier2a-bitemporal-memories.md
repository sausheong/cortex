# Tier 2a: Bi-Temporal Memories Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give memories a bi-temporal model — event-time validity (`valid_at`/`invalid_at`) and ingestion-time tracking (`created_at` already exists; add `expired_at`) — plus a soft-invalidation primitive that retires a memory without deleting it, and make recall return only currently-valid memories by default while keeping history queryable.

**Architecture:** Pure-deterministic foundation, no LLM and no ingest-behavior change. Add four nullable temporal columns to `memories` via the existing `ensureColumn` migration pattern. Add an `InvalidateMemory` method that soft-retires a memory (sets `invalid_at`/`expired_at`, never deletes). Add a "currently valid" SQL predicate and thread it through all three memory-read paths (FTS search, vector search, by-entity) so recall hides retired/expired memories by default; add an opt-in `WithAsOf(t)` / `WithIncludeInvalid()` to query history. This is Phase 2a of Tier 2; Phase 2b (LLM contradiction detection + background reconciliation that *calls* `InvalidateMemory`) is a separate later plan.

**Tech Stack:** Go 1.25.1, `modernc.org/sqlite` (pure-Go, no CGo), SQLite. Tests use in-package helpers `openTestDB`, `openTestDBWithEmbedder`, `mockLLM`, `testEmbedder`.

## Global Constraints

- **Module:** `github.com/sausheong/cortex`, root package `cortex`. Go 1.25.1.
- **No CGo, single binary.** Only `modernc.org/sqlite`. No new external dependencies.
- **Backward compatibility is mandatory.** New columns added via `ensureColumn` (idempotent `ALTER TABLE ADD COLUMN`). All new temporal columns are **nullable** with no default. Never DROP. Existing rows (and rows written by the unchanged ingest path) have `valid_at = NULL`, `invalid_at = NULL`, `expired_at = NULL` and MUST be treated as "currently valid forever" so existing behavior is preserved.
- **No ingest behavior change in this phase.** `Remember`/`PutMemory` continue to write memories exactly as today (temporal columns left NULL). Conflict detection/auto-invalidation is Phase 2b, explicitly out of scope here.
- **`Forget` stays a hard delete.** Soft-invalidation (`InvalidateMemory`) is a separate, additive operation. Do not change `Forget` semantics.
- **Confidence invariant** `[0,1]`, `coerceConfidence` maps zero/NaN→1.0 — unchanged.
- **Time:** all timestamps stored UTC, matching existing code (`time.Now().UTC()`).
- **No `Co-Authored-By` trailer on commits.**
- **Tests:** `go test ./...` must pass (hermetic). The `llm/openai` live-API tests fail on HTTP 429 quota — pre-existing, out of scope; ignore those specific failures.
- **TDD:** failing test → confirm fail → minimal implementation → confirm pass → commit.

---

## Temporal Model (definitions every task depends on)

Four columns on `memories`, all `DATETIME NULL`:

- **`valid_at`** — event time the fact became true in the world. NULL = "true since always / unknown start". (Populated by Phase 2b extraction; NULL for all existing + ingest-written rows.)
- **`invalid_at`** — event time the fact stopped being true. NULL = "still true". Set by supersession in 2b.
- **`expired_at`** — ingestion/transaction time the system retired this memory (soft-delete marker). NULL = "live". Non-NULL = retired, hidden from default recall.
- (`created_at` already exists = ingestion time the system learned it. Do not change.)

**"Currently valid" predicate** (the single source of truth, used everywhere):

```sql
-- a memory is currently valid when it is not expired and not yet event-invalidated
(expired_at IS NULL AND invalid_at IS NULL)
```

**"Valid as of time T" predicate** (for history queries):

```sql
-- known to the system by T (created_at <= T), not retired by T,
-- event-validity window contains T
(created_at <= :t)
  AND (expired_at IS NULL OR expired_at > :t)
  AND (valid_at IS NULL OR valid_at <= :t)
  AND (invalid_at IS NULL OR invalid_at > :t)
```

These two predicates are implemented once as Go helpers in Task 2 and reused verbatim.

---

## File Structure

| File | Responsibility | Change |
|------|----------------|--------|
| `store.go` | schema + migrations | Add four `ensureColumn` calls for the temporal columns |
| `types.go` | structs + options | Add temporal fields to `Memory`; add `recallConfig` temporal options (`asOf *time.Time`, `includeInvalid bool`) + `WithAsOf`/`WithIncludeInvalid`; add `InvalidateOption` if needed |
| `memory.go` | memory CRUD + read paths | Scan new columns; add `InvalidateMemory`; thread the validity predicate into `SearchMemories` and `GetMemoriesByEntity` |
| `temporal.go` (new) | the two predicates + arg builders | `currentlyValidClause()` and `validAsOfClause(t)` returning SQL fragment + args |
| `search.go` | vector read path | Thread validity filter into `SearchMemoryVector` row resolution |
| `recall.go` | recall plumbing | Pass `recallConfig` temporal options into the memory sub-query helpers |
| `*_test.go` | tests | One test file change per task |

---

### Task 1: Add bi-temporal columns to the memories schema

**Files:**
- Modify: `store.go` (`Open`, the `ensureColumn` loop area `store.go:153-159`)
- Modify: `types.go` (`Memory` struct `types.go:39-47`)
- Modify: `memory.go` (every `SELECT ... FROM memories` scan must read the new columns: `PutMemory` unaffected on write, but `SearchMemories` `memory.go:69-87`, `GetMemoriesByEntity` `memory.go:106-124`, and the scan in `search.go` `SearchMemoryVector`)
- Test: `memory_test.go`

**Interfaces:**
- Consumes: existing `Memory`, `ensureColumn`.
- Produces: `Memory` struct with `ValidAt *time.Time`, `InvalidAt *time.Time`, `ExpiredAt *time.Time` (pointers so NULL is representable). `created_at` stays `time.Time`. After this task every memory read populates these (NULL→nil). No behavior change yet — just plumbing the columns through.

- [ ] **Step 1: Write the failing test**

Add to `memory_test.go`:

```go
func TestMemory_TemporalColumnsRoundTrip(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	m := &Memory{Content: "Alice's budget is 5000"}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}

	// Freshly ingested memory: temporal fields are nil (NULL in DB).
	got, err := c.SearchMemories(ctx, "budget", 10)
	if err != nil {
		t.Fatalf("SearchMemories: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 memory, got %d", len(got))
	}
	if got[0].ValidAt != nil || got[0].InvalidAt != nil || got[0].ExpiredAt != nil {
		t.Fatalf("expected nil temporal fields on fresh memory, got valid=%v invalid=%v expired=%v",
			got[0].ValidAt, got[0].InvalidAt, got[0].ExpiredAt)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestMemory_TemporalColumnsRoundTrip -v`
Expected: FAIL — `got[0].ValidAt undefined` (field doesn't exist on `Memory`).

- [ ] **Step 3: Add temporal fields to the Memory struct**

In `types.go`, the `Memory` struct (`types.go:39-47`), add three pointer fields after `UpdatedAt`:

```go
type Memory struct {
	ID         string    `json:"id"`
	Content    string    `json:"content"`
	EntityIDs  []string  `json:"entity_ids,omitempty"`
	Source     string    `json:"source,omitempty"`
	Confidence float64   `json:"confidence"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
	// Bi-temporal fields (Tier 2a). Pointers so SQL NULL is representable.
	// ValidAt/InvalidAt are event-time (when the fact became / stopped being
	// true). ExpiredAt is ingestion-time (when the system retired the memory).
	// All nil on memories written by the standard ingest path.
	ValidAt   *time.Time `json:"valid_at,omitempty"`
	InvalidAt *time.Time `json:"invalid_at,omitempty"`
	ExpiredAt *time.Time `json:"expired_at,omitempty"`
}
```

- [ ] **Step 4: Add the migration columns**

In `store.go`, after the existing confidence-migration loop (`store.go:153-159`), add:

```go
	for _, col := range []string{"valid_at", "invalid_at", "expired_at"} {
		if err := ensureColumn(db, "memories", col, "DATETIME"); err != nil {
			db.Close()
			return nil, fmt.Errorf("cortex: migrate memories.%s: %w", col, err)
		}
	}
```

(`DATETIME` with no `NOT NULL`/`DEFAULT` → nullable, defaults to NULL for existing rows.)

- [ ] **Step 5: Read the new columns in every memories SELECT**

There are three read sites. Update each SELECT column list and `Scan` to include the three temporal columns as `sql.NullTime`, converting to `*time.Time`.

Add this helper to `memory.go` (package-level):

```go
// nullTimePtr converts a sql.NullTime to a *time.Time (nil when not valid).
func nullTimePtr(nt sql.NullTime) *time.Time {
	if !nt.Valid {
		return nil
	}
	t := nt.Time
	return &t
}
```

**5a — `SearchMemories` (`memory.go`):** change the SELECT (currently `SELECT m.id, m.content, m.source, m.confidence, m.created_at, m.updated_at`) to append `, m.valid_at, m.invalid_at, m.expired_at`, and the scan to:

```go
		var vat, iat, eat sql.NullTime
		if err := rows.Scan(&m.ID, &m.Content, &m.Source, &m.Confidence, &m.CreatedAt, &m.UpdatedAt, &vat, &iat, &eat); err != nil {
			return nil, fmt.Errorf("cortex: scan memory: %w", err)
		}
		m.ValidAt = nullTimePtr(vat)
		m.InvalidAt = nullTimePtr(iat)
		m.ExpiredAt = nullTimePtr(eat)
```

**5b — `GetMemoriesByEntity` (`memory.go`):** same change to its SELECT (the one joining `memory_entities`) and scan block.

**5c — `SearchMemoryVector` (`search.go`):** the per-row resolve query (`search.go:150-153`, `SELECT id, content, source, confidence, created_at, updated_at FROM memories WHERE id = ?`) — append the three columns and extend the `.Scan(...)` with `&vat, &iat, &eat` + the `nullTimePtr` assignments. `sql` is already imported there.

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./ -run TestMemory_TemporalColumnsRoundTrip -v`
Expected: PASS.

- [ ] **Step 7: Run the full suite**

Run: `go test ./...`
Expected: PASS (columns are additive; all reads still return the same rows, now with nil temporal fields). Ignore `llm/openai` 429 failures.

- [ ] **Step 8: Commit**

```bash
git add store.go types.go memory.go search.go memory_test.go
git commit -m "feat: add bi-temporal columns to memories"
```

---

### Task 2: Validity predicates (temporal.go)

**Files:**
- Create: `temporal.go`
- Test: `temporal_test.go`

**Interfaces:**
- Produces:
  - `currentlyValidClause(alias string) string` — returns a SQL boolean fragment (no leading `AND`) using the given table alias (e.g. `"m"` → `(m.expired_at IS NULL AND m.invalid_at IS NULL)`; empty alias → unqualified columns).
  - `validAsOfClause(alias string) (string, []any)` — returns the as-of fragment with `?` placeholders and the four args (all the same time value), so callers append the args in order. Signature: takes the alias and the time, returns `(string, []any)`.
  - Final signature: `validAsOfClause(alias string, t time.Time) (string, []any)`.

These are the single source of truth for the predicate. Every read path in later tasks calls one of them — no hand-written temporal SQL elsewhere.

- [ ] **Step 1: Write the failing test**

Create `temporal_test.go`:

```go
package cortex

import (
	"strings"
	"testing"
	"time"
)

func TestCurrentlyValidClause(t *testing.T) {
	got := currentlyValidClause("m")
	want := "(m.expired_at IS NULL AND m.invalid_at IS NULL)"
	if got != want {
		t.Fatalf("currentlyValidClause(\"m\") = %q, want %q", got, want)
	}
	// Empty alias → unqualified columns.
	gotBare := currentlyValidClause("")
	if strings.Contains(gotBare, ".") {
		t.Fatalf("empty alias should produce unqualified columns, got %q", gotBare)
	}
}

func TestValidAsOfClause(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clause, args := validAsOfClause("m", ts)
	if !strings.Contains(clause, "m.created_at <= ?") ||
		!strings.Contains(clause, "m.expired_at IS NULL OR m.expired_at > ?") ||
		!strings.Contains(clause, "m.valid_at IS NULL OR m.valid_at <= ?") ||
		!strings.Contains(clause, "m.invalid_at IS NULL OR m.invalid_at > ?") {
		t.Fatalf("validAsOfClause missing expected sub-clauses: %q", clause)
	}
	if len(args) != 4 {
		t.Fatalf("expected 4 args (one per placeholder), got %d", len(args))
	}
	for i, a := range args {
		if a != ts {
			t.Fatalf("arg %d = %v, want %v", i, a, ts)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run 'TestCurrentlyValidClause|TestValidAsOfClause' -v`
Expected: FAIL — undefined `currentlyValidClause` / `validAsOfClause`.

- [ ] **Step 3: Implement temporal.go**

Create `temporal.go`:

```go
package cortex

import (
	"fmt"
	"time"
)

// qualify prefixes a column with the table alias and a dot, or returns the
// bare column when alias is empty.
func qualify(alias, col string) string {
	if alias == "" {
		return col
	}
	return alias + "." + col
}

// currentlyValidClause returns a SQL boolean fragment (no leading AND) that
// is true for memories that are currently valid: not retired by the system
// (expired_at IS NULL) and not event-invalidated (invalid_at IS NULL).
// Memories written by the standard ingest path have both NULL and so are
// always current. alias is the table alias (e.g. "m"); "" yields unqualified
// column names.
func currentlyValidClause(alias string) string {
	return fmt.Sprintf("(%s IS NULL AND %s IS NULL)",
		qualify(alias, "expired_at"), qualify(alias, "invalid_at"))
}

// validAsOfClause returns a SQL boolean fragment (no leading AND) plus its
// bound args, selecting memories valid as of time t: known to the system by
// t, not retired by t, and whose event-validity window contains t. The four
// args are all t, in placeholder order.
func validAsOfClause(alias string, t time.Time) (string, []any) {
	clause := fmt.Sprintf(
		"(%s <= ? AND (%s IS NULL OR %s > ?) AND (%s IS NULL OR %s <= ?) AND (%s IS NULL OR %s > ?))",
		qualify(alias, "created_at"),
		qualify(alias, "expired_at"), qualify(alias, "expired_at"),
		qualify(alias, "valid_at"), qualify(alias, "valid_at"),
		qualify(alias, "invalid_at"), qualify(alias, "invalid_at"),
	)
	return clause, []any{t, t, t, t}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ -run 'TestCurrentlyValidClause|TestValidAsOfClause' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add temporal.go temporal_test.go
git commit -m "feat: add bi-temporal validity SQL predicates"
```

---

### Task 3: InvalidateMemory soft-retire primitive

**Files:**
- Modify: `memory.go` (add `InvalidateMemory`)
- Modify: `types.go` (no new types needed; method takes explicit args)
- Test: `memory_test.go`

**Interfaces:**
- Consumes: the `memories` temporal columns (Task 1).
- Produces: `InvalidateMemory(ctx, id string, invalidAt *time.Time) error`. Sets `expired_at = now()` (system retired it) and, when `invalidAt != nil`, `invalid_at = *invalidAt` (the event time the fact stopped being true). When `invalidAt` is nil, only `expired_at` is set (retired without a known event-time boundary). Idempotent-ish: invalidating an already-invalidated memory updates the timestamps again (acceptable; Phase 2b calls this once per supersession). Returns an error if the id doesn't exist. **Does not delete** anything, **does not touch FTS or embeddings** (the memory stays searchable for history queries; default recall hides it via the validity predicate in Task 4).

- [ ] **Step 1: Write the failing test**

Add to `memory_test.go`:

```go
func TestInvalidateMemory(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	m := &Memory{Content: "Alice's budget is 5000"}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}

	eventTime := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if err := c.InvalidateMemory(ctx, m.ID, &eventTime); err != nil {
		t.Fatalf("InvalidateMemory: %v", err)
	}

	// Row still exists; expired_at set, invalid_at = eventTime.
	var expired, invalid sql.NullTime
	err := c.db.QueryRowContext(ctx,
		`SELECT expired_at, invalid_at FROM memories WHERE id = ?`, m.ID,
	).Scan(&expired, &invalid)
	if err != nil {
		t.Fatalf("memory row should still exist: %v", err)
	}
	if !expired.Valid {
		t.Fatal("expected expired_at to be set")
	}
	if !invalid.Valid || !invalid.Time.Equal(eventTime) {
		t.Fatalf("expected invalid_at = %v, got valid=%v time=%v", eventTime, invalid.Valid, invalid.Time)
	}
}

func TestInvalidateMemory_NotFound(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	if err := c.InvalidateMemory(ctx, "nonexistent-id", nil); err == nil {
		t.Fatal("expected error for nonexistent memory, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestInvalidateMemory -v`
Expected: FAIL — `c.InvalidateMemory undefined`.

- [ ] **Step 3: Implement InvalidateMemory**

Add to `memory.go`:

```go
// InvalidateMemory soft-retires a memory: it sets expired_at to now (the
// system has retired this memory) and, when invalidAt is non-nil, sets
// invalid_at to the event time the fact stopped being true. The row, its
// FTS entry, and its embedding are left intact so the memory remains
// available to point-in-time history queries; default recall hides it via
// the currently-valid predicate. Returns an error if no memory has the
// given id. This is the primitive Phase 2b's reconciliation calls on
// supersession; it never deletes (use Forget for hard deletion).
func (c *Cortex) InvalidateMemory(ctx context.Context, id string, invalidAt *time.Time) error {
	now := time.Now().UTC()

	var res sql.Result
	var err error
	if invalidAt != nil {
		res, err = c.db.ExecContext(ctx,
			`UPDATE memories SET expired_at = ?, invalid_at = ?, updated_at = ? WHERE id = ?`,
			now, invalidAt.UTC(), now, id,
		)
	} else {
		res, err = c.db.ExecContext(ctx,
			`UPDATE memories SET expired_at = ?, updated_at = ? WHERE id = ?`,
			now, now, id,
		)
	}
	if err != nil {
		return fmt.Errorf("cortex: invalidate memory: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("cortex: invalidate memory rows affected: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("cortex: memory %q not found", id)
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ -run TestInvalidateMemory -v`
Expected: PASS.

- [ ] **Step 5: Run the full suite**

Run: `go test ./...`
Expected: PASS. Ignore `llm/openai` 429.

- [ ] **Step 6: Commit**

```bash
git add memory.go memory_test.go
git commit -m "feat: add InvalidateMemory soft-retire primitive"
```

---

### Task 4: Filter recall to currently-valid memories

**Files:**
- Modify: `memory.go` (`SearchMemories`, `GetMemoriesByEntity`)
- Modify: `search.go` (`SearchMemoryVector` row resolution)
- Test: `memory_test.go`, `search_test.go`

**Interfaces:**
- Consumes: `currentlyValidClause` (Task 2), `InvalidateMemory` (Task 3).
- Produces: `SearchMemories`, `GetMemoriesByEntity`, and `SearchMemoryVector` all exclude memories where `expired_at IS NOT NULL OR invalid_at IS NOT NULL` by default. Signatures unchanged (the default-valid behavior needs no new params; the history opt-in is Task 5). This is the behavioral payoff: an invalidated memory disappears from recall but stays in the DB.

- [ ] **Step 1: Write the failing test**

Add to `memory_test.go`:

```go
func TestSearchMemories_HidesInvalidated(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	stale := &Memory{Content: "Alice's budget is 5000"}
	if err := c.PutMemory(ctx, stale); err != nil {
		t.Fatal(err)
	}
	current := &Memory{Content: "Alice's budget is 10000"}
	if err := c.PutMemory(ctx, current); err != nil {
		t.Fatal(err)
	}

	// Before invalidation: both match.
	got, err := c.SearchMemories(ctx, "budget", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 before invalidation, got %d", len(got))
	}

	// Invalidate the stale one.
	if err := c.InvalidateMemory(ctx, stale.ID, nil); err != nil {
		t.Fatal(err)
	}

	got, err = c.SearchMemories(ctx, "budget", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != current.ID {
		t.Fatalf("expected only the current memory after invalidation, got %d results", len(got))
	}
}
```

Add to `search_test.go`:

```go
func TestSearchMemoryVector_HidesInvalidated(t *testing.T) {
	c := openTestDBWithEmbedder(t)
	ctx := context.Background()

	m := &Memory{Content: "Alice's budget is 5000"}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	vecs, _ := c.cfg.embedder.Embed(ctx, []string{m.Content})
	if err := c.putEmbedding(ctx, m.ID, "memory", vecs[0]); err != nil {
		t.Fatal(err)
	}

	// Visible before invalidation.
	got, err := c.SearchMemoryVector(ctx, "Alice's budget is 5000", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 before invalidation, got %d", len(got))
	}

	if err := c.InvalidateMemory(ctx, m.ID, nil); err != nil {
		t.Fatal(err)
	}

	got, err = c.SearchMemoryVector(ctx, "Alice's budget is 5000", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("expected 0 after invalidation, got %d", len(got))
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./ -run 'TestSearchMemories_HidesInvalidated|TestSearchMemoryVector_HidesInvalidated' -v`
Expected: FAIL — invalidated memories still returned (no filter yet).

- [ ] **Step 3: Add the filter to SearchMemories**

In `SearchMemories` (`memory.go`), the FTS query currently is:

```go
		`SELECT m.id, m.content, m.source, m.confidence, m.created_at, m.updated_at, m.valid_at, m.invalid_at, m.expired_at
		 FROM memories m
		 JOIN memories_fts f ON m.rowid = f.rowid
		 WHERE memories_fts MATCH ?
		 ORDER BY f.rank
		 LIMIT ?`
```

Add the validity predicate to the WHERE. Build it with the helper:

```go
	rows, err := c.db.QueryContext(ctx,
		`SELECT m.id, m.content, m.source, m.confidence, m.created_at, m.updated_at, m.valid_at, m.invalid_at, m.expired_at
		 FROM memories m
		 JOIN memories_fts f ON m.rowid = f.rowid
		 WHERE memories_fts MATCH ? AND `+currentlyValidClause("m")+`
		 ORDER BY f.rank
		 LIMIT ?`,
		ftsQuery(query), limit,
	)
```

(The predicate has no `?` placeholders, so the arg list is unchanged.)

- [ ] **Step 4: Add the filter to GetMemoriesByEntity**

In `GetMemoriesByEntity` (`memory.go`), append ` AND `+currentlyValidClause("m")` to its WHERE clause (it joins `memory_entities me ON m.id = me.memory_id WHERE me.entity_id = ?`). Result:

```go
		`SELECT m.id, m.content, m.source, m.confidence, m.created_at, m.updated_at, m.valid_at, m.invalid_at, m.expired_at
		 FROM memories m
		 JOIN memory_entities me ON m.id = me.memory_id
		 WHERE me.entity_id = ? AND `+currentlyValidClause("m"),
```

- [ ] **Step 5: Add the filter to SearchMemoryVector**

In `SearchMemoryVector` (`search.go`), the per-row resolve query selects by id. Add the predicate so an invalidated memory whose embedding still matches is skipped:

```go
		err := c.db.QueryRowContext(ctx,
			`SELECT id, content, source, confidence, created_at, updated_at, valid_at, invalid_at, expired_at
			 FROM memories WHERE id = ? AND `+currentlyValidClause("")+``, r.refID,
		).Scan(...)
```

`currentlyValidClause("")` → unqualified columns, correct for the single-table query. The existing `sql.ErrNoRows → continue` handling now also skips invalidated memories (the row exists but fails the predicate, so `QueryRow` returns `ErrNoRows`). Verify that `continue` path is present and unchanged.

- [ ] **Step 6: Run tests to verify they pass**

Run: `go test ./ -run 'TestSearchMemories_HidesInvalidated|TestSearchMemoryVector_HidesInvalidated' -v`
Expected: PASS.

- [ ] **Step 7: Run the full suite**

Run: `go test ./...`
Expected: PASS. Existing recall tests are unaffected (all their memories have NULL temporal fields → pass the predicate). Ignore `llm/openai` 429.

- [ ] **Step 8: Commit**

```bash
git add memory.go search.go memory_test.go search_test.go
git commit -m "feat: hide invalidated memories from default recall"
```

---

### Task 5: History queries (WithAsOf / WithIncludeInvalid)

**Files:**
- Modify: `types.go` (`recallConfig` + new options)
- Modify: `memory.go` (`SearchMemories` gains an internal variant honoring the temporal mode; or accept options)
- Modify: `recall.go` (thread the temporal mode from `recallConfig` into the memory sub-query helpers)
- Test: `recall_test.go`

**Interfaces:**
- Consumes: `validAsOfClause` (Task 2), `currentlyValidClause`.
- Produces: two new `RecallOption`s:
  - `WithAsOf(t time.Time)` — recall the graph as it was valid at time `t` (uses `validAsOfClause`).
  - `WithIncludeInvalid()` — include retired/invalidated memories (no validity filter at all).
  Default (neither set) = currently-valid only (Task 4 behavior). These set fields on `recallConfig`; `Recall` passes the resolved temporal mode down to `recallMemories` and `recallMemoryVector`, which select the right predicate. Mutually-exclusive precedence: `WithIncludeInvalid` (no filter) > `WithAsOf` (as-of filter) > default (currently-valid).

- [ ] **Step 1: Write the failing test**

Add to `recall_test.go`:

```go
func TestRecall_WithIncludeInvalid(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	stale := &Memory{Content: "Alice's budget is 5000"}
	if err := c.PutMemory(ctx, stale); err != nil {
		t.Fatal(err)
	}
	if err := c.InvalidateMemory(ctx, stale.ID, nil); err != nil {
		t.Fatal(err)
	}

	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
			return []StructuredQuery{{Type: "memory_lookup", Params: map[string]any{"query": q}}}, nil
		},
	})

	// Default recall: invalidated memory hidden.
	def, err := c.Recall(ctx, "budget", WithLimit(10))
	if err != nil {
		t.Fatal(err)
	}
	if len(def) != 0 {
		t.Fatalf("default recall should hide invalidated memory, got %d", len(def))
	}

	// WithIncludeInvalid: it reappears.
	all, err := c.Recall(ctx, "budget", WithLimit(10), WithIncludeInvalid())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("WithIncludeInvalid should return the invalidated memory, got %d", len(all))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestRecall_WithIncludeInvalid -v`
Expected: FAIL — `WithIncludeInvalid undefined`.

- [ ] **Step 3: Add the temporal mode to recallConfig and options**

In `types.go`, extend `recallConfig` (`types.go:142-146`) and add options near the other `RecallOption`s:

```go
type recallConfig struct {
	limit          int
	source         string
	minConfidence  float64
	asOf           *time.Time // non-nil → recall as the graph was valid at this time
	includeInvalid bool       // true → no validity filter (include retired memories)
}

// WithAsOf recalls memories as they were valid at time t (point-in-time
// history). Mutually exclusive with WithIncludeInvalid, which takes
// precedence if both are set.
func WithAsOf(t time.Time) RecallOption {
	return func(c *recallConfig) { c.asOf = &t }
}

// WithIncludeInvalid includes retired/invalidated memories in recall
// (no validity filtering). Takes precedence over WithAsOf.
func WithIncludeInvalid() RecallOption {
	return func(c *recallConfig) { c.includeInvalid = true }
}
```

Ensure `time` is imported in `types.go` (it is — `types.go:6`).

- [ ] **Step 4: Define a temporal-mode value passed to the read paths**

To avoid threading three booleans, add a small internal struct in `temporal.go`:

```go
// temporalMode selects which validity predicate a memory read applies.
type temporalMode struct {
	includeInvalid bool
	asOf           *time.Time
}

// clause returns the SQL fragment (no leading AND) and its args for this
// mode. includeInvalid → always-true ("1=1", no args). asOf set →
// validAsOfClause. Otherwise → currentlyValidClause.
func (m temporalMode) clause(alias string) (string, []any) {
	if m.includeInvalid {
		return "1=1", nil
	}
	if m.asOf != nil {
		return validAsOfClause(alias, *m.asOf)
	}
	return currentlyValidClause(alias), nil
}
```

- [ ] **Step 5: Make the memory read paths accept a temporalMode**

Refactor `SearchMemories` and `SearchMemoryVector` and `GetMemoriesByEntity` to build their validity fragment from a `temporalMode` instead of hard-calling `currentlyValidClause`. Keep the public signatures stable by adding internal variants:

In `memory.go`, rename the body of `SearchMemories` to `searchMemoriesMode(ctx, query, limit, mode)` and make `SearchMemories` call it with the default mode:

```go
func (c *Cortex) SearchMemories(ctx context.Context, query string, limit int) ([]Memory, error) {
	return c.searchMemoriesMode(ctx, query, limit, temporalMode{})
}

func (c *Cortex) searchMemoriesMode(ctx context.Context, query string, limit int, mode temporalMode) ([]Memory, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	clause, targs := mode.clause("m")
	args := []any{ftsQuery(query)}
	args = append(args, targs...)
	args = append(args, limit)
	rows, err := c.db.QueryContext(ctx,
		`SELECT m.id, m.content, m.source, m.confidence, m.created_at, m.updated_at, m.valid_at, m.invalid_at, m.expired_at
		 FROM memories m
		 JOIN memories_fts f ON m.rowid = f.rowid
		 WHERE memories_fts MATCH ? AND `+clause+`
		 ORDER BY f.rank
		 LIMIT ?`,
		args...,
	)
	// ... rest of scan loop + entity-link loading unchanged ...
}
```

Note the **arg order**: `ftsQuery(query)` first (the MATCH `?`), then the temporal args (the as-of placeholders, if any), then `limit`. The `currentlyValidClause` and `1=1` modes contribute zero args, so order is preserved; only `WithAsOf` adds four.

Do the analogous internal-variant refactor for `SearchMemoryVector` (`search.go`) — add `searchMemoryVectorMode(ctx, query, limit, mode)`; the per-row resolve query appends `AND `+clause and its args go after `r.refID`. For `GetMemoriesByEntity`, add a `mode` variant similarly (used by callers that want history; default stays currently-valid).

- [ ] **Step 6: Thread the mode through recall**

In `recall.go`, `Recall` builds `cfg *recallConfig`. Construct a `temporalMode` from it and pass to the memory sub-query helpers. The sub-query dispatch (`executeSubQuery`) currently calls `c.recallMemories(ctx, query, limit)` and `c.recallMemoryVector(ctx, query, limit)`. Thread the mode:

- Add a `mode temporalMode` field to the call path. Simplest: store the resolved mode on a struct the helpers can read, OR pass it as a parameter. Pass as a parameter: change `executeSubQuery` signature to accept `mode temporalMode` and forward it; `Recall` computes `mode := temporalMode{includeInvalid: cfg.includeInvalid, asOf: cfg.asOf}` and passes it into each `executeSubQuery` call (it's called inside the goroutine loop — capture `mode` in the closure).
- `recallMemories` calls `searchMemoriesMode(ctx, query, limit, mode)`; `recallMemoryVector` calls `searchMemoryVectorMode(...)`. The keyword/vector/graph sub-queries over chunks/entities are unaffected (chunks and entities have no temporal columns this phase) — they ignore `mode`.

- [ ] **Step 7: Run test to verify it passes**

Run: `go test ./ -run TestRecall_WithIncludeInvalid -v`
Expected: PASS.

- [ ] **Step 8: Add an as-of test**

Add to `recall_test.go`:

```go
func TestRecall_WithAsOf(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	m := &Memory{Content: "Alice's budget is 5000"}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	// Invalidate as of 2026-03-01 (fact stopped being true then).
	cut := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if err := c.InvalidateMemory(ctx, m.ID, &cut); err != nil {
		t.Fatal(err)
	}

	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
			return []StructuredQuery{{Type: "memory_lookup", Params: map[string]any{"query": q}}}, nil
		},
	})

	// As of before the cutoff: the fact was still valid → returned.
	before := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	got, err := c.Recall(ctx, "budget", WithLimit(10), WithAsOf(before))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("as-of before cutoff should return the memory, got %d", len(got))
	}

	// As of after the cutoff: invalidated → hidden.
	after := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	got, err = c.Recall(ctx, "budget", WithLimit(10), WithAsOf(after))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("as-of after cutoff should hide the memory, got %d", len(got))
	}
}
```

Note: this test relies on `created_at <= asOf`. `PutMemory` sets `created_at = now()` (2026-06-xx at run time), which is AFTER both `before` (2026-02) and `after` (2026-04). That means the as-of clause's `created_at <= ?` fails for BOTH and returns 0 in each case — the test would be wrong.

**Resolution:** the as-of test must control `created_at`. Since `PutMemory` stamps it, the test should backdate the row directly after insert:

```go
	// Backdate created_at so the as-of window is meaningful.
	ingest := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := c.db.ExecContext(ctx, `UPDATE memories SET created_at = ? WHERE id = ?`, ingest, m.ID); err != nil {
		t.Fatal(err)
	}
```

Insert this immediately after `PutMemory` and before `InvalidateMemory`. (Add to the test above.)

- [ ] **Step 9: Run the as-of test**

Run: `go test ./ -run TestRecall_WithAsOf -v`
Expected: PASS.

- [ ] **Step 10: Run the full suite**

Run: `go test ./...`
Expected: PASS. Ignore `llm/openai` 429.

- [ ] **Step 11: Commit**

```bash
git add types.go temporal.go memory.go search.go recall.go recall_test.go
git commit -m "feat: point-in-time recall via WithAsOf and WithIncludeInvalid"
```

---

### Task 6: Expose history in MCP and document the model

**Files:**
- Modify: `cmd/cortex/mcp/tools.go` (recall tool gains optional `as_of` / `include_invalid` params)
- Modify: `cmd/cortex/CORTEX.md.template` (document temporal recall)
- Test: build + existing tests

**Interfaces:**
- Consumes: `WithAsOf`, `WithIncludeInvalid` (Task 5).
- Produces: the MCP `recall` tool accepts optional `as_of` (RFC3339 timestamp string) and `include_invalid` (bool); the agent contract documents that cortex tracks fact history.

- [ ] **Step 1: Add the params to the recall tool**

In `cmd/cortex/mcp/tools.go`, the `recall` tool definition — add two optional params alongside `limit`/`min_confidence`:

```go
			mcp.WithString("as_of", mcp.Description("Optional RFC3339 timestamp; recall the graph as it was valid at this time (point-in-time history).")),
			mcp.WithBoolean("include_invalid", mcp.Description("Include retired/superseded memories that are no longer current. Default false.")),
```

In the handler, after the existing opts parsing:

```go
			if asOfStr := req.GetString("as_of", ""); asOfStr != "" {
				ts, err := time.Parse(time.RFC3339, asOfStr)
				if err != nil {
					return mcp.NewToolResultError("as_of must be an RFC3339 timestamp"), nil
				}
				opts = append(opts, cortex.WithAsOf(ts))
			}
			if req.GetBool("include_invalid", false) {
				opts = append(opts, cortex.WithIncludeInvalid())
			}
```

Add `"time"` to the imports of `tools.go` if not present. (Check `req.GetBool` exists in the mcp-go API used elsewhere; the codebase uses `req.GetFloat`/`req.GetInt`/`req.GetString` — confirm the bool accessor name, it may be `req.GetBool`. If the accessor differs, use the correct one from the mcp-go version in go.mod.)

- [ ] **Step 2: Build to verify it compiles**

Run: `go build ./...`
Expected: success. If `req.GetBool` doesn't exist, find the correct boolean accessor (grep the mcp-go package or other tools) and adjust.

- [ ] **Step 3: Document temporal recall in the agent contract**

In `cmd/cortex/CORTEX.md.template`, add a short subsection (match surrounding markdown style) explaining: cortex tracks when facts were true; recall returns current facts by default; agents can ask for history with `as_of` / `include_invalid`. Keep it to 2-4 lines.

- [ ] **Step 4: Build + full suite**

Run: `go build ./... && go test ./...`
Expected: build success; tests pass (ignore `llm/openai` 429).

- [ ] **Step 5: Commit**

```bash
git add cmd/cortex/mcp/tools.go cmd/cortex/CORTEX.md.template
git commit -m "feat: expose temporal recall (as_of, include_invalid) via MCP"
```

---

## Self-Review

**Spec coverage** (Phase 2a = bi-temporal schema + soft-invalidation + recall-filters-to-valid + history opt-in):
- Bi-temporal columns: Task 1. ✅
- Validity predicates (single source of truth): Task 2. ✅
- Soft-invalidation primitive (never deletes): Task 3. ✅
- Default recall hides invalidated: Task 4. ✅
- Point-in-time / include-invalid history queries: Task 5. ✅
- MCP + agent-contract exposure: Task 6. ✅
- Phase 2b (LLM contradiction detection + background reconciliation calling `InvalidateMemory`): explicitly OUT of scope, noted in Architecture + Global Constraints. ✅

**Placeholder scan:** All code steps show complete code. No TBD/"handle errors"/"similar to". The one judgment call (mcp-go bool accessor name) is flagged with a concrete fallback (grep for the correct accessor). ✅

**Type consistency:**
- `Memory.ValidAt/InvalidAt/ExpiredAt` are `*time.Time` (Task 1), read via `nullTimePtr` everywhere.
- `currentlyValidClause(alias string) string` and `validAsOfClause(alias string, t time.Time) (string, []any)` (Task 2) — used by `temporalMode.clause` (Task 5) and directly in Task 4.
- `temporalMode{includeInvalid bool; asOf *time.Time}` and `.clause(alias) (string, []any)` (Task 5) — consumed by the `*Mode` read variants.
- `InvalidateMemory(ctx, id string, invalidAt *time.Time) error` (Task 3) — called by tests and (future) 2b.
- Arg-order invariant in `searchMemoriesMode` (MATCH arg → temporal args → limit) is called out explicitly because the as-of mode is the only one adding placeholders.

**Known scope edges (documented, not gaps):**
- Task 4 changes `currentlyValidClause`-based filtering to be hard-coded; Task 5 then refactors those same call sites to `temporalMode`. Tasks 4 and 5 both edit the same read functions — execute in order (4 → 5); Task 5's brief must note it's refactoring Task 4's hard-coded clause into the mode-driven variant.
- The as-of test (Task 5) must backdate `created_at` because `PutMemory` stamps it at run-time — called out inline with the fix.
- Relationships remain additive/non-temporal this phase (decided: memories first).
- No FTS/embedding deletion on invalidate — intentional, so history stays searchable; default recall hides via predicate.

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-06-22-tier2a-bitemporal-memories.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — execute tasks in this session with checkpoints.

**Which approach?**
