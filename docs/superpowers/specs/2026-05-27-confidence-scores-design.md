# Confidence Scores on Extracted Items — Design

**Date:** 2026-05-27
**Status:** Draft (pending user review)
**Author:** sausheong + Claude

## Background

Cortex stores knowledge as entities, typed relationships, and short memory statements. Today every claim is treated as equally true — there is no way for an extractor (or a caller) to express "I'm 90% sure of this" vs "I'm guessing." This means:

- Recall returns confident facts and speculation interleaved with no signal to the consumer.
- Lint (a future feature) cannot challenge low-confidence stale claims because no claim is low-confidence.
- Agents writing back into cortex cannot record uncertainty even when they have it.

This spec adds a confidence score to extracted entities, relationships, and memories. The score is exposed everywhere claims surface (Recall results, vault pages, CLI output) but does not change default ranking — it is informational by default, with an opt-in filter.

## Goals

- One float column per claim table, populated by the LLM extractor at ingest time.
- Backward-compatible: existing brain.db files migrate transparently; existing tests pass unchanged.
- Visible everywhere it matters: Recall results, vault frontmatter and bullets, CLI output.
- Opt-in filter (`WithMinConfidence`) — never silently drops data by default.

## Non-goals

- A calibration pipeline that learns the LLM's actual reliability.
- Confidence on chunks (chunks are raw content, not claims).
- Bulk re-extraction of already-ingested data.
- Automatic ranking changes in recall (re-rank or hard-drop).
- A migrations framework (single ALTER, idempotent at Open time).

## Schema and data model

One column per claim table:

```sql
ALTER TABLE entities      ADD COLUMN confidence REAL NOT NULL DEFAULT 1.0;
ALTER TABLE relationships ADD COLUMN confidence REAL NOT NULL DEFAULT 1.0;
ALTER TABLE memories      ADD COLUMN confidence REAL NOT NULL DEFAULT 1.0;
```

**Scale:** 0.0–1.0 float. 1.0 = the LLM is confident; lower = uncertain.

**Migration mechanism.** SQLite does not support `ADD COLUMN IF NOT EXISTS`. A new helper in `store.go`:

```go
func ensureColumn(db *sql.DB, table, column, ddl string) error
```

queries `PRAGMA table_info(<table>)` and runs the `ALTER TABLE` statement only if the column is absent. Called for all three tables after the existing `CREATE TABLE IF NOT EXISTS` block runs. Idempotent. No version table. No migration framework.

**Go types.** `Entity`, `Relationship`, `Memory`, and `Result` each gain:

```go
Confidence float64 `json:"confidence"`
```

**Coercion at the Put boundary.** Code that constructs these structs without setting Confidence will get Go's zero value (0.0), which is not a valid "missing" sentinel. The `PutEntity`, `PutRelationship`, `PutMemory` methods coerce as follows before storing:

- `Confidence == 0.0` → store as 1.0 (treated as "caller did not specify; default fully confident")
- `Confidence < 0.0` → clamp to 0.0
- `Confidence > 1.0` → clamp to 1.0
- All other values stored as-is

The 0.0 → 1.0 coercion is what preserves backward compatibility: existing callers (deterministic extractor, manual API use, tests) don't set Confidence and get the same behavior as today.

The clamp-don't-fail policy applies to bad LLM outputs (e.g. `confidence: 1.5`). Failing an entire ingest because of one out-of-range number is a worse experience than clamping and continuing.

## Extraction prompt and parsing

The LLM extractor prompt at `extractor/llmext/extractor.go:10-20` is updated to request a `confidence` field on every item:

```
Analyze the following text and extract structured knowledge.
Return a JSON object with the following fields:
- "entities": array of objects with "name", "type", optional "attributes", and "confidence"
- "relationships": array of objects with "source", "target", "type", optional "attributes", and "confidence"
- "memories": array of objects with "content", optional "entity_ids", and "confidence"

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

Return ONLY valid JSON, no markdown formatting.
```

**Four-anchor calibration.** The 1.0 / 0.7 / 0.4 / 0.2 anchors discretize the float scale so the LLM produces a small set of well-defined values rather than uncalibrated continuous output. Empirically more reliable than asking for a free-form probability.

**Parsing.** The JSON parsers at the LLM provider boundary (wherever `Extract` results are unmarshaled into `cortex.Extraction`) accept `confidence` on each item. Missing field → Go zero value 0.0 → coerced to 1.0 by the Put layer.

**Non-LLM extractors unaffected.** The deterministic extractor (`extractor/deterministic/`) and any user-written `Extractor` implementations produce items with Confidence=0.0 → coerced to 1.0. Only the LLM extractor — which can actually generate calibrated values — populates the field meaningfully.

## Recall API and result surface

**Result struct** (`types.go:45`) gains `Confidence float64`. Populated wherever per-strategy recall functions build their Result items (`recall.go:112-200` — `recallMemories`, `recallKeyword`, `recallVector`, `recallGraph`).

- `recallMemories` — reads `confidence` from the matched `memories` row.
- `recallGraph` — reads `confidence` from the matched `entities` row.
- `recallKeyword` and `recallVector` — operate on the `chunks` table; chunks have no confidence column. Resolution: if the chunk's `entity_id` is non-null, the chunk inherits that entity's confidence. If null, the result reports Confidence=1.0 (treated as unscored — same default as legacy data). Cheap one-extra-query per result; acceptable given keyword/vector hit counts are bounded by the recall limit.

**New recall option:**

```go
func WithMinConfidence(c float64) RecallOption {
    return func(cfg *recallConfig) { cfg.minConfidence = c }
}
```

`recallConfig` gains `minConfidence float64`. Default 0.0 (no filtering). Applied as a post-RRF, pre-limit filter:

