# Tier 3 Phase 3a: Event-Time Extraction Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make memories carry **event time** (`valid_at` — when the fact became true in the world), extracted from the ingested text, so the bi-temporal schema added in Tier 2a stops being empty. Today `valid_at` is NULL on every normally-ingested memory; after this phase, "Alice joined Stripe in March 2026" stores `valid_at = 2026-03-01`, and `cortex recall ... --as-of 2026-02` correctly excludes it. This is the deferred Tier 2.3 work and the prerequisite for Phase 3b's time-aware query expansion (research item 3.2).

**Architecture:** Three cohesive changes. (1) `PutMemory` persists `valid_at`/`invalid_at` on insert (currently it writes neither, so extractor-supplied event-time is silently dropped). (2) A shared, lenient date parser `parseEventTime` in the `cortex` package turns natural-language and ISO dates ("March 2026", "2026-03", "2026-03-15", RFC3339) into a `*time.Time`, degrading gracefully to `nil` on anything it can't parse. (3) Both LLM extractors (anthropic, openai) gain a `valid_at` field in their memory JSON, an updated extraction prompt asking for it, and a parse step that runs the shared parser. The end-to-end chain `Remember → extractor sets Memory.ValidAt → PutMemory persists it → recall WithAsOf filters on it` is validated by a final integration test.

**Tech Stack:** Go 1.25.1, `modernc.org/sqlite` (pure-Go, no CGo), SQLite FTS5, Anthropic + OpenAI provider SDKs. Tests use the in-package helpers `openTestDB`, `openTestDBWithEmbedder`, `mockLLM` (`recall_test.go:266`), `testEmbedder` (`search_test.go:25`), and the provider packages' own `parseExtractionJSON` tests (no live API).

## Global Constraints

- **Module path:** `github.com/sausheong/cortex` (root package is `cortex`).
- **No CGo, single binary.** Only existing dependencies — no new ones. The date parser uses only the standard library `time` package.
- **Backward compatibility:** existing databases must keep working. The `valid_at`/`invalid_at`/`expired_at` columns already exist (added in Tier 2a via `ensureColumn`, `store.go:160-165`). No new migration. Memories ingested before this phase keep `valid_at = NULL` and remain "currently valid" — unchanged behavior.
- **Bi-temporal semantics (do not change):** `valid_at` is **event time** (when the fact became true). `invalid_at` is event-time end (set by `InvalidateMemory`/reconcile, not at ingest). `expired_at` is **ingestion time** of retirement (set only by `InvalidateMemory`, *never* at insert — a freshly-ingested memory is never born retired). The currently-valid predicate is `(expired_at IS NULL AND invalid_at IS NULL)` (`temporal.go:23-26`); a memory with only `valid_at` set is still currently valid.
- **Graceful degradation:** unparseable or absent event-time must yield `nil` (NULL in DB), never an error that fails an ingest. An LLM that omits `valid_at` must behave exactly as today.
- **Confidence invariant:** unchanged. `coerceConfidence` maps zero/NaN → 1.0 (`types.go:269`).
- **Commit trailer:** do NOT add a `Co-Authored-By` trailer to commits (user preference for this repo).
- **Test command:** `go test ./...` must pass. Hermetic only — no live API calls. (`llm/openai` live tests fail on HTTP 429 quota — pre-existing, out of scope; default `go test ./...` excludes them as they need real keys.)
- **TDD:** write the failing test first, watch it fail, implement minimally, watch it pass, commit.

---

## File Structure

| File | Responsibility | Change |
|------|----------------|--------|
| `memory.go` | memory CRUD | `PutMemory`: persist `valid_at`/`invalid_at` on insert when set |
| `temporal.go` | temporal helpers | Add `parseEventTime(s string) *time.Time` (shared, lenient date parser) |
| `temporal_test.go` | temporal tests | Table-test `parseEventTime` formats |
| `memory_test.go` | memory tests | `PutMemory` round-trips `ValidAt` |
| `llm/anthropic/llm.go` | anthropic extractor | `valid_at` in memory JSON + prompt + parse |
| `llm/anthropic/llm_test.go` | anthropic tests | `parseExtractionJSON` reads `valid_at` |
| `llm/openai/llm.go` | openai extractor | `valid_at` in memory JSON + prompt + parse |
| `llm/openai/llm_test.go` | openai tests | `parseExtractionJSON` reads `valid_at` |
| `cortex_test.go` | integration test | `Remember` → `Recall(WithAsOf)` end-to-end |

