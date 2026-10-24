# Tier 2b: Memory Reconciliation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Detect when a newer memory contradicts an older one and resolve it by soft-invalidating the stale one — the Zep/Graphiti two-stage pattern (LLM detects contradiction candidates; deterministic code decides supersede-vs-keep and closes the validity interval). Runs as a background `reconcile` pass over the existing maintain loop, dry-run by default with a separate apply step.

**Architecture:** Builds directly on Tier 2a's primitives (`InvalidateMemory`, `valid_at`/`invalid_at`, temporal recall). Conflict candidates are scoped **per-entity** (memories sharing a linked entity via `memory_entities`). Detection is delegated to an **optional `Reconciler` provider interface** — reconciliation type-asserts for it and no-ops gracefully if the configured LLM doesn't implement it, so the core `LLM` interface stays unchanged. Resolution is **deterministic Go** (interval-overlap logic), never the LLM's discretion. The pass produces a `ReconcileReport` (dry-run, like `lint`); a separate `--apply` path commits the invalidations (like `merge`). No ingest-path change; `Remember` stays additive.

**Tech Stack:** Go 1.25.1, `modernc.org/sqlite` (pure-Go, no CGo). Reuses the `LLM` provider pattern (anthropic/openai/mock), the `MergeEntities`/`MergeEntitiesDryRun` report+apply precedent, and the `RenderLintMarkdown` report-rendering pattern. Tests use `openTestDB`, `mockLLM` (extended), `testEmbedder`.

## Global Constraints

- **Module:** `github.com/sausheong/cortex`, root package `cortex`. Go 1.25.1.
- **No CGo, single binary.** Only `modernc.org/sqlite`. No new external dependencies.
- **The core `LLM` interface (`Extract`/`Decompose`/`Summarize`) MUST NOT change.** Conflict detection is a NEW separate optional interface; providers opt in. Reconciliation degrades gracefully (no-op with a clear reason) when the provider doesn't implement it or no LLM is configured.
- **Detection (LLM) is separated from resolution (deterministic code).** The LLM only flags candidate contradictions; the supersede-vs-keep-both decision and the interval math are reproducible Go. Never invalidate solely on an LLM boolean without the deterministic gate.
- **Soft-invalidate only.** Reconciliation calls Tier 2a's `InvalidateMemory` (sets `expired_at`/`invalid_at`, never deletes). `Forget` stays the only hard delete.
- **Dry-run by default.** The `Reconcile` API and `cortex reconcile` CLI produce a report and change nothing unless `--apply` (CLI) / the apply method (API) is invoked. Mirrors `MergeEntitiesDryRun` vs `MergeEntities`.
- **Candidate scope is per-entity.** Only memories sharing a linked entity are compared. Document this limitation (cross-entity contradictions are not detected this phase).
- **Confidence invariant** `[0,1]`, `coerceConfidence` unchanged. **Timestamps UTC.**
- **No `Co-Authored-By` trailer on commits.**
- **Tests:** `go test ./...` must pass (hermetic — detection is exercised via an extended `mockLLM`, never a live API). The `llm/openai` live-API tests fail on HTTP 429 quota — pre-existing, out of scope; ignore those.
- **TDD:** failing test → confirm fail → minimal implementation → confirm pass → commit.

---

## Core Model (definitions every task depends on)

**The two-stage pipeline, per entity:**

1. **Candidate gather** (deterministic): for an entity, load its currently-valid memories (oldest→newest by `created_at`). These are the candidate set.
2. **Detection** (LLM, via `Reconciler`): given the candidate memories, the LLM returns pairs `{staleID, supersededByID, reason}` where the second memory contradicts/supersedes the first. The LLM only *flags*; it does not decide what happens.
3. **Resolution** (deterministic): for each flagged pair, apply the gate:
   - Both must be currently-valid (not already invalidated).
   - The superseding memory must be **newer** (`created_at` strictly greater) than the stale one — a newer fact supersedes an older one, never the reverse. This is the deterministic guard that makes the LLM's direction-of-supersession non-authoritative.
   - If the gate passes → plan an invalidation: stale memory gets `invalid_at = superseding.created_at` (the event boundary; absent a better signal, the moment the newer fact was learned) via `InvalidateMemory`.
   - If the gate fails (same age, wrong direction, already invalid) → skip, record as rejected.

**Key types (defined across tasks, listed here for cross-reference):**

```go
// A single proposed supersession (Task 3 produces these).
type Supersession struct {
    StaleID         string  // memory to invalidate
    StaleContent    string  // for the report
    SupersededByID  string  // the newer memory that wins
    SupersededByContent string
    Reason          string  // LLM's stated reason
    InvalidAt       time.Time // = superseding memory's created_at
}

// Detection result from the Reconciler interface (Task 2).
type ConflictPair struct {
    StaleID        string `json:"stale_id"`
    SupersededByID string `json:"superseded_by_id"`
    Reason         string `json:"reason"`
}

// The dry-run report (Task 4).
type ReconcileReport struct {
    EntitiesScanned   int
    MemoriesScanned   int
    Proposed          []Supersession // gate-passed, ready to apply
    Rejected          []RejectedPair // gate-failed, with why
    Skipped           bool           // true if no Reconciler available
    SkipReason        string
}

type RejectedPair struct {
    StaleID        string
    SupersededByID string
    Reason         string // why the deterministic gate rejected it
}
```

