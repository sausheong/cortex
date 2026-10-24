# Tier 3 Phase 3c: Recall-Side Ranking (Time-Aware Query Expansion + MMR Reranker) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Two recall-side ranking upgrades from the research synthesis, both building on earlier Tier 3 work.
- **3.2 Time-aware query expansion** (+11% recall in LongMemEval): when a query carries a time reference ("what was the budget in Q1", "before the reorg"), narrow recall to memories whose event-time window matches — using the `valid_at` event-time that Phase 3a now extracts. A `temporal_filter` sub-query type carries a parsed time bound; degrades to a no-op when there's no time reference or no LLM.
- **3.4 MMR reranker** (primary), with a **graph-distance** signal folded in: after RRF merge, re-order the top results by Maximal Marginal Relevance (balance relevance against redundancy) so near-duplicate results don't crowd out diverse ones, and give a small boost to results whose entities are graph-close to the query's matched entities. Cuts the "more results = more noise" effect.

**Architecture:** Seven tasks, two independent tracks plus a surfacing task.
- *Time-aware track (Tasks 1–3):* a `validInRangeClause` window predicate in `temporal.go`; extend `temporalMode` (or a sibling) to carry an explicit `[from,to)` event-time window; a `WithAsOfRange`/internal window applied when a `temporal_filter` sub-query is present; the providers' decompose prompt learns to emit `temporal_filter` with a parsed range; deterministic fallback parses obvious date tokens from the query.
- *MMR track (Tasks 4–6):* a pure `mmrRerank` over result vectors in `math.go`; fetch result embeddings + a graph-distance map; wire the rerank as a post-RRF stage in `Recall` behind a `WithRerank` option (default on, but degrade to identity when no embedder).
- *Surfacing (Task 7):* CLI/MCP/CORTEX.md for the temporal filter and rerank toggle.

**Tech Stack:** Go 1.25.1, `modernc.org/sqlite` (pure-Go, no CGo), SQLite FTS5, `github.com/mark3labs/mcp-go`. Tests use in-package helpers `openTestDB`, `openTestDBWithEmbedder`, `mockLLM`, `mockExtractor`, `testEmbedder`.

## Global Constraints

- **Module path:** `github.com/sausheong/cortex` (root package is `cortex`).
- **No CGo, single binary.** Only existing dependencies. MMR and time math use only `sort`/`strings`/`time`/`math` from the stdlib.
- **Backward compatibility:** `Recall`'s default behavior must not regress existing tests. The MMR rerank, when enabled (default), must be a *stable refinement* — with the deterministic `testEmbedder` and existing fixtures it must not drop or reorder results in a way that breaks current assertions about membership. If a current test asserts exact order and MMR would change it, that is a real behavior change: STOP and report it for adjudication rather than editing the assertion. Time-aware filtering only narrows results **when a `temporal_filter` sub-query is present** — a query with no time reference behaves exactly as today.
- **Determinism:** MMR and any tie-breaks must be deterministic (stable sort, fixed λ). No `map` iteration leaking into output order.
- **Detection vs application (consistent with reconcile design):** the LLM only *proposes* a time range (fuzzy); the deterministic Go window clause *applies* it. A malformed/absent range degrades to no temporal narrowing, never an error.
- **Bi-temporal invariants (Phases 2a/3a):** `valid_at` = event-time start, `invalid_at` = event-time end, `expired_at` = ingestion-time of retirement. The currently-valid predicate stays `(expired_at IS NULL AND invalid_at IS NULL)`. The new window clause filters on the **event-time** dimension and must compose with (not replace) the existing expired/invalid retirement filter unless the caller explicitly asks for history.
- **Confidence-weighted scoring (Tier 1):** the final score is `rrf_score * confidence`. MMR operates as a *reranking* of the already-confidence-weighted candidate set; it must not discard the confidence weighting.
- **Commit trailer:** do NOT add a `Co-Authored-By` trailer to commits (user preference for this repo).
- **Test command:** `go test ./...` must pass. Hermetic only — no live API calls. (`llm/openai` live tests fail on HTTP 429 quota — pre-existing, out of scope.)
- **TDD:** write the failing test first, watch it fail, implement minimally, watch it pass, commit.

---

## File Structure

| File | Responsibility | Change |
|------|----------------|--------|
| `temporal.go` | temporal predicates | Add `validInRangeClause(alias, from, to)`; extend mode/window plumbing |
| `temporal_test.go` | temporal tests | Window-clause unit tests |
| `types.go` | options/types | `WithRerank(bool)`, `rerank` on `recallConfig`; window fields; `RerankLambda` const |
| `recall.go` | recall pipeline | `temporal_filter` sub-query dispatch; post-RRF MMR rerank stage |
| `recall_test.go` | recall tests | temporal_filter narrowing; MMR rerank ordering |
| `math.go` | fusion + rerank | `mmrRerank(...)` pure function |
| `math_test.go` | math tests | MMR unit tests |
| `search.go` | vector lookups | helper to fetch stored embedding vectors by ref keys (for MMR) |
| `llm/anthropic/llm.go`, `llm/openai/llm.go` | decompose | teach decompose to emit `temporal_filter` with a range |
| `cmd/cortex/main.go`, `cmd/cortex/mcp/tools.go`, `cmd/cortex/CORTEX.md.template` | surfaces | `--no-rerank` / rerank param; time-aware note |

---

### Task 1: Event-time window clause

**Files:**
- Modify: `temporal.go` (add `validInRangeClause`)
- Test: `temporal_test.go`

