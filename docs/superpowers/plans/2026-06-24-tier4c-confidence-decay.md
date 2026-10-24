# Tier 4 Phase 4c: Confidence Decay + Auto-Prune Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the graph **forget gracefully**. Old, unreinforced memories should lose confidence over time (deterministic age-based exponential decay), and memories whose decayed confidence falls below a floor should be auto-**soft-invalidated** (system-retired, not deleted — reversible via `as_of`/`include_invalid`). This is the reconsolidation half of supermemory's "dreaming" done in the open: a repeatable, idempotent-in-effect pass that down-weights stale facts and prunes the truly dead ones, while never destroying anything. The user chose **decay + auto-prune threshold**.

**Architecture:** Five tasks. (1) A `last_decay_at` anchor column on memories (idempotent migration) so repeated decay passes compose correctly regardless of cadence. (2) A pure `decayedConfidence(current, elapsed, halfLife) float64` — exponential half-life decay, the deterministic core. (3) `DecayConfidence(ctx, opts...) (DecayReport, error)` — scans currently-valid memories, applies decay since each one's `last_decay_at` (or `created_at` if never decayed), writes the new confidence + advances `last_decay_at`, and **auto-prunes** (soft-invalidate) any whose decayed confidence drops below the floor. (4) Report rendering. (5) CLI `cortex decay` with `--half-life`, `--floor`, `--dry-run`.

**The decay math (load-bearing correctness property):** exponential decay composes. Decaying by the elapsed time *since the last decay* on every run yields a result mathematically identical to one decay over the total elapsed time, for any split of the interval: `c·0.5^(t₁/H)·0.5^(t₂/H) = c·0.5^((t₁+t₂)/H)`. This is what makes a cron-driven repeated pass safe — running daily vs weekly converges to the same confidence for the same wall-clock age. The `last_decay_at` anchor is what enables it: each run decays only the un-decayed remainder.

**Tech Stack:** Go 1.25.1, `modernc.org/sqlite` (pure-Go, no CGo). Tests use `openTestDB`. The decay math is pure and unit-tested without a DB.

## Global Constraints

- **Module path:** `github.com/sausheong/cortex` (root package is `cortex`).
- **No CGo, single binary.** Only existing dependencies. Decay math uses stdlib `math` only.
- **Backward compatibility:** `last_decay_at` added via the existing idempotent `ensureColumn` pattern (`store.go:107`), nullable `DATETIME`. Existing DBs: NULL `last_decay_at` means "never decayed" → the first decay run anchors from `created_at`. No `DROP`, no destructive migration. A DB that never runs decay behaves exactly as today (confidence unchanged).
- **Soft-invalidate only (never delete):** auto-prune calls `InvalidateMemory(ctx, id, nil)` — `invalidAt` is **nil** because the fact did not stop being true in the world; the system stopped *trusting* it. That sets `expired_at` (system-retirement / ingestion-time) and leaves `invalid_at` NULL (event-time). The memory drops out of default recall but remains reachable via `as_of`/`include_invalid`. This is the correct bi-temporal semantics: decay-pruning is a system-confidence judgment, not an event-time fact change. Document this distinction explicitly.
- **Only decay currently-valid memories:** already-retired memories (`expired_at` or `invalid_at` set) are skipped — decaying a retired memory is meaningless and could re-touch it. Use the currently-valid predicate.
- **Determinism & idempotent-in-effect:** the decay math is deterministic given (confidence, elapsed, halfLife). Because decay composes and `last_decay_at` advances, running the pass twice in immediate succession (near-zero elapsed on the second run) is a near-no-op — confidence barely changes. The pass must be safe to run on any cadence. NOTE: `time.Now()` is used to compute elapsed; that's inherent to a time-based decay and is fine in production code (only the *pure* `decayedConfidence` is unit-tested with injected elapsed; the DB method's now() is exercised via integration tests that backdate `last_decay_at`).
- **Confidence stays in [0,1]:** decayed confidence is clamped to `[0,1]`; it only ever decreases (decay never raises confidence). The existing `coerceConfidence` zero==1.0 rule applies at *ingest*; decay operates on already-stored values and must NOT re-coerce a genuine decayed value toward 1.0 (decay writes the literal decayed number, including small values near the floor).
- **Floor semantics:** a memory is auto-pruned when its *post-decay* confidence is **strictly below** the floor (default e.g. 0.05). The floor is configurable. A dry-run computes and reports what *would* change without writing.
- **Commit trailer:** do NOT add a `Co-Authored-By` trailer to commits.
- **Test command:** `go test ./...` must pass. Hermetic only. (`llm/openai` live-429 pre-existing, out of scope.)
- **TDD:** failing test first, watch fail, implement, watch pass, commit.