```go
if cfg.minConfidence > 0 {
    filtered := final[:0]
    for _, r := range final {
        if r.Confidence >= cfg.minConfidence {
            filtered = append(filtered, r)
        }
    }
    final = filtered
}
```

Hard threshold (`>=`), not a soft re-rank. Consistent with the design's "expose only, no automatic ranking change" decision.

**CLI surface:**

`cortex recall <query> [--min-confidence 0.5]` accepts the optional flag. Output format includes confidence inline as a whole percentage:

```
[memory  score=0.82 conf=95%] Alice joined Stripe — notes/intro.md
[memory  score=0.71 conf=40%] Alice might leave Stripe next year — slack-export
```

Whole percent only — float precision past that is noise given LLM calibration limits.

**MCP and HTTP surfaces:** the new `Confidence` field on `Result` serializes automatically via JSON tags. Clients reading the JSON gain the field with no server code change. The min-confidence filter becomes an optional request parameter on both surfaces, mirroring the Go API option.

**`cortex entity get <id>` CLI output** gains one display line per entity showing confidence (since the display layer is already being touched).

## Vault export interaction

`vault/render.go` is updated:

**Entity frontmatter** gains a line:

```yaml
confidence: 0.95
```

Only emitted when `Confidence != 1.0`. Keeps fully-confident pages clean.

**Memory bullets** in the `## Memories` section get an inline suffix when below 1.0:

```markdown
- Alice might leave Stripe next year (conf 40%) — `slack-export`
```

Whole-percent display, parenthesized, before the source suffix.

**Re-rendering existing pages.** Users who want confidence to appear on already-exported pages run `cortex export --full` (or delete `.cortex-export.json`). Behavior already documented in the vault export spec.

## File-by-file changes

| File | Change |
|---|---|
| `store.go` | Add `ensureColumn(db, table, column, ddl)` helper; call it three times in `Open` after the existing schema block |
| `types.go` | Add `Confidence float64 \`json:"confidence"\`` to `Entity`, `Relationship`, `Memory`, `Result` |
| `entity.go` | `PutEntity` coerces + clamps; `GetEntity` and `FindEntities` SELECT include `confidence` |
| `relationship.go` | `PutRelationship` coerces + clamps; `GetRelationships` includes `confidence` |
| `memory.go` | `PutMemory` coerces + clamps; `GetMemoriesByEntity` and `SearchMemories` include `confidence` |
| `recall.go` | All four `recall<Strategy>` functions populate `Result.Confidence`; new post-RRF filter; new `WithMinConfidence` option |
| `types.go` | Add `WithMinConfidence` option + `minConfidence` field on `recallConfig` |
| `extractor/llmext/extractor.go` | Update `extractionPrompt` to request `confidence` per item |
| `llm/openai/llm.go` | Accept `confidence` field on entity/relationship/memory items when unmarshaling extraction JSON |
| `llm/anthropic/llm.go` | Same as openai — accept `confidence` field on extraction items |
| `vault/render.go` | Emit frontmatter `confidence:` when != 1.0; inline `(conf N%)` on memory bullets when < 1.0 |
| `cmd/cortex/main.go` | `recall` accepts `--min-confidence`; recall + entity get display confidence |
| `docs/CORTEX.md` and `cmd/cortex/CORTEX.md.template` | Brief mention so agents factor confidence into how they assert facts |
| `README.md` | Short paragraph on confidence in recall/remember sections |

## Testing strategy

**Unit tests (hermetic, no LLM):**

- `store_test.go` — schema migration is idempotent: Open a fresh db, close, reopen — no errors, schema unchanged. Open a db with the *old* schema (build a fixture: create temp db, run only the original `CREATE TABLE` statements without confidence columns, close, reopen with new code, verify columns exist and existing rows have confidence=1.0).
- `entity_test.go` / `relationship_test.go` / `memory_test.go` — Put with Confidence=0.0 stores 1.0. Put with 1.5 stores 1.0. Put with -0.1 stores 0.0. Round-trip preserves valid in-range values exactly.
- `recall_test.go` — Result includes Confidence. `WithMinConfidence(0.5)` filters out lower-confidence items but does not change ranking among remaining items.
- `extractor/llmext/extractor_test.go` — fixture LLM returns JSON with confidence fields; parsed Extraction carries them through. Second fixture omits confidence; results have zero values that get defaulted upstream.

**Integration test (opt-in via `live` build tag):**

- One test that calls a real configured LLM with the updated prompt against a fixed input, parses the result, and asserts every item carries a `confidence` field in (0, 1]. Guards against prompt drift over model versions.

**Vault golden tests:**

- New `vault/testdata/entity_with_confidence.golden.md` — entity at confidence 0.7 with one memory at 0.4 and one at 1.0. Verifies frontmatter `confidence: 0.7`, memory bullet with `(conf 40%)`, second memory bullet with no suffix (since 1.0 is the default).
- Existing goldens are unchanged because all existing test entities/memories have Confidence=1.0 (default).

## Rollout

1. Schema migration runs on the next `cortex.Open` after deploy. Idempotent — running on an already-migrated db is a no-op.
2. Existing rows become confidence=1.0. Existing recall results gain a `confidence: 1.0` field; existing recall ranking unchanged because no filter is applied by default.
3. Existing tests continue to pass because Confidence=0.0 → coerced to 1.0 at the Put boundary; consumers see 1.0 = old behavior.
4. New ingests via the LLM extractor produce real confidence values. Users opt into the filter via `--min-confidence` or `WithMinConfidence`.
5. Vault re-export on next `cortex export` run picks up the new frontmatter/bullet rendering for newly-modified pages. To backfill all pages, run `cortex export --full`.

No data migration, no downtime, no breaking changes for consumers of any surface.

## Open questions

None blocking. Design is complete enough to plan from.