**Interfaces:**
- Consumes: the bi-temporal columns; the existing `qualify` helper (`temporal.go:11`).
- Produces: `validInRangeClause(alias string, from, to *time.Time) (string, []any)` — a SQL boolean fragment (no leading AND) selecting memories whose **event-validity window overlaps** the half-open query window `[from, to)`. Semantics: a memory with event window `[valid_at, invalid_at)` (NULLs = open-ended) overlaps `[from, to)` iff `(valid_at IS NULL OR valid_at < to) AND (invalid_at IS NULL OR invalid_at > from)`. A nil `from` means "since the beginning"; a nil `to` means "until forever". With both nil the clause is `1=1` (no narrowing). This is *event-time only* — it does not touch `expired_at` (system retirement), so callers compose it with the currently-valid retirement filter separately.

- [ ] **Step 1: Write the failing test**

Add to `temporal_test.go`:

```go
func TestValidInRangeClause(t *testing.T) {
	feb := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	apr := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)

	// Both nil → no narrowing.
	clause, args := validInRangeClause("m", nil, nil)
	if clause != "1=1" || len(args) != 0 {
		t.Fatalf("both-nil: got %q args=%v", clause, args)
	}

	// from only → invalid_at > from (event must not have ended before window).
	clause, args = validInRangeClause("m", &feb, nil)
	if !strings.Contains(clause, "invalid_at") || len(args) != 1 {
		t.Fatalf("from-only: got %q args=%v", clause, args)
	}

	// to only → valid_at < to (event must have begun before window end).
	clause, args = validInRangeClause("m", nil, &apr)
	if !strings.Contains(clause, "valid_at") || len(args) != 1 {
		t.Fatalf("to-only: got %q args=%v", clause, args)
	}

	// both → overlap predicate, 2 args in order [to, from].
	clause, args = validInRangeClause("m", &feb, &apr)
	if len(args) != 2 {
		t.Fatalf("both: expected 2 args, got %v", args)
	}
	// arg order must match the placeholder order in the clause.
	if !(args[0].(time.Time).Equal(apr) && args[1].(time.Time).Equal(feb)) {
		t.Fatalf("both: arg order wrong, got %v (want [apr, feb])", args)
	}
}
```

> **Arg-order contract:** the clause is written `(valid_at IS NULL OR valid_at < ?) AND (invalid_at IS NULL OR invalid_at > ?)` so the placeholders are, in order, `to` then `from`. The test pins this. Whatever order the implementer writes the clause in, the returned args slice MUST match the placeholder order — this is the bug class that breaks SQL silently.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestValidInRangeClause -v`
Expected: FAIL — `validInRangeClause` undefined.

- [ ] **Step 3: Implement the clause**

Add to `temporal.go`:

```go
// validInRangeClause returns a SQL boolean fragment (no leading AND) plus its
// args, selecting memories whose event-validity window [valid_at, invalid_at)
// overlaps the half-open query window [from, to). NULL valid_at means
// "valid since the beginning"; NULL invalid_at means "still valid". A nil
// from/to leaves that side of the window open. With both nil it returns
// "1=1" (no narrowing). This filters the EVENT-TIME dimension only; it does
// not consider expired_at (system retirement), which callers apply
// separately via the currently-valid predicate.
//
// Overlap of [valid_at, invalid_at) with [from, to):
//   (valid_at IS NULL OR valid_at < to) AND (invalid_at IS NULL OR invalid_at > from)
// Placeholders appear in the order: to, then from.
func validInRangeClause(alias string, from, to *time.Time) (string, []any) {
	var parts []string
	var args []any
	if to != nil {
		parts = append(parts, fmt.Sprintf("(%s IS NULL OR %s < ?)",
			qualify(alias, "valid_at"), qualify(alias, "valid_at")))
		args = append(args, *to)
	}
	if from != nil {
		parts = append(parts, fmt.Sprintf("(%s IS NULL OR %s > ?)",
			qualify(alias, "invalid_at"), qualify(alias, "invalid_at")))
		args = append(args, *from)
	}
	if len(parts) == 0 {
		return "1=1", nil
	}
	return "(" + strings.Join(parts, " AND ") + ")", args
}
```

(`temporal.go` already imports `fmt`, `strings`, `time`.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ -run TestValidInRangeClause -v`
Expected: PASS.

