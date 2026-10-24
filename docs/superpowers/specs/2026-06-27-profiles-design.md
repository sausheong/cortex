# Profiles — Design Spec

**Date:** 2026-06-27
**Status:** Approved (design); pending implementation plan
**Author:** brainstormed with Claude

## Summary

Add a per-entity **Profile**: a cached, two-part (`static[]` / `dynamic[]`) digest
built from an entity's linked memories. It gives an agent baseline context — who
the user is and what they're currently focused on — in a single fast call,
instead of forcing the agent to guess what to `recall`. Inspired by
supermemory's "user profiles" (a standing, real-time compaction of the graph),
adapted to cortex's entity-centric model.

The owner entity always has a profile. Other entities are opt-in via a tracking
marker. Profiles are cached on the entity and rebuilt when stale.

## Motivation

Cortex's core loop is "agents read from cortex before responding and write back
after." Today the only read primitive is `recall(query)`, which requires the
agent to know what to ask. The most valuable baseline context — identity,
durable preferences, current focus — is exactly what search misses, because the
agent doesn't think to query for it. A maintained digest makes that context
zero-query and cheap to fetch.

This is the highest impact-to-value learning from the supermemory comparison: it
changes the shape of the integration (a new always-available context primitive)
rather than improving a single number.

## Scope

**In scope:**
- `Profile(ctx, entityID)` Go API returning a `static[]` / `dynamic[]` digest.
- Owner-always + opt-in tracking for other entities.
- Recency-based partition (no schema change).
- LLM distillation with raw-text fallback.
- Cached on the entity with a freshness check (TTL + dirty count).
- A `Profile` pass in `Maintain` that refreshes eligible entities.
- Surfaces: Go API, CLI, MCP tool, CORTEX.md agent contract.

**Out of scope (explicit non-goals):**
- A `Static`/`kind` field on `Memory` (the "#2" learning). Profiles deliberately
  use recency-only partitioning so this feature ships independently. When the
  Static field lands later, the partition can be upgraded to promote identity
  facts immediately.
- New database tables or columns. Everything rides in the entity's existing
  `attributes` JSON.
- Multi-tenant / per-`containerTag` profiles (cortex is single-user/personal;
  the owner entity is the anchor).
- Structured LLM output for distillation (uses the existing `Summarize`).

## Architecture

New files, following the existing `<feature>.go` + `<feature>_render.go` pairing
(cf. `reconcile.go` / `reconcile_render.go`):

- `profile.go` — logic: `Profile`, `buildProfile`, partition, freshness check,
  `RefreshProfiles`, `TrackProfile`, `UntrackProfile`.
- `profile_render.go` — human + markdown + `--json` formatting.
- `profile_test.go`, `profile_render_test.go` — hermetic tests.
- Edits to `types.go` (public `Profile` type + options), `maintain.go`
  (4th pass), `cmd/cortex/main.go` (CLI), the MCP server (tool), and the
  CORTEX.md template (contract).

### Data flow

```
Profile(ctx, entityID)
  ├─ GetEntity(entityID)                    // not found → error
  ├─ read attributes["_profile"] (cachedProfile)
  ├─ freshness check (rebuild if ANY):
  │     1. no cache present
  │     2. now - BuiltAt > TTL (default 24h)
  │     3. MemoryCount != current count
  ├─ fresh → return cached (Cached: true)
  └─ stale → buildProfile → persist → return (Cached: false)

buildProfile(entity)
  ├─ mems := GetMemoriesByEntity(entityID)  // already temporal-filtered to valid
  ├─ partition → static[], dynamic[]
  ├─ distill:
  │     LLM present → Summarize(static), Summarize(dynamic) → bullets
  │     LLM absent  → raw grouped texts (ranked, capped); Distilled=false
  └─ marshal cachedProfile → attributes["_profile"]; PutEntity
```

Reuses existing primitives only: `GetEntity`, `PutEntity` (attributes JSON),
`GetMemoriesByEntity` (temporal-aware), `Summarize`. No new storage.

## Data model

No schema migration. Two reserved keys in the entity's `attributes` JSON.

### Tracking marker

```
attributes["_profiled"] = true   // absent/false = not tracked
```

The owner entity (`type=person, source=owner`) is profile-eligible regardless of
this marker.

### Cached digest (internal)

```go
type cachedProfile struct {
    Static      []string  `json:"static"`
    Dynamic     []string  `json:"dynamic"`
    BuiltAt     time.Time `json:"built_at"`
    MemoryCount int       `json:"memory_count"`
    Distilled   bool      `json:"distilled"`
}
```

Stored as `attributes["_profile"]`.

### Public return type (in `types.go`)

```go
type Profile struct {
    EntityID  string    `json:"entity_id"`
    Name      string    `json:"name"`
    Static    []string  `json:"static"`
    Dynamic   []string  `json:"dynamic"`
    BuiltAt   time.Time `json:"built_at"`
    Distilled bool      `json:"distilled"` // false → raw fallback (no LLM)
    Cached    bool      `json:"cached"`    // true → served from cache
}
```

### Freshness check

Rebuild if **any** of:
1. no `_profile` cached yet, or
2. `now - BuiltAt > TTL` (default **24h**; `WithProfileTTL(d)` option), or
3. `MemoryCount` != `len(GetMemoriesByEntity(id))` (cheap dirty check; catches
   new and retired memories since the last build).

### Partition rule (deterministic; recency-based)