---

## File Structure

| File | Responsibility | Change |
|------|----------------|--------|
| `types.go` | interfaces + structs | Add `Reconciler` interface; `ConflictPair`, `Supersession`, `RejectedPair`, `ReconcileReport` types; reconcile options |
| `reconcile.go` (new) | the reconciliation engine | candidate gather, detection call, deterministic gate, dry-run + apply |
| `reconcile_render.go` (new) | report rendering | `RenderReconcileMarkdown(ReconcileReport) string` |
| `llm/anthropic/llm.go` | provider | implement `DetectConflicts` (the Reconciler method) |
| `llm/openai/llm.go` | provider | implement `DetectConflicts` |
| `internal/testutil` + in-package mock | test double | extend `mockLLM` with a `detectConflictsFn` |
| `cmd/cortex/reconcile.go` (new) | CLI | `cortex reconcile [--apply] [--out <file>]` |
| `cmd/cortex/main.go` | CLI dispatch | wire the `reconcile` subcommand |
| `cmd/cortex/mcp/tools.go` | MCP | optional: a `reconcile` tool (dry-run only) |
| `*_test.go` | tests | per task |

---

### Task 1: The Reconciler interface and detection types

**Files:**
- Modify: `types.go`
- Test: `types_test.go` (compile-level / interface-satisfaction test)

**Interfaces:**
- Produces:
  - `Reconciler` interface: `DetectConflicts(ctx context.Context, memories []Memory) ([]ConflictPair, error)`. Separate from `LLM` — providers implement it optionally.
  - `ConflictPair`, `Supersession`, `RejectedPair`, `ReconcileReport` structs (as defined in Core Model).
- Consumes: existing `Memory`.

- [ ] **Step 1: Write the failing test**

Add to `types_test.go` (create if absent, `package cortex`):

```go
package cortex

import (
	"context"
	"testing"
)

// compile-time check: a type implementing DetectConflicts satisfies Reconciler.
type stubReconciler struct{}

func (stubReconciler) DetectConflicts(_ context.Context, _ []Memory) ([]ConflictPair, error) {
	return nil, nil
}

func TestReconcilerInterfaceSatisfied(t *testing.T) {
	var r Reconciler = stubReconciler{}
	if r == nil {
		t.Fatal("expected non-nil Reconciler")
	}
	// ConflictPair / Supersession / ReconcileReport must exist with expected fields.
	_ = ConflictPair{StaleID: "a", SupersededByID: "b", Reason: "x"}
	_ = Supersession{StaleID: "a", SupersededByID: "b"}
	_ = ReconcileReport{EntitiesScanned: 0}
	_ = RejectedPair{StaleID: "a"}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestReconcilerInterfaceSatisfied -v`
Expected: FAIL — `undefined: Reconciler` (and the structs).

- [ ] **Step 3: Add the interface and types**

In `types.go`, near the `LLM`/`Embedder`/`Extractor` interfaces (`types.go:84-97`):

```go
// Reconciler is an optional capability a provider may implement to detect
// contradictions among a set of memories. It is intentionally separate from
// LLM so the core interface stays stable and providers opt in. Reconciliation
// (reconcile.go) type-asserts the configured LLM for this interface and
// no-ops gracefully when it is absent.
//
// DetectConflicts receives a set of memories (typically all currently-valid
// memories linked to one entity) and returns pairs where one memory
// supersedes/contradicts another. The implementation only FLAGS candidates;
// the deterministic gate in reconcile.go decides what is actually applied.
type Reconciler interface {
	DetectConflicts(ctx context.Context, memories []Memory) ([]ConflictPair, error)
}

// ConflictPair is a detected contradiction: the memory identified by
// SupersededByID contradicts/replaces the one identified by StaleID.
type ConflictPair struct {
	StaleID        string `json:"stale_id"`
	SupersededByID string `json:"superseded_by_id"`
	Reason         string `json:"reason"`
}
```

Then near `MergeStats`/report types (`types.go:260+`):

```go
// Supersession is a gate-passed proposed invalidation produced by a reconcile
// dry-run: StaleID will be soft-invalidated with InvalidAt, because the newer
// SupersededByID memory contradicts it.
type Supersession struct {
	StaleID             string
	StaleContent        string
	SupersededByID      string
	SupersededByContent string
	Reason              string
	InvalidAt           time.Time
}

// RejectedPair is an LLM-flagged contradiction that the deterministic gate
// rejected (e.g. wrong supersession direction, already invalidated).
type RejectedPair struct {
	StaleID        string
	SupersededByID string
	Reason         string // why the gate rejected it
}

// ReconcileReport summarizes a reconcile dry-run.
type ReconcileReport struct {
	EntitiesScanned int
	MemoriesScanned int
	Proposed        []Supersession
	Rejected        []RejectedPair
	Skipped         bool   // true when no Reconciler-capable LLM is configured
	SkipReason      string
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ -run TestReconcilerInterfaceSatisfied -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add types.go types_test.go
git commit -m "feat: add Reconciler interface and reconciliation types"
```

---

### Task 2: Extend the test mock with detection

**Files:**
- Modify: `recall_test.go` (the in-package `mockLLM`, `recall_test.go:265+`)
- Test: covered by Task 3+ (this is test infrastructure); add a tiny direct test.