---

### Task 1: PutMemory persists event-time on insert

**Files:**
- Modify: `memory.go` (`PutMemory`, `memory.go:23-71`)
- Test: `memory_test.go`

**Interfaces:**
- Consumes: the `Memory.ValidAt`/`InvalidAt` pointer fields (`types.go:51-53`), already populated by callers that set event-time.
- Produces: `PutMemory` writes `valid_at` and `invalid_at` into the `memories` row when the corresponding pointer is non-nil (NULL otherwise). `expired_at` is deliberately **not** written here — a freshly-ingested memory is never retired. After this task, `getMemoryByID` round-trips `ValidAt` set via `PutMemory`.

- [ ] **Step 1: Write the failing test**

Add to `memory_test.go`:

```go
func TestPutMemory_PersistsValidAt(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	when := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	m := &Memory{Content: "Alice joined Stripe", ValidAt: &when}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}

	got, err := c.getMemoryByID(ctx, m.ID)
	if err != nil {
		t.Fatalf("getMemoryByID: %v", err)
	}
	if got == nil {
		t.Fatal("expected memory, got nil")
	}
	if got.ValidAt == nil {
		t.Fatal("expected ValidAt to be persisted, got nil")
	}
	if !got.ValidAt.Equal(when) {
		t.Fatalf("ValidAt mismatch: got %v want %v", got.ValidAt, when)
	}
	// A memory with only valid_at set must still be currently valid (not retired).
	if got.InvalidAt != nil || got.ExpiredAt != nil {
		t.Fatalf("expected invalid_at/expired_at nil, got invalid=%v expired=%v", got.InvalidAt, got.ExpiredAt)
	}
}
```

