# Tier 1: Recall Quality Improvements Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make cortex's highest-value records (memories) retrievable by vector and FTS, re-inject source chunks alongside ranked memories, and turn confidence into a ranking signal plus an abstention hint — all with no schema migration risk to existing data.

**Architecture:** Three cohesive changes to the existing hybrid-recall pipeline. (1) Add memory vector + FTS retrieval so the embeddings already written at ingest are actually searched. (2) Weight the RRF-merged score by confidence instead of only hard-filtering. (3) Compute a lightweight recall "strength" signal and surface it through the API and MCP tool so agents can abstain rather than fabricate. All changes live in the root `cortex` package plus the MCP tool layer.

**Tech Stack:** Go 1.25.1, `modernc.org/sqlite` (pure-Go, no CGo), SQLite FTS5, `github.com/mark3labs/mcp-go`. Tests use the in-package helpers `openTestDB`, `openTestDBWithEmbedder`, `mockLLM`, `testEmbedder`.

## Global Constraints

- **Module path:** `github.com/sausheong/cortex` (root package is `cortex`).
- **No CGo, single binary.** Only `modernc.org/sqlite`. No new external dependencies.
- **Backward compatibility:** existing databases must keep working. New tables/indexes use `CREATE ... IF NOT EXISTS`; new columns use the existing `ensureColumn` idempotent-migration pattern. Never `DROP`.
- **Confidence invariant:** confidence is `[0,1]`; `coerceConfidence` maps zero/NaN → `1.0`. Do not change this.
- **Commit trailer:** do NOT add a `Co-Authored-By` trailer to commits (user preference for this repo).
- **Test command:** `go test ./...` must pass. Hermetic only — no live API calls.
- **TDD:** write the failing test first, watch it fail, implement minimally, watch it pass, commit.

---

## File Structure

| File | Responsibility | Change |
|------|----------------|--------|
| `store.go` | schema + migrations | Add `memories_fts` virtual table to `schemaSQL` |
| `memory.go` | memory CRUD + search | Sync `memories_fts` in `PutMemory`; replace `LIKE` in `SearchMemories` with FTS5; add `SearchMemoryVector` |
| `search.go` | vector search | Generalize so memory embeddings (`ref_type='memory'`) are searched |
| `recall.go` | hybrid recall pipeline | Add memory-vector sub-query; confidence-weighted scoring; source-chunk re-injection; recall-strength signal |
| `types.go` | option/result types | Add `RecallResponse`/strength fields; new sub-query type constant if needed |
| `cmd/cortex/mcp/tools.go` | MCP recall tool | Surface recall-strength / abstention hint in tool output |
| `*_test.go` | tests | One test file change per task |

---

### Task 1: Add `memories_fts` table and sync it on write

**Files:**
- Modify: `store.go` (the `schemaSQL` const, around `store.go:61-72`)
- Modify: `memory.go` (`PutMemory`, `memory.go:13-50`)
- Test: `memory_test.go`

**Interfaces:**
- Consumes: existing `Memory` struct, `c.db`.
- Produces: a `memories_fts` FTS5 table kept in sync with `memories`, mirroring the `chunks` / `chunks_fts` pattern. After this task, every memory row has a matching `memories_fts(rowid, content)` row.

- [ ] **Step 1: Write the failing test**

Add to `memory_test.go`:

```go
func TestPutMemory_PopulatesFTS(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	m := &Memory{Content: "Alice migrated the billing service to Stripe"}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}

	// The FTS table must contain the memory, joinable by rowid.
	var got string
	err := c.db.QueryRowContext(ctx,
		`SELECT m.content FROM memories m
		 JOIN memories_fts f ON m.rowid = f.rowid
		 WHERE memories_fts MATCH ?`, "Stripe",
	).Scan(&got)
	if err != nil {
		t.Fatalf("expected FTS match for memory, got error: %v", err)
	}
	if got != m.Content {
		t.Fatalf("FTS content mismatch: got %q want %q", got, m.Content)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestPutMemory_PopulatesFTS -v`
