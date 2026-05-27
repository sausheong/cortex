# Cortex Lint — Design

**Date:** 2026-05-27
**Status:** Draft (approved during brainstorming)
**Author:** sausheong + Claude

## Background

Cortex accumulates knowledge over time. Some of what accumulates is noise: entities extracted from one mention that never connect to anything, duplicate spellings of the same person, sources that produced nothing useful, memories floating with no entity link. `cortex lint` surfaces these so the user (or an agent reading the report) can clean up via `cortex merge` and `cortex forget`.

This is a read-only feature. It never mutates the graph. It complements the existing destructive primitives by helping the user know *what* to clean up.

## Goals

- Surface six categories of cleanup candidates via cheap SQL queries.
- Pure read operation — never modifies the graph.
- Markdown report by default (terminal-friendly, scriptable via `--out`).
- Stateless — every run is fresh; no schema changes, no config files.
- LLM-free and embedder-free — runs on any cortex configuration.

## Non-goals

- Vector-similarity near-duplicate detection (current near-dup is case-insensitive exact match; vector similarity is a follow-up).
- LLM contradiction detection.
- Stale-claim detection.
- JSON output (markdown only in v1; JSON is a future flag).
- Ignore lists / dismissed findings.
- Suggested fix commands inline.
- Auto-fix mode.
- MCP / HTTP exposure (CLI + Go API only in v1).

## The six checks

Each check is one SQL query (or two for self-joins). All findings carry the entity ID + minimum context needed to act on them.

| Check | What it finds | Why it matters |
|---|---|---|
| **Orphans** | Entities with zero relationships (inbound or outbound) AND zero memory links. | Likely extraction noise — someone or something mentioned but not connected to anything. |
| **EntitiesNoMemories** | Entities that have relationships but no memory links. | Less severe than orphans; could be an artifact of forgotten memories or direct API use. |
| **NearDuplicates** | Pairs of entities with same `type` and case-insensitively-equal `name`. | Almost always the same real-world thing — direct merge candidates. |
| **DeadSources** | Distinct `source` values present on memory rows but with no live entity carrying that source. | Defensive hygiene; usually surfaces nothing on a clean cortex-managed graph but catches weird states (direct SQL inserts, partial migrations, etc.). |
| **UnlinkedMemories** | Memories with zero rows in `memory_entities`. | Findable via search but invisible to graph traversal. |
| **LowConfidenceMemories** | Memories with `confidence < threshold` (default 0.3, configurable). Opt-in via `--low-confidence`. | Worth reviewing — LLM was uncertain. Skipped by default to avoid noise. |

### Markdown report layout

```markdown
# Cortex Lint Report

Scanned 247 entities, 89 relationships, 312 memories.

## Orphan entities (3)

Entities with no relationships and no memory links — likely noise.

- `ent_01H7AAAA` — "Mauve" (concept)
- `ent_01H7BBBB` — "internal" (concept)
- `ent_01H7CCCC` — "the thing" (concept)

## Near-duplicate entity names (2 pairs)

Same type + case-insensitively-equal name. Consider `cortex merge`.

- "Alice Chen" / "alice chen" (person): `ent_01H7DDDD` / `ent_01H7EEEE`
- "Stripe" / "STRIPE" (organization): `ent_01H7FFFF` / `ent_01H7GGGG`

## Entities without memories (12)
…

## Dead sources (1)

Source values present on memory rows but no live entity carries them.

- `notes/old-import.md`

## Memories with no entity links (5)

These memories are findable via search but not via graph traversal.

- `mem_01H7HHHH` — "Stripe is hiring aggressively in payments" (source: slack-export)
- …

## Low-confidence memories (skipped — pass --low-confidence to include)
```

Empty sections are omitted entirely. A healthy graph might only show the count summary and 1-2 sections.

## Go API

```go
// Lint scans the graph and returns a structured report of cleanup candidates.
// Pure read operation — never mutates the graph.
func (c *Cortex) Lint(ctx context.Context, opts ...LintOption) (LintReport, error)

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

type EntityRef struct {
    ID   string
    Name string
    Type string
}

type DuplicatePair struct {
    Type string
    A    EntityRef
    B    EntityRef
}

type MemoryRef struct {
    ID         string
    Content    string // first 80 chars + "..." if longer
    Source     string
    Confidence float64
}

// Options
type LintOption func(*lintConfig)

type lintConfig struct {
    lowConfidence          bool
    lowConfidenceThreshold float64
}

func WithLowConfidence() LintOption
func WithLowConfidenceThreshold(t float64) LintOption // implies WithLowConfidence
```

`WithLowConfidenceThreshold` implicitly enables the low-confidence section so the user doesn't have to pass both flags.

The struct-based result makes the report easy to test (assert counts and IDs without parsing markdown), easy to render, and easy to expose later via MCP/HTTP without re-implementing the checks.

## SQL approach per check

### Orphans

```sql
SELECT e.id, e.name, e.type FROM entities e
WHERE NOT EXISTS (SELECT 1 FROM relationships WHERE source_id = e.id OR target_id = e.id)
  AND NOT EXISTS (SELECT 1 FROM memory_entities WHERE entity_id = e.id)
ORDER BY e.type, e.name
```

### EntitiesNoMemories

```sql
SELECT e.id, e.name, e.type FROM entities e
WHERE NOT EXISTS (SELECT 1 FROM memory_entities WHERE entity_id = e.id)
  AND EXISTS (SELECT 1 FROM relationships WHERE source_id = e.id OR target_id = e.id)
ORDER BY e.type, e.name
```