**Interfaces:**
- Produces: `mockLLM` gains a `detectConflictsFn func(ctx, []Memory) ([]ConflictPair, error)` field and a `DetectConflicts` method, so `mockLLM` satisfies both `LLM` and `Reconciler`. This lets reconcile tests inject deterministic detection without a live API.
- Consumes: `Reconciler` (Task 1).

- [ ] **Step 1: Write the failing test**

Add to `recall_test.go`:

```go
func TestMockLLM_SatisfiesReconciler(t *testing.T) {
	var r Reconciler = &mockLLM{
		detectConflictsFn: func(_ context.Context, mems []Memory) ([]ConflictPair, error) {
			return []ConflictPair{{StaleID: "x", SupersededByID: "y", Reason: "test"}}, nil
		},
	}
	got, err := r.DetectConflicts(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].SupersededByID != "y" {
		t.Fatalf("unexpected detection result: %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestMockLLM_SatisfiesReconciler -v`
Expected: FAIL — `mockLLM` has no field `detectConflictsFn` / no `DetectConflicts` method.

- [ ] **Step 3: Extend mockLLM**

In `recall_test.go`, add the field to the `mockLLM` struct and the method:

```go
// add to the mockLLM struct definition:
	detectConflictsFn func(ctx context.Context, memories []Memory) ([]ConflictPair, error)

// add as a method:
func (m *mockLLM) DetectConflicts(ctx context.Context, memories []Memory) ([]ConflictPair, error) {
	if m.detectConflictsFn != nil {
		return m.detectConflictsFn(ctx, memories)
	}
	return nil, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ -run TestMockLLM_SatisfiesReconciler -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add recall_test.go
git commit -m "test: extend mockLLM with DetectConflicts"
```

---

### Task 3: Candidate gather + deterministic gate + dry-run Reconcile

**Files:**
- Create: `reconcile.go`
- Modify: `types.go` (reconcile options if needed — see step)
- Test: `reconcile_test.go`

**Interfaces:**
- Consumes: `Reconciler` (Task 1), `mockLLM.DetectConflicts` (Task 2), `InvalidateMemory` + temporal recall (Tier 2a), `GetMemoriesByEntity` (currently-valid by default), entity enumeration.
- Produces: `Reconcile(ctx, opts ...ReconcileOption) (ReconcileReport, error)` — the **dry-run**. It: enumerates entities; for each, gathers currently-valid linked memories; if ≥2, calls `DetectConflicts`; runs the deterministic gate on each returned pair; accumulates `Proposed`/`Rejected`. Changes nothing. If the configured LLM doesn't implement `Reconciler` (or is nil), returns `ReconcileReport{Skipped: true, SkipReason: ...}` with no error.

- [ ] **Step 1: Write the failing test**

Create `reconcile_test.go`:

```go
package cortex

import (
	"context"
	"testing"
	"time"
)

func TestReconcile_DryRun_ProposesSupersession(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "Alice"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	older := &Memory{Content: "Alice's budget is 5000", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, older); err != nil {
		t.Fatal(err)
	}
	newer := &Memory{Content: "Alice's budget is 10000", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, newer); err != nil {
		t.Fatal(err)
	}
	// Force a created_at ordering so newer is strictly newer (PutMemory uses now();
	// two inserts in the same test may share a timestamp).
	older.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if _, err := c.db.ExecContext(ctx, `UPDATE memories SET created_at = ? WHERE id = ?`, older.CreatedAt, older.ID); err != nil {
		t.Fatal(err)
	}
	newer.CreatedAt = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if _, err := c.db.ExecContext(ctx, `UPDATE memories SET created_at = ? WHERE id = ?`, newer.CreatedAt, newer.ID); err != nil {
		t.Fatal(err)
	}

	c.SetLLM(&mockLLM{
		detectConflictsFn: func(_ context.Context, mems []Memory) ([]ConflictPair, error) {
			// Flag: newer supersedes older.
			return []ConflictPair{{StaleID: older.ID, SupersededByID: newer.ID, Reason: "budget changed"}}, nil
		},
	})

	rep, err := c.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if rep.Skipped {
		t.Fatalf("expected not skipped, got skip reason %q", rep.SkipReason)
	}
	if len(rep.Proposed) != 1 {
		t.Fatalf("expected 1 proposed supersession, got %d (rejected=%d)", len(rep.Proposed), len(rep.Rejected))
	}
	p := rep.Proposed[0]
	if p.StaleID != older.ID || p.SupersededByID != newer.ID {
		t.Fatalf("wrong supersession: %+v", p)
	}
	if !p.InvalidAt.Equal(newer.CreatedAt) {
		t.Fatalf("expected InvalidAt = newer.CreatedAt %v, got %v", newer.CreatedAt, p.InvalidAt)
	}

	// Dry-run changed nothing: both memories still currently-valid.
	got, err := c.SearchMemories(ctx, "budget", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("dry-run must not invalidate; expected 2 valid memories, got %d", len(got))
	}
}

func TestReconcile_GateRejectsWrongDirection(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "Bob"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	older := &Memory{Content: "Bob lives in Paris", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, older); err != nil {
		t.Fatal(err)
	}
	newer := &Memory{Content: "Bob lives in Berlin", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, newer); err != nil {
		t.Fatal(err)
	}
	older.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c.db.ExecContext(ctx, `UPDATE memories SET created_at = ? WHERE id = ?`, older.CreatedAt, older.ID)
	newer.CreatedAt = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	c.db.ExecContext(ctx, `UPDATE memories SET created_at = ? WHERE id = ?`, newer.CreatedAt, newer.ID)

	c.SetLLM(&mockLLM{
		detectConflictsFn: func(_ context.Context, _ []Memory) ([]ConflictPair, error) {
			// WRONG direction: claims the OLDER supersedes the NEWER.
			return []ConflictPair{{StaleID: newer.ID, SupersededByID: older.ID, Reason: "backwards"}}, nil
		},
	})

	rep, err := c.Reconcile(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Proposed) != 0 {
		t.Fatalf("gate must reject older-supersedes-newer, got %d proposed", len(rep.Proposed))
	}
	if len(rep.Rejected) != 1 {
		t.Fatalf("expected 1 rejected pair, got %d", len(rep.Rejected))
	}
}

func TestReconcile_SkipsWithoutReconciler(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	// Default: no LLM set, so no Reconciler.
	rep, err := c.Reconcile(ctx)
	if err != nil {
		t.Fatalf("Reconcile should not error when no reconciler: %v", err)
	}
	if !rep.Skipped {
		t.Fatal("expected Skipped=true when no Reconciler is configured")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./ -run TestReconcile -v`
