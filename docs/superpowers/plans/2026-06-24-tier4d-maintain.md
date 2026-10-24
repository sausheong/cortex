# Tier 4 Phase 4d: `cortex maintain` Orchestration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fold the three reconsolidation passes built in Phases 4a–4c — **reconcile** (resolve contradictions → supersedes edges), **relate** (build derives/extends edges), and **decay** (age confidence + auto-prune) — into a single one-shot, idempotent-in-effect `cortex maintain` command, designed to be driven by cron/launchd. This is supermemory's "Dynamic Dreaming" done in the open on cortex's own primitives: a periodic background pass that reconciles, links, and forgets. The user chose a **cron-friendly one-shot** (no daemon) — the OS scheduler owns cadence; ship sample cron/launchd snippets in the docs.

**Architecture:** Four tasks. (1) A `MaintainReport` aggregating the three sub-reports + a `Maintain(ctx, opts...)` method that runs reconcile → relate → decay in a fixed order, each independently skippable, collecting all three reports (and not aborting the whole pass if one sub-pass has nothing to do / is Skipped). (2) A combined `RenderMaintainMarkdown`. (3) The `cortex maintain` CLI with `--dry-run`, `--no-reconcile`/`--no-relate`/`--no-decay` toggles, decay tuning flags passthrough, `--out`. (4) Docs: cron/launchd sample snippets + a CORTEX.md "maintenance" section.

