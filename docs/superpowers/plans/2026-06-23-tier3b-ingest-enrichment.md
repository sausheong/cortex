# Tier 3 Phase 3b: Ingest Enrichment (Fact-Augmented Embeddings + Speaker Provenance) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Two ingest-side recall-quality upgrades from the research synthesis.
- **3.1 Fact-augmented embedding keys** (+9.4% recall in LongMemEval): embed a memory's content *prefixed with its linked entity context* ("Entities: Alice, Stripe. <content>") rather than the bare content, so semantically-thin memories become findable by entity. Only the embedding text changes — stored content is untouched, and the query embedding stays bare (asymmetric, à la Anthropic Contextual Retrieval).
- **3.3 Speaker/source provenance** (benchmarks #2, #7): record *who asserted* each memory (user / assistant / document / …) so attribution questions work and the assistant's own statements are indexable. A new `speaker` column, a `WithSpeaker` ingest option, surfaced through recall and the CLI/MCP.

**Architecture:** Six tasks. (1) Add `Memory.Speaker` + a `speaker` column (idempotent `ensureColumn`) + persist it in `PutMemory` + scan it in all four memory read paths. (2) `WithSpeaker` option; `Remember` stamps speaker like it stamps source. (3) Surface `Speaker` on `Result` in both memory recall paths. (4) A pure `augmentForEmbedding` helper (unit-tested). (5) Wire fact-augmentation into `Remember`'s memory-embed call (spy-embedder integration test proves the augmented text is what gets embedded, while the query stays bare). (6) CLI `--speaker`/`--source` on `cortex remember`, MCP `speaker` param, and a CORTEX.md note.

**Tech Stack:** Go 1.25.1, `modernc.org/sqlite` (pure-Go, no CGo), SQLite FTS5, `github.com/mark3labs/mcp-go`. Tests use in-package helpers `openTestDB`, `openTestDBWithEmbedder`, `mockLLM`, `mockExtractor`, `testEmbedder`.

## Global Constraints

- **Module path:** `github.com/sausheong/cortex` (root package is `cortex`).
- **No CGo, single binary.** Only existing dependencies. The augmentation helper uses only `strings`/`sort` from the stdlib.
- **Backward compatibility:** existing databases must keep working. The `speaker` column is added via the existing idempotent `ensureColumn` pattern (`store.go:107`), nullable, no default beyond SQL NULL. Memories ingested before this phase have `speaker = NULL` and behave exactly as today. Never `DROP`.
- **Fact-augmentation is embedding-only:** `augmentForEmbedding` changes *only the text passed to `embedder.Embed` for a memory at ingest*. It must NOT change `Memory.Content`, the FTS index, the stored row, or the query-side embedding. A memory with no linked entities embeds its bare content (helper returns input unchanged).
- **Determinism:** the entity-context prefix must be deterministically ordered (sorted) so embeddings and tests are stable across runs (Go map iteration is random).
- **Confidence invariant:** unchanged (`coerceConfidence`, `types.go:269`).
- **Bi-temporal invariant (from Phase 3a):** `PutMemory` persists `valid_at`/`invalid_at`, never `expired_at`. This phase's `PutMemory` edits must preserve that — add `speaker` to the column list without disturbing the temporal columns.
- **Commit trailer:** do NOT add a `Co-Authored-By` trailer to commits (user preference for this repo).
- **Test command:** `go test ./...` must pass. Hermetic only — no live API calls. (`llm/openai` live tests fail on HTTP 429 quota — pre-existing, out of scope.)
- **TDD:** write the failing test first, watch it fail, implement minimally, watch it pass, commit.

---

## File Structure

| File | Responsibility | Change |
|------|----------------|--------|
| `types.go` | core types | Add `Speaker string` to `Memory` and `Result`; add `WithSpeaker` option + `speaker` field on `rememberConfig` |
| `store.go` | schema/migration | `ensureColumn(db, "memories", "speaker", "TEXT")` |
| `memory.go` | memory CRUD + reads | `PutMemory` persists `speaker`; the 4 read paths SELECT + scan `speaker` |
| `cortex.go` | ingest | `Remember` stamps `speaker` on memories; embed the fact-augmented memory text |
| `recall.go` | recall result building | Carry `Speaker` into memory `Result`s; add `augmentForEmbedding` (or in cortex.go) |
| `cmd/cortex/main.go` | CLI | `cortex remember` gains `--speaker` and `--source` flags |
| `cmd/cortex/mcp/tools.go` | MCP | `remember` tool gains a `speaker` param |
| `cmd/cortex/CORTEX.md.template` | agent contract | Note speaker provenance |
| `*_test.go` | tests | One test file change per task |

---

### Task 1: Speaker field, column, persistence, and read-back

**Files:**
- Modify: `types.go` (`Memory` struct, `types.go:39-54`)
- Modify: `store.go` (`Open`, after the temporal-column migration `store.go:160-165`)
- Modify: `memory.go` (`PutMemory` insert `memory.go:36-43`; the four memory read paths)
- Test: `memory_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `Memory.Speaker` persists through `PutMemory` and round-trips through `getMemoryByID`, `searchMemoriesMode`, `getMemoriesByEntityMode`, `searchMemoryVectorMode`. NULL when unset.

**The four read paths that build a `Memory` and must scan `speaker`:**
- `memory.go:94-102` `searchMemoriesMode` SELECT + `memory.go:112` scan
- `memory.go:150-156` `getMemoriesByEntityMode` SELECT + `memory.go:166` scan
- `memory.go:275-278` `getMemoryByID` SELECT + scan
- `search.go:163-166` `searchMemoryVectorMode` SELECT + scan

Each currently selects `id, content, source, confidence, created_at, updated_at, valid_at, invalid_at, expired_at`. Add `speaker` to each SELECT (place it right after `source` for readability) and scan it into a `sql.NullString`, assigning `m.Speaker = ns.String` when valid.

- [ ] **Step 1: Write the failing test**

Add to `memory_test.go`:

```go
func TestPutMemory_PersistsSpeaker(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	m := &Memory{Content: "Prefers async standups", Speaker: "user"}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}

	got, err := c.getMemoryByID(ctx, m.ID)
	if err != nil {
		t.Fatalf("getMemoryByID: %v", err)
	}
	if got == nil || got.Speaker != "user" {
		t.Fatalf("expected speaker 'user', got %+v", got)
	}

	// And via FTS search read path.
	found, err := c.SearchMemories(ctx, "async standups", 10)
	if err != nil {
		t.Fatalf("SearchMemories: %v", err)
	}
	if len(found) != 1 || found[0].Speaker != "user" {
		t.Fatalf("expected speaker via search, got %+v", found)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestPutMemory_PersistsSpeaker -v`
Expected: FAIL — `Memory` has no field `Speaker` (compile error), then once added, the column/scan don't exist.

- [ ] **Step 3: Add the field**

In `types.go`, in the `Memory` struct, add after `Source` (`types.go:43`):

```go
	Speaker    string    `json:"speaker,omitempty"`
```

- [ ] **Step 4: Add the column migration**

In `store.go`, after the temporal-column loop (`store.go:160-165`), add:

```go
	if err := ensureColumn(db, "memories", "speaker", "TEXT"); err != nil {
		db.Close()
		return nil, fmt.Errorf("cortex: migrate memories.speaker: %w", err)
	}
```

- [ ] **Step 5: Persist speaker in PutMemory**

In `memory.go`, extend the `PutMemory` INSERT (`memory.go:36-43`) to include `speaker`. Use a nil-able value so an empty speaker stores NULL:

```go
	var speaker any
	if m.Speaker != "" {
		speaker = m.Speaker
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO memories (id, content, source, speaker, confidence, created_at, updated_at, valid_at, invalid_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.Content, m.Source, speaker, m.Confidence, m.CreatedAt, m.UpdatedAt, m.ValidAt, m.InvalidAt,
	)
	if err != nil {
		return fmt.Errorf("cortex: insert memory: %w", err)
	}
```

(Still no `expired_at` — preserve the Phase 3a invariant.)

- [ ] **Step 6: Scan speaker in all four read paths**

For each of the four read paths listed above, add `speaker` to the SELECT column list (after `source`) and scan into a `sql.NullString`. Pattern per path — declare `var spk sql.NullString` alongside the existing `vat, iat, eat sql.NullTime`, add `&spk` to the `Scan` in the right position, and after scanning set:

```go
		if spk.Valid {
			m.Speaker = spk.String
		}
```

Apply to: `searchMemoriesMode` (`memory.go`), `getMemoriesByEntityMode` (`memory.go`), `getMemoryByID` (`memory.go`), `searchMemoryVectorMode` (`search.go`). Keep the column order consistent: `id, content, source, speaker, confidence, created_at, updated_at, valid_at, invalid_at, expired_at`.

> Note: `source` is currently scanned into `m.Source` (a plain string) in these paths — it relies on the driver mapping NULL→"". `speaker` uses `sql.NullString` to be explicit; both are fine. Match the surrounding style but prefer `sql.NullString` for the new column.

- [ ] **Step 7: Run test to verify it passes**

Run: `go test ./ -run TestPutMemory_PersistsSpeaker -v`
Expected: PASS.

- [ ] **Step 8: Run the full suite**

Run: `go test ./...`
Expected: PASS (root cortex; openai live-429 out of scope).

- [ ] **Step 9: Commit**

```bash
git add types.go store.go memory.go search.go memory_test.go
git commit -m "feat: add speaker provenance column to memories"
```

---

### Task 2: WithSpeaker ingest option; Remember stamps speaker

**Files:**
- Modify: `types.go` (`rememberConfig` `types.go:164-168`; add `WithSpeaker`)
- Modify: `cortex.go` (`Remember` memory loop `cortex.go:99-107`)
- Test: `cortex_test.go`

**Interfaces:**
- Consumes: Task 1's `Memory.Speaker` + persistence.
- Produces: `WithSpeaker(s string) RememberOption`; when passed to `Remember`, every memory ingested in that call gets `Speaker = s` unless the extractor already set one (mirror the `source` stamping logic at `cortex.go:102-104`).

- [ ] **Step 1: Write the failing test**

Add to `cortex_test.go`:

```go
func TestRemember_StampsSpeaker(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	c.SetExtractor(&mockExtractor{
		extractFn: func(_ context.Context, _, _ string) (*Extraction, error) {
			return &Extraction{
				Memories: []Memory{{Content: "Likes terse PR descriptions"}},
			}, nil
		},
	})

	if err := c.Remember(ctx, "I like terse PR descriptions", WithSpeaker("user")); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	found, err := c.SearchMemories(ctx, "terse PR descriptions", 10)
	if err != nil {
		t.Fatalf("SearchMemories: %v", err)
	}
	if len(found) != 1 || found[0].Speaker != "user" {
		t.Fatalf("expected speaker 'user' stamped on ingest, got %+v", found)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestRemember_StampsSpeaker -v`
Expected: FAIL — `WithSpeaker` undefined.

- [ ] **Step 3: Add the rememberConfig field and option**

In `types.go`, add `speaker string` to `rememberConfig` (`types.go:164-168`):

```go
type rememberConfig struct {
	source       string
	speaker      string
	contentType  string
	maxChunkSize int
}
```

And add the option near `WithSource` (`types.go:170-172`):

```go
// WithSpeaker stamps a speaker/provenance label (e.g. "user", "assistant",
// a document name) on every memory ingested in this Remember call, unless
// the extractor already set one. Records who asserted the fact, so
// attribution questions and assistant-statement recall work.
func WithSpeaker(speaker string) RememberOption {
	return func(c *rememberConfig) { c.speaker = speaker }
}
```

- [ ] **Step 4: Stamp speaker in Remember**

In `cortex.go`, the memory loop (`cortex.go:99-107`) already stamps `source`. Add the speaker stamp right after the source stamp:

```go
	for i := range extraction.Memories {
		m := &extraction.Memories[i]
		m.EntityIDs = allEntityIDs
		if cfg.source != "" && m.Source == "" {
			m.Source = cfg.source
		}
		if cfg.speaker != "" && m.Speaker == "" {
			m.Speaker = cfg.speaker
		}
		if err := c.PutMemory(ctx, m); err != nil {
			return fmt.Errorf("cortex: store memory: %w", err)
		}
		// ... embedding (unchanged in this task) ...
	}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./ -run TestRemember_StampsSpeaker -v`
Expected: PASS.

- [ ] **Step 6: Run the full suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add types.go cortex.go cortex_test.go
git commit -m "feat: WithSpeaker option stamps memory provenance on ingest"
```

---

### Task 3: Surface speaker in recall results

**Files:**
- Modify: `types.go` (`Result` struct `types.go:56-64`)
- Modify: `recall.go` (`recallMemories` `recall.go:162-186`; `recallMemoryVector` `recall.go:188-215`)
- Test: `recall_test.go`

**Interfaces:**
- Consumes: `Memory.Speaker` from the read paths (Task 1).
- Produces: memory `Result`s carry `Speaker`, so a recall response shows who asserted each fact.

- [ ] **Step 1: Write the failing test**

Add to `recall_test.go`:

```go
func TestRecall_SurfacesSpeaker(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	if err := c.PutMemory(ctx, &Memory{Content: "Standups moved to 10am", Speaker: "assistant"}); err != nil {
		t.Fatal(err)
	}
	c.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
			return []StructuredQuery{{Type: "memory_lookup", Params: map[string]any{"query": q}}}, nil
		},
	})

	results, err := c.Recall(ctx, "standups", WithLimit(10))
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
	if mem == nil || mem.Speaker != "assistant" {
		t.Fatalf("expected memory result with speaker 'assistant', got %+v", results)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestRecall_SurfacesSpeaker -v`
Expected: FAIL — `Result` has no `Speaker` field (compile error), then it isn't populated.

- [ ] **Step 3: Add Speaker to Result**

In `types.go`, in `Result` add after `Source` (`types.go:62`):

```go
	Speaker    string         `json:"speaker,omitempty"`
```

- [ ] **Step 4: Populate it in both memory recall paths**

In `recall.go`, both `recallMemories` and `recallMemoryVector` build a `Result` from each `Memory` with the fields `Type/Content/Confidence/EntityIDs/Source`. Add `Speaker: m.Speaker` to the `Result` literal in **both** functions:

```go
		res := Result{
			Type:       "memory",
			Content:    m.Content,
			Confidence: m.Confidence,
			EntityIDs:  m.EntityIDs,
			Source:     m.Source,
			Speaker:    m.Speaker,
		}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./ -run TestRecall_SurfacesSpeaker -v`
Expected: PASS.

- [ ] **Step 6: Run the full suite**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add types.go recall.go recall_test.go
git commit -m "feat: surface memory speaker in recall results"
```

---

### Task 4: Fact-augmented embedding helper

**Files:**
- Modify: `recall.go` (add `augmentForEmbedding`) — keep it in the root `cortex` package near other helpers.
- Test: `recall_test.go`

**Interfaces:**
- Consumes: a memory's content + the names of its linked entities.
- Produces: `augmentForEmbedding(content string, entityNames []string) string` — returns `"Entities: <sorted, comma-joined names>. " + content` when there is at least one entity, else `content` unchanged. Sorted for determinism. Pure function, no DB.

- [ ] **Step 1: Write the failing test**

Add to `recall_test.go`:

```go
func TestAugmentForEmbedding(t *testing.T) {
	// No entities → unchanged.
	if got := augmentForEmbedding("plain content", nil); got != "plain content" {
		t.Fatalf("no-entity case: got %q", got)
	}
	// Entities → sorted prefix, content preserved.
	got := augmentForEmbedding("ships in April", []string{"Stripe", "Alice"})
	want := "Entities: Alice, Stripe. ships in April"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// Empty-string entities are ignored (no blank tokens).
	got = augmentForEmbedding("x", []string{"", "Bob", ""})
	want = "Entities: Bob. x"
	if got != want {
		t.Fatalf("blank-token case: got %q want %q", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestAugmentForEmbedding -v`
Expected: FAIL — `augmentForEmbedding` undefined.

- [ ] **Step 3: Implement the helper**

Add to `recall.go` (it already imports `sort` and `strings`):

```go
// augmentForEmbedding prepends a memory's linked-entity context to its
// content for the purpose of embedding only (fact-augmented keys, à la
// Anthropic Contextual Retrieval). The stored content, FTS index, and
// query-side embedding are unaffected — this text is passed solely to the
// embedder at ingest, so a semantically-thin memory ("ships in April")
// becomes findable by the entities it concerns. Names are sorted for a
// deterministic key; blank names are dropped; with no names the content is
// returned unchanged.
func augmentForEmbedding(content string, entityNames []string) string {
	names := make([]string, 0, len(entityNames))
	for _, n := range entityNames {
		if strings.TrimSpace(n) != "" {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return content
	}
	sort.Strings(names)
	return "Entities: " + strings.Join(names, ", ") + ". " + content
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ -run TestAugmentForEmbedding -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add recall.go recall_test.go
git commit -m "feat: fact-augmented embedding key helper"
```

---

### Task 5: Wire fact-augmentation into Remember's memory embedding

**Files:**
- Modify: `cortex.go` (`Remember`, the memory-embed call `cortex.go:108-119`)
- Test: `cortex_test.go`

**Interfaces:**
- Consumes: `augmentForEmbedding` (Task 4); the entity names available in `Remember` via the `nameToID` map (`cortex.go:28,37`).
- Produces: the memory embedding stored at ingest is the embedding of the **augmented** text (entity context + content), not the bare content. The chunk-embed path (`cortex.go:80-90`) is unchanged — chunks have no entity linkage in standard ingest, so 3.1 is scoped to memories. The query-side embedding (`SearchMemoryVector`) is unchanged — bare query, asymmetric retrieval.

> **Entity names available at the memory loop:** `Remember` builds `nameToID map[string]string` (entity name → ID) at `cortex.go:28`, and links every memory to `allEntityIDs` (all extracted entities, `cortex.go:94-101`). Since each memory links to all entities, the augmentation set is the keys of `nameToID`. Compute the name slice once before the memory loop.

- [ ] **Step 1: Write the failing test**

Add to `cortex_test.go`. Use a recording embedder declared in the test to capture exactly what text was embedded:

```go
// recordingEmbedder captures every text passed to Embed, for asserting what
// gets embedded. Deterministic 8-dim vectors like testEmbedder.
type recordingEmbedder struct {
	embedded []string
}

func (e *recordingEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	e.embedded = append(e.embedded, texts...)
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = make([]float32, 8) // value doesn't matter for this test
	}
	return out, nil
}
func (e *recordingEmbedder) Dimensions() int { return 8 }

func TestRemember_EmbedsFactAugmentedMemory(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	rec := &recordingEmbedder{}
	c.SetEmbedder(rec)
	c.SetExtractor(&mockExtractor{
		extractFn: func(_ context.Context, _, _ string) (*Extraction, error) {
			return &Extraction{
				Entities: []Entity{
					{Name: "Alice", Type: "person"},
					{Name: "Stripe", Type: "org"},
				},
				Memories: []Memory{{Content: "ships in April"}},
			}, nil
		},
	})

	if err := c.Remember(ctx, "Alice says Stripe ships in April"); err != nil {
		t.Fatalf("Remember: %v", err)
	}

	// The memory must have been embedded with entity context prepended, not bare.
	var sawAugmented bool
	for _, txt := range rec.embedded {
		if txt == "Entities: Alice, Stripe. ships in April" {
			sawAugmented = true
		}
		if txt == "ships in April" {
			t.Fatalf("memory was embedded bare, expected fact-augmented; embedded=%v", rec.embedded)
		}
	}
	if !sawAugmented {
		t.Fatalf("expected fact-augmented memory embedding, embedded=%v", rec.embedded)
	}
}
```

> Note: `Remember` also embeds the chunk (the raw input text "Alice says Stripe ships in April"), which is fine and distinct from the memory content "ships in April". The assertion targets the memory text specifically.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestRemember_EmbedsFactAugmentedMemory -v`
Expected: FAIL — the memory is embedded as bare "ships in April", tripping the `t.Fatalf`.

- [ ] **Step 3: Compute entity names and augment the memory embed**

In `cortex.go`, before the memory loop (after `allEntityIDs` is built at `cortex.go:94-97`), collect the entity names:

```go
	// Entity-context names for fact-augmented memory embeddings (3.1).
	entityNames := make([]string, 0, len(nameToID))
	for name := range nameToID {
		entityNames = append(entityNames, name)
	}
```

Then in the embed call inside the memory loop (`cortex.go:109-118`), change the embedded text from `m.Content` to the augmented form:

```go
		// Embed the memory if embedder is configured. Embed the
		// fact-augmented key (entity context + content), not the bare
		// content, so thin memories are findable by their entities. The
		// stored content and FTS index keep the original text.
		if c.cfg.embedder != nil {
			augmented := augmentForEmbedding(m.Content, entityNames)
			vecs, err := c.cfg.embedder.Embed(ctx, []string{augmented})
			if err != nil {
				return fmt.Errorf("cortex: embed memory: %w", err)
			}
			if len(vecs) > 0 {
				if err := c.putEmbedding(ctx, m.ID, "memory", vecs[0]); err != nil {
					return fmt.Errorf("cortex: store memory embedding: %w", err)
				}
			}
		}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ -run TestRemember_EmbedsFactAugmentedMemory -v`
Expected: PASS.

- [ ] **Step 5: Run the full suite**

Run: `go test ./...`
Expected: PASS. (Existing recall/vector tests embed via `PutMemory`+`putEmbedding` directly or via `Remember`; augmentation only changes the stored memory vector. Tests that assert *retrieval* of a memory by its own content via `SearchMemoryVector` use `openTestDBWithEmbedder` and embed the bare content directly through `putEmbedding`, so they are unaffected. If any `Remember`-based vector recall test now fails, verify it's because the stored vector changed — that's expected; the fix is to query consistently, not to revert augmentation. Report such a case rather than silently editing assertions.)

- [ ] **Step 6: Commit**

```bash
git add cortex.go cortex_test.go
git commit -m "feat: embed fact-augmented memory keys at ingest"
```

---

### Task 6: Surface speaker through CLI and MCP

**Files:**
- Modify: `cmd/cortex/main.go` (`cmdRemember` `cmd/cortex/main.go:254-270`)
- Modify: `cmd/cortex/mcp/tools.go` (the `remember` tool `tools.go:18-38`)
- Modify: `cmd/cortex/CORTEX.md.template`
- Test: build + `go test ./cmd/...` (CLI flag parse if a test harness exists; otherwise build-only)

**Interfaces:**
- Consumes: `cortex.WithSpeaker` (Task 2) and the existing `cortex.WithSource`.
- Produces: `cortex remember <text> [--speaker <s>] [--source <s>]`; the MCP `remember` tool accepts an optional `speaker`. The CLI currently has neither flag (`cmd/cortex/main.go:254-270` joins all args as text), so this also closes the missing `--source` parity gap.

- [ ] **Step 1: Parse --speaker and --source in cmdRemember**

Replace the body of `cmdRemember` (`cmd/cortex/main.go:254-270`) with flag-aware parsing (mirror the `cmdRecall` arg-loop style at `main.go:280-298`):

```go
func cmdRemember() {
	args := os.Args[2:]
	var speaker, source string
	var textParts []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--speaker":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --speaker requires a value")
				os.Exit(1)
			}
			speaker = args[i+1]
			i++
		case "--source":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "error: --source requires a value")
				os.Exit(1)
			}
			source = args[i+1]
			i++
		default:
			textParts = append(textParts, args[i])
		}
	}
	if len(textParts) == 0 {
		fmt.Fprintln(os.Stderr, "usage: cortex remember <text> [--speaker <s>] [--source <s>]")
		os.Exit(1)
	}
	text := strings.Join(textParts, " ")

	cx := openCortex()
	defer cx.Close()

	var opts []cortex.RememberOption
	if speaker != "" {
		opts = append(opts, cortex.WithSpeaker(speaker))
	}
	if source != "" {
		opts = append(opts, cortex.WithSource(source))
	}

	ctx := context.Background()
	if err := cx.Remember(ctx, text, opts...); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Remembered.")
}
```

- [ ] **Step 2: Add the speaker param to the MCP remember tool**

In `cmd/cortex/mcp/tools.go`, the `remember` tool (`tools.go:18-38`) currently reads `content` and optional `source`. Add a `speaker` string param to the tool definition and pass `cortex.WithSpeaker` when present. Locate the `mcp.WithString("source", ...)` declaration and the opts-building block (`tools.go:30-33`); add alongside:

In the tool definition:
```go
			mcp.WithString("speaker", mcp.Description("Who asserted this (e.g. \"user\", \"assistant\", a document name). Optional provenance label.")),
```

In the handler, after the existing source handling:
```go
			if spk := request.GetString("speaker", ""); spk != "" {
				opts = append(opts, cortex.WithSpeaker(spk))
			}
```

> Verify the exact request-accessor pattern by reading how `source` is read in the same handler (`tools.go:30-33`); match it (e.g. `request.GetString`, or the args-map style the file actually uses). Mirror it precisely.

- [ ] **Step 3: Note speaker in CORTEX.md.template**

In `cmd/cortex/CORTEX.md.template`, in the "When to remember" or "Confidence"/provenance area, add a short bullet (match surrounding markdown style). A natural spot is after the remember guidance (~line 55):

```
- When it matters who said something — the user vs. you (the assistant) vs. a document — pass a speaker label so the fact is attributable. Use `--speaker` on the CLI or the `speaker` field on the MCP tool. "assistant" marks facts you inferred or stated; "user" marks what the user told you directly.
```

(Follow the canonical-source note at the top of the template — this is the embedded copy; the doc comment says edit `docs/CORTEX.md` and copy here. If `docs/CORTEX.md` exists and is gitignored, update the template directly since that's what's embedded; note the divergence in the report.)

- [ ] **Step 4: Build and test**

Run: `go build ./...`
Expected: success.
Run: `go test ./...`
Expected: PASS. If `cmd/cortex` has a flag-parse test harness (check `cmd/cortex/main_test.go`), add a minimal test that `cortex remember "x" --speaker user` routes the speaker through; otherwise build-only is acceptable for the CLI wiring and note it in the report.

- [ ] **Step 5: Commit**

```bash
git add cmd/cortex/main.go cmd/cortex/mcp/tools.go cmd/cortex/CORTEX.md.template
git commit -m "feat: --speaker/--source on remember CLI and MCP tool"
```

---

## Self-Review

**Spec coverage** (Phase 3b = research items 3.1 + 3.3):
- **3.1 Fact-augmented embedding keys:** Task 4 (helper) + Task 5 (wired into ingest, embedding-only, asymmetric). ✅
- **3.3 Speaker/source provenance:** Task 1 (field+column+persist+read), Task 2 (WithSpeaker+stamp), Task 3 (surface in recall), Task 6 (CLI/MCP/contract). ✅

**Placeholder scan:** No TBD/TODO — every code step shows complete code and exact paths. ✅

**Type / linkage consistency:**
- `Memory.Speaker` (Task 1) → persisted (Task 1) → stamped (Task 2) → read back (Task 1) → surfaced on `Result` (Task 3) → set via CLI/MCP (Task 6). Linear, consistent. ✅
- `augmentForEmbedding` (Task 4) consumed only in `Remember`'s memory-embed path (Task 5). Pure, no DB. ✅
- The four memory read paths all gain `speaker` in SELECT+scan (Task 1) — enumerated explicitly so none is missed. ✅
- `PutMemory` column order after this phase: `id, content, source, speaker, confidence, created_at, updated_at, valid_at, invalid_at` — `expired_at` still never inserted (Phase 3a invariant preserved). ✅

**Backward compatibility:**
- `speaker` added via `ensureColumn` (idempotent, nullable). Old rows → NULL → empty string. ✅
- Fact-augmentation only changes the *memory vector* stored at ingest; content, FTS, and query embeddings are untouched. Memories with no entities embed bare content (helper returns input). ✅

**Known scope edges (documented, not gaps):**
- Fact-augmentation applies to **memories only**, not chunks: chunks have no entity linkage in the standard ingest path. Chunk augmentation is a possible future item (would need per-chunk entity association).
- Speaker is a free-text label, not an enum — the extractor/agent chooses the vocabulary ("user"/"assistant"/document name). No validation; consistent with how `source` works.
- Re-embedding existing memories with augmentation is **not** done (no backfill) — augmentation applies to memories ingested after this phase. A re-embed/backfill pass is Tier 4 reconsolidation territory.
- The vault export still omits `speaker` (and the temporal fields) — consistent with the Phase 3a whole-branch note; a vault-projection enrichment is a separate follow-up.

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-06-23-tier3b-ingest-enrichment.md`.**

This is Phase 3b of Tier 3 (ingest-side enrichment). After it merges, Phase 3c (recall-side ranking: 3.2 time-aware query expansion built on Phase 3a's event-time, and 3.4 MMR reranker with a graph-distance signal) gets written as its own plan.

Execute subagent-driven (recommended), then write Phase 3c.
