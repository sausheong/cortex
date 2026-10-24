# Memory.Static Field Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `Static bool` field to `Memory`, classified by the LLM extractor, that exempts identity/preference facts from confidence decay and promotes them into a profile's `static[]` section regardless of age.

**Architecture:** A single bool column on the `memories` table (idempotent migration, default 0). The field flows: extractor prompt → provider JSON parse → `Memory.Static` → `PutMemory` INSERT → memory SELECT/scan paths. Two consumers read it: `DecayConfidence` excludes static memories from its scan, and `partitionMemories` routes static memories to `static[]`.

**Tech Stack:** Go 1.22+, embedded SQLite (modernc.org/sqlite, no CGo), root `package cortex`.

## Global Constraints

- Module `github.com/sausheong/cortex`; root `package cortex`; Go 1.22+, pure-Go no CGo.
- Field default is **false** everywhere (safe, decaying default). SQLite stores it as `INTEGER NOT NULL DEFAULT 0`.
- Migration uses the existing idempotent `ensureColumn(db, table, column, ddl)` helper in `store.go`; old DBs default existing rows to 0.
- Tests hermetic; root-package tests use `package cortex`, the `openTestDB(t)` helper (entity_test.go), and the in-package `mockLLM` (recall_test.go). Provider/extractor tests live in their own packages.
- Commits must NOT carry a `Co-Authored-By` trailer.
- The decay scan WHERE is built as `currentlyValidClause("")` (decay.go:36) — append `AND static = 0` to it.
- `partitionMemories(mems []Memory, cfg profileConfig, now time.Time) (static, dynamic []Memory)` signature is unchanged; only its branching changes.
- SCOPE NOTE: the spec mentioned a CLI `[static]` marker in `entity get`, but `cmdEntityGet` (cmd/cortex/main.go) does NOT list memories (only attributes + relationships). That surface does not exist, so it is dropped. `Static` is visible via `recall --json` / `profile --json` (it serializes on `Memory`). No CLI changes in this plan.

---

### Task 1: Add Static field + schema migration

**Files:**
- Modify: `types.go` (add field to `Memory`)
- Modify: `store.go` (add `ensureColumn` call for `static`)
- Test: `memory_test.go` (append)

**Interfaces:**
- Produces: `Memory.Static bool` (json `static`); `memories.static` column (INTEGER NOT NULL DEFAULT 0).

- [ ] **Step 1: Write the failing test**

In `memory_test.go`:

```go
func TestMemory_StaticColumnExistsAndDefaultsFalse(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "Alice"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	// A memory written without setting Static must come back Static=false.
	m := &Memory{Content: "Alice exists", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, err := c.GetMemoriesByEntity(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d memories, want 1", len(got))
	}
	if got[0].Static {
		t.Errorf("expected Static=false by default, got true")
	}
}
```

