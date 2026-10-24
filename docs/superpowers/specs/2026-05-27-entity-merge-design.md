# Entity Merge — Design

**Date:** 2026-05-27
**Status:** Draft (approved during brainstorming)
**Author:** sausheong + Claude

## Background

Cortex's `PutEntity` upserts by `(name, type)`, so two entities with the same name and type are inherently merged. But over time the graph accumulates variants: `Alice Chen`, `alice chen`, `A. Chen` — same person, three rows. Today the only cleanup tool is `forget`, which deletes the entity and its memories. Users need a way to merge a duplicate into the canonical entity while preserving all its memories, relationships, and chunks.

## Goals

- Merge two entities (one kept, one dropped) such that every reference to the dropped entity now points at the kept one.
- Idempotent and atomic — the operation is wrapped in a transaction; either it all succeeds or nothing changes.
- Preview support via `--dry-run`.
- Provenance: the kept entity records what was merged into it.
- Zero schema changes.

## Non-goals

- Automatic duplicate detection (will land with `cortex lint`).
- Merge by name+type or fuzzy matching (CLI takes IDs only, matching `forget --entity` semantics).
- Undo / un-merge.
- MCP / HTTP surface (Go API + CLI only for v1; same posture as `forget` today).
- Cross-type merge (a `person` row cannot merge into an `organization` row — error).

## Scope and API

**New CLI command:**

```
cortex merge <keep-id> <drop-id> [--dry-run]
```

**New Go API on `Cortex`:**

```go
type MergeStats struct {
    Relationships int // re-targeted (after dedup)
    Memories      int // memory_entities rows re-targeted
    Chunks        int // re-targeted
    Embeddings    int // dropped (stale embedding for drop entity)
    DupesDropped  int // duplicate relationships + memory_entity rows removed during dedup
    AttrConflicts int // count of attributes where keep already had a value (keep won)
}

func (c *Cortex) MergeEntities(ctx context.Context, keepID, dropID string) (MergeStats, error)
```

**Errors:**

| Condition | Error |
|---|---|
| `keepID == dropID` | `"cannot merge an entity into itself"` |
| `keepID` not found | `"keep entity not found: <id>"` |
| `dropID` not found | `"drop entity not found: <id>"` |
| Types differ | `"cannot merge across types: %s (keep) vs %s (drop)"` |

All errors are returned without committing anything.

## Merge algorithm

Whole operation runs in one SQLite transaction. On any error, ROLLBACK.

### Step 1: Validate

- Look up `keep` and `drop` rows. Missing → typed error.
- If `keep.Type != drop.Type` → typed error (type mismatch).
- If `keepID == dropID` → typed error.

### Step 2: Re-target chunks

```sql
UPDATE chunks SET entity_id = :keep WHERE entity_id = :drop
```