Expected: FAIL — `no such table: memories_fts` (table doesn't exist yet).

- [ ] **Step 3: Add the FTS table to the schema**

In `store.go`, immediately after the `memories` table definition in `schemaSQL` (after the block ending at `store.go:72`), add:

```sql
CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
	content,
	content_rowid='rowid'
);
```

- [ ] **Step 4: Sync FTS inside PutMemory**

In `memory.go`, `PutMemory` currently inserts into `memories` then loops over entity links inside one transaction (`memory.go:20-49`). The plain `ExecContext` for the memory insert does not capture the rowid. Change the memory insert to capture `result`, then insert into FTS using the rowid.

Replace the insert block (`memory.go:26-33`):

```go
	res, err := tx.ExecContext(ctx,
		`INSERT INTO memories (id, content, source, confidence, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		m.ID, m.Content, m.Source, m.Confidence, m.CreatedAt, m.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("cortex: insert memory: %w", err)
	}

	rowID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("cortex: memory last insert id: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO memories_fts (rowid, content) VALUES (?, ?)`,
		rowID, m.Content,
	); err != nil {
		return fmt.Errorf("cortex: insert memory fts: %w", err)
	}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./ -run TestPutMemory_PopulatesFTS -v`
Expected: PASS.

- [ ] **Step 6: Keep Forget consistent (delete FTS rows with memories)**

`deleteOrphanedMemories` (`cortex.go:289-326`) deletes memory rows. Add an FTS cleanup so the index doesn't accumulate orphans. In the per-orphan delete loop (`cortex.go:312-323`), before/after `DELETE FROM memories WHERE id = ?`, add:

```go
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM memories_fts WHERE rowid = (SELECT rowid FROM memories WHERE id = ?)`, id,
		); err != nil {
			return fmt.Errorf("cortex: delete orphaned memory fts: %w", err)
		}
```

Place this **before** the `DELETE FROM memories WHERE id = ?` statement (the subquery needs the row to still exist).

- [ ] **Step 7: Run the full suite**

Run: `go test ./...`
Expected: PASS (existing tests unaffected; new table is additive).

- [ ] **Step 8: Commit**

```bash
git add store.go memory.go memory_test.go
git commit -m "feat: index memories in FTS5 on write"
```

---

### Task 2: Replace LIKE memory search with FTS5

**Files:**
- Modify: `memory.go` (`SearchMemories`, `memory.go:55-101`)
- Test: `memory_test.go`

**Interfaces:**
- Consumes: `memories_fts` from Task 1.
- Produces: `SearchMemories(ctx, query, limit)` ranked by FTS5 `rank` (best first) instead of arbitrary LIKE order. Same signature `([]Memory, error)`, so `recallMemories` (`recall.go:123`) needs no change.

- [ ] **Step 1: Write the failing test**

Add to `memory_test.go`:

```go
func TestSearchMemories_RanksByRelevance(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	// Two memories; the query term appears in both, but one is a closer match.
	for _, content := range []string{
		"Alice prefers dark roast coffee",
		"Bob mentioned coffee once in passing about Alice and Stripe and billing",
	} {
		if err := c.PutMemory(ctx, &Memory{Content: content}); err != nil {
			t.Fatalf("PutMemory: %v", err)
		}
	}

	got, err := c.SearchMemories(ctx, "dark roast coffee", 10)
	if err != nil {
		t.Fatalf("SearchMemories: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected at least one result")
	}
	// FTS5 rank should put the focused "dark roast coffee" memory first.
	if got[0].Content != "Alice prefers dark roast coffee" {
		t.Fatalf("expected dark-roast memory ranked first, got %q", got[0].Content)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestSearchMemories_RanksByRelevance -v`
Expected: FAIL — current `LIKE` OR-match returns insertion order, so ranking assertion fails (or order is non-deterministic).

- [ ] **Step 3: Rewrite SearchMemories to use FTS5**

Replace the body of `SearchMemories` (`memory.go:55-101`) — the word-splitting + `LIKE` construction — with an FTS5 MATCH query mirroring `SearchKeyword` (`chunk.go:66-103`). Keep the entity-link loading tail (`memory.go:92-100`) unchanged.

```go
func (c *Cortex) SearchMemories(ctx context.Context, query string, limit int) ([]Memory, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}

	rows, err := c.db.QueryContext(ctx,
		`SELECT m.id, m.content, m.source, m.confidence, m.created_at, m.updated_at
		 FROM memories m
		 JOIN memories_fts f ON m.rowid = f.rowid
		 WHERE memories_fts MATCH ?
		 ORDER BY f.rank
		 LIMIT ?`,
		ftsQuery(query), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("cortex: search memories: %w", err)
	}
	defer rows.Close()

	var memories []Memory
	for rows.Next() {
		var m Memory
		if err := rows.Scan(&m.ID, &m.Content, &m.Source, &m.Confidence, &m.CreatedAt, &m.UpdatedAt); err != nil {
			return nil, fmt.Errorf("cortex: scan memory: %w", err)
		}
		memories = append(memories, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cortex: iterate memories: %w", err)
	}

	for i := range memories {
		entityIDs, err := c.loadMemoryEntityIDs(ctx, memories[i].ID)
		if err != nil {
			return nil, err
		}
		memories[i].EntityIDs = entityIDs
	}
	return memories, nil
}
```

- [ ] **Step 4: Add an FTS query sanitizer**

Raw user queries can contain FTS5 syntax characters (e.g. `"`, `*`, `:`) that throw `fts5: syntax error`. `SearchKeyword` passes the raw query today and is technically exposed to the same issue; centralize a safe helper and use it in both. Add to `memory.go` (or `search.go` — keep it in the `cortex` package):

```go
// ftsQuery turns an arbitrary user string into a safe FTS5 MATCH expression:
// each whitespace-separated token is wrapped in double quotes (escaping any
// embedded quotes) and OR-joined, so no token is interpreted as FTS syntax.
// Returns "" for an all-whitespace input (caller should treat as no-op).
func ftsQuery(q string) string {
	fields := strings.Fields(q)
	if len(fields) == 0 {
		return ""
	}
	quoted := make([]string, len(fields))
	for i, f := range fields {
		quoted[i] = `"` + strings.ReplaceAll(f, `"`, `""`) + `"`
	}
	return strings.Join(quoted, " OR ")
}
```

Note: this changes match semantics from substring (`LIKE '%w%'`) to token (`"w"`). That is the intended improvement — FTS tokenization with relevance ranking. Ensure `strings` is imported in `memory.go` (it already is, per `memory.go:6`).

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./ -run TestSearchMemories -v`
Expected: PASS.