Expected: FAIL — `c.Reconcile undefined`.

- [ ] **Step 3: Add ReconcileOption (minimal) to types.go**

For now the dry-run needs no options, but define the type for forward-compatibility and the apply path (Task 5). In `types.go`:

```go
type ReconcileOption func(*reconcileConfig)
type reconcileConfig struct {
	apply bool // false = dry-run (default)
}

// withApply is internal; the public apply entry point is ApplyReconcile
// (Task 5). Kept unexported so the default Reconcile is always dry-run.
func withApply() ReconcileOption {
	return func(c *reconcileConfig) { c.apply = true }
}
```

- [ ] **Step 4: Implement reconcile.go (dry-run path)**

Create `reconcile.go`:

```go
package cortex

import (
	"context"
	"fmt"
)

// Reconcile scans the graph per-entity for contradicting memories and returns
// a report of proposed supersessions WITHOUT changing anything (dry-run).
// Detection is delegated to the configured LLM if it implements Reconciler;
// if it does not (or none is configured), the report is marked Skipped.
// The supersede-vs-keep decision is made by a deterministic gate here, not by
// the LLM: a flagged pair is only proposed when the superseding memory is
// strictly newer than the stale one and both are currently valid.
//
// Use ApplyReconcile to actually invalidate the proposed stale memories.
func (c *Cortex) Reconcile(ctx context.Context, opts ...ReconcileOption) (ReconcileReport, error) {
	cfg := &reconcileConfig{}
	for _, o := range opts {
		o(cfg)
	}

	rec, ok := c.cfg.llm.(Reconciler)
	if c.cfg.llm == nil || !ok {
		return ReconcileReport{
			Skipped:    true,
			SkipReason: "no Reconciler-capable LLM configured",
		}, nil
	}

	var report ReconcileReport

	entityIDs, err := c.allEntityIDs(ctx)
	if err != nil {
		return ReconcileReport{}, err
	}

	for _, eid := range entityIDs {
		mems, err := c.GetMemoriesByEntity(ctx, eid) // currently-valid only (Tier 2a default)
		if err != nil {
			return ReconcileReport{}, err
		}
		if len(mems) < 2 {
			continue
		}
		report.EntitiesScanned++
		report.MemoriesScanned += len(mems)

		pairs, err := rec.DetectConflicts(ctx, mems)
		if err != nil {
			return ReconcileReport{}, fmt.Errorf("cortex: detect conflicts for entity %s: %w", eid, err)
		}

		byID := make(map[string]Memory, len(mems))
		for _, m := range mems {
			byID[m.ID] = m
		}

		for _, p := range pairs {
			stale, sOK := byID[p.StaleID]
			newer, nOK := byID[p.SupersededByID]
			// Deterministic gate.
			if !sOK || !nOK {
				report.Rejected = append(report.Rejected, RejectedPair{
					StaleID: p.StaleID, SupersededByID: p.SupersededByID,
					Reason: "id not in current candidate set",
				})
				continue
			}
			if !newer.CreatedAt.After(stale.CreatedAt) {
				report.Rejected = append(report.Rejected, RejectedPair{
					StaleID: p.StaleID, SupersededByID: p.SupersededByID,
					Reason: "superseding memory is not strictly newer than stale memory",
				})
				continue
			}
			report.Proposed = append(report.Proposed, Supersession{
				StaleID:             stale.ID,
				StaleContent:        stale.Content,
				SupersededByID:      newer.ID,
				SupersededByContent: newer.Content,
				Reason:              p.Reason,
				InvalidAt:           newer.CreatedAt,
			})
		}
	}

	// Apply path is Task 5; dry-run returns here.
	return report, nil
}

// allEntityIDs returns every entity id in the graph.
func (c *Cortex) allEntityIDs(ctx context.Context) ([]string, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT id FROM entities`)
	if err != nil {
		return nil, fmt.Errorf("cortex: list entity ids: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("cortex: scan entity id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cortex: iterate entity ids: %w", err)
	}
	return ids, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./ -run TestReconcile -v`
Expected: PASS (all three: proposes, gate-rejects-wrong-direction, skips-without-reconciler).

- [ ] **Step 6: Run the full suite**

Run: `go test ./...`
Expected: PASS. Ignore `llm/openai` 429.

- [ ] **Step 7: Commit**

```bash
git add reconcile.go types.go reconcile_test.go
git commit -m "feat: reconcile dry-run with per-entity detection and deterministic gate"
```

---

### Task 4: ApplyReconcile + report rendering

**Files:**
- Modify: `reconcile.go` (add `ApplyReconcile`)
- Create: `reconcile_render.go`
- Test: `reconcile_test.go`, `reconcile_render_test.go`

**Interfaces:**
- Consumes: `Reconcile` (Task 3), `InvalidateMemory` (Tier 2a).
- Produces:
  - `ApplyReconcile(ctx, opts...) (ReconcileReport, error)` — runs the same dry-run scan, then for each `Proposed` calls `InvalidateMemory(ctx, stale.ID, &p.InvalidAt)`. Returns the report (now representing what was applied). Stops and returns the error on the first invalidation failure (partial application is acceptable and recorded — but document it).
  - `RenderReconcileMarkdown(ReconcileReport) string` — human-readable, mirroring `RenderLintMarkdown`.

- [ ] **Step 1: Write the failing test**

Add to `reconcile_test.go`:

```go
func TestApplyReconcile_InvalidatesStale(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "Carol"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	older := &Memory{Content: "Carol's title is Engineer", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, older); err != nil {
		t.Fatal(err)
	}
	newer := &Memory{Content: "Carol's title is Director", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, newer); err != nil {
		t.Fatal(err)
	}
	older.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c.db.ExecContext(ctx, `UPDATE memories SET created_at = ? WHERE id = ?`, older.CreatedAt, older.ID)
	newer.CreatedAt = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	c.db.ExecContext(ctx, `UPDATE memories SET created_at = ? WHERE id = ?`, newer.CreatedAt, newer.ID)

	c.SetLLM(&mockLLM{
		detectConflictsFn: func(_ context.Context, _ []Memory) ([]ConflictPair, error) {
			return []ConflictPair{{StaleID: older.ID, SupersededByID: newer.ID, Reason: "promoted"}}, nil
		},
	})

	rep, err := c.ApplyReconcile(ctx)
	if err != nil {
		t.Fatalf("ApplyReconcile: %v", err)
	}
	if len(rep.Proposed) != 1 {
		t.Fatalf("expected 1 applied supersession, got %d", len(rep.Proposed))
	}

	// After apply: only the newer memory is currently-valid.
	got, err := c.SearchMemories(ctx, "Carol title", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != newer.ID {
		t.Fatalf("expected only newer memory valid after apply, got %d", len(got))
	}

	// The stale one is still retrievable with history.
	all, err := c.Recall(ctx, "Carol title", WithLimit(10), WithIncludeInvalid())
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("expected both memories via WithIncludeInvalid, got %d", len(all))
	}
}
```

Create `reconcile_render_test.go`:

```go
package cortex

import (
	"strings"
	"testing"
	"time"
)

func TestRenderReconcileMarkdown(t *testing.T) {
	rep := ReconcileReport{
		EntitiesScanned: 1,
		MemoriesScanned: 2,
		Proposed: []Supersession{{
			StaleID: "a", StaleContent: "budget 5000",
			SupersededByID: "b", SupersededByContent: "budget 10000",
			Reason: "changed", InvalidAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		}},
	}
	md := RenderReconcileMarkdown(rep)
	for _, want := range []string{"budget 5000", "budget 10000", "changed", "1"} {
		if !strings.Contains(md, want) {
			t.Fatalf("rendered report missing %q:\n%s", want, md)
		}
	}
}

func TestRenderReconcileMarkdown_Skipped(t *testing.T) {
	md := RenderReconcileMarkdown(ReconcileReport{Skipped: true, SkipReason: "no reconciler"})
	if !strings.Contains(md, "no reconciler") {
		t.Fatalf("expected skip reason in output, got:\n%s", md)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./ -run 'TestApplyReconcile|TestRenderReconcileMarkdown' -v`
Expected: FAIL — `ApplyReconcile` / `RenderReconcileMarkdown` undefined.

- [ ] **Step 3: Implement ApplyReconcile**

In `reconcile.go`, refactor so the scan logic is shared, then add apply. Simplest: have `ApplyReconcile` call `Reconcile` for the report, then apply:

```go
// ApplyReconcile runs the reconcile scan and then soft-invalidates each
// proposed stale memory (setting invalid_at to the superseding memory's
// created_at). Returns the report describing what was applied. On the first
// invalidation error it returns that error; supersessions applied before the
// failure remain applied (soft-invalidation is reversible). Returns the
// Skipped report unchanged when no Reconciler is configured.
func (c *Cortex) ApplyReconcile(ctx context.Context, opts ...ReconcileOption) (ReconcileReport, error) {
	report, err := c.Reconcile(ctx, opts...)
	if err != nil || report.Skipped {
		return report, err
	}
	for _, p := range report.Proposed {
		invalidAt := p.InvalidAt
		if err := c.InvalidateMemory(ctx, p.StaleID, &invalidAt); err != nil {
			return report, fmt.Errorf("cortex: apply supersession (stale %s): %w", p.StaleID, err)
		}
	}
	return report, nil
}
```

- [ ] **Step 4: Implement reconcile_render.go**

Model on `RenderLintMarkdown` (`lint_render.go`). Create `reconcile_render.go`:

```go
package cortex

import (
	"fmt"
	"strings"
)

// RenderReconcileMarkdown renders a ReconcileReport as a human-readable
// markdown summary, mirroring RenderLintMarkdown.
func RenderReconcileMarkdown(r ReconcileReport) string {
	var b strings.Builder
	b.WriteString("# Reconcile Report\n\n")

	if r.Skipped {
		fmt.Fprintf(&b, "Skipped: %s\n", r.SkipReason)
		return b.String()
	}

	fmt.Fprintf(&b, "Entities scanned: %d\n", r.EntitiesScanned)
	fmt.Fprintf(&b, "Memories scanned: %d\n", r.MemoriesScanned)
	fmt.Fprintf(&b, "Proposed supersessions: %d\n", len(r.Proposed))
	fmt.Fprintf(&b, "Rejected (gate): %d\n\n", len(r.Rejected))

	if len(r.Proposed) > 0 {
		b.WriteString("## Proposed Supersessions\n\n")
		for _, p := range r.Proposed {
			fmt.Fprintf(&b, "- **stale** %q (%s)\n", p.StaleContent, p.StaleID)
			fmt.Fprintf(&b, "  **superseded by** %q (%s)\n", p.SupersededByContent, p.SupersededByID)
			fmt.Fprintf(&b, "  reason: %s; invalid_at: %s\n\n", p.Reason, p.InvalidAt.Format("2006-01-02"))
		}
	}

	if len(r.Rejected) > 0 {
		b.WriteString("## Rejected by gate\n\n")
		for _, rj := range r.Rejected {
			fmt.Fprintf(&b, "- %s ⟶ %s: %s\n", rj.StaleID, rj.SupersededByID, rj.Reason)
		}
	}

	return b.String()
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./ -run 'TestApplyReconcile|TestRenderReconcileMarkdown' -v`
Expected: PASS.

- [ ] **Step 6: Run the full suite**

Run: `go test ./...`
Expected: PASS. Ignore `llm/openai` 429.

- [ ] **Step 7: Commit**

```bash
git add reconcile.go reconcile_render.go reconcile_test.go reconcile_render_test.go
git commit -m "feat: ApplyReconcile and reconcile report rendering"
```

---

### Task 5: Provider implementations (anthropic + openai DetectConflicts)

**Files:**
- Modify: `llm/anthropic/llm.go`
- Modify: `llm/openai/llm.go`
- Test: parsing unit tests (hermetic — no live calls)

**Interfaces:**
- Consumes: `cortex.Memory`, `cortex.ConflictPair`.
- Produces: `DetectConflicts` on both `*anthropic.LLM` and `*openai.LLM`, satisfying `cortex.Reconciler`. Each builds a prompt listing the candidate memories (id + content), asks the model to return JSON `{"conflicts":[{"stale_id","superseded_by_id","reason"}]}`, and parses it. **Parsing is the only hermetically-testable part** — extract a `parseConflictsJSON(raw string) ([]cortex.ConflictPair, error)` and unit-test that directly.

- [ ] **Step 1: Write the failing test (anthropic parser)**

Create `llm/anthropic/conflicts_test.go`:

```go
package anthropic

import "testing"

func TestParseConflictsJSON(t *testing.T) {
	raw := `{"conflicts":[{"stale_id":"a","superseded_by_id":"b","reason":"budget changed"}]}`
	got, err := parseConflictsJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].StaleID != "a" || got[0].SupersededByID != "b" {
		t.Fatalf("unexpected parse: %+v", got)
	}
}

func TestParseConflictsJSON_CodeFence(t *testing.T) {
	raw := "```json\n{\"conflicts\":[]}\n```"
	got, err := parseConflictsJSON(raw)
	if err != nil {
		t.Fatalf("should strip code fence: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %d", len(got))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./llm/anthropic/ -run TestParseConflictsJSON -v`
Expected: FAIL — `parseConflictsJSON` undefined.

- [ ] **Step 3: Implement DetectConflicts + parser (anthropic)**

In `llm/anthropic/llm.go`, add a detection prompt constant, the method, and the parser. The method mirrors `Extract` (`llm/anthropic/llm.go:91`): build a system prompt + a user message that lists the memories, call `l.client.Messages.New`, extract text, parse.

```go
const detectConflictsPrompt = `You are given a list of memories about the same subject, each with an ID.
Identify pairs where one memory CONTRADICTS or SUPERSEDES another (e.g. a value
changed, a status updated, a fact corrected). Do NOT flag memories that merely
add detail without contradicting.

Return ONLY valid JSON, no markdown:
{"conflicts":[{"stale_id":"<id of the outdated memory>","superseded_by_id":"<id of the newer memory that replaces it>","reason":"<short reason>"}]}

If there are no contradictions, return {"conflicts":[]}.`

// DetectConflicts implements cortex.Reconciler.
func (l *LLM) DetectConflicts(ctx context.Context, memories []cortex.Memory) ([]cortex.ConflictPair, error) {
	if len(memories) < 2 {
		return nil, nil
	}
	var sb strings.Builder
	for _, m := range memories {
		fmt.Fprintf(&sb, "ID %s: %s\n", m.ID, m.Content)
	}

	msg, err := l.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     l.model,
		MaxTokens: l.maxTokens,
		System:    []anthropic.TextBlockParam{{Text: detectConflictsPrompt}},
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(sb.String()))},
	})
	if err != nil {
		return nil, fmt.Errorf("anthropic: detect conflicts: %w", err)
	}
	return parseConflictsJSON(extractText(msg))
}

func parseConflictsJSON(raw string) ([]cortex.ConflictPair, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		if len(lines) > 2 {
			raw = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}
	var parsed struct {
		Conflicts []cortex.ConflictPair `json:"conflicts"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("anthropic: parse conflicts: %w", err)
	}
	return parsed.Conflicts, nil
}
```

(`strings`, `fmt`, `json` are already imported in this file. `cortex.ConflictPair`'s JSON tags `stale_id`/`superseded_by_id`/`reason` are defined in Task 1, so unmarshal binds directly.)

- [ ] **Step 4: Run test to verify it passes (anthropic)**

Run: `go test ./llm/anthropic/ -run TestParseConflictsJSON -v`
Expected: PASS.

- [ ] **Step 5: Mirror for openai**

Create `llm/openai/conflicts_test.go` with the same two tests (package `openai`). Implement `DetectConflicts` + `parseConflictsJSON` in `llm/openai/llm.go`, mirroring its `Extract` call structure (`llm/openai/llm.go:84+`). Run `go test ./llm/openai/ -run TestParseConflictsJSON -v` (red→green).

- [ ] **Step 6: Verify both providers satisfy the interface**

Add a compile-time assertion in each provider file (after the method):

```go
// anthropic/llm.go
var _ cortex.Reconciler = (*LLM)(nil)
// openai/llm.go
var _ cortex.Reconciler = (*LLM)(nil)
```

- [ ] **Step 7: Build + full suite**

Run: `go build ./... && go test ./...`
Expected: build success; tests pass (the new parser tests are hermetic). Ignore `llm/openai` 429 live-API failures.

- [ ] **Step 8: Commit**

```bash
git add llm/anthropic/llm.go llm/anthropic/conflicts_test.go llm/openai/llm.go llm/openai/conflicts_test.go
git commit -m "feat: implement DetectConflicts for anthropic and openai providers"
```

---

### Task 6: CLI `reconcile` subcommand

**Files:**
- Create: `cmd/cortex/reconcile.go`
- Modify: `cmd/cortex/main.go` (dispatch)
- Test: build + manual (CLI has no unit harness; mirror `cmd/cortex/lint.go` structure)

**Interfaces:**
- Consumes: `Reconcile`, `ApplyReconcile`, `RenderReconcileMarkdown`.
- Produces: `cortex reconcile [--apply] [--out <file>]`. Default = dry-run: print the rendered report. `--apply` = run `ApplyReconcile` and report what changed. `--out` writes markdown to a file (like `cortex lint --out`).

- [ ] **Step 1: Implement the subcommand**

Model on `cmd/cortex/lint.go` (`cmdLint`, arg parsing, `--out`). Create `cmd/cortex/reconcile.go`:

```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/sausheong/cortex"
)

func cmdReconcile() {
	apply := false
	out := ""
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--apply":
			apply = true
		case "--out":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "usage: cortex reconcile [--apply] [--out <file>]")
				os.Exit(1)
			}
			i++
			out = args[i]
		default:
			fmt.Fprintln(os.Stderr, "usage: cortex reconcile [--apply] [--out <file>]")
			os.Exit(1)
		}
	}

	cx := openCortex() // use the same opener the other subcommands use; see lint.go
	defer cx.Close()
	ctx := context.Background()

	var report cortex.ReconcileReport
	var err error
	if apply {
		report, err = cx.ApplyReconcile(ctx)
	} else {
		report, err = cx.Reconcile(ctx)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: %v\n", err)
		os.Exit(1)
	}

	md := cortex.RenderReconcileMarkdown(report)
	if out != "" {
		if err := os.WriteFile(out, []byte(md), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write report: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("wrote reconcile report to %s\n", out)
		return
	}
	fmt.Print(md)
}
```

**Note:** check `cmd/cortex/lint.go` for the exact cortex-opening helper name (it may be `openCortex()`, or inline `cortex.Open(...)` with env-based path). Use whatever `cmdLint` uses — do not invent `openCortex` if it doesn't exist; copy the real pattern.

- [ ] **Step 2: Wire dispatch in main.go**

In `cmd/cortex/main.go`, find the subcommand switch (where `lint`, `export`, etc. are dispatched) and add:

```go
	case "reconcile":
		cmdReconcile()
```

Also add `reconcile` to any usage/help text alongside `lint`.

- [ ] **Step 3: Build to verify it compiles**

Run: `go build ./...`
Expected: success.

- [ ] **Step 4: Smoke-test the dry-run on an empty DB**

Run: `go run ./cmd/cortex reconcile` (against a temp/empty DB per the project's env convention — check how lint is invoked).
Expected: prints a Reconcile Report with "Skipped: no Reconciler-capable LLM configured" (no LLM in a bare CLI invocation) OR zero proposals. Either is correct; confirm no crash.

- [ ] **Step 5: Full suite**

Run: `go build ./... && go test ./...`
Expected: pass. Ignore `llm/openai` 429.

- [ ] **Step 6: Commit**

```bash
git add cmd/cortex/reconcile.go cmd/cortex/main.go
git commit -m "feat: add cortex reconcile CLI subcommand"
```

---

### Task 7: Document reconciliation (CORTEX.md + README)

**Files:**
- Modify: `cmd/cortex/CORTEX.md.template`
- Modify: `README.md` (the maintain-loop / CLI reference section)
- Test: build only

**Interfaces:** none new.

- [ ] **Step 1: Document in the agent contract**

In `cmd/cortex/CORTEX.md.template`, in the maintain/temporal section, add 2-4 lines: cortex can detect when newer memories supersede older ones via `cortex reconcile` (dry-run) / `cortex reconcile --apply`; superseded memories are soft-invalidated (hidden from default recall, available via history). Match surrounding style.

- [ ] **Step 2: Document in README**

In `README.md`, in the maintain-loop area (where `lint`/`merge` are described — see the "Maintain" block near the top and the CLI reference), add `reconcile` with a one-line description and note it requires an LLM provider that implements conflict detection (anthropic/openai do). Note the per-entity scope limitation.

- [ ] **Step 3: Build (template is embedded) + suite**

Run: `go build ./... && go test ./...`
Expected: pass.

- [ ] **Step 4: Commit**

```bash
git add cmd/cortex/CORTEX.md.template README.md
git commit -m "docs: document memory reconciliation (reconcile)"
```

---

## Self-Review

**Spec coverage** (Phase 2b = per-entity candidates + optional Reconciler interface + LLM-detect/deterministic-resolve + report-then-apply):
- Reconciler interface (optional, graceful degrade): Task 1, used in Task 3's type-assertion + skip path. ✅
- Test mock detection: Task 2. ✅
- Per-entity candidate gather + deterministic gate + dry-run: Task 3. ✅
- Apply (soft-invalidate via Tier 2a `InvalidateMemory`) + report rendering: Task 4. ✅
- Provider implementations (anthropic + openai), hermetic parser tests: Task 5. ✅
- CLI `reconcile [--apply]` (report-then-apply, like merge dry-run/apply): Task 6. ✅
- Docs: Task 7. ✅
- Deterministic resolution separated from LLM detection (the load-bearing design constraint): enforced by the gate in Task 3 (strictly-newer + currently-valid), tested by `TestReconcile_GateRejectsWrongDirection`. ✅
- Soft-invalidate only / dry-run default / per-entity scope: all honored + documented. ✅

**Placeholder scan:** All code steps show complete code. Two flagged judgment calls with concrete fallbacks: (a) the CLI cortex-opening helper name — instructed to copy `cmdLint`'s real pattern, not invent `openCortex`; (b) provider `Extract` call structure — pointed at the exact existing method to mirror. No TBD/"handle errors"/"similar to". ✅

**Type consistency:**
- `Reconciler.DetectConflicts(ctx, []Memory) ([]ConflictPair, error)` (Task 1) — implemented by `mockLLM` (Task 2), anthropic/openai (Task 5), consumed by `Reconcile` (Task 3).
- `ConflictPair` JSON tags (`stale_id`/`superseded_by_id`/`reason`, Task 1) — relied on by the provider parsers (Task 5).
- `Supersession`/`RejectedPair`/`ReconcileReport` (Task 1) — produced by `Reconcile` (Task 3), consumed by `ApplyReconcile` + `RenderReconcileMarkdown` (Task 4).
- `InvalidateMemory(ctx, id, *time.Time)` (Tier 2a, already merged) — called by `ApplyReconcile` (Task 4) with `&p.InvalidAt`.
- `ReconcileOption`/`reconcileConfig` (Task 3) — the `apply` field is currently internal; `ApplyReconcile` is the public apply entry point (it re-runs the dry-run then applies), so `withApply` is unexported and could even be omitted. Noted: if a reviewer flags `withApply` as unused (YAGNI), it's safe to drop — `ApplyReconcile` doesn't need it. Keep the option type for forward-compat or remove the unused func; reviewer's call.

**Known scope edges (documented, not gaps):**
- **Per-entity scope:** memories with no shared entity, or genuinely cross-entity contradictions, are not detected. Documented in Architecture, Global Constraints, README (Task 7).
- **`InvalidAt = superseding.created_at`:** absent an extracted event-time (Phase 2a never sets `valid_at`), the supersession boundary is the *ingestion* time of the newer fact. This is a reasonable approximation; a future phase that extracts event-time can refine it.
- **Partial apply:** `ApplyReconcile` stops on first invalidation error; earlier applies persist (soft-invalidation is reversible). Documented in the method comment.
- **`ApplyReconcile` re-runs detection:** it calls `Reconcile` again rather than taking a prior report, so the LLM is queried twice across a dry-run-then-apply CLI flow. Acceptable (the apply path is the source of truth and avoids acting on a stale report); noted for the reviewer in case they prefer threading the report through.
- **Same-entity candidate set sent whole to the LLM:** an entity with very many memories sends them all in one detection prompt. Fine at personal scale; a future batching refinement is out of scope.

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-06-23-tier2b-memory-reconciliation.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — execute tasks in this session with checkpoints.

**Which approach?**