(This test will fail to COMPILE until the scan path reads the column — that is expected; Tasks 1 and 2 together make it pass. Run after Task 2. For Task 1's own gate, use the build + a direct column check below.)

- [ ] **Step 2: Add the field**

In `types.go`, in the `Memory` struct, after the `Speaker` field add:

```go
	// Static marks an identity/preference fact (vs. an episodic one). Static
	// memories are exempt from confidence decay and surface in a profile's
	// static[] section; non-static memories decay and feed dynamic[]. Set by
	// the LLM extractor; defaults false.
	Static bool `json:"static"`
```

- [ ] **Step 3: Add the migration**

In `store.go`, after the `speaker` `ensureColumn` block and before/near the `last_decay_at` block, add:

```go
	if err := ensureColumn(db, "memories", "static", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		db.Close()
		return nil, fmt.Errorf("cortex: migrate memories.static: %w", err)
	}
```

- [ ] **Step 4: Verify build + column presence**

Run: `go build ./`
Expected: success.

Run: `go test ./ -run TestOpen -v` (existing Open/migration tests still pass)
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add types.go store.go memory_test.go
git commit -m "feat(memory): add Static field and schema migration"
```

---

### Task 2: Persist + read Static through all memory paths

**Files:**
- Modify: `memory.go` (`PutMemory` INSERT; `searchMemoriesMode`, `getMemoriesByEntityMode`, `getMemoryByID` SELECT + scan)
- Test: `memory_test.go` (the Task 1 test now passes; add a round-trip test)

**Interfaces:**
- Consumes: `Memory.Static`, `memories.static` column.
- Produces: `Static` correctly written by `PutMemory` and read by all three SELECT paths.

- [ ] **Step 1: Write the failing test**

In `memory_test.go`, append:

```go
func TestMemory_StaticRoundTrips(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "Bob"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	m := &Memory{Content: "Bob is from Seattle", EntityIDs: []string{e.ID}, Static: true}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}

	// via GetMemoriesByEntity
	got, err := c.GetMemoriesByEntity(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !got[0].Static {
		t.Fatalf("GetMemoriesByEntity: expected Static=true, got %+v", got)
	}

	// via SearchMemories (FTS)
	found, err := c.SearchMemories(ctx, "Seattle", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) == 0 || !found[0].Static {
		t.Fatalf("SearchMemories: expected Static=true, got %+v", found)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./ -run 'TestMemory_StaticRoundTrips|TestMemory_StaticColumnExistsAndDefaultsFalse' -v`
Expected: FAIL — `Static` is always false (not written/read yet).

- [ ] **Step 3: Update PutMemory INSERT**

In `memory.go`, `PutMemory`, change the INSERT to include `static`:

```go
	res, err := tx.ExecContext(ctx,
		`INSERT INTO memories (id, content, source, speaker, confidence, created_at, updated_at, valid_at, invalid_at, static)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.Content, m.Source, speaker, m.Confidence, m.CreatedAt, m.UpdatedAt, m.ValidAt, m.InvalidAt, m.Static,
	)
```

- [ ] **Step 4: Update the three SELECT paths**

In `memory.go`, in `searchMemoriesMode`, `getMemoriesByEntityMode`, and `getMemoryByID`:

(a) add `, m.static` (or `, static` for the unqualified getMemoryByID query) to the SELECT column list, immediately after `expired_at`. The three current SELECT lists are:
- `searchMemoriesMode`: `SELECT m.id, m.content, m.source, m.speaker, m.confidence, m.created_at, m.updated_at, m.valid_at, m.invalid_at, m.expired_at` → append `, m.static`
- `getMemoriesByEntityMode`: same `m.`-qualified list → append `, m.static`
- `getMemoryByID`: `SELECT id, content, source, speaker, confidence, created_at, updated_at, valid_at, invalid_at, expired_at` → append `, static`

(b) in each scan, add `&m.Static` as the final scanned column, after `&eat`:

```go
		if err := rows.Scan(&m.ID, &m.Content, &m.Source, &spk, &m.Confidence, &m.CreatedAt, &m.UpdatedAt, &vat, &iat, &eat, &m.Static); err != nil {
```

For `getMemoryByID` (single row):

```go
	).Scan(&m.ID, &m.Content, &m.Source, &spk, &m.Confidence, &m.CreatedAt, &m.UpdatedAt, &vat, &iat, &eat, &m.Static)
```

- [ ] **Step 5: Run to verify both tests pass**

Run: `go test ./ -run 'TestMemory_StaticRoundTrips|TestMemory_StaticColumnExistsAndDefaultsFalse' -v`
Expected: PASS.

- [ ] **Step 6: Run the full package to check for regressions**

Run: `go test ./`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add memory.go memory_test.go
git commit -m "feat(memory): persist and read Static across all memory paths"
```

---

### Task 3: Decay exemption for static memories

**Files:**
- Modify: `decay.go` (scan WHERE)
- Test: `decay_test.go` (append)

**Interfaces:**
- Consumes: `memories.static` column.
- Produces: static memories never decayed or pruned.

- [ ] **Step 1: Write the failing test**

In `decay_test.go`, append (match the file's existing setup style — it uses `openTestDB(t)` and writes memories with explicit old `created_at`):

```go
func TestDecay_SkipsStaticMemories(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "Alice"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	// A static memory and a non-static memory, both old and low-confidence.
	stat := &Memory{Content: "Alice is from Seattle", EntityIDs: []string{e.ID}, Confidence: 0.2, Static: true}
	if err := c.PutMemory(ctx, stat); err != nil {
		t.Fatal(err)
	}
	dyn := &Memory{Content: "Alice has a meeting", EntityIDs: []string{e.ID}, Confidence: 0.2, Static: false}
	if err := c.PutMemory(ctx, dyn); err != nil {
		t.Fatal(err)
	}
	// Age both far back so decay would fire.
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, id := range []string{stat.ID, dyn.ID} {
		if _, err := c.db.ExecContext(ctx, `UPDATE memories SET created_at = ?, last_decay_at = NULL WHERE id = ?`, old, id); err != nil {
			t.Fatal(err)
		}
	}

	rep, err := c.DecayConfidence(ctx)
	if err != nil {
		t.Fatal(err)
	}

	// Only the dynamic memory should appear in the decay changes.
	for _, ch := range rep.Changes {
		if ch.ID == stat.ID {
			t.Errorf("static memory %s should not be decayed", stat.ID)
		}
	}
	// Sanity: the dynamic memory WAS decayed (proves the test setup triggers decay).
	sawDyn := false
	for _, ch := range rep.Changes {
		if ch.ID == dyn.ID {
			sawDyn = true
		}
	}
	if !sawDyn {
		t.Error("expected the non-static memory to be decayed")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./ -run TestDecay_SkipsStaticMemories -v`
Expected: FAIL — the static memory appears in `rep.Changes`.

- [ ] **Step 3: Exclude static from the decay scan**

In `decay.go`, change the scan query WHERE (currently `WHERE `+currentlyValidClause("")`) to:

```go
	rows, err := c.db.QueryContext(ctx,
		`SELECT id, content, confidence, created_at, last_decay_at
		 FROM memories
		 WHERE `+currentlyValidClause("")+` AND static = 0`,
	)
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./ -run TestDecay_SkipsStaticMemories -v`
Expected: PASS.

- [ ] **Step 5: Run decay + maintain tests for regressions**

Run: `go test ./ -run 'TestDecay|TestMaintain' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add decay.go decay_test.go
git commit -m "feat(decay): exempt static memories from confidence decay"
```

---

### Task 4: Profile partition routes static → static[]

**Files:**
- Modify: `profile.go` (`partitionMemories`)
- Test: `profile_test.go` (append)

**Interfaces:**
- Consumes: `Memory.Static`.
- Produces: a `Static==true` memory always lands in `static[]`, never `dynamic[]`, regardless of `CreatedAt`.

- [ ] **Step 1: Write the failing test**

In `profile_test.go`, append:

```go
func TestPartitionMemories_StaticAlwaysStatic(t *testing.T) {
	now := time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC)
	cfg := profileConfig{recentK: 5, window: 30 * 24 * time.Hour, staticCap: 15}

	mk := func(content string, daysAgo int, static bool) Memory {
		return Memory{Content: content, Confidence: 0.9, CreatedAt: now.AddDate(0, 0, -daysAgo), Static: static}
	}
	mems := []Memory{
		mk("identity-today", 0, true),   // static, created TODAY -> must be static[]
		mk("episode-today", 1, false),   // non-static, recent -> dynamic[]
		mk("identity-old", 200, true),   // static, old -> static[]
	}

	static, dynamic := partitionMemories(mems, cfg, now)

	inStatic := func(s string) bool {
		for _, m := range static {
			if m.Content == s {
				return true
			}
		}
		return false
	}
	inDynamic := func(s string) bool {
		for _, m := range dynamic {
			if m.Content == s {
				return true
			}
		}
		return false
	}

	if !inStatic("identity-today") {
		t.Error("static memory created today must be in static[]")
	}
	if inDynamic("identity-today") {
		t.Error("static memory must never be in dynamic[]")
	}
	if !inStatic("identity-old") {
		t.Error("old static memory must be in static[]")
	}
	if !inDynamic("episode-today") {
		t.Error("recent non-static memory must be in dynamic[]")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./ -run TestPartitionMemories_StaticAlwaysStatic -v`
Expected: FAIL — `identity-today` currently lands in `dynamic[]` (recency-only).

- [ ] **Step 3: Add static-first routing**

In `profile.go`, replace the body of `partitionMemories` with the static-aware version (the recency logic is unchanged for non-static memories):

```go
func partitionMemories(mems []Memory, cfg profileConfig, now time.Time) (static, dynamic []Memory) {
	cutoff := now.Add(-cfg.window)

	var recent, rest []Memory
	for _, m := range mems {
		// Static memories are never dynamic: route straight to the static pool.
		if m.Static {
			rest = append(rest, m)
			continue
		}
		if m.CreatedAt.After(cutoff) || m.CreatedAt.Equal(cutoff) {
			recent = append(recent, m)
		} else {
			rest = append(rest, m)
		}
	}

	// Dynamic: newest first, capped to recentK. Overflow falls back to static.
	sort.SliceStable(recent, func(i, j int) bool {
		return recent[i].CreatedAt.After(recent[j].CreatedAt)
	})
	if len(recent) > cfg.recentK {
		rest = append(rest, recent[cfg.recentK:]...)
		recent = recent[:cfg.recentK]
	}
	dynamic = recent

	// Static: confidence desc, then recency desc; capped to staticCap.
	sort.SliceStable(rest, func(i, j int) bool {
		if rest[i].Confidence != rest[j].Confidence {
			return rest[i].Confidence > rest[j].Confidence
		}
		return rest[i].CreatedAt.After(rest[j].CreatedAt)
	})
	if len(rest) > cfg.staticCap {
		rest = rest[:cfg.staticCap]
	}
	static = rest
	return static, dynamic
}
```

- [ ] **Step 4: Run to verify it passes**

Run: `go test ./ -run TestPartitionMemories -v`
Expected: PASS (the new test AND the existing `TestPartitionMemories_*` tests, which use non-static memories).

- [ ] **Step 5: Run profile tests for regressions**

Run: `go test ./ -run 'TestProfile|TestPartition|TestRefreshProfiles' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add profile.go profile_test.go
git commit -m "feat(profile): route static memories to static[] regardless of age"
```

---

### Task 5: Extractor classifies static (prompt + both providers)

**Files:**
- Modify: `extractor/llmext/extractor.go` (prompt)
- Modify: `llm/openai/llm.go` (memory parse struct in `parseExtractionJSON`)
- Modify: `llm/anthropic/llm.go` (memory parse struct)
- Test: `llm/openai/llm_test.go` (append, mirroring the existing `TestParseExtractionJSON_ReadsValidAt`)

**Interfaces:**
- Consumes: LLM JSON with optional `"static"` per memory.
- Produces: `Memory.Static` set from extracted JSON; bare-string memory → false.

CONFIRMED (verified against source): `llm/openai/llm.go` has a named, hermetic
parser `func parseExtractionJSON(raw string) (*cortex.Extraction, error)` (line
~321), already covered by hermetic tests like `TestParseExtractionJSON_ReadsValidAt`
(llm_test.go) — no live key needed. Use that exact pattern. Note the signature
takes a `string`, not `[]byte`.

- [ ] **Step 1: Write the failing test**

In `llm/openai/llm_test.go`, append (mirroring `TestParseExtractionJSON_ReadsValidAt`):

```go
func TestParseExtractionJSON_ReadsStatic(t *testing.T) {
	raw := `{"entities":[],"relationships":[],"memories":[` +
		`{"content":"Bob is from Seattle","confidence":1.0,"static":true},` +
		`{"content":"Bob has a meeting","confidence":1.0,"static":false}]}`
	ex, err := parseExtractionJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(ex.Memories) != 2 {
		t.Fatalf("got %d memories, want 2", len(ex.Memories))
	}
	if !ex.Memories[0].Static {
		t.Error("expected first memory Static=true")
	}
	if ex.Memories[1].Static {
		t.Error("expected second memory Static=false")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./llm/openai/ -run TestParseExtractionJSON_ReadsStatic -v`
Expected: FAIL — `static` not parsed yet.

- [ ] **Step 3: Extend the extractor prompt**

In `extractor/llmext/extractor.go`, in `extractionPrompt`:

(a) change the memories line to mention `static`:
```
- "memories": array of objects with "content" (a concise factual statement), optional "entity_ids", optional "static" (boolean), and "confidence"
```

(b) after the confidence guidance block, add:
```
For each memory, "static" marks whether it is a stable fact:
- true  = identity facts and durable preferences (role, hometown,
          "prefers dark mode", relationships, long-term traits)
- false = episodic or time-bound facts (meetings, "learning X this week",
          "exam tomorrow", anything with a date or that will expire)
When unsure, use false.
```

- [ ] **Step 4: Parse `static` in both providers**

In `llm/openai/llm.go`, in the memory parse struct (`memObj`), add the field and pass it through:

```go
		var memObj struct {
			Content    string  `json:"content"`
			Confidence float64 `json:"confidence"`
			ValidAt    string  `json:"valid_at"`
			Static     bool    `json:"static"`
		}
		if err := json.Unmarshal(m, &memObj); err == nil && memObj.Content != "" {
			extraction.Memories = append(extraction.Memories, cortex.Memory{
				Content:    memObj.Content,
				Confidence: memObj.Confidence,
				ValidAt:    cortex.ParseEventTime(memObj.ValidAt),
				Static:     memObj.Static,
			})
			continue
		}
```

Apply the identical change to `llm/anthropic/llm.go` (same `memObj` struct + same `cortex.Memory{}` field).

- [ ] **Step 5: Run to verify it passes**

Run: `go test ./llm/openai/ -run TestParseExtractionJSON_ReadsStatic -v`
Expected: PASS.

- [ ] **Step 6: Build everything**

Run: `go build ./...`
Expected: success.

- [ ] **Step 7: Commit**

```bash
git add extractor/llmext/extractor.go llm/openai/llm.go llm/anthropic/llm.go llm/openai/llm_test.go
git commit -m "feat(extract): classify memory static at ingest (prompt + providers)"
```

---

### Task 6: Full verification + docs

**Files:** none (verification) + `README.md` (optional doc touch)

- [ ] **Step 1: Full offline test suite**

Run: `OPENAI_API_KEY= go test ./...`
Expected: all packages PASS (the `llm/openai` and `llm/anthropic` live integration tests skip themselves when the key is unset).

- [ ] **Step 2: Vet + build**

Run: `go vet ./... && go build ./...`
Expected: no output / success.

- [ ] **Step 3: README (optional)**

If the README's Knowledge Model / memory section enumerates memory fields or describes decay, add a sentence that identity/preference memories are marked `static` (LLM-classified), are exempt from decay, and populate a profile's `static[]`. Match existing tone. Commit:

```bash
git add README.md
git commit -m "docs(memory): document static memories"
```

---

## Self-Review

**Spec coverage:**
- `Static bool` field + migration (default false) → Task 1. ✓
- Persist + read across PutMemory + 3 SELECT paths → Task 2. ✓
- Decay exemption (`AND static = 0`) → Task 3. ✓
- Profile partition static-first → Task 4. ✓
- Extractor prompt + both provider parses → Task 5. ✓
- LLM-during-extraction classification → Task 5. ✓
- Tests at memory/decay/profile/extractor layers → Tasks 1–5. ✓
- Full verification → Task 6. ✓
- CLI `[static]` marker → DROPPED (entity get lists no memories; documented in Global Constraints). Spec deviation, intentional.

**Placeholder scan:** No TBD/TODO. Task 5 Step 1 carries an explicit branch (real parser vs mockLLM-flow fallback) with a reporting instruction rather than a placeholder — the implementer picks the path that fits the actual code and reports it. This is a genuine unknown about the provider file's structure, bounded with concrete alternatives.

**Type consistency:** `Memory.Static bool` defined once (Task 1), read in Task 2 scans, consumed in Task 3 (SQL) and Task 4 (`m.Static`), produced in Task 5 (`memObj.Static` → `cortex.Memory{Static:...}`). `partitionMemories` signature unchanged. `ensureColumn`/`currentlyValidClause` names verified against source.

**Note on `/docs` ignore:** This plan lives under `docs/` which is gitignored; it is intentionally not committed.