- [ ] **Step 5: Run the full suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add temporal.go temporal_test.go
git commit -m "feat: event-time window overlap clause for time-aware recall"
```

---

### Task 2: Carry an event-time window through the memory read paths

**Files:**
- Modify: `temporal.go` (extend `temporalMode` with an optional window, or add window fields the clause composes)
- Modify: `memory.go` (`searchMemoriesMode`, `getMemoriesByEntityMode`), `search.go` (`searchMemoryVectorMode`) — compose the window clause with the existing mode clause
- Test: `memory_test.go`

**Interfaces:**
- Consumes: `validInRangeClause` (Task 1); the existing `temporalMode.clause(alias)` (`temporal.go:51-60`).
- Produces: `temporalMode` gains `rangeFrom, rangeTo *time.Time`. `temporalMode.clause(alias)` composes: when a range is set, AND the existing validity predicate with `validInRangeClause`. So a window narrows the **currently-valid** set by event-time, by default (history modes still take precedence as before). After this task, `searchMemoriesMode(ctx, q, limit, mode)` with a window returns only memories whose event window overlaps it.

> **Composition rule (precedence):** keep the existing precedence (includeInvalid → "1=1"; asOf → validAsOfClause; else currentlyValid), then AND the range clause on top in ALL branches EXCEPT when there is no range (`rangeFrom == nil && rangeTo == nil`), in which case behavior is exactly as today. Rationale: a time-window question is about event-time and should still respect retirement unless the caller asked for history. Document this in the `clause` doc comment.

- [ ] **Step 1: Write the failing test**

Add to `memory_test.go`:

```go
func TestSearchMemoriesMode_EventTimeWindow(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	// Two memories with different event-time starts.
	q1 := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	q3 := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	if err := c.PutMemory(ctx, &Memory{Content: "budget set to 5000 in January", ValidAt: &q1}); err != nil {
		t.Fatal(err)
	}
	if err := c.PutMemory(ctx, &Memory{Content: "budget set to 9000 in March", ValidAt: &q3}); err != nil {
		t.Fatal(err)
	}

	// Window covering only Q1 (Jan–Feb).
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	mode := temporalMode{rangeFrom: &from, rangeTo: &to}

	got, err := c.searchMemoriesMode(ctx, "budget", 10, mode)
	if err != nil {
		t.Fatalf("searchMemoriesMode: %v", err)
	}
	if len(got) != 1 || got[0].Content != "budget set to 5000 in January" {
		t.Fatalf("expected only the January memory in the Q1 window, got %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestSearchMemoriesMode_EventTimeWindow -v`
Expected: FAIL — `temporalMode` has no `rangeFrom`/`rangeTo` (compile error), then no narrowing.

- [ ] **Step 3: Extend temporalMode and its clause**

In `temporal.go`, extend the struct and `clause`:

```go
// temporalMode selects which validity predicate a memory read applies, and
// optionally an event-time window to narrow by.
type temporalMode struct {
	includeInvalid bool
	asOf           *time.Time
	rangeFrom      *time.Time // optional event-time window start (3.2)
	rangeTo        *time.Time // optional event-time window end (3.2)
}

// clause returns the SQL fragment (no leading AND) and its args for this
// mode. Base predicate by precedence: includeInvalid → "1=1"; asOf →
// validAsOfClause; else currentlyValidClause. When an event-time window
// (rangeFrom/rangeTo) is set, the window overlap clause is AND-ed onto the
// base predicate so a time-scoped question narrows by event-time while still
// honoring retirement (unless includeInvalid/asOf already governs).
func (m temporalMode) clause(alias string) (string, []any) {
	var base string
	var args []any
	switch {
	case m.includeInvalid:
		base = "1=1"
	case m.asOf != nil:
		base, args = validAsOfClause(alias, *m.asOf)
	default:
		base = currentlyValidClause(alias)
	}

	if m.rangeFrom == nil && m.rangeTo == nil {
		return base, args
	}
	rClause, rArgs := validInRangeClause(alias, m.rangeFrom, m.rangeTo)
	args = append(args, rArgs...)
	return "(" + base + " AND " + rClause + ")", args
}
```

> Arg-order note: the base args come first (they appear first in the composed clause), then the range args. The three read paths build their full arg list as `[<their leading args>, <clause args...>, <trailing args>]` — the clause-args ordering here must match the placeholder order in the composed string. Verify each call site still interleaves correctly (MATCH/entity-id/ref-id leading args, then clause args, then limit).

- [ ] **Step 4: Verify the three read paths need no change**

`searchMemoriesMode` (`memory.go`), `getMemoriesByEntityMode` (`memory.go`), `searchMemoryVectorMode` (`search.go`) already call `mode.clause(alias)` and splice its args. Since `clause` now returns the composed clause+args, they should work unchanged. Read each and confirm the arg splice order is `[leading..., targs..., trailing...]`. If any path hardcoded an assumption about the number of clause args, fix it. (They use `append`, so they should be fine.)

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./ -run TestSearchMemoriesMode_EventTimeWindow -v`
Expected: PASS.

- [ ] **Step 6: Run the full suite**

Run: `go test ./...`
Expected: PASS — existing `WithAsOf`/`WithIncludeInvalid` tests still pass (no range set → base clause unchanged).

- [ ] **Step 7: Commit**

```bash
git add temporal.go memory.go search.go memory_test.go
git commit -m "feat: compose event-time window into temporal read mode"
```

---

### Task 3: temporal_filter sub-query + decompose support

**Files:**
- Modify: `recall.go` (`executeSubQuery` dispatch; parse range params; thread a window into `mode`)
- Modify: `types.go` (recallConfig window fields if needed; or pass via mode)
- Modify: `llm/anthropic/llm.go`, `llm/openai/llm.go` (decompose prompt + parse `params.from`/`params.to`)
- Test: `recall_test.go`, provider decompose tests

**Interfaces:**
- Consumes: Task 2's `temporalMode` window; the existing sub-query dispatch (`recall.go:162`).
- Produces: a `temporal_filter` sub-query type. When `decomposeQuery` yields a `temporal_filter` with `params.query` (text) and optional `params.from`/`params.to` (date strings parsed via `cortex.ParseEventTime`), `Recall` applies that window to the **memory** read paths for the whole recall (the window is recall-global, derived from the query's time reference). Concretely: in `Recall`, before executing sub-queries, scan the decomposed sub-queries for a `temporal_filter`; if present, set `mode.rangeFrom/rangeTo` from its parsed params; the `temporal_filter` itself also acts as a `memory_lookup` over `params.query`. No time reference → no `temporal_filter` → unchanged behavior.

> **Design note (recall-global window):** the window is a property of the *question*, so it should constrain all memory retrieval in that recall, not just one sub-query's results. The simplest correct wiring: parse the window out of the decomposed sub-queries in `Recall` and fold it into the `mode` that is already passed to every `executeSubQuery`. The `temporal_filter` sub-query then behaves like `memory_lookup` for its text. Keep chunk/graph paths unwindowed (chunks have no event-time).

- [ ] **Step 1: Write the failing test (recall-level, deterministic, no live LLM)**

Add to `recall_test.go`:

```go
func TestRecall_TemporalFilterNarrowsByEventTime(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	jan := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	mar := time.Date(2026, 3, 15, 0, 0, 0, 0, time.UTC)
	if err := c.PutMemory(ctx, &Memory{Content: "budget was 5000", ValidAt: &jan}); err != nil {
		t.Fatal(err)
	}
	if err := c.PutMemory(ctx, &Memory{Content: "budget was 9000", ValidAt: &mar}); err != nil {
		t.Fatal(err)
	}

	// LLM decomposes into a temporal_filter scoped to Q1 (Jan–Mar 1).
	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
			return []StructuredQuery{
				{Type: "temporal_filter", Params: map[string]any{
					"query": q,
					"from":  "2026-01-01",
					"to":    "2026-03-01",
				}},
			}, nil
		},
	})

	got, err := c.Recall(ctx, "what was the budget in Q1", WithLimit(10))
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(got) != 1 || got[0].Content != "budget was 5000" {
		t.Fatalf("expected only the Q1 memory, got %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestRecall_TemporalFilterNarrowsByEventTime -v`
Expected: FAIL — `temporal_filter` is unhandled, so the window isn't applied and both memories return (or none).

- [ ] **Step 3: Parse the window in Recall and dispatch the sub-query**

In `recall.go`, after `subQueries := c.decomposeQuery(ctx, query)` and before resolving `mode`, extract a window:

```go
	// Time-aware query expansion (3.2): if the decomposition carries a
	// temporal_filter, derive a recall-global event-time window from its
	// parsed from/to and narrow the memory read paths by it. Absent or
	// unparseable bounds degrade to no narrowing.
	for _, sq := range subQueries {
		if sq.Type != "temporal_filter" {
			continue
		}
		if fromStr, ok := sq.Params["from"].(string); ok {
			mode.rangeFrom = ParseEventTime(fromStr)
		}
		if toStr, ok := sq.Params["to"].(string); ok {
			mode.rangeTo = ParseEventTime(toStr)
		}
	}
```

Make sure `mode` is declared before this loop (move the `mode := temporalMode{...}` line up, then set the includeInvalid/asOf fields, then apply the window). Then in `executeSubQuery` (`recall.go:162`), add a case so `temporal_filter` runs as a memory lookup:

```go
	case "temporal_filter":
		return c.recallMemories(ctx, query, limit, mode)
```

(`recallMemories` already receives `mode`, which now carries the window — so its results are windowed.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ -run TestRecall_TemporalFilterNarrowsByEventTime -v`
Expected: PASS.

- [ ] **Step 5: Teach both providers' decompose to emit temporal_filter**

In `llm/anthropic/llm.go` and `llm/openai/llm.go`, update `decomposePrompt` to add `temporal_filter` to the allowed types with `from`/`to` params. Append to the type list and add an example. Exact additions (both files, identical):

In the type bullet list, change the `"type"` line to include `temporal_filter`:
```
- "type": one of "keyword_search", "memory_lookup", "vector_search", "graph_traverse", "temporal_filter"
```
And add a guidance + example block before "Return ONLY valid JSON":
```
Use "temporal_filter" when the query refers to a time or period (e.g. "in Q1", "last year", "before the reorg", "in March 2026"). Its params take "query" plus optional "from" and "to" date strings (e.g. "2026-01-01", "2026-03"). Resolve relative references to absolute dates when you can; omit a bound you cannot determine.
Example: {"queries":[{"type":"temporal_filter","params":{"query":"budget","from":"2026-01-01","to":"2026-04-01"}}]}
```

> No provider unit test is strictly required for prompt text, but if the provider package has a decompose-parse test, add a case that a JSON response containing a `temporal_filter` with `from`/`to` parses into a `StructuredQuery` with those params intact (the existing `Decompose` JSON unmarshal already handles arbitrary `params`, so this is a parse-through check). If no such harness exists, note it and rely on the recall-level test in Step 1.

- [ ] **Step 6: Run the full suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add recall.go types.go llm/anthropic/llm.go llm/openai/llm.go recall_test.go
git commit -m "feat: temporal_filter sub-query for time-aware recall"
```

---

### Task 4: MMR rerank pure function

**Files:**
- Modify: `math.go` (add `mmrRerank`)
- Test: `math_test.go`

**Interfaces:**
- Consumes: candidate items each with a relevance score and an embedding vector.
- Produces: `mmrRerank(items []mmrCandidate, lambda float64, limit int) []string` — returns the candidate IDs reordered by Maximal Marginal Relevance: greedily pick the item maximizing `lambda*relevance - (1-lambda)*maxSimToAlreadyPicked`, until `limit` picked or candidates exhausted. Deterministic (stable tie-break by original relevance, then id). `mmrCandidate{id string; score float64; vec []float32}`. Items with nil/empty vec are treated as zero-similarity to everything (so they're ranked by relevance alone — graceful when embeddings are missing).

- [ ] **Step 1: Write the failing test**

Add to `math_test.go`:

```go
func TestMMRRerank_PrefersDiversity(t *testing.T) {
	// Three candidates: A and B are near-identical vectors (redundant),
	// C is distinct. By raw relevance order: A, B, C. MMR with moderate
	// lambda should pull C ahead of B (diversity) once A is picked.
	a := mmrCandidate{id: "A", score: 0.9, vec: []float32{1, 0, 0}}
	b := mmrCandidate{id: "B", score: 0.8, vec: []float32{0.99, 0.01, 0}}
	cc := mmrCandidate{id: "C", score: 0.7, vec: []float32{0, 1, 0}}

	got := mmrRerank([]mmrCandidate{a, b, cc}, 0.5, 3)
	if len(got) != 3 {
		t.Fatalf("expected 3, got %v", got)
	}
	if got[0] != "A" {
		t.Fatalf("expected A first (highest relevance), got %q", got[0])
	}
	if got[1] != "C" {
		t.Fatalf("expected C second (diversity over redundant B), got %v", got)
	}
}

func TestMMRRerank_NoVectorsFallsBackToRelevance(t *testing.T) {
	// No vectors → pure relevance order, stable.
	items := []mmrCandidate{
		{id: "X", score: 0.5},
		{id: "Y", score: 0.9},
		{id: "Z", score: 0.7},
	}
	got := mmrRerank(items, 0.5, 3)
	want := []string{"Y", "Z", "X"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("relevance fallback: got %v want %v", got, want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestMMRRerank -v`
Expected: FAIL — `mmrRerank`/`mmrCandidate` undefined.

- [ ] **Step 3: Implement mmrRerank**

Add to `math.go` (it imports `sort`; `cosineSimilarity` is already there):

```go
// mmrCandidate is one item to be reranked: an id, its relevance score
// (already confidence-weighted), and its embedding vector (may be nil).
type mmrCandidate struct {
	id    string
	score float64
	vec   []float32
}

// mmrRerank reorders candidates by Maximal Marginal Relevance: it greedily
// selects, at each step, the unpicked candidate maximizing
//   lambda*relevance - (1-lambda)*maxSimilarityToAlreadyPicked
// balancing relevance (lambda=1 → pure relevance) against diversity
// (lambda=0 → pure novelty). Returns the selected ids in pick order, up to
// limit. Deterministic: candidates are pre-sorted by score (then id) so ties
// resolve stably, and a candidate with a nil/empty vector contributes zero
// similarity (ranked on relevance alone — graceful when embeddings are
// absent).
func mmrRerank(items []mmrCandidate, lambda float64, limit int) []string {
	n := len(items)
	if n == 0 {
		return nil
	}
	// Stable base order: relevance desc, then id asc.
	order := make([]mmrCandidate, n)
	copy(order, items)
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].score != order[j].score {
			return order[i].score > order[j].score
		}
		return order[i].id < order[j].id
	})

	if limit > n {
		limit = n
	}
	picked := make([]mmrCandidate, 0, limit)
	used := make([]bool, n)
	result := make([]string, 0, limit)

	for len(result) < limit {
		bestIdx := -1
		var bestScore float64
		for i := range order {
			if used[i] {
				continue
			}
			// Max similarity to already-picked.
			var maxSim float64
			for _, p := range picked {
				s := float64(cosineSimilarity(order[i].vec, p.vec))
				if s > maxSim {
					maxSim = s
				}
			}
			mmr := lambda*order[i].score - (1-lambda)*maxSim
			if bestIdx == -1 || mmr > bestScore {
				bestIdx = i
				bestScore = mmr
			}
		}
		if bestIdx == -1 {
			break
		}
		used[bestIdx] = true
		picked = append(picked, order[bestIdx])
		result = append(result, order[bestIdx].id)
	}
	return result
}
```

> Note: `cosineSimilarity` returns 0 when either vector is empty/zero-magnitude (`math.go:32`), so the nil-vec case is handled without special-casing. The first pick always maximizes `lambda*score` (no picked set yet → maxSim 0), i.e. the highest-relevance item, matching the test.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ -run TestMMRRerank -v`
Expected: PASS.

- [ ] **Step 5: Run the full suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add math.go math_test.go
git commit -m "feat: MMR rerank pure function"
```

---

### Task 5: Fetch result vectors and a graph-distance signal

**Files:**
- Modify: `search.go` (add `embeddingByRef` helper) and `recall.go` (a graph-distance helper)
- Test: `search_test.go` / `recall_test.go`

**Interfaces:**
- Consumes: the `embeddings` table (keyed `ref_id`, `ref_type`); `Traverse` for graph distance.
- Produces:
  - `embeddingByRef(ctx, refID, refType string) []float32` — loads a stored embedding vector (nil if none). Used to give MMR the vectors for already-ranked results without re-embedding.
  - `graphDistanceBoost(ctx, results []Result, queryEntityIDs []string) map[string]float64` — a small additive boost for results whose entities are within depth-1 of any query-matched entity (the entities surfaced by the `graph_traverse` sub-query). Keyed by the same result key space (`mem:`/`chunk:`/`entity:` + id). Best-effort: returns an empty map on any error. This is the "graph-distance signal folded in" — kept deliberately small so MMR/relevance stays primary.

> **Scope guard:** graph distance is a *secondary* signal. Implement it as a bounded boost (e.g. +0.1 * confidence for a directly-linked entity) applied to the relevance score BEFORE MMR, not as its own reranker. Keep it simple and deterministic. If wiring graph distance proves to add disproportionate complexity, the MMR rerank alone satisfies the "MMR primary" decision — in that case implement `embeddingByRef` + MMR (Tasks 5–6) and record graph-distance as a documented follow-up rather than forcing it. Decide based on what the result-key→entity mapping cleanly supports; report the choice.

- [ ] **Step 1: Write the failing test for embeddingByRef**

Add to `search_test.go`:

```go
func TestEmbeddingByRef(t *testing.T) {
	c := openTestDBWithEmbedder(t)
	ctx := context.Background()

	m := &Memory{Content: "vectorized memory"}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	vecs, _ := c.cfg.embedder.Embed(ctx, []string{m.Content})
	if err := c.putEmbedding(ctx, m.ID, "memory", vecs[0]); err != nil {
		t.Fatal(err)
	}

	got := c.embeddingByRef(ctx, m.ID, "memory")
	if len(got) != c.cfg.embedder.Dimensions() {
		t.Fatalf("expected %d-dim vector, got %d", c.cfg.embedder.Dimensions(), len(got))
	}

	// Missing ref → nil.
	if v := c.embeddingByRef(ctx, "nonexistent", "memory"); v != nil {
		t.Fatalf("expected nil for missing ref, got %v", v)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestEmbeddingByRef -v`
Expected: FAIL — `embeddingByRef` undefined.

- [ ] **Step 3: Implement embeddingByRef**

Add to `search.go` (uses `decodeFloat32s` from `math.go`):

```go
// embeddingByRef loads the stored embedding vector for a ref (chunk/memory),
// or nil if none exists. Lets the reranker reuse vectors written at ingest
// instead of re-embedding ranked results.
func (c *Cortex) embeddingByRef(ctx context.Context, refID, refType string) []float32 {
	var blob []byte
	err := c.db.QueryRowContext(ctx,
		`SELECT vector FROM embeddings WHERE ref_id = ? AND ref_type = ? LIMIT 1`,
		refID, refType,
	).Scan(&blob)
	if err != nil {
		return nil
	}
	return decodeFloat32s(blob)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ -run TestEmbeddingByRef -v`
Expected: PASS.

- [ ] **Step 5: (If implementing graph distance) write its test and helper**

If you proceed with the graph-distance boost (see scope guard), add a test asserting that a result whose entity is directly linked to a query-matched entity gets a positive boost while an unrelated result gets none, and implement `graphDistanceBoost`. If you defer it, write a one-line note in the report and skip to Task 6 (MMR-only rerank). Either way the rerank in Task 6 must function.

- [ ] **Step 6: Run the full suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add search.go recall.go search_test.go recall_test.go
git commit -m "feat: embeddingByRef lookup (and graph-distance boost) for rerank"
```

---

### Task 6: Wire MMR rerank into Recall

**Files:**
- Modify: `types.go` (`WithRerank` option, `rerank` field on `recallConfig`, `RerankLambda` const)
- Modify: `recall.go` (`Recall` — post-RRF, pre-limit rerank stage)
- Test: `recall_test.go`

**Interfaces:**
- Consumes: `mmrRerank` (Task 4), `embeddingByRef` + optional `graphDistanceBoost` (Task 5).
- Produces: after the confidence-weighted sort and before the limit cap, `Recall` (when `cfg.rerank` is true — default — and an embedder is configured) reorders the candidate `final` slice by MMR over result vectors. `WithRerank(false)` disables it (falls back to pure confidence-weighted order). Without an embedder, rerank is a no-op (no vectors → MMR == relevance order, so it's safe either way, but skip the work).

> **Default-on with safety:** set `rerank: true` in the `recallConfig` default (`recall.go:31` `cfg := &recallConfig{limit: 20}` → add `rerank: true`). The rerank must be a *refinement*: it reorders the existing candidate set, never adds/removes members (except the limit cap that already existed). Build `mmrCandidate`s from `final` using each result's `Score` (already confidence-weighted) and a vector from `embeddingByRef` keyed by the result's type+id. Map the reranked id order back to the `Result` slice.

> **Result→vector keying:** the candidate `final` entries are `Result`s, but MMR needs ids that map back. Reuse the prefixed keys: a memory result's key is `"mem:"+id`, chunk `"chunk:"+id`, entity `"entity:"+id`. The `Result` struct doesn't carry its own id today. Simplest approach: rerank over the `merged []rankedItem` (which DO carry the prefixed id and score) BEFORE building `final`, fetching vectors per key via a small type-switch on the prefix to choose refType (`mem:`→"memory", `chunk:`→"chunk", entity has no embedding → nil vec). Then build `final` in the reranked order. Implement whichever is cleaner; the test only pins observable order.

- [ ] **Step 1: Write the failing test**

Add to `recall_test.go`. Use the embedder so vectors exist; assert that a redundant lower-relevance duplicate is pushed below a diverse result after rerank, and that `WithRerank(false)` preserves the pure-relevance order:

```go
func TestRecall_MMRRerankReordersForDiversity(t *testing.T) {
	c := openTestDBWithEmbedder(t)
	ctx := context.Background()

	// Seed three memories; two near-duplicates, one distinct. Embed each.
	seed := func(content string) {
		m := &Memory{Content: content}
		if err := c.PutMemory(ctx, m); err != nil {
			t.Fatal(err)
		}
		vecs, _ := c.cfg.embedder.Embed(ctx, []string{content})
		if err := c.putEmbedding(ctx, m.ID, "memory", vecs[0]); err != nil {
			t.Fatal(err)
		}
	}
	seed("alpha alpha alpha report")
	seed("alpha alpha alpha summary") // near-duplicate of the first
	seed("zeta distinct topic")

	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
			return []StructuredQuery{{Type: "memory_vector", Params: map[string]any{"query": q}}}, nil
		},
	})

	// With rerank (default): the distinct result should not be crowded out —
	// it should rank ahead of the near-duplicate second item.
	got, err := c.Recall(ctx, "alpha report", WithLimit(3))
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(got) < 2 {
		t.Fatalf("expected multiple results, got %d", len(got))
	}
	// Assert the result set is intact (rerank refines order, never drops members).
	if len(got) != 3 {
		t.Fatalf("rerank must preserve membership, expected 3 got %d", len(got))
	}
}
```

> Note: with the deterministic 8-dim `testEmbedder`, exact MMR ordering is hard to predict precisely; assert the robust property (membership preserved, count intact) rather than a brittle exact order. If you can construct vectors where the diversity reordering is deterministic and obvious, assert it; otherwise keep the membership-preservation assertion plus a `WithRerank(false)` comparison showing the two orders can differ. The load-bearing guarantee is *rerank never drops/adds results*.

- [ ] **Step 2: Run test to verify it fails (or compile-fails on WithRerank)**

Run: `go test ./ -run TestRecall_MMRRerankReordersForDiversity -v`
Expected: FAIL — `WithRerank` undefined (compile), then once added but unwired, behavior unchanged.

- [ ] **Step 3: Add the option, field, and constant**

In `types.go`: add `rerank bool` to `recallConfig`; add the option and a lambda constant:

```go
// WithRerank toggles the post-fusion MMR diversity rerank (default on).
// Disable to get the pure confidence-weighted fusion order.
func WithRerank(on bool) RecallOption {
	return func(c *recallConfig) { c.rerank = on }
}
```

```go
// RerankLambda is the MMR tradeoff: relevance weight vs. diversity weight.
// 0.7 keeps relevance primary while still demoting near-duplicates.
const RerankLambda = 0.7
```

And set the default in `Recall`: `cfg := &recallConfig{limit: 20, rerank: true}`.

- [ ] **Step 4: Wire the rerank stage into Recall**

In `recall.go`, after the confidence-weighted `sort.SliceStable(final, ...)` (`recall.go:95-97`) and before the min-confidence filter / limit, rerank when enabled and an embedder exists. Implement per the keying note (rerank over `merged` ids or over `final` with a parallel id slice — your choice). Sketch (over `final` with a parallel key derivation):

```go
	// MMR diversity rerank (3.4): reorder the candidate set so near-duplicate
	// results don't crowd out diverse ones. Refinement only — never drops or
	// adds members. No-op without an embedder (no vectors to compare).
	if cfg.rerank && c.cfg.embedder != nil && len(final) > 1 {
		final = c.rerankMMR(ctx, final)
	}
```

Add `rerankMMR` to `recall.go`:

```go
// rerankMMR reorders results by Maximal Marginal Relevance over their stored
// embedding vectors, balancing relevance (Score) against diversity. Results
// keep their Score; only order changes. Memory/chunk results use their stored
// vectors; entities (no embedding) contribute a nil vector and rank on
// relevance alone.
func (c *Cortex) rerankMMR(ctx context.Context, results []Result) []Result {
	byID := make(map[string]Result, len(results))
	cands := make([]mmrCandidate, 0, len(results))
	for i, r := range results {
		// Synthesize a stable unique id (results lack their own id field).
		key := fmt.Sprintf("%d", i)
		byID[key] = r
		var vec []float32
		switch r.Type {
		case "memory":
			// memory result vectors keyed by memory id; recover via entity? No —
			// results don't carry id. Use content embedding is wrong (re-embeds).
			vec = nil
		}
		_ = vec
		cands = append(cands, mmrCandidate{id: key, score: r.Score, vec: nil})
	}
	_ = byID
	_ = cands
	return results // placeholder — see Step 4 note
}
```

> **STOP — keying problem to resolve in implementation:** `Result` does not carry its own id, so `rerankMMR` over `final` cannot fetch per-result vectors. The clean fix is to rerank over `merged []rankedItem` (which carry the prefixed id `mem:`/`chunk:`/`entity:` and score) BEFORE building `final`, then build `final` in the reranked order. Refactor accordingly:
>
> 1. After RRF `merged := rrfMerge(lists, 60)` and before building `final`, compute the confidence-weighted score per merged item (you already do this when building `final`). 
> 2. Build `[]mmrCandidate` from `merged`: id = `item.id` (prefixed), score = `item.score * confidence` (look up confidence from `resultMap[item.id]`), vec = `c.vectorForKey(ctx, item.id)` where `vectorForKey` splits the prefix (`mem:`→`embeddingByRef(id,"memory")`, `chunk:`→`embeddingByRef(id,"chunk")`, `entity:`→nil).
> 3. `order := mmrRerank(cands, RerankLambda, len(cands))`.
> 4. Build `final` by iterating `order` and pulling `resultMap[key]` (setting `.Score`), instead of iterating `merged`.
> 5. The existing confidence-weighted `sort` is then redundant for the rerank path — gate it: when reranking, skip the plain sort (MMR defines order); when not reranking, keep the sort. Keep min-confidence filter and limit AFTER, unchanged.
>
> Implement this clean version; delete the placeholder sketch above. The test asserts membership preservation, so the refactor must keep every merged result present.

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./ -run TestRecall_MMRRerankReordersForDiversity -v`
Expected: PASS.

- [ ] **Step 6: Run the full suite — watch for ordering regressions**

Run: `go test ./...`
Expected: PASS. If any existing recall test fails on ORDER (not membership), per the Global Constraints STOP and report it — MMR default-on is a deliberate behavior change and an over-specified order assertion must be adjudicated, not silently rewritten. Membership/threshold assertions should be unaffected.

- [ ] **Step 7: Commit**

```bash
git add types.go recall.go recall_test.go
git commit -m "feat: MMR diversity rerank as default post-fusion stage"
```

---

### Task 7: Surface rerank toggle and time-aware recall

**Files:**
- Modify: `cmd/cortex/main.go` (`cmdRecall` — `--no-rerank` flag)
- Modify: `cmd/cortex/mcp/tools.go` (recall tool — `rerank` bool param)
- Modify: `cmd/cortex/CORTEX.md.template` (note time-aware recall + rerank)
- Test: CLI arg-parse test if a harness exists; build otherwise

**Interfaces:**
- Consumes: `cortex.WithRerank` (Task 6); the `temporal_filter` is automatic via decompose (no flag needed — it's LLM-driven).
- Produces: `cortex recall <query> [--no-rerank]`; MCP recall tool gains an optional `rerank` bool (default true); CORTEX.md explains that recall is time-aware (mentions periods in queries) and diversity-reranked.

- [ ] **Step 1: Add --no-rerank to cmdRecall**

`cmdRecall` (`cmd/cortex/main.go:272`) already has an arg loop with `--min-confidence`. Add a `--no-rerank` boolean case that appends `cortex.WithRerank(false)`. Mirror the existing flag-parse style; if a `parseRecallArgs` helper exists or is warranted (consistent with `parseRememberArgs`/`parseMergeArgs`), extract one and unit-test it. Otherwise add the flag inline and note it.

- [ ] **Step 2: Add rerank param to the MCP recall tool**

In `cmd/cortex/mcp/tools.go`, the recall tool: add `mcp.WithBoolean("rerank", mcp.Description("Diversity-rerank results (default true). Set false for pure relevance order."))` and in the handler `if !req.GetBool("rerank", true) { opts = append(opts, cortex.WithRerank(false)) }` (match the existing bool-accessor style used for `include_invalid`).

- [ ] **Step 3: Update CORTEX.md.template**

Add a short note in the recall guidance area that (a) recall is time-aware — including a period in the query ("in Q1", "before the launch") narrows by when facts were true; (b) results are diversity-reranked by default. Match surrounding markdown style.

- [ ] **Step 4: Build and test**

Run: `go build ./...` then `go test ./...`
Expected: success / PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/cortex/main.go cmd/cortex/main_test.go cmd/cortex/mcp/tools.go cmd/cortex/CORTEX.md.template
git commit -m "feat: --no-rerank CLI flag, rerank MCP param, time-aware recall docs"
```

---

## Self-Review

**Spec coverage** (Phase 3c = research items 3.2 + 3.4):
- **3.2 Time-aware query expansion:** Task 1 (window clause), Task 2 (window in read mode), Task 3 (`temporal_filter` sub-query + decompose). ✅
- **3.4 MMR reranker (primary) + graph-distance:** Task 4 (MMR fn), Task 5 (vector fetch + optional graph-distance boost), Task 6 (wired default-on). ✅ Graph-distance is explicitly scoped as a secondary, optional-if-clean signal (the user chose "Both, MMR primary").

**Placeholder scan:** Task 6 Step 4 intentionally contains a *labeled* placeholder sketch immediately followed by a STOP note that specifies the clean implementation to write and to delete the sketch — this is guidance, not shippable code. Every other step shows complete code. The implementer must follow the STOP note's clean version. ✅ (flagged so the reviewer checks the placeholder was replaced.)

**Type / linkage consistency:**
- `temporalMode` gains `rangeFrom/rangeTo` (Task 2), set from `temporal_filter` params (Task 3) via `ParseEventTime` (Phase 3a). Composed clause arg-order pinned by Task 1's test. ✅
- `mmrRerank`/`mmrCandidate` (Task 4) consumed by `rerankMMR` (Task 6) using `embeddingByRef` (Task 5) via a prefix→refType split. ✅
- `WithRerank`/`rerank`/`RerankLambda` (Task 6) surfaced in CLI/MCP (Task 7). ✅
- Rerank operates over the prefixed-key `merged` items so vectors are fetchable — the keying problem is called out explicitly in Task 6. ✅

**Backward compatibility / regression risk:**
- Time-aware filtering is inert without a `temporal_filter` sub-query (no time reference → unchanged). ✅
- MMR is default-on — a deliberate behavior change. The plan repeatedly instructs: rerank is membership-preserving; if an existing test fails on ORDER, STOP and report rather than edit. This is the main risk and is guarded. ✅
- Without an embedder, rerank no-ops. ✅

**Known scope edges (documented, not gaps):**
- Graph-distance is a bounded secondary boost and may be deferred if it doesn't map cleanly onto the result-key→entity relation (Task 5 scope guard) — MMR-primary still satisfies the decision. The implementer reports the choice.
- `temporal_filter` relies on the LLM resolving relative dates ("last quarter") to absolute ones; unparseable bounds degrade to no narrowing (deterministic fallback is bounds-from-params only, not NLP date extraction — full relative-date NLP is out of scope).
- MMR λ is a fixed constant (0.7), not tunable per-call beyond on/off. Per-call λ is a possible follow-up.
- Chunks/entities without embeddings rank on relevance alone within MMR (nil vec → zero similarity). Intended.

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-06-23-tier3c-recall-ranking.md`.**

This is Phase 3c of Tier 3 (recall-side ranking), the final phase of the four Tier 3 research items. After it merges, Tier 3 is complete: 3.1 + 3.3 (Phase 3b), event-time foundation (Phase 3a), 3.2 + 3.4 (this phase). Tier 4 (background reconsolidation / "dreaming") remains as the next tier if desired.

Execute subagent-driven (recommended).