`time` is already imported in `memory_test.go`? Verify with `grep -n '"time"' memory_test.go`; if absent, add it.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestPutMemory_PersistsValidAt -v`
Expected: FAIL — `got.ValidAt` is nil because the current INSERT (`memory.go:36-40`) never writes `valid_at`.

- [ ] **Step 3: Persist valid_at / invalid_at in the insert**

In `memory.go`, `PutMemory` currently inserts six columns (`memory.go:36-40`). Extend the insert to include `valid_at` and `invalid_at`, passing the pointer values directly — `database/sql` writes a nil `*time.Time` as SQL NULL, and a non-nil one as the time. Replace the insert block:

```go
	res, err := tx.ExecContext(ctx,
		`INSERT INTO memories (id, content, source, confidence, created_at, updated_at, valid_at, invalid_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.Content, m.Source, m.Confidence, m.CreatedAt, m.UpdatedAt, m.ValidAt, m.InvalidAt,
	)
	if err != nil {
		return fmt.Errorf("cortex: insert memory: %w", err)
	}
```

Do **not** write `expired_at` — it stays NULL at insert (retirement is `InvalidateMemory`'s job). Leave the rest of `PutMemory` (FTS sync, entity links, commit) unchanged.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ -run TestPutMemory_PersistsValidAt -v`
Expected: PASS.

- [ ] **Step 5: Run the full suite**

Run: `go test ./...`
Expected: PASS. (Existing `PutMemory` callers pass nil pointers, so they now write NULL explicitly — identical to the prior implicit-NULL behavior.)

- [ ] **Step 6: Commit**

```bash
git add memory.go memory_test.go
git commit -m "feat: persist memory event-time (valid_at) on insert"
```

---

### Task 2: Lenient event-time date parser

**Files:**
- Modify: `temporal.go` (add `parseEventTime`)
- Test: `temporal_test.go`

**Interfaces:**
- Consumes: an arbitrary date string from an LLM extractor (provider packages will call it).
- Produces: `parseEventTime(s string) *time.Time` — tries a fixed list of layouts (most specific first), returns a UTC `*time.Time` on the first match, `nil` for empty/unparseable input. Exported as a package-level function (lowercase is fine since providers import the `cortex` package and call `cortex.parseEventTime`? **No** — providers are external packages and cannot see unexported identifiers. Export it as `ParseEventTime`.)

> **Naming decision (resolve before coding):** the providers (`llm/anthropic`, `llm/openai`) are separate packages that import `cortex`. They can only call an **exported** helper. Name it **`ParseEventTime`** (exported). The in-package test may call it directly.

- [ ] **Step 1: Write the failing test**

Add to `temporal_test.go`:

```go
func TestParseEventTime(t *testing.T) {
	mustUTC := func(y int, mo time.Month, d int) *time.Time {
		v := time.Date(y, mo, d, 0, 0, 0, 0, time.UTC)
		return &v
	}

	cases := []struct {
		in   string
		want *time.Time
	}{
		{"", nil},
		{"   ", nil},
		{"not a date", nil},
		{"2026-03-15", mustUTC(2026, 3, 15)},
		{"2026-03", mustUTC(2026, 3, 1)},
		{"2026", mustUTC(2026, 1, 1)},
		{"March 2026", mustUTC(2026, 3, 1)},
		{"Mar 2026", mustUTC(2026, 3, 1)},
		{"2026-03-15T00:00:00Z", mustUTC(2026, 3, 15)},
	}

	for _, tc := range cases {
		got := ParseEventTime(tc.in)
		if tc.want == nil {
			if got != nil {
				t.Errorf("ParseEventTime(%q) = %v, want nil", tc.in, got)
			}
			continue
		}
		if got == nil {
			t.Errorf("ParseEventTime(%q) = nil, want %v", tc.in, tc.want)
			continue
		}
		if !got.Equal(*tc.want) {
			t.Errorf("ParseEventTime(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestParseEventTime -v`
Expected: FAIL — `ParseEventTime` undefined.

- [ ] **Step 3: Implement ParseEventTime**

Add to `temporal.go` (it already imports `time` and `fmt`; add `strings`):

```go
// eventTimeLayouts are the date formats ParseEventTime accepts, tried in
// order. More specific formats come first so "2026-03-15" is not matched as
// a bare year. All are interpreted in UTC; a month-only or year-only date
// resolves to the first day (and first month) of the period.
var eventTimeLayouts = []string{
	time.RFC3339,
	"2006-01-02",
	"2006-01",
	"January 2006",
	"Jan 2006",
	"2006",
}

// ParseEventTime turns a natural-language or ISO date string into a UTC
// *time.Time, or nil when the string is empty or unparseable. It is the
// lenient front-door for LLM-supplied event times (memory valid_at): the
// extractor may emit "March 2026", "2026-03", an RFC3339 stamp, or nothing
// at all, and any failure degrades to nil (NULL in storage) rather than
// failing the ingest.
func ParseEventTime(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, layout := range eventTimeLayouts {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}
```

`time.ParseInLocation` with a UTC location ensures month/year-only inputs resolve in UTC rather than local time. `fmt` may become unused in `temporal.go` if it was only used elsewhere — it is still used by `currentlyValidClause`/`validAsOfClause`, so leave it.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ -run TestParseEventTime -v`
Expected: PASS.

- [ ] **Step 5: Run the full suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add temporal.go temporal_test.go
git commit -m "feat: lenient event-time date parser (ParseEventTime)"
```

---

### Task 3: Anthropic extractor emits and parses valid_at

**Files:**
- Modify: `llm/anthropic/llm.go` (`defaultExtractionPrompt`, `extractionJSON`, `parseExtractionJSON`)
- Test: `llm/anthropic/llm_test.go`

**Interfaces:**
- Consumes: `cortex.ParseEventTime` (Task 2); the existing `parseExtractionJSON` flow.
- Produces: when the LLM returns a memory object with a `valid_at` string, the resulting `cortex.Memory` has `ValidAt` set (via `ParseEventTime`). When `valid_at` is absent/empty/unparseable, `ValidAt` stays nil — current behavior preserved. The extraction prompt asks the model to include `valid_at` when the text states when a fact became true.

- [ ] **Step 1: Write the failing test**

First inspect the existing test file for the parse-test pattern: `grep -n "parseExtractionJSON\|func Test" llm/anthropic/llm_test.go`. Add a test mirroring that style:

```go
func TestParseExtractionJSON_ReadsValidAt(t *testing.T) {
	raw := `{"entities":[],"relationships":[],"memories":[
		{"content":"Alice joined Stripe","valid_at":"2026-03"},
		{"content":"Bob likes tea"}
	]}`
	ex, err := parseExtractionJSON(raw)
	if err != nil {
		t.Fatalf("parseExtractionJSON: %v", err)
	}
	if len(ex.Memories) != 2 {
		t.Fatalf("expected 2 memories, got %d", len(ex.Memories))
	}
	if ex.Memories[0].ValidAt == nil {
		t.Fatal("expected ValidAt set on first memory")
	}
	want := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if !ex.Memories[0].ValidAt.Equal(want) {
		t.Fatalf("ValidAt = %v, want %v", ex.Memories[0].ValidAt, want)
	}
	if ex.Memories[1].ValidAt != nil {
		t.Fatal("expected nil ValidAt on memory without valid_at")
	}
}
```

Ensure `"time"` is imported in `llm/anthropic/llm_test.go` (add if missing).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./llm/anthropic/ -run TestParseExtractionJSON_ReadsValidAt -v`
Expected: FAIL — the memory object parse (`llm.go:283-286`) has no `valid_at` field, so `ValidAt` is nil.

- [ ] **Step 3: Parse valid_at in the memory object**

In `llm/anthropic/llm.go`, the memory-object branch of `parseExtractionJSON` (`llm.go:282-300`) unmarshals `{content, confidence}`. Add `valid_at` and run it through `cortex.ParseEventTime`:

```go
	for _, m := range ej.Memories {
		var memObj struct {
			Content    string  `json:"content"`
			Confidence float64 `json:"confidence"`
			ValidAt    string  `json:"valid_at"`
		}
		if err := json.Unmarshal(m, &memObj); err == nil && memObj.Content != "" {
			extraction.Memories = append(extraction.Memories, cortex.Memory{
				Content:    memObj.Content,
				Confidence: memObj.Confidence,
				ValidAt:    cortex.ParseEventTime(memObj.ValidAt),
			})
			continue
		}
		var memStr string
		if err := json.Unmarshal(m, &memStr); err == nil && memStr != "" {
			extraction.Memories = append(extraction.Memories, cortex.Memory{
				Content: memStr,
			})
		}
	}
```

- [ ] **Step 4: Update the extraction prompt**

In `defaultExtractionPrompt` (`llm.go:14-24`), change the memories bullet and add a guidance line. Replace:

```
- "memories": array of objects with "content" (string, a concise factual statement)
```

with:

```
- "memories": array of objects with "content" (string, a concise factual statement) and optional "valid_at" (string: when the fact became true in the world, e.g. "2026-03", "March 2026", "2026-03-15"; omit if the text gives no time)
```

And add, just before the final "Return ONLY valid JSON" line:

```
When a memory states when something became true (a start date, a join date, an "as of" date), set "valid_at" to that date. Do not guess; omit it if the text does not say.
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./llm/anthropic/ -run TestParseExtractionJSON_ReadsValidAt -v`
Expected: PASS.

- [ ] **Step 6: Run the package suite**

Run: `go test ./llm/anthropic/`
Expected: PASS (existing hermetic parse tests unaffected; `valid_at` is additive and optional).

- [ ] **Step 7: Commit**

```bash
git add llm/anthropic/llm.go llm/anthropic/llm_test.go
git commit -m "feat: anthropic extractor emits and parses memory valid_at"
```

---

### Task 4: OpenAI extractor emits and parses valid_at

**Files:**
- Modify: `llm/openai/llm.go` (`defaultExtractionPrompt`, `parseExtractionJSON`)
- Test: `llm/openai/llm_test.go`

**Interfaces:**
- Consumes: `cortex.ParseEventTime` (Task 2).
- Produces: identical behavior to Task 3, for the OpenAI provider. The two providers are near-duplicate by design; this mirrors Task 3 exactly.

- [ ] **Step 1: Write the failing test**

Inspect: `grep -n "parseExtractionJSON\|func Test\|\"time\"" llm/openai/llm_test.go`. Add (mirroring Task 3, adjust package-local helpers if the file uses a different style):

```go
func TestParseExtractionJSON_ReadsValidAt(t *testing.T) {
	raw := `{"entities":[],"relationships":[],"memories":[
		{"content":"Alice joined Stripe","valid_at":"2026-03"},
		{"content":"Bob likes tea"}
	]}`
	ex, err := parseExtractionJSON(raw)
	if err != nil {
		t.Fatalf("parseExtractionJSON: %v", err)
	}
	if len(ex.Memories) != 2 {
		t.Fatalf("expected 2 memories, got %d", len(ex.Memories))
	}
	if ex.Memories[0].ValidAt == nil {
		t.Fatal("expected ValidAt set on first memory")
	}
	want := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	if !ex.Memories[0].ValidAt.Equal(want) {
		t.Fatalf("ValidAt = %v, want %v", ex.Memories[0].ValidAt, want)
	}
	if ex.Memories[1].ValidAt != nil {
		t.Fatal("expected nil ValidAt on memory without valid_at")
	}
}
```

Ensure `"time"` is imported in `llm/openai/llm_test.go`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./llm/openai/ -run TestParseExtractionJSON_ReadsValidAt -v`
Expected: FAIL — no `valid_at` field in the parse (`llm.go:291-302`).

- [ ] **Step 3: Parse valid_at in the memory object**

In `llm/openai/llm.go`, the memory-object branch (`llm.go:291-302`) — apply the same change as Task 3 Step 3:

```go
	for _, m := range ej.Memories {
		var memObj struct {
			Content    string  `json:"content"`
			Confidence float64 `json:"confidence"`
			ValidAt    string  `json:"valid_at"`
		}
		if err := json.Unmarshal(m, &memObj); err == nil && memObj.Content != "" {
			extraction.Memories = append(extraction.Memories, cortex.Memory{
				Content:    memObj.Content,
				Confidence: memObj.Confidence,
				ValidAt:    cortex.ParseEventTime(memObj.ValidAt),
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

- [ ] **Step 4: Update the extraction prompt**

In `defaultExtractionPrompt` (`llm.go:15-25`), apply the same two prompt edits as Task 3 Step 4 (memories bullet + guidance line before "Return ONLY valid JSON").

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./llm/openai/ -run TestParseExtractionJSON_ReadsValidAt -v`
Expected: PASS.

- [ ] **Step 6: Run the package suite**

Run: `go test ./llm/openai/`
Expected: PASS for hermetic tests. (Any live test needing real API keys / failing on 429 is pre-existing and out of scope — confirm the only failures, if any, are live-tagged or key-gated, not the new parse test.)

- [ ] **Step 7: Commit**

```bash
git add llm/openai/llm.go llm/openai/llm_test.go
git commit -m "feat: openai extractor emits and parses memory valid_at"
```

---

### Task 5: End-to-end event-time ingest → as-of recall

**Files:**
- Test: `cortex_test.go` (integration test using `mockLLM` extractor + `WithAsOf`)

**Interfaces:**
- Consumes: the whole chain — a mock extractor returning a `Memory` with `ValidAt` set, `Remember` storing it via `PutMemory` (Task 1), and `Recall(..., WithAsOf(t))` filtering on `valid_at` through `validAsOfClause` (`temporal.go:34`).
- Produces: proof that event-time flows end to end. A memory valid as of 2026-03 is **excluded** from a recall as-of 2026-02 and **included** from a recall as-of 2026-04. No production code change — if this fails, an earlier task is wrong.

- [ ] **Step 1: Write the test**

Inspect the existing extractor-mock pattern (`cortex_test.go:15`, `cortex_test.go:391-397` define `mockExtractor` with `extractFn`). Reuse it. Add to `cortex_test.go`:

```go
func TestRemember_EventTimeFlowsToAsOfRecall(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	when := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	c.SetExtractor(&mockExtractor{
		extractFn: func(_ context.Context, _, _ string) (*Extraction, error) {
			return &Extraction{
				Memories: []Memory{
					{Content: "Alice joined Stripe", ValidAt: &when},
				},
			}, nil
		},
	})
	// Force the memory_lookup path so recall reads memories (with temporal mode).
	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
			return []StructuredQuery{
				{Type: "memory_lookup", Params: map[string]any{"query": q}},
			}, nil
		},
	})

	if err := c.Remember(ctx, "Alice joined Stripe in March 2026"); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	// As of February 2026, the fact was not yet true → excluded.
	before := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	got, err := c.Recall(ctx, "Stripe", WithAsOf(before))
	if err != nil {
		t.Fatalf("Recall before: %v", err)
	}
	for _, r := range got {
		if r.Content == "Alice joined Stripe" {
			t.Fatalf("memory should be excluded as-of %v, but was returned", before)
		}
	}

	// As of April 2026, the fact was true → included.
	after := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	got, err = c.Recall(ctx, "Stripe", WithAsOf(after))
	if err != nil {
		t.Fatalf("Recall after: %v", err)
	}
	var found bool
	for _, r := range got {
		if r.Content == "Alice joined Stripe" {
			found = true
		}
	}
	if !found {
		t.Fatalf("memory should be included as-of %v, but was not returned", after)
	}
}
```

> **Verify before coding:** confirm the mock extractor type name and field (`mockExtractor`/`extractFn`) by reading `cortex_test.go:388-398`. If the name differs, use the actual one. Confirm `"time"` is imported in `cortex_test.go`.

- [ ] **Step 2: Run the test**

Run: `go test ./ -run TestRemember_EventTimeFlowsToAsOfRecall -v`
Expected: PASS (all production code is already in place from Tasks 1–2). If it FAILS, the failure pinpoints which earlier task is incomplete — fix there, not here.

- [ ] **Step 3: Run the full suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add cortex_test.go
git commit -m "test: event-time ingest flows end-to-end to as-of recall"
```

---

## Self-Review

**Spec coverage** (Phase 3a = the deferred research item 2.3, prerequisite for 3.2):
- **Persist event-time:** Task 1 (`PutMemory` writes `valid_at`/`invalid_at`). ✅
- **Parse event-time:** Task 2 (`ParseEventTime`), Tasks 3–4 (both extractors emit + parse). ✅
- **End-to-end proof:** Task 5 (`Remember` → `WithAsOf`). ✅

**Placeholder scan:** No TBD/TODO — every code step shows complete code and exact paths. ✅

**Type / linkage consistency:**
- `ParseEventTime` is **exported** (Task 2) so the external provider packages can call `cortex.ParseEventTime` (Tasks 3, 4). A lowercase name would not compile from those packages — called out explicitly in Task 2. ✅
- `PutMemory` passes `*time.Time` pointers directly to `database/sql`, which encodes nil as NULL — consistent with how `InvalidateMemory` already handles `invalid_at`. ✅
- `expired_at` is never written at insert (Global Constraints + Task 1), preserving the currently-valid invariant: a fresh memory has `expired_at IS NULL AND invalid_at IS NULL`. ✅

**Known scope edges (documented, not gaps):**
- The deterministic extractor (`extractor/deterministic`) and hybrid extractor are not changed — they don't infer event-time, so their memories keep `valid_at = NULL` (currently valid). Out of scope; LLM extraction is where event-time lives.
- `valid_at` precision is day-level (month/year inputs resolve to first-of-period). Sub-day event-time is not a personal-knowledge use case; RFC3339 still parses if supplied.
- No CLI flag to set `valid_at` manually on `cortex remember` — extraction-only for this phase. A manual `--valid-at` flag is a possible Phase 3b/CLI follow-up.

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-06-23-tier3a-event-time-extraction.md`.**

This is Phase 3a of Tier 3. After it merges, Phase 3b (fact-augmented embeddings, time-aware query expansion built on this event-time, speaker provenance, MMR/graph reranker) gets written as its own plan.

Execute subagent-driven (recommended), then write Phase 3b.