No collision possible (`entity_id` isn't unique in `chunks`). Rows affected → `MergeStats.Chunks`.

### Step 3: Re-target memory_entities (with dedup)

The PK `(memory_id, entity_id)` collides if a memory is already linked to both keep and drop. Two-step:

```sql
-- Drop links from `drop` that would collide.
DELETE FROM memory_entities
WHERE entity_id = :drop
  AND memory_id IN (SELECT memory_id FROM memory_entities WHERE entity_id = :keep);

-- Re-target the rest.
UPDATE memory_entities SET entity_id = :keep WHERE entity_id = :drop;
```

Rows from the DELETE → `MergeStats.DupesDropped` (incremented).
Rows from the UPDATE → `MergeStats.Memories`.

### Step 4: Re-target relationships (with dedup)

Relationships table has no unique constraint, but semantically `(source_id, target_id, type)` is a logical identity. Same two-step pattern, applied separately to `source_id` and `target_id`:

```sql
-- Re-target source_id, dropping rows that would duplicate existing keep edges.
DELETE FROM relationships
WHERE source_id = :drop
  AND EXISTS (
    SELECT 1 FROM relationships k
    WHERE k.source_id = :keep
      AND k.target_id = relationships.target_id
      AND k.type      = relationships.type
  );
UPDATE relationships SET source_id = :keep WHERE source_id = :drop;

-- Same for target_id.
DELETE FROM relationships
WHERE target_id = :drop
  AND EXISTS (
    SELECT 1 FROM relationships k
    WHERE k.target_id = :keep
      AND k.source_id = relationships.source_id
      AND k.type      = relationships.type
  );
UPDATE relationships SET target_id = :keep WHERE target_id = :drop;

-- Drop self-loops created by the merge (e.g. "drop knows keep" → "keep knows keep").
DELETE FROM relationships WHERE source_id = :keep AND target_id = :keep;
```

Rows from UPDATEs → `MergeStats.Relationships` (sum).
Rows from DELETEs (all three) → `MergeStats.DupesDropped` (incremented).

### Step 5: Drop the stale embedding

```sql
DELETE FROM embeddings WHERE ref_id = :drop AND ref_type = 'entity'
```

Rows affected → `MergeStats.Embeddings`. The keep entity's embedding stays — its content didn't change, so re-embedding isn't needed.

### Step 6: Union attributes + record provenance

Load both entities' attributes. Union into the keep entity's map, **keep wins on duplicate keys**:

```go
attrConflicts := 0
for k, v := range dropEntity.Attributes {
    if _, exists := keepEntity.Attributes[k]; exists {
        attrConflicts++
        continue
    }
    keepEntity.Attributes[k] = v
}
```

`attrConflicts` → `MergeStats.AttrConflicts`.

Append a `merged_from` entry to the keep entity's attributes:

```go
type mergeRecord struct {
    ID       string         `json:"id"`        // drop entity's ID
    Name     string         `json:"name"`      // drop entity's name (before merge)
    Type     string         `json:"type"`
    Source   string         `json:"source,omitempty"`
    Attrs    map[string]any `json:"attrs,omitempty"` // full snapshot of drop's attrs
    MergedAt time.Time      `json:"merged_at"`
}
```

The `merged_from` value on the keep entity is an array (so the entity can survive multiple merges across its lifetime):

```go
existing, _ := keepEntity.Attributes["merged_from"].([]any)
existing = append(existing, mergeRecord{...})
keepEntity.Attributes["merged_from"] = existing
```

Persist:

```sql
UPDATE entities SET attributes = ?, updated_at = ? WHERE id = ?
```

**`name`, `type`, `source`, `confidence` are not touched** — keep wins on scalars (per design decisions).

### Step 7: Delete the drop entity

```sql
DELETE FROM entities WHERE id = :drop
```

By this point no live rows reference `drop`. The CASCADE/SET NULL behavior in the FK definitions is irrelevant — we've cleared dependents manually as defense in depth.

### Step 8: Commit

Return `MergeStats`. On any error in steps 1–7, ROLLBACK.

## Dry-run behavior

`--dry-run` runs steps 1–6 in the same transaction, then **ROLLBACK instead of COMMIT**. Stats are computed identically to a real run; zero leftover state. Simpler than maintaining a parallel "what-would-happen" pass.

CLI prints `Dry-run: would ...` prefix and `No changes written.` footer; identical stats output otherwise.

## CLI

```
cortex merge <keep-id> <drop-id> [--dry-run]
```

Output (real run):

```
Merging ent_01H7DROP → ent_01H7KEEP (Alice Chen, person)
  4 relationships re-targeted (1 duplicate dropped)
  7 memory links re-targeted
  2 chunks re-targeted
  1 stale embedding removed
  3 attributes unioned (1 conflict, keep value preserved)
Merge complete. ent_01H7DROP deleted.
```

Output (dry-run):

```
Dry-run: would merge ent_01H7DROP → ent_01H7KEEP (Alice Chen, person)
  4 relationships would be re-targeted (1 duplicate)
  ...
No changes written.
```

Errors → stderr, exit 1:

```
$ cortex merge ent_doesnt_exist ent_01H7DROP
error: keep entity not found: ent_doesnt_exist

$ cortex merge ent_01H7KEEP ent_01H7KEEP
error: cannot merge an entity into itself

$ cortex merge ent_person_id ent_org_id
error: cannot merge across types: person (keep) vs organization (drop)
```

`printUsage` gains:

```
  merge <keep-id> <drop-id> [--dry-run]
                                 Merge drop entity into keep entity; re-target all references
```

## Package layout

No new package. The merge method lives next to other graph mutations.

| File | Change |
|---|---|
| `entity.go` (or new `merge.go` if entity.go grows too large) | Add `MergeEntities()` + attribute-union helper |
| `types.go` | Add `MergeStats` struct |
| `cmd/cortex/main.go` | Add `cmdMerge()`, switch case `"merge"`, usage entry |
| `entity_test.go` (or new `merge_test.go`) | Integration tests |
| `README.md` | Add `### cortex merge` subsection in CLI Reference |
| `docs/CORTEX.md` + `cmd/cortex/CORTEX.md.template` | One paragraph on merging duplicates |

If `entity.go` grows past ~400 lines with merge in it, split into `merge.go` in the same package. Either choice is fine — keep together unless it gets unwieldy.

## Testing strategy

All hermetic (no LLM, no embedder). Seed a fixed graph via `PutEntity`/`PutRelationship`/`PutMemory`. Cover:

1. **Basic merge.** Drop has 2 relationships + 1 memory + 1 chunk. After merge: keep entity has all references, drop is gone, stats match.
2. **Relationship dedup.** Both entities have a `works_at Stripe` relationship. After merge: only one such relationship exists, `DupesDropped == 1`.
3. **memory_entities dedup.** Memory is linked to both. After merge: junction count drops by one, memory still linked to keep exactly once.
4. **Self-loop cleanup.** Drop has `drop knows keep`. After merge: no `keep knows keep`, the edge is dropped.
5. **Attribute union + conflict.** Keep has `{role: "engineer"}`, drop has `{role: "developer", team: "payments"}`. After merge: keep has `{role: "engineer", team: "payments", merged_from: [...]}`. `AttrConflicts == 1`.
6. **`merged_from` is appended, not overwritten.** Two sequential merges into the same keep produce a `merged_from` array of length 2.
7. **Errors:** missing keep, missing drop, self-merge, type mismatch. Each returns a typed error and no DB changes.
8. **Dry-run.** Stats match a real run; DB state is unchanged (compare row counts before and after).
9. **Chunks re-target.** Chunk with `entity_id = drop` now has `entity_id = keep`.
10. **Embedding cleanup.** Verify drop's embedding row is gone after merge.

CLI tests cover argument parsing: missing IDs → usage error; `--dry-run` flag recognized.

## Migration / rollout

Zero schema changes. `merged_from` lives in the existing `entities.attributes` JSON column. Existing brain.db files work immediately. Feature is additive — nothing existing changes behavior.

## Open questions

None blocking. Spec complete enough to plan from.