- [ ] **Step 6: Guard against existing-DB upgrades (backfill note)**

Databases created before Task 1 have `memories` rows but no `memories_fts` rows, so FTS search would miss them. Add an idempotent backfill in `Open` (`store.go:142-153`), after the schema is applied and before returning `c`:

```go
	// Backfill memories_fts for databases created before the FTS table existed.
	if _, err := db.Exec(
		`INSERT INTO memories_fts (rowid, content)
		 SELECT m.rowid, m.content FROM memories m
		 WHERE m.rowid NOT IN (SELECT rowid FROM memories_fts)`,
	); err != nil {
		db.Close()
		return nil, fmt.Errorf("cortex: backfill memories_fts: %w", err)
	}
```

- [ ] **Step 7: Write the backfill test**

Add to `memory_test.go`:

```go
func TestOpen_BackfillsMemoriesFTS(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	// First open: insert a memory, then bypass FTS to simulate an old DB by
	// clearing the FTS table.
	c, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ctx := context.Background()
	if err := c.PutMemory(ctx, &Memory{Content: "legacy memory about Saturn"}); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}
	if _, err := c.db.ExecContext(ctx, `DELETE FROM memories_fts`); err != nil {
		t.Fatalf("clear fts: %v", err)
	}
	c.Close()

	// Reopen: backfill must repopulate FTS.
	c2, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer c2.Close()

	got, err := c2.SearchMemories(ctx, "Saturn", 10)
	if err != nil {
		t.Fatalf("SearchMemories: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 backfilled result, got %d", len(got))
	}
}
```

`filepath` is already imported in test files that use `openTestDB`; if this test lives in `memory_test.go` and that import is absent, add `"path/filepath"`.

- [ ] **Step 8: Run test to verify it passes**

Run: `go test ./ -run TestOpen_BackfillsMemoriesFTS -v`
Expected: PASS.

- [ ] **Step 9: Run the full suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 10: Commit**

```bash
git add memory.go store.go memory_test.go
git commit -m "feat: FTS5 relevance ranking for memory search, with backfill"
```

---

### Task 3: Search memory embeddings (fix the write-but-never-read bug)

**Files:**
- Modify: `search.go` (add `SearchMemoryVector`, reusing `searchVectorRaw`)
- Test: `search_test.go`

**Interfaces:**
- Consumes: `searchVectorRaw(ctx, queryVec, refType, limit)` (`search.go:37`), which already accepts a `refType` but is only ever called with `"chunk"`. Memory embeddings are written with `ref_type='memory'` at `cortex.go:115`.
- Produces: `SearchMemoryVector(ctx, query, limit) ([]Memory, error)` — embeds the query, vector-searches `ref_type='memory'`, loads the `Memory` rows (with entity links) by ID.

- [ ] **Step 1: Write the failing test**

Add to `search_test.go`:

```go
func TestSearchMemoryVector(t *testing.T) {
	c := openTestDBWithEmbedder(t)
	ctx := context.Background()

	// PutMemory does not embed; Remember does. Embed explicitly via the
	// same path Remember uses by storing a memory then its embedding.
	m := &Memory{Content: "Alice loves hiking in the Alps"}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}
	vecs, err := c.cfg.embedder.Embed(ctx, []string{m.Content})
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if err := c.putEmbedding(ctx, m.ID, "memory", vecs[0]); err != nil {
		t.Fatalf("putEmbedding: %v", err)
	}

	got, err := c.SearchMemoryVector(ctx, "Alice loves hiking in the Alps", 10)
	if err != nil {
		t.Fatalf("SearchMemoryVector: %v", err)
	}
	if len(got) != 1 || got[0].ID != m.ID {
		t.Fatalf("expected to find memory %s, got %+v", m.ID, got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestSearchMemoryVector -v`
Expected: FAIL — `c.SearchMemoryVector undefined`.

- [ ] **Step 3: Implement SearchMemoryVector**

Add to `search.go`, modeled on `SearchVector` (`search.go:78-123`) but resolving `Memory` rows and loading entity links:

```go
// SearchMemoryVector embeds the query and performs a brute-force vector
// search over memory embeddings (ref_type='memory'), returning the matching
// memories ordered by cosine similarity. These embeddings are written at
// ingest by Remember but were previously never searched.
func (c *Cortex) SearchMemoryVector(ctx context.Context, query string, limit int) ([]Memory, error) {
	if c.cfg.embedder == nil {
		return nil, fmt.Errorf("cortex: no embedder configured")
	}

	vecs, err := c.cfg.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("cortex: embed query: %w", err)
	}
	if len(vecs) == 0 {
		return nil, fmt.Errorf("cortex: embedder returned no vectors")
	}

	results, err := c.searchVectorRaw(ctx, vecs[0], "memory", limit)
	if err != nil {
		return nil, err
	}

	memories := make([]Memory, 0, len(results))
	for _, r := range results {
		var m Memory
		err := c.db.QueryRowContext(ctx,
			`SELECT id, content, source, confidence, created_at, updated_at
			 FROM memories WHERE id = ?`, r.refID,
		).Scan(&m.ID, &m.Content, &m.Source, &m.Confidence, &m.CreatedAt, &m.UpdatedAt)
		if err == sql.ErrNoRows {
			continue // embedding exists but memory was deleted
		}
		if err != nil {
			return nil, fmt.Errorf("cortex: get memory %s: %w", r.refID, err)
		}
		entityIDs, err := c.loadMemoryEntityIDs(ctx, m.ID)
		if err != nil {
			return nil, err
		}
		m.EntityIDs = entityIDs
		memories = append(memories, m)
	}
	return memories, nil
}
```