---

## File Structure

| File | Responsibility | Change |
|------|----------------|--------|
| `store.go` | migration | `ensureColumn(db, "memories", "last_decay_at", "DATETIME")` |
| `math.go` | pure decay | `decayedConfidence(current, elapsed, halfLife) float64` |
| `math_test.go` | decay tests | half-life, composition, clamp |
| `types.go` | types | `DecayReport`, `DecayChange`, `DecayOption` (`WithHalfLife`, `WithFloor`, `WithDecayDryRun`), `decayConfig` |
| `decay.go` | new file | `DecayConfidence` (scan → decay → write → auto-prune) |
| `decay_render.go` | new file | `RenderDecayMarkdown` |
| `cmd/cortex/decay.go` | new file | `cmdDecay` |
| `cmd/cortex/main.go` | dispatch | `case "decay"` |
| `*_test.go` | tests | per task |

---

### Task 1: last_decay_at anchor column

**Files:**
- Modify: `store.go` (`Open`, after the speaker migration)
- Test: `store_test.go`

**Interfaces:**
- Produces: a nullable `last_decay_at DATETIME` column on `memories`, added idempotently. NULL = never decayed.

- [ ] **Step 1: Write the failing test**

Add to `store_test.go`:

```go
func TestSchema_MemoriesHaveLastDecayAt(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	m := &Memory{Content: "decayable fact"}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}

	// The column must exist and default to NULL for a fresh memory.
	var lastDecay sql.NullTime
	err := c.db.QueryRowContext(ctx,
		`SELECT last_decay_at FROM memories WHERE id = ?`, m.ID,
	).Scan(&lastDecay)
	if err != nil {
		t.Fatalf("select last_decay_at: %v", err)
	}
	if lastDecay.Valid {
		t.Fatalf("expected NULL last_decay_at for a fresh memory, got %v", lastDecay.Time)
	}
}
```

