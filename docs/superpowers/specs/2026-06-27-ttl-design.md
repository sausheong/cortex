# Memory TTL (ForgetAfter) — Design Spec

**Date:** 2026-06-27
**Status:** Approved (design); pending implementation plan
**Author:** brainstormed with Claude

## Summary

Add a `ForgetAfter *time.Time` field to `Memory`: a pre-declared expiry the LLM
extractor resolves at ingest (relative phrases like "tomorrow" resolved against
the injected current date). A new `Maintain` "expire" pass soft-retires memories
whose `forget_after` has passed. Completes cortex's memory-longevity model:
**static** (never decays) / **default** (decays gradually) / **TTL** (expires on
a deadline).

## Motivation

Transient facts — "meeting at 3pm today", "exam tomorrow", "in town this week" —
should not linger as current knowledge after their moment passes. Today they
persist until decay slowly erodes confidence, which is both too slow and
semantically wrong (the fact didn't get less certain; it stopped being
relevant). TTL gives such facts a clean, pre-declared end. It pairs with the
just-shipped static field: static = never expires, TTL = expires on a date, and
everything else decays in between.

## Scope

**In scope:**
- `ForgetAfter *time.Time` field on `Memory` (default nil).
- Schema migration (nullable `forget_after DATETIME`, NULL default).
- LLM extractor resolves `forget_after`, given today's date injected into the
  prompt (prompt guidance + parse in both providers).
- Current-date injection into `Extract` at call time.
- Persistence: `PutMemory` INSERT + all memory SELECT/scan paths.
- New `ExpireMemories` operation + `expire.go`/`expire_render.go`.
- A 5th `Maintain` pass ("expire"), after decay, before profile.
- CLI `cortex expire` subcommand + expire section in `cortex maintain` output.
- Tests across memory/expire/maintain/extractor layers + a post-merge live check.

**Out of scope (non-goals):**
- Recall-time expiry filtering. Expiry is durable (sets `expired_at` via the
  pass), not a live query filter. The row actually retires.
- MCP tool for expire (maintenance op, like decay/reconcile — no MCP surface).
- An offset grammar ("1d"/"2w") resolved in code. The LLM returns an absolute
  date; `ParseEventTime` parses it. (Considered and rejected for simplicity.)
- Backfilling existing memories. They default to nil (no expiry).
- Hard deletion on expiry. Soft-retire only (non-destructive, recoverable).

## Architecture

Touches the memory pipeline + adds one operation. Files:

- `types.go` — add `ForgetAfter *time.Time`; `ExpireReport`, `ExpireChange`,
  `ExpireOption`, `expireConfig`, `WithExpireDryRun`; extend `maintainConfig`
  (`skipExpire`), `MaintainReport` (`Expire *ExpireReport`), add `WithoutExpire`.
- `store.go` — migration: add `forget_after` column to `memories`.
- `memory.go` — `PutMemory` INSERT; the three SELECT/scan paths
  (`searchMemoriesMode`, `getMemoriesByEntityMode`, `getMemoryByID`).
- `llm/openai/llm.go`, `llm/anthropic/llm.go` — current-date injection in
  `Extract`; `forget_after` in the memory parse struct + prompt guidance in
  `defaultExtractionPrompt`.
- `extractor/llmext/extractor.go` — `forget_after` guidance in its prompt.
- `expire.go` — `ExpireMemories`.
- `expire_render.go` — `RenderExpireMarkdown`.
- `maintain.go` — 5th pass.
- `maintain_render.go` — expire section.
- `cmd/cortex/expire.go` — CLI subcommand; `cmd/cortex/main.go` — dispatch +
  usage.

## Data model

On `Memory` (`types.go`), beside the bi-temporal fields:

```go
// ForgetAfter is an ingestion-set expiry: the event time at which this
// memory should stop being current (e.g. "meeting at 3pm today",
// "exam tomorrow"). The Maintain expire pass soft-retires memories past
// this time. nil = no expiry (the default for durable facts). Set by the
// LLM extractor, which resolves relative phrases against the ingest date.
ForgetAfter *time.Time `json:"forget_after,omitempty"`
```

Schema (`store.go`), via the existing idempotent `ensureColumn` helper (same
path as `valid_at`/`invalid_at`/`expired_at`):

```sql
ALTER TABLE memories ADD COLUMN forget_after DATETIME
```

NULL default → existing memories have no expiry. Backward-compatible, no backfill.

Time-field relationships (the four temporal fields):
- `ValidAt` — when the fact became true (event time, existing).
- `InvalidAt` — when it stopped being true (event time, existing).
- `ExpiredAt` — when the system retired it (ingestion time, existing).
- `ForgetAfter` — pre-declared future expiry (new). When it passes, the expire
  pass sets `ExpiredAt = now`, leaving `InvalidAt` NULL.

## Extraction

**Current-date injection.** The LLM must know the ingest date to resolve
relative phrases. `Extract(ctx, text, prompt)` prepends a dated line at call
time (so it works for both the provider `defaultExtractionPrompt` const and the
`llmext` prompt without const surgery):

```go
dated := fmt.Sprintf("Today's date is %s.\n\n", time.Now().UTC().Format("2006-01-02")) + prompt
```

This is applied in each provider's `Extract`. It changes every extraction call's
prompt (harmless for non-TTL extraction; also helps the LLM resolve relative
`valid_at` dates as a side benefit).

**Prompt guidance** (added to the memories-field instructions in the provider
`defaultExtractionPrompt` and the `llmext` prompt, alongside `valid_at`):

```
- "forget_after": string (date) — set ONLY for memories that stop being
  relevant after a known point: appointments, deadlines, "today"/"tomorrow"/
  "this week", anything time-bound. Resolve relative phrases against today's
  date and return an absolute date (e.g. today is 2026-06-27, "tomorrow" ->
  "2026-06-28"). Omit for durable facts. A memory with forget_after should
  NOT be marked static.
```