### NearDuplicates

```sql
SELECT a.id, a.name, a.type, b.id, b.name, b.type
FROM entities a
JOIN entities b
  ON a.type = b.type
 AND LOWER(a.name) = LOWER(b.name)
 AND a.id < b.id
ORDER BY a.type, LOWER(a.name)
```

The `a.id < b.id` clause prevents self-pairs and double-counting (e.g. `(A, B)` and `(B, A)`).

### DeadSources

```sql
SELECT DISTINCT source FROM memories
WHERE source IS NOT NULL AND source != ''
  AND source NOT IN (
    SELECT DISTINCT source FROM entities
    WHERE source IS NOT NULL AND source != ''
  )
ORDER BY source
```

### UnlinkedMemories

```sql
SELECT id, content, source, confidence FROM memories m
WHERE NOT EXISTS (SELECT 1 FROM memory_entities WHERE memory_id = m.id)
ORDER BY created_at DESC
```

Content is truncated to 80 chars + `"..."` in the Go layer (not SQL — keeps the query portable).

### LowConfidenceMemories

```sql
SELECT id, content, source, confidence FROM memories
WHERE confidence < ?
ORDER BY confidence ASC
```

Only runs when `cfg.lowConfidence` is true.

### Counts (for header summary)

```sql
SELECT COUNT(*) FROM entities
SELECT COUNT(*) FROM relationships
SELECT COUNT(*) FROM memories
```

## CLI

```
cortex lint [--low-confidence] [--low-confidence-threshold <0-1>] [--out <file>]
```

- `--low-confidence` — include the low-confidence memories section (default off).
- `--low-confidence-threshold <n>` — override the default 0.3 cutoff. Implies `--low-confidence`.
- `--out <path>` — write report to file instead of stdout.

Default behavior: print markdown to stdout, exit 0. Lint is informational; findings do not cause non-zero exit codes.

`printUsage` gains:

```
  lint [--low-confidence] [--low-confidence-threshold <0-1>] [--out <file>]
                                 Scan the graph for cleanup candidates (orphans, near-duplicates, etc.)
```

## Errors

The only failure modes are DB errors (catastrophic). Typed as `cortex: lint: <step>: <err>`. No per-check error accumulation — if one check fails, the whole lint fails. Lint is simple enough that partial reports would create more confusion than value.

## Package layout

| File | Responsibility |
|---|---|
| `lint.go` (new) | `Lint()`, all six check helpers (`findOrphans`, `findNearDuplicates`, etc.), counts |
| `lint_render.go` (new) | `renderLintMarkdown(r LintReport) string` |
| `lint_test.go` (new) | Integration tests against in-memory cortex |
| `types.go` | Add `LintReport`, `EntityRef`, `DuplicatePair`, `MemoryRef`, `LintOption`, `lintConfig`, `WithLowConfidence`, `WithLowConfidenceThreshold` |
| `cmd/cortex/lint.go` (new) | `cmdLint()`, `parseLintArgs`, `lintOptions` |
| `cmd/cortex/lint_test.go` (new) | CLI flag-parsing tests |

Keeping check logic out of `cmd/cortex` (only the markdown rendering is consumed there) means future MCP/HTTP surfaces can call `Lint()` and format however they want.

## Testing strategy

All hermetic (no LLM, no embedder). Seed via `PutEntity` / `PutRelationship` / `PutMemory`.

| # | Test | Asserts |
|---|---|---|
| 1 | Empty graph | Counts all zero; no findings in any section. |
| 2 | Healthy graph | Seeded with wired-up entities, rels, memories. All finding lists empty. |
| 3 | Orphan detection | One orphan entity surfaces; one well-connected entity does not. |
| 4 | EntitiesNoMemories | Entity with rels but no memory links surfaces in `EntitiesNoMemories`, not `Orphans`. |
| 5 | Near-duplicate names | `"Alice Chen"` / `"alice chen"` (same type) → one pair. `"Alice Chen"` (person) and `"Alice Chen"` (organization) → no pair (different types). |
| 6 | Near-duplicate self-guard | Same entity does not pair with itself (`a.id < b.id` clause). |
| 7 | Dead source | Direct INSERT a memory with source `"old.md"` and no matching entity. `"old.md"` appears in `DeadSources`. |
| 8 | Unlinked memory | Memory with no `EntityIDs` surfaces in `UnlinkedMemories`. |
| 9 | Low-conf default off | Memory at 0.2 confidence: default `Lint()` returns empty `LowConfidenceMemories`. |
| 10 | Low-conf opt-in | Same setup with `WithLowConfidence()` → surfaces. |
| 11 | Low-conf custom threshold | Memory at 0.5: threshold 0.6 → surfaces; threshold 0.4 → doesn't. |
| 12 | Render snapshot | End-to-end render: assert specific lines appear (count summary, "## Orphan entities", an entity ID). Don't golden-file the whole output. |

CLI tests:

| # | Test | Asserts |
|---|---|---|
| 13 | Flag parsing | `--low-confidence`, `--low-confidence-threshold 0.5`, `--out /tmp/x`, unknown flag, and threshold-implies-enable behavior. |

## Rollout

Zero schema changes. Zero new dependencies. Existing brain.db files work immediately. Feature is purely additive — nothing existing changes behavior.

## Open questions

None blocking. Spec complete enough to plan from.