Confirm `database/sql` is imported in `store_test.go` (add if missing).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestSchema_MemoriesHaveLastDecayAt -v`
Expected: FAIL — `no such column: last_decay_at`.

- [ ] **Step 3: Add the migration**

In `store.go`, after the `speaker` column migration (`store.go:167`-ish, the `ensureColumn(db, "memories", "speaker", "TEXT")` block), add:

```go
	if err := ensureColumn(db, "memories", "last_decay_at", "DATETIME"); err != nil {
		db.Close()
		return nil, fmt.Errorf("cortex: migrate memories.last_decay_at: %w", err)
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ -run TestSchema_MemoriesHaveLastDecayAt -v`
Expected: PASS.

- [ ] **Step 5: Full suite + commit**

Run: `go test ./...` → PASS. `go vet ./...` clean.

```bash
git add store.go store_test.go
git commit -m "feat: add last_decay_at anchor column to memories"
```

---

### Task 2: Pure exponential decay function

**Files:**
- Modify: `math.go`
- Test: `math_test.go`

**Interfaces:**
- Produces: `decayedConfidence(current float64, elapsed, halfLife time.Duration) float64` — returns `current · 0.5^(elapsed/halfLife)`, clamped to `[0,1]`. Guards: `halfLife <= 0` → return `current` unchanged (no decay configured); `elapsed <= 0` → return `current` (no time passed); the result never exceeds `current` (decay only decreases) and never goes below 0.

> `math.go` currently imports `encoding/binary`, `math`, `sort`. Add `time`.

- [ ] **Step 1: Write the failing test**

Add to `math_test.go`:

```go
func TestDecayedConfidence(t *testing.T) {
	const H = 30 * 24 * time.Hour // 30-day half-life

	// One half-life halves confidence.
	if got := decayedConfidence(1.0, H, H); !approxEq(got, 0.5, 1e-9) {
		t.Fatalf("one half-life: got %v want 0.5", got)
	}
	// Two half-lives → quarter.
	if got := decayedConfidence(1.0, 2*H, H); !approxEq(got, 0.25, 1e-9) {
		t.Fatalf("two half-lives: got %v want 0.25", got)
	}
	// Composition: decaying t1 then t2 == decaying (t1+t2) once.
	t1, t2 := 10*24*time.Hour, 20*24*time.Hour
	once := decayedConfidence(1.0, t1+t2, H)
	twice := decayedConfidence(decayedConfidence(1.0, t1, H), t2, H)
	if !approxEq(once, twice, 1e-9) {
		t.Fatalf("composition broken: once=%v twice=%v", once, twice)
	}
	// elapsed <= 0 → unchanged.
	if got := decayedConfidence(0.8, 0, H); got != 0.8 {
		t.Fatalf("zero elapsed: got %v want 0.8", got)
	}
	if got := decayedConfidence(0.8, -time.Hour, H); got != 0.8 {
		t.Fatalf("negative elapsed: got %v want 0.8", got)
	}
	// halfLife <= 0 → unchanged (no decay configured).
	if got := decayedConfidence(0.8, H, 0); got != 0.8 {
		t.Fatalf("zero half-life: got %v want 0.8", got)
	}
	// Result never exceeds current, never below 0.
	if got := decayedConfidence(0.3, 100*H, H); got < 0 || got > 0.3 {
		t.Fatalf("clamp: got %v out of (0, 0.3]", got)
	}
}

func approxEq(a, b, eps float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= eps
}
```

> If `approxEq` already exists in the test package, drop the duplicate definition.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestDecayedConfidence -v`
Expected: FAIL — `decayedConfidence` undefined.

- [ ] **Step 3: Implement**

Add to `math.go`:

```go
// decayedConfidence applies exponential half-life decay to a confidence
// value: it returns current · 0.5^(elapsed/halfLife), clamped to [0, 1].
// Decay only ever decreases confidence. A non-positive halfLife or elapsed
// returns current unchanged (no decay). Exponential decay composes —
// decaying by t1 then t2 equals decaying by t1+t2 once — which is what lets
// the decay pass run on any cadence and converge to the same value for a
// given age.
func decayedConfidence(current float64, elapsed, halfLife time.Duration) float64 {
	if halfLife <= 0 || elapsed <= 0 {
		return current
	}
	factor := math.Pow(0.5, float64(elapsed)/float64(halfLife))
	out := current * factor
	if out < 0 {
		return 0
	}
	if out > current {
		return current // decay never raises
	}
	return out
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ -run TestDecayedConfidence -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add math.go math_test.go
git commit -m "feat: pure exponential confidence decay function"
```

---

### Task 3: DecayConfidence (scan → decay → write → auto-prune)

**Files:**
- Modify: `types.go` (DecayReport/DecayChange/options)
- Create: `decay.go`
- Test: `decay_test.go`

**Interfaces:**
- Consumes: `decayedConfidence` (Task 2), `last_decay_at` (Task 1), `InvalidateMemory` (existing), the currently-valid predicate.
- Produces:
  - Types in `types.go`:
    ```go
    type DecayChange struct {
        ID            string  `json:"id"`
        Content       string  `json:"content"`
        OldConfidence float64 `json:"old_confidence"`
        NewConfidence float64 `json:"new_confidence"`
        Pruned        bool    `json:"pruned"` // true = auto-soft-invalidated (below floor)
    }
    type DecayReport struct {
        Scanned int           `json:"scanned"`
        Decayed int           `json:"decayed"` // confidence changed
        Pruned  int           `json:"pruned"`  // soft-invalidated below floor
        DryRun  bool          `json:"dry_run"`
        Changes []DecayChange `json:"changes"`
    }
    type DecayOption func(*decayConfig)
    type decayConfig struct {
        halfLife time.Duration
        floor    float64
        dryRun   bool
    }
    func WithHalfLife(d time.Duration) DecayOption { return func(c *decayConfig){ c.halfLife = d } }
    func WithFloor(f float64) DecayOption          { return func(c *decayConfig){ c.floor = f } }
    func WithDecayDryRun() DecayOption             { return func(c *decayConfig){ c.dryRun = true } }
    ```
  - `DecayConfidence(ctx, opts...) (DecayReport, error)` in `decay.go`:
    - defaults: `halfLife = 90*24h` (90 days), `floor = 0.05`.
    - Select currently-valid memories with `id, content, confidence, created_at, last_decay_at`.
    - For each: `anchor = last_decay_at if set else created_at`; `elapsed = now - anchor`; `newConf = decayedConfidence(conf, elapsed, halfLife)`.
    - If `newConf == conf` (no measurable change, e.g. elapsed≈0): count Scanned, skip write.
    - Else (dry-run): record a `DecayChange` (with `Pruned = newConf < floor`), do NOT write.
    - Else (apply): `UPDATE memories SET confidence = ?, last_decay_at = ? WHERE id = ?` (advance the anchor to now); if `newConf < floor`, also `InvalidateMemory(ctx, id, nil)` (soft-retire, expired_at only). Record the change with `Pruned`.
    - Tally Scanned/Decayed/Pruned.

> **Anchor advance is essential:** writing `last_decay_at = now` on every applied decay is what makes composition work — the next run decays only from `now` forward. A dry-run must NOT advance the anchor (it writes nothing).

> **Pruned memories and the next run:** once pruned, a memory is no longer currently-valid (expired_at set), so subsequent decay runs skip it. Good — no double-processing.

- [ ] **Step 1: Write the failing test**

Add to `decay_test.go` (new file):

```go
package cortex

import (
	"context"
	"testing"
	"time"
)

func TestDecayConfidence_DecaysOldMemory(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	m := &Memory{Content: "aging fact", Confidence: 1.0}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	// Backdate created_at to ~one 30-day half-life ago and leave last_decay_at NULL.
	anchor := time.Now().UTC().Add(-30 * 24 * time.Hour)
	if _, err := c.db.ExecContext(ctx,
		`UPDATE memories SET created_at = ? WHERE id = ?`, anchor, m.ID); err != nil {
		t.Fatal(err)
	}

	report, err := c.DecayConfidence(ctx, WithHalfLife(30*24*time.Hour), WithFloor(0.05))
	if err != nil {
		t.Fatalf("DecayConfidence: %v", err)
	}
	if report.Decayed != 1 {
		t.Fatalf("expected 1 decayed, got %d (%+v)", report.Decayed, report)
	}

	got, err := c.getMemoryByID(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	// ~one half-life → ~0.5. Allow tolerance for the exact elapsed.
	if got.Confidence < 0.45 || got.Confidence > 0.55 {
		t.Fatalf("expected ~0.5 after one half-life, got %v", got.Confidence)
	}
}

func TestDecayConfidence_AutoPrunesBelowFloor(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	m := &Memory{Content: "ancient fact", Confidence: 1.0}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	// Backdate far past so decayed confidence falls well below the floor.
	anchor := time.Now().UTC().Add(-3650 * 24 * time.Hour) // ~10 years
	if _, err := c.db.ExecContext(ctx,
		`UPDATE memories SET created_at = ? WHERE id = ?`, anchor, m.ID); err != nil {
		t.Fatal(err)
	}

	report, err := c.DecayConfidence(ctx, WithHalfLife(30*24*time.Hour), WithFloor(0.05))
	if err != nil {
		t.Fatal(err)
	}
	if report.Pruned != 1 {
		t.Fatalf("expected 1 pruned, got %d (%+v)", report.Pruned, report)
	}

	// Pruned memory is soft-invalidated: hidden from default recall, but
	// reachable unfiltered. expired_at set, invalid_at NULL.
	got, err := c.getMemoryByID(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExpiredAt == nil {
		t.Fatal("expected expired_at set on pruned memory")
	}
	if got.InvalidAt != nil {
		t.Fatal("expected invalid_at NULL (decay-prune is system-retirement, not event-invalidation)")
	}

	// It must NOT appear in a default (currently-valid) search.
	found, err := c.SearchMemories(ctx, "ancient fact", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 {
		t.Fatalf("pruned memory should be hidden from default recall, got %d", len(found))
	}
}

func TestDecayConfidence_DryRunWritesNothing(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	m := &Memory{Content: "dry run fact", Confidence: 1.0}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	anchor := time.Now().UTC().Add(-60 * 24 * time.Hour)
	if _, err := c.db.ExecContext(ctx,
		`UPDATE memories SET created_at = ? WHERE id = ?`, anchor, m.ID); err != nil {
		t.Fatal(err)
	}

	report, err := c.DecayConfidence(ctx, WithHalfLife(30*24*time.Hour), WithDecayDryRun())
	if err != nil {
		t.Fatal(err)
	}
	if !report.DryRun || len(report.Changes) != 1 {
		t.Fatalf("expected dry-run with 1 change, got %+v", report)
	}

	// Nothing written: confidence unchanged.
	got, err := c.getMemoryByID(ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Confidence != 1.0 {
		t.Fatalf("dry-run must not write; confidence changed to %v", got.Confidence)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestDecayConfidence -v`
Expected: FAIL — `DecayConfidence` / option funcs / report types undefined.

- [ ] **Step 3: Add types (types.go) and implement DecayConfidence (decay.go)**

Add the types from the Interfaces block to `types.go`. Then create `decay.go`:

```go
package cortex

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

const (
	defaultDecayHalfLife = 90 * 24 * time.Hour // 90 days
	defaultDecayFloor    = 0.05
)

// DecayConfidence applies age-based exponential decay to the confidence of
// currently-valid memories and auto-soft-invalidates (prunes) any whose
// decayed confidence falls below the floor. Decay is anchored at each
// memory's last_decay_at (or created_at if never decayed) and the anchor is
// advanced to now on each applied pass, so the operation composes: running it
// on any cadence converges to the same confidence for a given memory age.
// Pruning is soft (expired_at only, invalid_at left NULL) — the memory is
// system-retired, not marked false; it stays reachable via as_of /
// include_invalid. Dry-run computes the report without writing anything.
func (c *Cortex) DecayConfidence(ctx context.Context, opts ...DecayOption) (DecayReport, error) {
	cfg := &decayConfig{halfLife: defaultDecayHalfLife, floor: defaultDecayFloor}
	for _, o := range opts {
		o(cfg)
	}

	now := time.Now().UTC()
	report := DecayReport{DryRun: cfg.dryRun}

	rows, err := c.db.QueryContext(ctx,
		`SELECT id, content, confidence, created_at, last_decay_at
		 FROM memories
		 WHERE `+currentlyValidClause(""),
	)
	if err != nil {
		return DecayReport{}, fmt.Errorf("cortex: decay scan: %w", err)
	}

	type candidate struct {
		id      string
		content string
		conf    float64
		anchor  time.Time
	}
	var cands []candidate
	for rows.Next() {
		var id, content string
		var conf float64
		var createdAt time.Time
		var lastDecay sql.NullTime
		if err := rows.Scan(&id, &content, &conf, &createdAt, &lastDecay); err != nil {
			rows.Close()
			return DecayReport{}, fmt.Errorf("cortex: decay scan row: %w", err)
		}
		anchor := createdAt
		if lastDecay.Valid {
			anchor = lastDecay.Time
		}
		cands = append(cands, candidate{id: id, content: content, conf: conf, anchor: anchor})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return DecayReport{}, fmt.Errorf("cortex: decay iterate: %w", err)
	}
	rows.Close()

	for _, cd := range cands {
		report.Scanned++
		elapsed := now.Sub(cd.anchor)
		newConf := decayedConfidence(cd.conf, elapsed, cfg.halfLife)
		if newConf == cd.conf {
			continue // no measurable change
		}
		pruned := newConf < cfg.floor
		report.Changes = append(report.Changes, DecayChange{
			ID: cd.id, Content: cd.content,
			OldConfidence: cd.conf, NewConfidence: newConf, Pruned: pruned,
		})
		report.Decayed++
		if pruned {
			report.Pruned++
		}

		if cfg.dryRun {
			continue
		}

		if _, err := c.db.ExecContext(ctx,
			`UPDATE memories SET confidence = ?, last_decay_at = ? WHERE id = ?`,
			newConf, now, cd.id,
		); err != nil {
			return report, fmt.Errorf("cortex: decay update %s: %w", cd.id, err)
		}
		if pruned {
			if err := c.InvalidateMemory(ctx, cd.id, nil); err != nil {
				return report, fmt.Errorf("cortex: decay prune %s: %w", cd.id, err)
			}
		}
	}

	return report, nil
}
```

> Note: `currentlyValidClause("")` returns the unqualified predicate (no table alias) — confirm that's what the bare `FROM memories` query needs (it is; `temporal.go`'s `qualify` returns the bare column for `""`).

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./ -run TestDecayConfidence -v`
Expected: PASS (all three).

- [ ] **Step 5: Full suite + commit**

Run: `go test ./...` → PASS. `go vet ./...` clean.

```bash
git add types.go decay.go decay_test.go
git commit -m "feat: confidence decay with auto-prune below floor"
```

---

### Task 4: Decay report rendering

**Files:**
- Create: `decay_render.go`
- Test: `decay_render_test.go`

**Interfaces:**
- Produces: `RenderDecayMarkdown(r DecayReport) string` — header, dry-run/applied wording, scanned/decayed/pruned counts, and a changes list (`content (id): old → new [PRUNED]`).

- [ ] **Step 1: Write the failing test**

Add to `decay_render_test.go`:

```go
package cortex