`sql` and `fmt` are already imported in `search.go` (`search.go:5-8`).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ -run TestSearchMemoryVector -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add search.go search_test.go
git commit -m "feat: search memory embeddings via SearchMemoryVector"
```

---

### Task 4: Wire memory vector search into recall

**Files:**
- Modify: `recall.go` (`executeSubQuery` `recall.go:103-121`, add `recallMemoryVector`)
- Test: `recall_test.go`

**Interfaces:**
- Consumes: `SearchMemoryVector` (Task 3); the existing sub-query dispatch in `executeSubQuery`.
- Produces: a new sub-query type `"memory_vector"` handled in `executeSubQuery`, producing ranked items keyed `mem:<id>` (same key space as `recallMemories`, so RRF naturally fuses keyword + vector hits on the same memory).

- [ ] **Step 1: Write the failing test**

Add to `recall_test.go`:

```go
func TestRecall_UsesMemoryVector(t *testing.T) {
	c := openTestDBWithEmbedder(t)
	ctx := context.Background()

	m := &Memory{Content: "Quarterly board meeting is in Zurich"}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}
	vecs, _ := c.cfg.embedder.Embed(ctx, []string{m.Content})
	if err := c.putEmbedding(ctx, m.ID, "memory", vecs[0]); err != nil {
		t.Fatalf("putEmbedding: %v", err)
	}

	// Decompose into memory_vector only, to isolate the new path.
	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
			return []StructuredQuery{
				{Type: "memory_vector", Params: map[string]any{"query": q}},
			}, nil
		},
	})

	results, err := c.Recall(ctx, "Quarterly board meeting is in Zurich", WithLimit(10))
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(results) == 0 || results[0].Content != m.Content {
		t.Fatalf("expected memory via vector path, got %+v", results)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestRecall_UsesMemoryVector -v`
Expected: FAIL — `memory_vector` isn't a handled sub-query type, so no results.

- [ ] **Step 3: Add the recallMemoryVector helper**

In `recall.go`, add (modeled on `recallMemories`, `recall.go:123-143`):

```go
func (c *Cortex) recallMemoryVector(ctx context.Context, query string, limit int) ([]rankedItem, map[string]Result) {
	if c.cfg.embedder == nil {
		return nil, nil
	}
	mems, err := c.SearchMemoryVector(ctx, query, limit)
	if err != nil || len(mems) == 0 {
		return nil, nil
	}

	items := make([]rankedItem, len(mems))
	results := make(map[string]Result, len(mems))
	for i, m := range mems {
		key := "mem:" + m.ID
		items[i] = rankedItem{id: key, rank: i}
		results[key] = Result{
			Type:       "memory",
			Content:    m.Content,
			Confidence: m.Confidence,
			EntityIDs:  m.EntityIDs,
			Source:     m.Source,
		}
	}
	return items, results
}
```

- [ ] **Step 4: Dispatch the new sub-query type**

In `executeSubQuery` (`recall.go:109-120`), add a case:

```go
	case "memory_vector":
		return c.recallMemoryVector(ctx, query, limit)
```

- [ ] **Step 5: Make the no-LLM fallback include memory_vector**

In `decomposeQuery` (`recall.go:94-98`), the fallback list currently has `keyword_search` + `memory_lookup`. Add `memory_vector` and `vector_search` so a configured embedder is used even without an LLM:

```go
	return []StructuredQuery{
		{Type: "keyword_search", Params: map[string]any{"query": query}},
		{Type: "memory_lookup", Params: map[string]any{"query": query}},
		{Type: "memory_vector", Params: map[string]any{"query": query}},
		{Type: "vector_search", Params: map[string]any{"query": query}},
	}
```

`recallMemoryVector` and `recallVector` both no-op safely when no embedder is configured, so this is harmless without embeddings.

- [ ] **Step 6: Run test to verify it passes**

Run: `go test ./ -run TestRecall_UsesMemoryVector -v`
Expected: PASS.

- [ ] **Step 7: Run the full suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add recall.go recall_test.go
git commit -m "feat: add memory_vector retrieval path to recall"
```

---

### Task 5: Confidence-weighted RRF scoring

**Files:**
- Modify: `recall.go` (`Recall`, `recall.go:53-80`)
- Test: `recall_test.go`

**Interfaces:**
- Consumes: `rrfMerge` output (`math.go:61`) and `resultMap` (`recall.go:26`).
- Produces: final ordering where the RRF score is scaled by the result's confidence, so a high-confidence and low-confidence memory at the same rank no longer tie — the confident one wins. The existing `WithMinConfidence` hard filter remains, applied after weighting.

- [ ] **Step 1: Write the failing test**

Add to `recall_test.go`:

```go
func TestRecall_ConfidenceWeightsRanking(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	// Two memories matching the same query at similar FTS rank, different
	// confidence. The higher-confidence one should rank first after weighting.
	low := &Memory{Content: "Project Phoenix ships in March", Confidence: 0.3}
	high := &Memory{Content: "Project Phoenix ships in April", Confidence: 0.95}
	if err := c.PutMemory(ctx, low); err != nil {
		t.Fatal(err)
	}
	if err := c.PutMemory(ctx, high); err != nil {
		t.Fatal(err)
	}

	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
			return []StructuredQuery{
				{Type: "memory_lookup", Params: map[string]any{"query": q}},
			}, nil
		},
	})

	results, err := c.Recall(ctx, "Project Phoenix ships", WithLimit(10))
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Content != high.Content {
		t.Fatalf("expected high-confidence memory first, got %q", results[0].Content)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestRecall_ConfidenceWeightsRanking -v`
Expected: FAIL — without weighting, ties break by map iteration / FTS rank, not confidence, so ordering is not guaranteed to put `high` first.

- [ ] **Step 3: Apply confidence weighting in Recall**

In `Recall` (`recall.go:55-62`), where merged items are turned into final results, scale the score by confidence. Replace the build-final-results loop:

```go
	// Build final results from merged ranked items, weighting the fusion
	// score by each result's confidence so equally-ranked items are broken
	// by how certain we are about them. Confidence is in [0,1]; coerced at
	// ingest so unset == 1.0 (no penalty).
	final := make([]Result, 0, len(merged))
	for _, item := range merged {
		if r, ok := resultMap[item.id]; ok {
			conf := r.Confidence
			if conf <= 0 {
				conf = 1.0 // defensive: treat unset/zero as full confidence
			}
			r.Score = item.score * conf
			final = append(final, r)
		}
	}

	// Re-sort by the confidence-weighted score (rrfMerge sorted by raw score).
	sort.SliceStable(final, func(i, j int) bool {
		return final[i].Score > final[j].Score
	})
```

- [ ] **Step 4: Add the sort import**

`recall.go` imports (`recall.go:3-8`) are `context`, `fmt`, `strings`, `sync`. Add `"sort"`.

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./ -run TestRecall_ConfidenceWeightsRanking -v`
Expected: PASS.

- [ ] **Step 6: Run the full suite**

Run: `go test ./...`
Expected: PASS. (Note: `TestRecall_ResultIncludesConfidence` and other recall tests assert presence/membership, not exact order, so weighting won't break them. If any ordering assertion fails, that test was over-specified — verify the failure is ordering-only before adjusting.)

- [ ] **Step 7: Commit**

```bash
git add recall.go recall_test.go
git commit -m "feat: weight recall ranking by confidence"
```

---

### Task 6: Source-chunk re-injection on ranked memories

**Files:**
- Modify: `recall.go` (`recallMemories` and `recallMemoryVector` — attach source chunk to memory results)
- Test: `recall_test.go`

**Interfaces:**
- Consumes: `Result.Metadata` (`types.go:56`) as the carrier for the re-injected chunk; the `memories.source` ↔ `chunks.metadata.source` linkage (the only link between a memory and its originating chunk today — both are stamped with `cfg.source` in `Remember`, `cortex.go:31,67,102`).
- Produces: memory `Result`s whose `Metadata["source_excerpt"]` holds the text of a chunk sharing the same `source`, when one exists. This gives the agent the crisp fact (memory) plus supporting detail (chunk) — supermemory's "atomic memory + source chunk" pattern.

- [ ] **Step 1: Write the failing test**

Add to `recall_test.go`:

```go
func TestRecall_ReinjectsSourceChunk(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	const src = "meeting-notes-2026-06"
	// A memory and a chunk that share a source.
	if err := c.PutMemory(ctx, &Memory{
		Content: "Decision: adopt Postgres for analytics",
		Source:  src,
	}); err != nil {
		t.Fatal(err)
	}
	ch := &Chunk{
		Content:  "After debating SQLite vs Postgres, the team decided to adopt Postgres for analytics because of concurrent writers.",
		Metadata: map[string]any{"source": src},
	}
	if err := c.PutChunk(ctx, ch); err != nil {
		t.Fatal(err)
	}

	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
			return []StructuredQuery{
				{Type: "memory_lookup", Params: map[string]any{"query": q}},
			}, nil
		},
	})

	results, err := c.Recall(ctx, "Postgres analytics decision", WithLimit(10))
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	var mem *Result
	for i := range results {
		if results[i].Type == "memory" {
			mem = &results[i]
			break
		}
	}
	if mem == nil {
		t.Fatal("expected a memory result")
	}
	excerpt, ok := mem.Metadata["source_excerpt"].(string)
	if !ok || excerpt != ch.Content {
		t.Fatalf("expected source_excerpt to be the chunk content, got %v", mem.Metadata["source_excerpt"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestRecall_ReinjectsSourceChunk -v`
Expected: FAIL — no `source_excerpt` is attached.

- [ ] **Step 3: Add a chunk-by-source lookup helper**

Add to `chunk.go`:

```go
// firstChunkBySource returns the content of one chunk whose metadata.source
// equals src, or "" if none. Used to re-inject supporting detail next to a
// ranked memory that shares the same source. Best-effort: errors return "".
func (c *Cortex) firstChunkBySource(ctx context.Context, src string) string {
	if src == "" {
		return ""
	}
	var content string
	err := c.db.QueryRowContext(ctx,
		`SELECT content FROM chunks
		 WHERE json_extract(metadata, '$.source') = ?
		 ORDER BY created_at ASC LIMIT 1`, src,
	).Scan(&content)
	if err != nil {
		return ""
	}
	return content
}
```

SQLite (modernc) ships the JSON1 functions, so `json_extract` is available. `context` is already imported in `chunk.go`.

- [ ] **Step 4: Attach the excerpt in both memory recall paths**

In `recallMemories` (`recall.go:131-141`) and `recallMemoryVector` (from Task 4), after building each memory `Result`, enrich it. Replace the `results[key] = Result{...}` assignment in **both** functions with:

```go
		res := Result{
			Type:       "memory",
			Content:    m.Content,
			Confidence: m.Confidence,
			EntityIDs:  m.EntityIDs,
			Source:     m.Source,
		}
		if excerpt := c.firstChunkBySource(ctx, m.Source); excerpt != "" {
			res.Metadata = map[string]any{"source_excerpt": excerpt}
		}
		results[key] = res
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./ -run TestRecall_ReinjectsSourceChunk -v`
Expected: PASS.

- [ ] **Step 6: Run the full suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add recall.go chunk.go recall_test.go
git commit -m "feat: re-inject source chunk excerpt alongside ranked memories"
```

---

### Task 7: Recall-strength / abstention signal

**Files:**
- Modify: `types.go` (add `RecallResult` aggregate type)
- Modify: `recall.go` (add `RecallWithStrength` wrapper)
- Test: `recall_test.go`

**Interfaces:**
- Consumes: the `[]Result` from `Recall` and their confidence-weighted `Score`s.
- Produces: `RecallWithStrength(ctx, query, opts...) (RecallResult, error)` where `RecallResult{Results []Result; Strength float64; Abstain bool}`. `Strength` is the top result's confidence-weighted score (0 if empty). `Abstain` is true when there are no results or the strength is below a threshold — a hint for agents to say "I don't have that" instead of fabricating. Existing `Recall` is unchanged (additive API).

- [ ] **Step 1: Write the failing test**

Add to `recall_test.go`:

```go
func TestRecallWithStrength_AbstainsWhenEmpty(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
			return []StructuredQuery{
				{Type: "memory_lookup", Params: map[string]any{"query": q}},
			}, nil
		},
	})

	// Nothing stored → no results → abstain.
	out, err := c.RecallWithStrength(ctx, "anything at all")
	if err != nil {
		t.Fatalf("RecallWithStrength: %v", err)
	}
	if !out.Abstain {
		t.Fatalf("expected Abstain=true on empty recall, got %+v", out)
	}
	if out.Strength != 0 {
		t.Fatalf("expected Strength 0 on empty recall, got %v", out.Strength)
	}
}

func TestRecallWithStrength_DoesNotAbstainWhenConfident(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	if err := c.PutMemory(ctx, &Memory{Content: "User's name is Sau Sheong", Confidence: 1.0}); err != nil {
		t.Fatal(err)
	}
	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
			return []StructuredQuery{
				{Type: "memory_lookup", Params: map[string]any{"query": q}},
			}, nil
		},
	})

	out, err := c.RecallWithStrength(ctx, "What is the user's name")
	if err != nil {
		t.Fatalf("RecallWithStrength: %v", err)
	}
	if out.Abstain {
		t.Fatalf("expected Abstain=false with a confident hit, got %+v", out)
	}
	if out.Strength <= 0 {
		t.Fatalf("expected positive strength, got %v", out.Strength)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestRecallWithStrength -v`
Expected: FAIL — `c.RecallWithStrength undefined`, `RecallResult` undefined.

- [ ] **Step 3: Add the RecallResult type and threshold constant**

In `types.go`, near `Result` (`types.go:49-57`), add:

```go
// RecallResult wraps a ranked recall with an aggregate strength signal and
// an abstention hint. Strength is the confidence-weighted score of the top
// result (0 when empty). Abstain is true when the engine has nothing
// sufficiently relevant — a cue for agents to say "I don't know that"
// rather than fabricate an answer.
type RecallResult struct {
	Results  []Result `json:"results"`
	Strength float64  `json:"strength"`
	Abstain  bool     `json:"abstain"`
}

// AbstainThreshold is the confidence-weighted top-score below which
// RecallWithStrength advises abstention. Tuned conservatively: the goal is
// to catch empty/irrelevant recalls, not to suppress weak-but-real hits.
const AbstainThreshold = 0.005
```

Rationale for the value: RRF scores are `sum(1/(60+rank+1))`; a single top hit at rank 0 scores `1/61 ≈ 0.0164`, weighted by confidence. A memory at confidence ≥ ~0.3 with one hit clears `0.005`; zero results scores 0. Adjust later if tuning shows false abstentions.

- [ ] **Step 4: Implement RecallWithStrength**

In `recall.go`, add:

```go
// RecallWithStrength runs Recall and adds an aggregate strength score plus
// an abstention hint. Use this when the caller (e.g. an agent) needs to
// decide whether the knowledge graph actually knows the answer.
func (c *Cortex) RecallWithStrength(ctx context.Context, query string, opts ...RecallOption) (RecallResult, error) {
	results, err := c.Recall(ctx, query, opts...)
	if err != nil {
		return RecallResult{}, err
	}
	out := RecallResult{Results: results}
	if len(results) > 0 {
		out.Strength = results[0].Score
	}
	out.Abstain = out.Strength < AbstainThreshold
	return out, nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./ -run TestRecallWithStrength -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add types.go recall.go recall_test.go
git commit -m "feat: add recall strength and abstention signal"
```

---

### Task 8: Surface abstention in the MCP recall tool

**Files:**
- Modify: `cmd/cortex/mcp/tools.go` (the `recall` tool handler, `cmd/cortex/mcp/tools.go:41-69`)
- Test: manual (MCP layer has no unit harness here; verify by build + description review)

**Interfaces:**
- Consumes: `cortex.RecallWithStrength` (Task 7).
- Produces: the MCP `recall` tool returns `{results, strength, abstain}` JSON and its description tells the agent to trust `abstain`.

- [ ] **Step 1: Update the recall handler to return strength**

In `cmd/cortex/mcp/tools.go`, replace the `cx.Recall(...)` call and its result return (`tools.go:63-67`) with:

```go
			out, err := cx.RecallWithStrength(ctx, query, opts...)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(out)
```

- [ ] **Step 2: Update the tool description to mention abstention**

In the `recall` tool's `mcp.WithDescription(...)` (`tools.go:43`), append a sentence so the agent acts on the signal. Change the description string to end with:

```
... The web does not know the user's personal context; this does. The response includes an "abstain" flag: when true, cortex has nothing relevant — tell the user you don't have that information rather than guessing.
```

- [ ] **Step 3: Build to verify it compiles**

Run: `go build ./...`
Expected: success, no errors.

- [ ] **Step 4: Run the full suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/cortex/mcp/tools.go
git commit -m "feat: surface abstention hint in MCP recall tool"
```

---

### Task 9: Update CORTEX.md agent contract for abstention

**Files:**
- Modify: `cmd/cortex/init_schema.go` (the embedded `CORTEX.md` template)
- Test: build only

**Interfaces:**
- Consumes: nothing new.
- Produces: the `CORTEX.md` dropped by `cortex init-schema` instructs agents to honor the `abstain` flag.

- [ ] **Step 1: Locate the recall guidance in the template**

Run: `grep -n "recall\|abstain\|don't know\|fabricate" cmd/cortex/init_schema.go`
Expected: finds the recall section of the embedded contract.

- [ ] **Step 2: Add an abstention instruction**

In the recall guidance block of the embedded `CORTEX.md` string in `cmd/cortex/init_schema.go`, add a bullet:

```
- When `recall` returns `"abstain": true`, cortex has no relevant knowledge. Say you don't have that information — do not invent an answer or fall back to assumptions.
```

(Match the surrounding markdown bullet style exactly; if the template uses a numbered list or a different prefix, follow it.)

- [ ] **Step 3: Build to verify it compiles**

Run: `go build ./...`
Expected: success.

- [ ] **Step 4: Commit**

```bash
git add cmd/cortex/init_schema.go
git commit -m "docs: instruct agents to honor recall abstention in CORTEX.md"
```

---

## Self-Review

**Spec coverage** (Tier 1 = items 1.1, 1.2, 1.3 from the research doc):
- **1.1 Fix memory retrieval (G1, G2):** Tasks 1–4 (FTS table + write sync, FTS-ranked search with backfill, memory vector search, wired into recall). ✅
- **1.2 Two-tier recall (source-chunk re-injection):** Task 6. ✅
- **1.3 Confidence-weighted ranking + abstention (G6, G7):** Tasks 5, 7, 8, 9. ✅

**Placeholder scan:** No TBD/TODO/"handle edge cases" — every code step shows complete code and exact paths. ✅

**Type consistency:**
- `RecallResult{Results, Strength, Abstain}` defined in Task 7, consumed in Task 8. ✅
- `ftsQuery` defined in Task 2, used in `SearchMemories` (Task 2). `SearchKeyword` may also adopt it but is not required for Tier 1; left as-is to keep scope tight (note for a follow-up).
- `firstChunkBySource` defined Task 6, used Task 6 in both `recallMemories` and `recallMemoryVector`. ✅
- `recallMemoryVector` defined Task 4, modified Task 6 — both tasks touch `recall.go`; if executed out of order, Task 6's edit assumes Task 4's function exists. Dependency is linear (4 → 6); execute in order. ✅
- Sub-query key space: `recallMemories` and `recallMemoryVector` both key results `mem:<id>`, so RRF fuses keyword + vector hits on the same memory rather than double-counting distinct entries. Intended. ✅

**Known scope edges (documented, not gaps):**
- `SearchKeyword` still passes raw query to FTS (pre-existing); `ftsQuery` could be applied there too — deferred, out of Tier 1 scope.
- Re-injection links memory→chunk by shared `source` label only (the sole link available today). Memories ingested without a source, or sources with many chunks, get the first chunk or none. A precise memory↔chunk FK is Tier 2+ work.

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-06-22-tier1-recall-quality.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

**Which approach?**