**Why this order (reconcile → relate → decay):**
1. **Reconcile first** — resolve contradictions and supersede stale memories *before* building relations, so derives/extends aren't drawn to memories that are about to be invalidated.
2. **Relate second** — build derives/extends edges over the now-reconciled, currently-valid set.
3. **Decay last** — age confidence and prune over the final memory set, after structural changes settle. (Pruning here soft-invalidates; since it runs last, it doesn't strand half-built edges in this pass — and edges to a later-pruned memory are harmless metadata.)

**Tech Stack:** Go 1.25.1, `modernc.org/sqlite` (pure-Go, no CGo). Tests use `openTestDB`, `mockLLM` (which now implements both `Reconciler` and `RelationDetector`).

## Global Constraints

- **Module path:** `github.com/sausheong/cortex` (root package is `cortex`).
- **No CGo, single binary.** Only existing dependencies.
- **Compose, don't reimplement:** `Maintain` calls the existing `ApplyReconcile`, `BuildMemoryEdges`, and `DecayConfidence` (or their dry-run equivalents) — it adds NO new detection/decay logic. It is pure orchestration: run each, collect each report, render the aggregate.
- **Each sub-pass independently skippable + resilient:** a `--no-X` toggle skips that pass. A Skipped sub-report (e.g. reconcile/relate when no detector LLM is configured) is NOT an error — `Maintain` records it and continues to the next pass. Decay needs no LLM, so it runs even on a detector-less setup. The only hard-error that aborts `Maintain` is a real engine error (DB failure) from a sub-pass, propagated up.
- **Dry-run threads through:** `maintain --dry-run` must make ALL three sub-passes dry-run (reconcile dry-run = `Reconcile` not `ApplyReconcile`; decay dry-run = `WithDecayDryRun`; relate has no dry-run today — see the relate note below). A dry-run `maintain` must write nothing.
- **Relate dry-run gap (resolve explicitly):** `BuildMemoryEdges` records as it detects — it has no dry-run mode (Phase 4b decision: edges are additive/idempotent). For `maintain --dry-run`, relate must NOT record. **Decision for this phase:** under `--dry-run`, `Maintain` SKIPS the relate pass entirely and marks its sub-report Skipped with reason "relate has no dry-run; skipped under --dry-run". This keeps the dry-run guarantee (writes nothing) honest without adding a relate dry-run mode now. Document it. (A real relate dry-run is a possible follow-up.)
- **Determinism / idempotent-in-effect:** because each composed pass is itself idempotent-in-effect (reconcile re-validates + edges ON CONFLICT; relate edges idempotent; decay composes via anchor), running `maintain` repeatedly on a stable graph converges. The whole pass inherits this.
- **Bi-temporal / confidence invariants:** unchanged — `Maintain` introduces none, it only sequences the existing passes.
- **Commit trailer:** do NOT add a `Co-Authored-By` trailer to commits.
- **Test command:** `go test ./...` must pass. Hermetic only. (`llm/openai` live-429 pre-existing, out of scope.)
- **TDD:** failing test first, watch fail, implement, watch pass, commit.

---

## File Structure

| File | Responsibility | Change |
|------|----------------|--------|
| `types.go` | types | `MaintainReport`, `MaintainOption` (`WithMaintainDryRun`, `WithoutReconcile`/`WithoutRelate`/`WithoutDecay`, decay tuning passthrough), `maintainConfig` |
| `maintain.go` | new file | `Maintain(ctx, opts...)` — sequence reconcile → relate → decay |
| `maintain_render.go` | new file | `RenderMaintainMarkdown` (composes the three existing renders) |
| `maintain.go`/`*_test.go` | tests | orchestration tests with mockLLM |
| `cmd/cortex/maintain.go` | new file | `cmdMaintain` |
| `cmd/cortex/main.go` | dispatch | `case "maintain"` |
| `docs/CRON.md` (or README section) | docs | cron/launchd snippets |
| `cmd/cortex/CORTEX.md.template` | agent contract | maintenance section |

---

### Task 1: MaintainReport + Maintain orchestration

**Files:**
- Modify: `types.go`
- Create: `maintain.go`
- Test: `maintain_test.go`

**Interfaces:**
- Consumes: `Reconcile`/`ApplyReconcile`, `BuildMemoryEdges`, `DecayConfidence` + their option types.
- Produces:
  - Types in `types.go`:
    ```go
    type MaintainReport struct {
        DryRun    bool             `json:"dry_run"`
        Reconcile *ReconcileReport `json:"reconcile,omitempty"`
        Relate    *RelationReport  `json:"relate,omitempty"`
        Decay     *DecayReport     `json:"decay,omitempty"`
    }
    type MaintainOption func(*maintainConfig)
    type maintainConfig struct {
        dryRun       bool
        skipReconcile, skipRelate, skipDecay bool
        decayOpts    []DecayOption
    }
    func WithMaintainDryRun() MaintainOption       { return func(c *maintainConfig){ c.dryRun = true } }
    func WithoutReconcile() MaintainOption         { return func(c *maintainConfig){ c.skipReconcile = true } }
    func WithoutRelate() MaintainOption            { return func(c *maintainConfig){ c.skipRelate = true } }
    func WithoutDecay() MaintainOption             { return func(c *maintainConfig){ c.skipDecay = true } }
    func WithMaintainDecayOptions(o ...DecayOption) MaintainOption { return func(c *maintainConfig){ c.decayOpts = o } }
    ```
  - `Maintain(ctx, opts...) (MaintainReport, error)` in `maintain.go`:
    - reconcile (unless skipped): dry-run → `Reconcile`; else → `ApplyReconcile`. Store pointer in report.
    - relate (unless skipped): dry-run → SKIP, set `report.Relate = &RelationReport{Skipped: true, SkipReason: "relate has no dry-run; skipped under --dry-run"}`; else → `BuildMemoryEdges`. Store pointer.
    - decay (unless skipped): build decay opts = `cfg.decayOpts` + (`WithDecayDryRun()` if dry-run). Call `DecayConfidence`. Store pointer.
    - Any sub-pass returning a non-nil error aborts and propagates. A Skipped sub-report is not an error.

- [ ] **Step 1: Write the failing test**

Add to `maintain_test.go` (new file):

```go
package cortex

import (
	"context"
	"testing"
	"time"
)

func TestMaintain_RunsAllThreePasses(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	// Two contradicting memories on an entity (reconcile fodder) + an aging one.
	e := &Entity{Name: "Proj", Type: "project"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	older := &Memory{Content: "Proj budget is 5000", EntityIDs: []string{e.ID}}
	newer := &Memory{Content: "Proj budget is 9000", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, older); err != nil {
		t.Fatal(err)
	}
	if err := c.PutMemory(ctx, newer); err != nil {
		t.Fatal(err)
	}
	// Backdate older so newer is strictly newer (reconcile gate) and so decay has age.
	if _, err := c.db.ExecContext(ctx,
		`UPDATE memories SET created_at = ? WHERE id = ?`,
		newer.CreatedAt.Add(-48*time.Hour), older.ID); err != nil {
		t.Fatal(err)
	}

	c.SetLLM(&mockLLM{
		detectConflictsFn: func(_ context.Context, mems []Memory) ([]ConflictPair, error) {
			return []ConflictPair{{StaleID: older.ID, SupersededByID: newer.ID, Reason: "budget changed"}}, nil
		},
		detectRelationsFn: func(_ context.Context, mems []Memory) ([]MemoryRelation, error) {
			return nil, nil // no relations for this test; just prove the pass ran
		},
	})

	report, err := c.Maintain(ctx, WithMaintainDecayOptions(WithHalfLife(30*24*time.Hour), WithFloor(0.05)))
	if err != nil {
		t.Fatalf("Maintain: %v", err)
	}
	if report.Reconcile == nil || report.Relate == nil || report.Decay == nil {
		t.Fatalf("expected all three sub-reports present, got %+v", report)
	}
	if report.DryRun {
		t.Fatal("expected DryRun false")
	}
	// Reconcile should have applied the supersession.
	if len(report.Reconcile.Proposed) != 1 {
		t.Fatalf("expected 1 reconciled supersession, got %d", len(report.Reconcile.Proposed))
	}
	// The stale memory is now invalidated → a supersedes edge exists.
	edges, err := c.GetMemoryEdgesByType(ctx, newer.ID, EdgeSupersedes)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected supersedes edge from reconcile pass, got %d", len(edges))
	}
}

func TestMaintain_DryRunWritesNothing(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Name: "P", Type: "project"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	older := &Memory{Content: "P is red", EntityIDs: []string{e.ID}}
	newer := &Memory{Content: "P is blue", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, older); err != nil {
		t.Fatal(err)
	}
	if err := c.PutMemory(ctx, newer); err != nil {
		t.Fatal(err)
	}
	if _, err := c.db.ExecContext(ctx,
		`UPDATE memories SET created_at = ? WHERE id = ?`,
		newer.CreatedAt.Add(-48*time.Hour), older.ID); err != nil {
		t.Fatal(err)
	}

	c.SetLLM(&mockLLM{
		detectConflictsFn: func(_ context.Context, mems []Memory) ([]ConflictPair, error) {
			return []ConflictPair{{StaleID: older.ID, SupersededByID: newer.ID, Reason: "color changed"}}, nil
		},
	})

	report, err := c.Maintain(ctx, WithMaintainDryRun())
	if err != nil {
		t.Fatalf("Maintain dry-run: %v", err)
	}
	if !report.DryRun {
		t.Fatal("expected DryRun true")
	}
	// Relate is skipped under dry-run.
	if report.Relate == nil || !report.Relate.Skipped {
		t.Fatalf("expected relate Skipped under dry-run, got %+v", report.Relate)
	}
	// Reconcile dry-run proposed but DID NOT apply → no supersedes edge, older still valid.
	edges, err := c.GetMemoryEdgesByType(ctx, newer.ID, EdgeSupersedes)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 0 {
		t.Fatalf("dry-run must not record edges, got %d", len(edges))
	}
	got, err := c.getMemoryByID(ctx, older.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ExpiredAt != nil || got.InvalidAt != nil {
		t.Fatal("dry-run must not invalidate the stale memory")
	}
}

func TestMaintain_SkipToggles(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	report, err := c.Maintain(ctx, WithoutReconcile(), WithoutRelate(), WithoutDecay())
	if err != nil {
		t.Fatalf("Maintain: %v", err)
	}
	if report.Reconcile != nil || report.Relate != nil || report.Decay != nil {
		t.Fatalf("expected all sub-reports nil when all skipped, got %+v", report)
	}
}
```

> Confirm `mockLLM` implements both `Reconciler` and `RelationDetector` (it does after Phase 4b). `"time"` import needed in `maintain_test.go`.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestMaintain -v`
Expected: FAIL — `Maintain` / options / `MaintainReport` undefined.

- [ ] **Step 3: Add types + implement Maintain**

Add the types to `types.go`. Create `maintain.go`:

```go
package cortex

import "context"

// Maintain runs the reconsolidation passes — reconcile, relate, decay — in a
// fixed order as one idempotent-in-effect background pass (cortex's "dreaming").
// It is pure orchestration: it composes ApplyReconcile/Reconcile,
// BuildMemoryEdges, and DecayConfidence, collecting each sub-report. Each pass
// is independently skippable via options. A Skipped sub-report (e.g. reconcile
// or relate without a detector-capable LLM) is recorded, not an error; only a
// real engine error aborts the pass. Under dry-run, reconcile uses its dry-run
// path, decay uses WithDecayDryRun, and relate is skipped (it has no dry-run
// mode — edges are additive), so a dry-run Maintain writes nothing.
//
// Order rationale: reconcile first (retire contradictions before linking),
// relate second (link the reconciled set), decay last (age/prune the settled set).
func (c *Cortex) Maintain(ctx context.Context, opts ...MaintainOption) (MaintainReport, error) {
	cfg := &maintainConfig{}
	for _, o := range opts {
		o(cfg)
	}

	report := MaintainReport{DryRun: cfg.dryRun}

	// 1. Reconcile.
	if !cfg.skipReconcile {
		var rr ReconcileReport
		var err error
		if cfg.dryRun {
			rr, err = c.Reconcile(ctx)
		} else {
			rr, err = c.ApplyReconcile(ctx)
		}
		if err != nil {
			return report, err
		}
		report.Reconcile = &rr
	}

	// 2. Relate. No dry-run mode → skipped under dry-run to keep the
	// write-nothing guarantee.
	if !cfg.skipRelate {
		if cfg.dryRun {
			report.Relate = &RelationReport{
				Skipped:    true,
				SkipReason: "relate has no dry-run; skipped under --dry-run",
			}
		} else {
			rel, err := c.BuildMemoryEdges(ctx)
			if err != nil {
				return report, err
			}
			report.Relate = &rel
		}
	}

	// 3. Decay (+ auto-prune).
	if !cfg.skipDecay {
		decayOpts := cfg.decayOpts
		if cfg.dryRun {
			decayOpts = append(decayOpts, WithDecayDryRun())
		}
		dr, err := c.DecayConfidence(ctx, decayOpts...)
		if err != nil {
			return report, err
		}
		report.Decay = &dr
	}

	return report, nil
}
```

> Note on dry-run decay opts: `append(cfg.decayOpts, WithDecayDryRun())` — if `cfg.decayOpts` is nil this is fine; if a caller passes decay opts AND dry-run, both apply. Guard against mutating a shared backing array by copying if needed — but since `decayOpts` is rebuilt per call from the option closures, a fresh append is safe.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./ -run TestMaintain -v`
Expected: PASS (all three).

- [ ] **Step 5: Full suite + commit**

Run: `go test ./...` → PASS. `go vet ./...` clean.

```bash
git add types.go maintain.go maintain_test.go
git commit -m "feat: Maintain orchestrates reconcile, relate, and decay"
```

---

### Task 2: Combined maintain report rendering

**Files:**
- Create: `maintain_render.go`
- Test: `maintain_render_test.go`

**Interfaces:**
- Consumes: `MaintainReport`, the existing `RenderReconcileMarkdownMode`, `RenderRelationMarkdown`, `RenderDecayMarkdown`.
- Produces: `RenderMaintainMarkdown(r MaintainReport) string` — a top header (`# Maintain Report`, dry-run note) followed by each present sub-report's render under a section. Nil sub-reports (skipped via toggle) are omitted with a "skipped" note.

- [ ] **Step 1: Write the failing test**

Add to `maintain_render_test.go`:

```go
package cortex

import (
	"strings"
	"testing"
)

func TestRenderMaintainMarkdown(t *testing.T) {
	r := MaintainReport{
		DryRun:    true,
		Reconcile: &ReconcileReport{EntitiesScanned: 1, MemoriesScanned: 2},
		Relate:    &RelationReport{Skipped: true, SkipReason: "relate has no dry-run; skipped under --dry-run"},
		Decay:     &DecayReport{Scanned: 3, Decayed: 1, DryRun: true},
	}
	out := RenderMaintainMarkdown(r)
	for _, want := range []string{"Maintain", "Reconcile", "Relation", "Decay", "dry-run", "skipped under"} {
		if !strings.Contains(strings.ToLower(out), strings.ToLower(want)) {
			t.Fatalf("expected output to contain %q, got:\n%s", want, out)
		}
	}

	// Nil sub-report (toggled off) → a skipped note, no panic.
	r2 := MaintainReport{Decay: &DecayReport{Scanned: 0}}
	out2 := RenderMaintainMarkdown(r2)
	if !strings.Contains(out2, "Decay") {
		t.Fatalf("expected decay section, got:\n%s", out2)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestRenderMaintainMarkdown -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement** (`maintain_render.go`)

```go
package cortex

import (
	"fmt"
	"strings"
)

// RenderMaintainMarkdown renders a MaintainReport by composing the per-pass
// renders under a single header. Sub-passes toggled off (nil) are noted as
// skipped.
func RenderMaintainMarkdown(r MaintainReport) string {
	var b strings.Builder
	b.WriteString("# Maintain Report\n\n")
	if r.DryRun {
		b.WriteString("_Dry run — no changes written._\n\n")
	}

	b.WriteString("## Reconcile\n\n")
	if r.Reconcile != nil {
		// applied=true when not a dry-run (ApplyReconcile path).
		b.WriteString(RenderReconcileMarkdownMode(*r.Reconcile, !r.DryRun))
	} else {
		b.WriteString("_skipped_\n")
	}
	b.WriteString("\n")

	b.WriteString("## Relate\n\n")
	if r.Relate != nil {
		b.WriteString(RenderRelationMarkdown(*r.Relate))
	} else {
		b.WriteString("_skipped_\n")
	}
	b.WriteString("\n")

	b.WriteString("## Decay\n\n")
	if r.Decay != nil {
		b.WriteString(RenderDecayMarkdown(*r.Decay))
	} else {
		b.WriteString("_skipped_\n")
	}

	return b.String()
}

var _ = fmt.Sprintf // keep fmt imported if unused after edits; remove if not needed
```

> Remove the `var _ = fmt.Sprintf` line if `fmt` ends up used or unused cleanly — it's a placeholder guard; the implementer should drop it and only import `fmt` if actually used (likely it is NOT needed here since all formatting goes through the sub-renders and `strings.Builder`). Use only `strings`. Clean up the imports so `go vet` is happy.

- [ ] **Step 4: Run test to verify it passes + full suite + commit**

Run: `go test ./ -run TestRenderMaintainMarkdown -v` → PASS. `go test ./...` → PASS. `go vet ./...` clean (no unused imports).

```bash
git add maintain_render.go maintain_render_test.go
git commit -m "feat: render combined maintain report"
```

---

### Task 3: `cortex maintain` CLI command

**Files:**
- Create: `cmd/cortex/maintain.go`
- Modify: `cmd/cortex/main.go` (dispatch + usage)
- Test: build

**Interfaces:**
- Consumes: `cortex.Maintain`, `cortex.RenderMaintainMarkdown`, decay option constructors.
- Produces: `cortex maintain [--dry-run] [--no-reconcile] [--no-relate] [--no-decay] [--half-life <dur>] [--floor <0-1>] [--out <file>]`. Maps flags to `MaintainOption`s. `--half-life`/`--floor` build `WithMaintainDecayOptions(...)`. Prints or writes the combined report.

- [ ] **Step 1: Implement cmdMaintain** (`cmd/cortex/maintain.go`)

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

func cmdMaintain() {
	var opts []cortex.MaintainOption
	var decayOpts []cortex.DecayOption
	out := ""
	usage := "usage: cortex maintain [--dry-run] [--no-reconcile] [--no-relate] [--no-decay] [--half-life <dur>] [--floor <0-1>] [--out <file>]\n" +
		"  Runs the reconsolidation pass: reconcile contradictions, build\n" +
		"  derives/extends edges, then decay confidence and soft-retire stale\n" +
		"  memories. Designed to be run periodically (cron/launchd). Modifies the\n" +
		"  graph unless --dry-run. Under --dry-run, relate is skipped (no dry-run).\n" +
		"  --dry-run       preview; write nothing\n" +
		"  --no-reconcile  skip the reconcile pass\n" +
		"  --no-relate     skip the relate pass\n" +
		"  --no-decay      skip the decay pass\n" +
		"  --half-life     decay half-life Go duration (default 2160h = 90 days)\n" +
		"  --floor         decay prune threshold 0-1 (default 0.05)\n" +
		"  --out <file>    write the markdown report to a file"
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			opts = append(opts, cortex.WithMaintainDryRun())
		case "--no-reconcile":
			opts = append(opts, cortex.WithoutReconcile())
		case "--no-relate":
			opts = append(opts, cortex.WithoutRelate())
		case "--no-decay":
			opts = append(opts, cortex.WithoutDecay())
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
			decayOpts = append(decayOpts, cortex.WithHalfLife(d))
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
			decayOpts = append(decayOpts, cortex.WithFloor(f))
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
	if len(decayOpts) > 0 {
		opts = append(opts, cortex.WithMaintainDecayOptions(decayOpts...))
	}

	cx := openCortex()
	defer cx.Close()
	ctx := context.Background()

	report, err := cx.Maintain(ctx, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "maintain: %v\n", err)
		os.Exit(1)
	}

	md := cortex.RenderMaintainMarkdown(report)
	if out != "" {
		if err := os.WriteFile(out, []byte(md), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write report: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Wrote maintain report to %s\n", out)
		return
	}
	fmt.Print(md)
}
```

- [ ] **Step 2: Wire dispatch**

In `cmd/cortex/main.go`, add `case "maintain": cmdMaintain()` next to the other maintenance commands, and add `maintain` to `printUsage` (with a one-line description noting it's the periodic reconsolidation pass).

- [ ] **Step 3: Build + full suite + commit**

Run: `go build ./...` → success. `go test ./...` → PASS.

```bash
git add cmd/cortex/maintain.go cmd/cortex/main.go
git commit -m "feat: cortex maintain command for periodic reconsolidation"
```

---

### Task 4: Scheduling docs + CORTEX.md maintenance section

**Files:**
- Create: `docs/CRON.md` (or a README section — pick the repo's convention; `docs/` is gitignored per the repo, so prefer a README section OR a committed `SCHEDULING.md` at repo root — verify and choose)
- Modify: `cmd/cortex/CORTEX.md.template`
- Test: none (docs); build sanity only

**Interfaces:**
- Produces: sample cron + launchd snippets for running `cortex maintain` periodically; a CORTEX.md "Maintenance" section telling agents the graph self-reconsolidates and how/when to run or recommend `maintain`.

> **Doc location decision:** the repo `.gitignore` excludes `/docs`. So a `docs/CRON.md` would NOT be committed. Put the scheduling guidance in a **committed** location: either the repo `README.md` (a "Scheduling maintenance" section) or a root-level `SCHEDULING.md`. Verify the README exists and append a section; if no README, create `SCHEDULING.md`. State which you did in the report.

- [ ] **Step 1: Add scheduling docs**

Write a "Scheduling maintenance" section (to README.md or SCHEDULING.md) with:

```markdown
## Scheduling maintenance

`cortex maintain` runs the reconsolidation pass — reconcile contradictions,
build derives/extends edges, then decay confidence and soft-retire stale
memories. It is a one-shot, idempotent-in-effect command: run it periodically
and the OS scheduler owns the cadence.

Preview first:

    cortex maintain --dry-run

### cron (Linux)

Run nightly at 03:17 (off the hour to avoid the thundering herd):

    17 3 * * *  CORTEX_DB=/path/to/brain.db /usr/local/bin/cortex maintain --out /path/to/last-maintain.md

### launchd (macOS)

Create `~/Library/LaunchAgents/ai.cortex.maintain.plist`:

    <?xml version="1.0" encoding="UTF-8"?>
    <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
    <plist version="1.0">
    <dict>
      <key>Label</key><string>ai.cortex.maintain</string>
      <key>ProgramArguments</key>
      <array>
        <string>/usr/local/bin/cortex</string>
        <string>maintain</string>
      </array>
      <key>EnvironmentVariables</key>
      <dict><key>CORTEX_DB</key><string>/path/to/brain.db</string></dict>
      <key>StartCalendarInterval</key>
      <dict><key>Hour</key><integer>3</integer><key>Minute</key><integer>17</integer></dict>
    </dict>
    </plist>

Then: `launchctl load ~/Library/LaunchAgents/ai.cortex.maintain.plist`

Reconcile and relate need an LLM provider configured (see `cortex config`);
decay runs without one. A maintain run with no provider still decays and
prunes, and reports the reconcile/relate passes as skipped.
```

(Adjust paths/wording to the repo's style.)

- [ ] **Step 2: Add a CORTEX.md maintenance section**

In `cmd/cortex/CORTEX.md.template`, add a "Maintenance / reconsolidation" subsection near the lint/reconcile guidance, e.g.:

```markdown
### Reconsolidation (maintain)

`cortex maintain` is the periodic upkeep pass: it reconciles contradictions
(superseding stale memories), builds derives/extends links between related
memories, and decays the confidence of old, unreinforced memories — soft-
retiring those that fall below a floor. Nothing is hard-deleted; retired
memories remain reachable via `as_of` / `include_invalid`.

The user typically schedules this (cron/launchd). You can run `cortex maintain
--dry-run` to preview what a pass would change, or suggest the user enable
scheduled maintenance if the graph is accumulating contradictions or stale
facts. Recommend it when you notice the graph drifting; don't run the
mutating pass unprompted.
```

- [ ] **Step 3: Build sanity + commit**

Run: `go build ./...` → success (template is go:embed'd; confirm it still compiles).

```bash
git add README.md cmd/cortex/CORTEX.md.template   # or SCHEDULING.md instead of README.md
git commit -m "docs: scheduling maintenance with cron/launchd; CORTEX.md maintenance section"
```

---

## Self-Review

**Spec coverage** (Phase 4d = the maintain orchestration, completing Tier 4):
- `Maintain` sequencing reconcile→relate→decay (Task 1), combined render (Task 2), CLI (Task 3), scheduling docs + agent contract (Task 4). ✅

**Placeholder scan:** Task 2 Step 3 has a labeled `var _ = fmt.Sprintf` guard with an instruction to remove it and clean imports — flagged for the implementer to resolve (use only `strings`). No other placeholders. ✅

**Composition correctness:**
- `Maintain` reimplements nothing — it calls `Reconcile`/`ApplyReconcile`, `BuildMemoryEdges`, `DecayConfidence`. Pure orchestration. ✅
- Order reconcile→relate→decay is justified (retire before link, link before age). ✅
- Dry-run threads through: reconcile dry-run path, relate skipped (no dry-run mode — documented honestly), decay `WithDecayDryRun`. A dry-run `maintain` writes nothing — asserted in Task 1's dry-run test (no edge, stale not invalidated). ✅
- Skipped sub-reports are not errors; only engine errors abort — Task 1's skip-toggle + the mockLLM-less decay path cover this. ✅

**Backward compatibility:** purely additive — new method, new command, new docs. No schema change, no behavior change to existing commands. ✅

**Known scope edges (documented):**
- Relate has no dry-run, so `maintain --dry-run` skips it (honest write-nothing). A real relate dry-run is a follow-up.
- One-shot only (no `--watch` daemon) — the user chose cron-friendly; the OS scheduler owns cadence.
- `maintain` applies by default (mutating); `--dry-run` previews. Help says so.
- `docs/` is gitignored, so scheduling docs go to a committed location (README/SCHEDULING.md) — the implementer verifies and reports which.

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-06-24-tier4d-maintain.md`.**

Phase 4d of Tier 4 — the final phase. After it merges, **Tier 4 is complete**: memory edges (4a), derives/extends (4b), decay+prune (4c), and the `maintain` reconsolidation pass (4d) that runs them together on a schedule — supermemory's "Dynamic Dreaming" rebuilt in the open on cortex's primitives. Execute subagent-driven.