**Parse path** (both providers): the per-memory `memObj` struct gains
`ForgetAfter string \`json:"forget_after"\``, passed through
`cortex.ParseEventTime(memObj.ForgetAfter)` into `cortex.Memory{}`. Reuses the
lenient parser already used for `valid_at` (unparseable/empty → nil, never
fails ingest). The bare-string memory fallback leaves it nil.

**Persistence** (`memory.go`): `PutMemory` INSERT adds `forget_after`; the three
SELECT/scan paths add it, scanned via `nullTimePtr` like the other temporal
fields.

## The expire pass

`expire.go`:

```go
func (c *Cortex) ExpireMemories(ctx context.Context, opts ...ExpireOption) (ExpireReport, error)
```

1. Scan currently-valid memories whose expiry has passed:

```sql
SELECT id, content, forget_after
FROM memories
WHERE <currentlyValidClause("")> AND forget_after IS NOT NULL AND forget_after <= ?
```

(`?` = now.) NOTE: no `AND static = 0` — TTL wins over static, so the sweep
ignores `static`.

2. For each hit, call the existing `InvalidateMemory(ctx, id, nil)` — sets
   `expired_at = now`, leaves `invalid_at` NULL (the soft-retire primitive
   decay's auto-prune uses).

3. Return:

```go
type ExpireChange struct {
    ID          string    `json:"id"`
    Content     string    `json:"content"`
    ForgetAfter time.Time `json:"forget_after"`
}
type ExpireReport struct {
    Scanned int            `json:"scanned"`
    Expired int            `json:"expired"`
    DryRun  bool           `json:"dry_run"`
    Changes []ExpireChange `json:"changes"`
}
```

Dry-run computes the report without calling `InvalidateMemory`.

On a write error mid-sweep, wrap and return (abort), matching decay's behavior.

`expire_render.go`: `RenderExpireMarkdown(r ExpireReport) string`, mirroring
`RenderDecayMarkdown`.

## Maintain integration

A 5th pass, order: reconcile → relate → decay → **expire** → profile. Expire
before profile so a just-expired memory is not distilled into a fresh digest.

```go
// 4.5 Expire (retire memories past their forget_after).
if !cfg.skipExpire && !cfg.dryRun {
    er, err := c.ExpireMemories(ctx)
    if err != nil { return report, err }
    report.Expire = &er
}
```

Under `--dry-run` the pass is skipped (it writes via `InvalidateMemory`),
consistent with how relate is handled. `WithoutExpire()` toggle. The maintain
render adds a `## Expire` section (nil → `_skipped_`, else
`RenderExpireMarkdown`).

Decision rationale for a pass (not recall-time filtering): durable retirement —
the row gets `expired_at`, so lint/stats stop counting it and the graph does not
bloat; consistent with decay/reconcile retiring durably.

## Surfaces

- **Go API:** `Memory.ForgetAfter`; `ExpireMemories`; `ExpireOption` /
  `WithExpireDryRun`; `WithoutExpire`; `ExpireReport`. No other new methods.
- **CLI:** `cortex expire [--dry-run] [--out <file>]` (mirrors `cortex decay`);
  expire section in `cortex maintain` output. `maintain` runs it automatically.
- **MCP:** none (maintenance op).
- **Config:** none.

## Error handling

- Unparseable/missing `forget_after` from the LLM → `ParseEventTime` → nil → no
  expiry; never fails ingest (same as `valid_at`).
- `InvalidateMemory` error during the sweep → wrap and return (abort), matching
  decay's write-error behavior.
- Empty graph / nothing expiring → empty report, no error.

## Testing

- `memory_test.go`: `ForgetAfter` round-trips through `PutMemory` → all read
  paths (set and nil); migration adds the column to a pre-existing DB (NULL
  default).
- `expire_test.go`:
  - past `forget_after` → retired (`expired_at` set, `invalid_at` NULL), drops
    from default recall, visible via `WithIncludeInvalid`.
  - future `forget_after` → untouched.
  - nil `forget_after` → untouched.
  - static memory with past `forget_after` → IS expired (TTL-wins rule).
  - `--dry-run` writes nothing.
- `maintain_test.go`: expire pass runs in `Maintain` (report populated);
  respects `WithoutExpire()`; skipped under dry-run.
- `extractor`/provider tests: stub LLM returning `"forget_after":"2026-06-28"`
  flows to `Memory.ForgetAfter`; the date-injection prepends "Today's date is
  ..." to the prompt (assert via a capturing stub).
- `expire_render_test.go`: report markdown (populated + empty).
- Post-merge live check (gpt-5.4-mini): `remember "lunch with Dana tomorrow"` →
  confirm `forget_after` ≈ tomorrow; verify retirement via a past-dated memory +
  `cortex expire`.

## Locked decisions

- Field: **`ForgetAfter *time.Time`**, default nil.
- Expiry source: **LLM resolves, given today's injected date** (absolute date
  returned, parsed by `ParseEventTime`).
- On expiry: **soft-invalidate** (`expired_at = now`, `invalid_at` NULL).
- Sweep: **new Maintain "expire" pass** (durable), order decay → expire →
  profile.
- Static + TTL conflict: **TTL wins** (expire sweep ignores `static`).

## Notes

- `/docs` is gitignored; this spec stays local, not committed.
- No `Co-Authored-By` trailer on commits (repo preference).
- Two prompt sources exist: provider `defaultExtractionPrompt` (where `valid_at`
  guidance lives) and the `llmext` extractor prompt. Add `forget_after`
  guidance to both. The date injection happens in the provider `Extract`.
