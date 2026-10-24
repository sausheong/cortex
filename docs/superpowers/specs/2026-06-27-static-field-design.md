# Memory.Static Field — Design Spec

**Date:** 2026-06-27
**Status:** Approved (design); pending implementation plan
**Author:** brainstormed with Claude

## Summary

Add a `Static bool` field to `Memory` that distinguishes stable identity/preference
facts from episodic ones. The LLM extractor classifies each memory at ingest.
Static memories are (1) exempt from confidence decay and (2) promoted into a
profile's `static[]` section regardless of age — fixing the recency-only
partition limitation the profiles live-test exposed (all freshly-ingested facts
landed in `dynamic[]`).

This is the deferred "#2" follow-on to the profiles feature, delivering both of
its payoffs in one coherent change.

## Motivation

Two real problems:

1. **Uniform decay is wrong for identity facts.** `DecayConfidence` erodes
   "Alice's birthday is March 3" at the same rate as "Alice is in town this
   week." Identity facts should not decay.
2. **Profiles' recency-only partition strands new identity facts.** Verified
   live: ingesting "Alice is a staff engineer at Stripe" today puts it in
   `dynamic[]` (within the 30-day window), never `static[]`, until it ages out.

A single `Static` flag fixes both.

## Scope

**In scope:**
- `Static bool` field on `Memory` (default false).
- Schema migration (idempotent `ALTER TABLE ... ADD COLUMN`, default 0).
- LLM extractor classifies `static` at ingest (prompt + parse in both providers).
- Persistence: `PutMemory` INSERT + all memory SELECT/scan paths.
- Decay exemption: static memories excluded from the decay scan.
- Profile partition: static memories → `static[]` regardless of recency.
- CLI `entity get` renders a static marker on memories.
- Tests across memory/decay/profile/extractor layers + a post-merge live check.

**Out of scope (non-goals):**
- A `Kind` enum (fact/preference/episode). Both payoffs need only the binary
  split; bool is YAGNI-correct. Revisit only if per-kind decay rates are wanted.
- A `WithStatic()` Remember option / CLI flag / MCP arg. Classification is the
  LLM's job; `PutMemory` already accepts a `*Memory` with `Static` set for
  programmatic callers.
- Backfilling/ re-classifying existing memories. They default to false
  (decaying) until re-ingested.
- Heuristic or rule-based classification. The LLM does it.

## Architecture

Touches the existing memory pipeline; no new files. Files modified:

- `types.go` — add `Static bool` to `Memory`.
- `store.go` — migration: add `static` column to `memories`.
- `memory.go` — `PutMemory` INSERT; `GetMemoriesByEntity` /
  `getMemoriesByEntityMode` / `getMemoryByID` / `SearchMemories` SELECT + scan.
- `extractor/llmext/extractor.go` — extend `extractionPrompt`.
- `llm/openai/llm.go`, `llm/anthropic/llm.go` — add `static` to the memory
  parse struct and pass into `cortex.Memory{}`.
- `decay.go` — add `AND static = 0` to the decay scan.
- `profile.go` — `partitionMemories` static-first rule.
- `*_render.go` / CLI — render a static marker in `entity get`.

## Data model

On `Memory` (`types.go`):

```go
// Static marks an identity/preference fact (vs. an episodic one). Static
// memories are exempt from confidence decay and surface in a profile's
// static[] section; non-static memories decay and feed dynamic[]. Set by
// the LLM extractor; defaults false (the safe, decaying default).
Static bool `json:"static"`
```

Schema (`store.go`), via the existing idempotent `ensureColumn` helper (same
path as `speaker`, `last_decay_at`):

```sql
ALTER TABLE memories ADD COLUMN static INTEGER NOT NULL DEFAULT 0
```

- SQLite stores the bool as INTEGER 0/1.
- Default 0 → every existing memory becomes `static=false` (decaying) on
  migration. Backward-compatible, no backfill, no behavior change for old data.

**Rationale for bool + default false:** both payoffs need only the binary split,
and false is the safe default — a fact misclassified as dynamic merely decays
(recoverable), whereas defaulting true would wrongly freeze episodes. Consistent
with the `coerceConfidence` "unset → safe default" philosophy.

## Extraction

Prompt (`extractor/llmext/extractor.go`): add `"static"` to the memories field
description and a guidance block mirroring the existing confidence guidance:

```
- "static": boolean — true for stable identity facts and durable
  preferences (role, hometown, "prefers dark mode", relationships);
  false for episodic or time-bound facts (meetings, "learning X this
  week", "exam tomorrow", anything with a date or that will expire).
  When unsure, use false.
```

Parse path (both providers): the per-memory unmarshal struct (`memObj` in
`llm/openai/llm.go`, equivalent in `llm/anthropic/llm.go`) gains
`Static bool \`json:"static"\``, passed into the `cortex.Memory{...}`
construction. The bare-string memory fallback leaves `Static` false.

Deterministic extractor: unchanged — never sets `Static`, so non-LLM ingest
yields `static=false`.

## Payoff 1: Decay exemption

`decay.go` `DecayConfidence` scan query gains `AND static = 0`:

```sql
SELECT id, content, confidence, created_at, last_decay_at
FROM memories
WHERE <currentlyValidClause> AND static = 0
```

Static memories never enter the decay candidate set — no confidence erosion,
never auto-pruned below the floor. No change to `decayedConfidence` math.

## Payoff 2: Profile partition

`profile.go` `partitionMemories` — new static-first rule (signature unchanged;
`[]Memory` now carries `.Static`):

- `static[]` = all memories with `Static == true`, **plus** the existing
  aged/non-recent overflow — ranked confidence desc then recency desc, capped at
  `staticCap` (15).
- `dynamic[]` = recent **non-static** memories (within window, newest-first,
  capped at `recentK` 7).
- A static memory is never dynamic, even if created today.

Precedence: `Static==true` → static bucket; else the existing recency rule.

## Surfaces

- **Go API:** `Static` is a public `Memory` field; no new methods or options.
- **CLI:** `entity get <id>` renders a `[static]` marker on static memories. No
  new command.
- **MCP:** `Static` rides along in the existing `recall` JSON automatically once
  on the struct. No tool changes.
- **Config:** none.

## Error handling

None new. `Static` is a plain bool with a safe zero value; missing/malformed
JSON → false (graceful, consistent with the existing pipeline).

## Testing

- `memory_test.go`: `Static` round-trips through `PutMemory` →
  `GetMemoriesByEntity` / `getMemoryByID` (true and false); migration adds the
  column to a pre-existing DB and defaults old rows to false.
- `decay_test.go`: a `static=true` memory is NOT decayed/pruned even when old
  and below floor; a `static=false` one still is (regression guard).
- `profile_test.go`: a `static=true` memory created today lands in `static[]`,
  not `dynamic[]` (the live-test fix); existing recency tests still pass.
- `extractor/llmext` + provider tests: a stub LLM returning `"static":true`
  flows through to `Memory.Static`; bare-string memory → false.
- Post-merge live check (gateway, gpt-5.4-mini): re-run the Alice scenario,
  confirm identity facts populate `static[]`.

## Locked decisions

- Field shape: **bool `Static`**, default false (not a Kind enum).
- Classification: **LLM during extraction** (not heuristic, not explicit-only).
- Scope: **both** payoffs (decay exemption + profile partition).
- No new Remember option / CLI flag / MCP arg.

## Notes

- `/docs` is gitignored in this repo; this spec stays local, not committed.
- No `Co-Authored-By` trailer on commits (repo preference).