import (
	"strings"
	"testing"
)

func TestRenderDecayMarkdown(t *testing.T) {
	r := DecayReport{
		Scanned: 3, Decayed: 2, Pruned: 1, DryRun: true,
		Changes: []DecayChange{
			{ID: "a", Content: "fading", OldConfidence: 0.9, NewConfidence: 0.45, Pruned: false},
			{ID: "b", Content: "dead", OldConfidence: 0.5, NewConfidence: 0.01, Pruned: true},
		},
	}
	out := RenderDecayMarkdown(r)
	for _, want := range []string{"Decay", "fading", "dead", "PRUNED", "0.45", "Dry"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected output to contain %q, got:\n%s", want, out)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestRenderDecayMarkdown -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement** (`decay_render.go`, mirror `RenderRelationMarkdown`)

```go
package cortex

import (
	"fmt"
	"strings"
)

// RenderDecayMarkdown renders a DecayReport as human-readable markdown.
func RenderDecayMarkdown(r DecayReport) string {
	var b strings.Builder
	b.WriteString("# Decay Report\n\n")
	if r.DryRun {
		b.WriteString("_Dry run — no changes written._\n\n")
	}
	fmt.Fprintf(&b, "Scanned: %d\n", r.Scanned)
	fmt.Fprintf(&b, "Decayed: %d\n", r.Decayed)
	fmt.Fprintf(&b, "Pruned: %d\n\n", r.Pruned)

	if len(r.Changes) > 0 {
		b.WriteString("## Changes\n\n")
		for _, ch := range r.Changes {
			pruned := ""
			if ch.Pruned {
				pruned = " [PRUNED]"
			}
			fmt.Fprintf(&b, "- %q (%s): %.3f → %.3f%s\n",
				ch.Content, ch.ID, ch.OldConfidence, ch.NewConfidence, pruned)
		}
	}
	return b.String()
}
```

- [ ] **Step 4: Run test to verify it passes + full suite + commit**

Run: `go test ./ -run TestRenderDecayMarkdown -v` → PASS. `go test ./...` → PASS.

```bash
git add decay_render.go decay_render_test.go
git commit -m "feat: render decay report as markdown"
```

---

### Task 5: cortex decay CLI command

**Files:**
- Create: `cmd/cortex/decay.go`
- Modify: `cmd/cortex/main.go` (dispatch + usage)
- Test: build

**Interfaces:**
- Consumes: `cortex.DecayConfidence`, `cortex.RenderDecayMarkdown`.
- Produces: `cortex decay [--half-life <dur>] [--floor <0-1>] [--dry-run] [--out <file>]`. `--half-life` accepts a Go duration (e.g. `2160h` for 90 days) — document that and/or accept a `<N>d` shorthand (keep it simple: accept Go `time.ParseDuration` syntax; mention 90d≈2160h in help). Default behavior (no flags) applies decay with the 90-day half-life and 0.05 floor.

> **Safety ergonomics:** unlike `relate`, decay *modifies* confidence and can prune. Make `--dry-run` prominent in help. But default is apply (the user chose auto-prune; decay is reversible — pruning is soft, and confidence decay is gradual). Help must state it modifies confidence and may soft-retire memories below the floor, and point to `--dry-run`.

- [ ] **Step 1: Implement cmdDecay** (`cmd/cortex/decay.go`)

```go
package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/sausheong/cortex"
)

func cmdDecay() {
	var opts []cortex.DecayOption
	out := ""
	usage := "usage: cortex decay [--half-life <dur>] [--floor <0-1>] [--dry-run] [--out <file>]\n" +
		"  Applies age-based confidence decay to memories and soft-retires any\n" +
		"  whose confidence falls below the floor (reversible; hidden from default\n" +
		"  recall, reachable via include_invalid/as_of). Modifies the graph.\n" +
		"  --half-life  Go duration (default 2160h = 90 days)\n" +
		"  --floor      prune threshold 0-1 (default 0.05)\n" +
		"  --dry-run    report what would change without writing\n" +
		"  --out <file> write the markdown report to a file"
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--half-life":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, usage)
				os.Exit(1)
			}
			i++
			d, err := time.ParseDuration(args[i])
			if err != nil {
				fmt.Fprintf(os.Stderr, "invalid --half-life: %v\n", err)
				os.Exit(1)
			}
			opts = append(opts, cortex.WithHalfLife(d))
		case "--floor":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, usage)
				os.Exit(1)
			}
			i++
			f, err := strconv.ParseFloat(args[i], 64)
			if err != nil {
				fmt.Fprintf(os.Stderr, "invalid --floor: %v\n", err)
				os.Exit(1)
			}
			opts = append(opts, cortex.WithFloor(f))
		case "--dry-run":
			opts = append(opts, cortex.WithDecayDryRun())
		case "--out":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, usage)
				os.Exit(1)
			}
			i++
			out = args[i]
		default:
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(1)
		}
	}

	cx := openCortex()
	defer cx.Close()
	ctx := context.Background()

	report, err := cx.DecayConfidence(ctx, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "decay: %v\n", err)
		os.Exit(1)
	}

	md := cortex.RenderDecayMarkdown(report)
	if out != "" {
		if err := os.WriteFile(out, []byte(md), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write report: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Wrote decay report to %s\n", out)
		return
	}
	fmt.Print(md)
}
```

- [ ] **Step 2: Wire dispatch**

In `cmd/cortex/main.go`, add `case "decay": cmdDecay()` next to `case "reconcile":`/`case "relate":`, and add `decay` to `printUsage`.

- [ ] **Step 3: Build + full suite + commit**

Run: `go build ./...` → success. `go test ./...` → PASS.

```bash
git add cmd/cortex/decay.go cmd/cortex/main.go
git commit -m "feat: cortex decay command for confidence decay and pruning"
```

---

## Self-Review

**Spec coverage** (Phase 4c = decay + auto-prune):
- anchor column (Task 1), pure decay math (Task 2), DecayConfidence scan/decay/write/prune (Task 3), render (Task 4), CLI (Task 5). ✅

**Placeholder scan:** No TBD/TODO — complete code at every step. ✅

**Correctness invariants:**
- **Composition** (the load-bearing property) is unit-tested in Task 2 (`once == twice`) and made real by advancing `last_decay_at = now` on every applied decay (Task 3). ✅
- **Soft-prune semantics:** `InvalidateMemory(id, nil)` → `expired_at` set, `invalid_at` NULL — asserted explicitly in Task 3's auto-prune test (decay is system-retirement, not event-invalidation). ✅
- **Only currently-valid memories decay** (Task 3 query uses `currentlyValidClause`), so pruned memories aren't re-processed. ✅
- **Dry-run writes nothing** — asserted in Task 3 (confidence unchanged after dry-run). ✅
- **Decay only decreases, clamped [0,1]** — Task 2 guards + test. ✅
- **No re-coercion:** decay writes the literal decayed value via a direct `UPDATE ... SET confidence`, bypassing `coerceConfidence` (which would wrongly rescue a near-zero value to 1.0). Deliberate. ✅

**Backward compatibility:** `last_decay_at` nullable via `ensureColumn`; a DB that never runs `decay` is unchanged. Decay is opt-in (a command / the 4d maintain pass). ✅

**Known scope edges (documented):**
- `time.Now()` in `DecayConfidence` is inherent to time-based decay; the pure math is unit-tested with injected elapsed, the DB method via backdated anchors. Acceptable.
- Default half-life (90d) and floor (0.05) are starting points; tunable via flags. Real-world tuning is future work.
- Decay touches only memories, not entities/relationships (confidence exists on those too) — scoped to memories this phase, the highest-churn records. Entity/relationship decay is a possible follow-up.
- `cortex decay` applies by default (with `--dry-run` available); justified because decay is gradual and pruning is soft/reversible. The 4d `maintain` pass will likely run it dry or apply per config.

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-06-24-tier4c-confidence-decay.md`.**

Phase 4c of Tier 4. After merge: Phase 4d (`cortex maintain` — the one-shot orchestration folding reconcile + relate + decay into a single reconsolidation pass, with cron/launchd docs). Execute subagent-driven, then write Phase 4d.