- `dynamic[]` = most recent **K** memories by `created_at` (default **K=7**),
  restricted to a recency window (default **30 days**).
- `static[]` = the remaining (older and/or higher-confidence) memories, ranked
  by confidence then recency, capped at **N** (default **15**).
- Overlap rule: a memory in the recent window goes to `dynamic` only
  (dynamic wins); statics are drawn from outside the window.

Defaults are tunable via options (`WithProfileRecentK`, `WithProfileWindow`,
`WithProfileStaticCap`, `WithProfileTTL`).

**Known tradeoff:** a brand-new identity fact lands in `dynamic[]` until it ages
past the window. Accepted for v1; the future `Static` field would fix it.

## Build & distillation

`Profile(ctx, entityID, opts...)` is the read path (freshness check → cache or
rebuild). `buildProfile` does the work: load valid memories, partition, distill,
persist.

### Distillation prompts

Passed as the first text to the existing `Summarize(texts []string) (string, error)`:

- **static:** "Below are stable, long-term facts about {name}. Distill them into
  a concise deduplicated bullet list of who they are — role, traits, durable
  preferences. One fact per line, no preamble. Drop anything episodic or
  time-bound."
- **dynamic:** "Below are recent notes about {name}. Distill them into a short
  bullet list of their current context and recent activity. One item per line,
  no preamble."

Empty bucket → skip the call, empty list. Output is split into lines to form the
string slice.

### Maintain integration

A 4th pass after decay, skippable via `WithoutProfile()`:

```go
// 4. Profile (refresh owner + tracked entities' digests).
if !cfg.skipProfile {
    pr, err := c.RefreshProfiles(ctx)
    if err != nil { return report, err }
    report.Profile = &pr
}
```

`RefreshProfiles(ctx)` iterates owner + `_profiled` entities, rebuilding stale
ones, and returns:

```go
type ProfileReport struct {
    Scanned int      `json:"scanned"`
    Rebuilt int      `json:"rebuilt"`
    Skipped []string `json:"skipped,omitempty"` // entity IDs whose build failed
}
```

Under `--dry-run`, the profile pass is skipped (building writes to attributes),
consistent with how `relate` is handled.

### Tracking primitives

```go
func (c *Cortex) TrackProfile(ctx context.Context, entityID string) error    // _profiled=true
func (c *Cortex) UntrackProfile(ctx context.Context, entityID string) error  // remove _profiled and _profile
```

## Surfaces

### CLI (`cmd/cortex/main.go`)

```
cortex profile                 # owner profile (default target)
cortex profile <entity-id>     # specific entity's profile
cortex profile --track <id>    # mark entity as tracked
cortex profile --untrack <id>  # untrack + drop cache
cortex profile --json          # machine-readable output
```

Profiling an untracked non-owner entity works on demand (builds + returns) but
only owner + tracked entities are auto-refreshed by `Maintain`. When profiling
an untracked entity, print an inline tip: `cortex profile --track <id>` to keep
it fresh.

### MCP tool

```
tool: profile
  args:    { entity_id?: string }    // omitted → owner
  returns: { name, static[], dynamic[], built_at, distilled }
```

Registered alongside the existing recall/remember tools.

### CORTEX.md contract (`init-schema`)

Add a directive:

> **Getting context.** Call `profile` first for who the user is (identity,
> durable preferences, current focus) — one call, cached. Use `recall` for
> specific facts the profile doesn't cover. Don't `recall` for baseline
> "about me" context that `profile` already provides.

## Error handling

Follows cortex conventions (graceful degradation, flag-don't-fail):

- Entity not found → wrapped error from `Profile`.
- `Summarize` fails mid-build → log via the optional `logf` sink and fall back to
  raw texts (`Distilled:false`); never error. Mirrors recall's embedder-failure
  degradation.
- Malformed cached `_profile` JSON → treat as cache-miss and rebuild; never
  propagate.
- `Maintain` profile pass: a single entity's build failure is recorded in
  `ProfileReport.Skipped`, not aborting the whole pass.
- Entity with zero valid memories → empty `static`/`dynamic`, no LLM call, no
  error.

## Testing

Hermetic, table-driven, matching existing `_test.go` files. No live API.

- `profile_test.go`:
  - Partition: recency window, K/N caps, dynamic-wins overlap.
  - Freshness: all 3 triggers (no-cache, TTL-expired, count-changed).
  - Cache hit returns `Cached:true` with no rebuild (assert no LLM call).
  - Track/untrack mutate attributes correctly; untrack drops cache.
  - Distillation: stub LLM → bullets + `Distilled:true`; no LLM → raw fallback +
    `Distilled:false`; LLM error → raw fallback + logged.
  - `RefreshProfiles`: builds owner + tracked, skips untracked; dry-run writes
    nothing.
- `profile_render_test.go`: golden output for human + `--json`.

Stubs come from the existing `internal/testutil`.

## Defaults summary

| Knob | Default | Option |
|------|---------|--------|
| TTL | 24h | `WithProfileTTL` |
| Recent K (dynamic) | 7 | `WithProfileRecentK` |
| Recency window | 30 days | `WithProfileWindow` |
| Static cap (N) | 15 | `WithProfileStaticCap` |

## Future work (not this spec)

- `Static`/`kind` field on `Memory` → promote identity facts to `static[]`
  immediately and exempt them from decay.
- Structured distillation output if `Summarize` quality proves insufficient.
- Profile embedding for "find entities whose profile matches X" queries.
