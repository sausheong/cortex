# Tier 2c: Reviewed-Report Apply for Reconciliation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let a reviewed reconcile dry-run be applied exactly as reviewed — close the TOCTOU gap where `cortex reconcile --apply` re-runs LLM detection and may act on supersessions the user never saw. Achieved by serializing the dry-run report to JSON and adding a file-based apply path that re-validates each proposal against current state (skipping any now-stale) instead of re-detecting.

**Architecture:** Builds on Tier 2b. Report types gain stable JSON tags. A new `ApplyReconcileReport(ctx, report) (ReconcileReport, error)` applies a *given* report: for each proposed supersession it re-checks the same deterministic gate (stale still currently-valid; superseding still currently-valid and strictly newer) before calling Tier 2a's `InvalidateMemory`; proposals that no longer hold are skipped and recorded. The CLI gains `--json <file>` (dry-run emits machine-readable report) and `--from <file>` (apply a saved report). The existing one-shot `--apply` (detect-and-apply) stays. No change to detection, the gate logic, or `Reconcile` itself.

**Tech Stack:** Go 1.25.1, `modernc.org/sqlite` (pure-Go, no CGo), `encoding/json` (stdlib). Reuses Tier 2b's `Reconcile`/`ReconcileReport`/`Supersession`, Tier 2a's `InvalidateMemory` + currently-valid semantics, and the existing CLI patterns in `cmd/cortex/reconcile.go`.

## Global Constraints

- **Module:** `github.com/sausheong/cortex`, root package `cortex`. Go 1.25.1.
- **No CGo, single binary.** Only `modernc.org/sqlite` + stdlib. No new external dependencies.
- **Do not change detection, the deterministic gate's rule, or `Reconcile`/`ApplyReconcile`'s existing behavior.** This phase is purely additive: new serialization, a new report-driven apply method, new CLI flags. The one-shot `cortex reconcile --apply` (detect-and-apply) must keep working unchanged.
- **Re-validate, don't trust blindly.** `ApplyReconcileReport` MUST re-check each proposal against current DB state before invalidating (stale memory still currently-valid; superseding memory still exists, currently-valid, strictly newer than stale). Skip + record proposals that no longer hold. This keeps determinism without re-detecting.
- **Soft-invalidate only** via `InvalidateMemory`. `Forget` stays the only hard delete. No ingest-path change.
- **Confidence invariant** `[0,1]` unchanged. **Timestamps UTC.** JSON times are RFC3339 (Go's default `time.Time` JSON encoding).
- **No `Co-Authored-By` trailer on commits.**
- **Tests:** `go test ./...` must pass (hermetic — uses `openTestDB`, `mockLLM`). The `llm/openai` live-API tests fail on HTTP 429 quota — pre-existing, out of scope; ignore.
- **TDD:** failing test → confirm fail → minimal implementation → confirm pass → commit.

---

## Core Model

**The TOCTOU problem (Tier 2b):** `cortex reconcile` (dry-run) and `cortex reconcile --apply` are separate processes. `--apply` calls `ApplyReconcile`, which re-runs `Reconcile` — a second LLM detection pass. With non-deterministic LLM output, the applied set can differ from what the dry-run showed.

**The fix:** the dry-run can serialize its `ReconcileReport` to JSON (`--json <file>`). A later `cortex reconcile --apply --from <file>` deserializes it and calls the new `ApplyReconcileReport`, which applies the **reviewed** proposals — no detection. Because time passed, each proposal is re-validated against current state (the "re-validate, skip stale" guard) so a graph change between review and apply can't cause a wrong invalidation.

**Re-validation gate** (per proposed `Supersession`, in `ApplyReconcileReport`):
- Load the stale memory by `StaleID`. It must exist and be **currently valid** (`expired_at IS NULL AND invalid_at IS NULL`). If already invalidated/missing → skip (record reason "stale memory no longer current").
- Load the superseding memory by `SupersededByID`. It must exist and be **currently valid**. If not → skip ("superseding memory no longer current").
- The superseding memory's `created_at` must still be **strictly after** the stale memory's `created_at`. (Defends against a tampered/old report.) If not → skip ("superseding no longer strictly newer").
- All pass → `InvalidateMemory(ctx, StaleID, &InvalidAt)`.

Skipped proposals are recorded so the applied-report is honest about what it did vs. didn't.

**Applied report:** `ApplyReconcileReport` returns a `ReconcileReport` describing the outcome — `Proposed` = the ones actually applied; `Rejected` = the ones skipped by re-validation (with reasons). This mirrors how the dry-run report reads.

---

## File Structure

| File | Responsibility | Change |
|------|----------------|--------|
| `types.go` | report types | Add JSON tags to `Supersession`, `RejectedPair`, `ReconcileReport` |
| `reconcile.go` | apply engine | Add `ApplyReconcileReport(ctx, ReconcileReport) (ReconcileReport, error)` with the re-validation gate; add a shared `memoryByID` helper if needed |
| `cmd/cortex/reconcile.go` | CLI | Add `--json <file>` (dry-run emits JSON) and `--from <file>` (apply a saved report); keep `--apply` one-shot |
| `cmd/cortex/CORTEX.md.template` + `README.md` | docs | Document the review→apply workflow |
| `*_test.go` | tests | per task |

---

### Task 1: JSON tags on report types + round-trip

**Files:**
- Modify: `types.go` (`Supersession`, `RejectedPair`, `ReconcileReport`)
- Test: `reconcile_test.go`

**Interfaces:**
- Produces: the three report structs with explicit snake_case JSON tags so a serialized report is stable and human-reviewable. Field names/types unchanged — only tags added.

- [ ] **Step 1: Write the failing test**

Add to `reconcile_test.go`:

```go
func TestReconcileReport_JSONRoundTrip(t *testing.T) {
	orig := ReconcileReport{
		EntitiesScanned: 2,
		MemoriesScanned: 5,
		Proposed: []Supersession{{
			StaleID: "s1", StaleContent: "budget 5000",
			SupersededByID: "n1", SupersededByContent: "budget 10000",
			Reason: "changed", InvalidAt: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		}},
		Rejected: []RejectedPair{{StaleID: "s2", SupersededByID: "n2", Reason: "not newer"}},
	}

	data, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Snake_case tags must be present in the wire form.
	for _, want := range []string{`"stale_id"`, `"superseded_by_id"`, `"invalid_at"`, `"proposed"`, `"entities_scanned"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("expected JSON to contain %s, got: %s", want, data)
		}
	}

	var back ReconcileReport
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(back.Proposed) != 1 || back.Proposed[0].StaleID != "s1" ||
		!back.Proposed[0].InvalidAt.Equal(orig.Proposed[0].InvalidAt) {
		t.Fatalf("round-trip mismatch: %+v", back)
	}
	if back.EntitiesScanned != 2 || len(back.Rejected) != 1 {
		t.Fatalf("round-trip lost fields: %+v", back)
	}
}
```

Ensure `encoding/json`, `strings`, `time` are imported in `reconcile_test.go` (add any missing).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestReconcileReport_JSONRoundTrip -v`
Expected: FAIL — without tags, JSON keys are PascalCase (`"StaleID"`), so the `"stale_id"` assertions fail.

- [ ] **Step 3: Add JSON tags**

In `types.go`, replace the three struct definitions with tagged versions (fields/types identical, tags added):

```go
type Supersession struct {
	StaleID             string    `json:"stale_id"`
	StaleContent        string    `json:"stale_content"`
	SupersededByID      string    `json:"superseded_by_id"`
	SupersededByContent string    `json:"superseded_by_content"`
	Reason              string    `json:"reason"`
	InvalidAt           time.Time `json:"invalid_at"`
}

type RejectedPair struct {
	StaleID        string `json:"stale_id"`
	SupersededByID string `json:"superseded_by_id"`
	Reason         string `json:"reason"`
}

type ReconcileReport struct {
	EntitiesScanned int            `json:"entities_scanned"`
	MemoriesScanned int            `json:"memories_scanned"`
	Proposed        []Supersession `json:"proposed"`
	Rejected        []RejectedPair `json:"rejected"`
	Skipped         bool           `json:"skipped"`
	SkipReason      string         `json:"skip_reason,omitempty"`
}
```

(Keep the existing doc comments on each type/field.)

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ -run TestReconcileReport_JSONRoundTrip -v`
Expected: PASS.

- [ ] **Step 5: Run the full suite**

Run: `go test ./...`
Expected: PASS — adding tags doesn't change Go field access; `RenderReconcileMarkdown` and existing reconcile tests are unaffected. Ignore `llm/openai` 429.

- [ ] **Step 6: Commit**

```bash
git add types.go reconcile_test.go
git commit -m "feat: add JSON tags to reconcile report types"
```

---

### Task 2: ApplyReconcileReport with re-validation gate

**Files:**
- Modify: `reconcile.go`
- Test: `reconcile_test.go`

**Interfaces:**
- Consumes: `ReconcileReport`/`Supersession` (Task 1), `InvalidateMemory` (Tier 2a), and a way to load a single memory by id with its temporal fields.
- Produces: `ApplyReconcileReport(ctx context.Context, report ReconcileReport) (ReconcileReport, error)`. Applies the report's `Proposed` supersessions after re-validating each against current DB state. Returns an outcome report: `Proposed` = actually applied; `Rejected` = skipped-by-revalidation (with reasons); `EntitiesScanned`/`MemoriesScanned` copied from the input (informational). A `Skipped` input report is returned unchanged (nothing to apply). Does NOT call `Reconcile` and does NOT call the LLM.

- [ ] **Step 1: Write the failing test**

Add to `reconcile_test.go`:

```go
func TestApplyReconcileReport_AppliesReviewed(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "Dana"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	older := &Memory{Content: "Dana's role is IC", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, older); err != nil {
		t.Fatal(err)
	}
	newer := &Memory{Content: "Dana's role is Manager", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, newer); err != nil {
		t.Fatal(err)
	}
	older.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c.db.ExecContext(ctx, `UPDATE memories SET created_at = ? WHERE id = ?`, older.CreatedAt, older.ID)
	newer.CreatedAt = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	c.db.ExecContext(ctx, `UPDATE memories SET created_at = ? WHERE id = ?`, newer.CreatedAt, newer.ID)

	// A report as if produced by an earlier dry-run.
	report := ReconcileReport{
		Proposed: []Supersession{{
			StaleID: older.ID, SupersededByID: newer.ID,
			Reason: "promoted", InvalidAt: newer.CreatedAt,
		}},
	}

	applied, err := c.ApplyReconcileReport(ctx, report)
	if err != nil {
		t.Fatalf("ApplyReconcileReport: %v", err)
	}
	if len(applied.Proposed) != 1 {
		t.Fatalf("expected 1 applied, got %d (rejected=%d)", len(applied.Proposed), len(applied.Rejected))
	}
	// older now hidden from default recall; newer remains.
	got, err := c.SearchMemories(ctx, "Dana role", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != newer.ID {
		t.Fatalf("expected only newer valid, got %d", len(got))
	}
}

func TestApplyReconcileReport_SkipsStaleProposal(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "Eli"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	older := &Memory{Content: "Eli's city is Rome", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, older); err != nil {
		t.Fatal(err)
	}
	newer := &Memory{Content: "Eli's city is Oslo", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, newer); err != nil {
		t.Fatal(err)
	}
	older.CreatedAt = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	c.db.ExecContext(ctx, `UPDATE memories SET created_at = ? WHERE id = ?`, older.CreatedAt, older.ID)
	newer.CreatedAt = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	c.db.ExecContext(ctx, `UPDATE memories SET created_at = ? WHERE id = ?`, newer.CreatedAt, newer.ID)

	// The graph changed since the dry-run: the stale memory was ALREADY invalidated.
	cut := newer.CreatedAt
	if err := c.InvalidateMemory(ctx, older.ID, &cut); err != nil {
		t.Fatal(err)
	}

	report := ReconcileReport{
		Proposed: []Supersession{{
			StaleID: older.ID, SupersededByID: newer.ID,
			Reason: "stale", InvalidAt: newer.CreatedAt,
		}},
	}

	applied, err := c.ApplyReconcileReport(ctx, report)
	if err != nil {
		t.Fatal(err)
	}
	if len(applied.Proposed) != 0 {
		t.Fatalf("expected 0 applied (stale already invalid), got %d", len(applied.Proposed))
	}
	if len(applied.Rejected) != 1 {
		t.Fatalf("expected 1 rejected by re-validation, got %d", len(applied.Rejected))
	}
}

func TestApplyReconcileReport_SkippedInputUnchanged(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	in := ReconcileReport{Skipped: true, SkipReason: "no reconciler"}
	out, err := c.ApplyReconcileReport(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Skipped || len(out.Proposed) != 0 {
		t.Fatalf("skipped input should pass through unchanged, got %+v", out)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./ -run TestApplyReconcileReport -v`
Expected: FAIL — `c.ApplyReconcileReport undefined`.

- [ ] **Step 3: Add a single-memory loader**

`ApplyReconcileReport` needs to load one memory (with temporal fields) by id, INCLUDING invalidated ones (so it can detect "already invalid"). The existing read paths filter to currently-valid, so add an unfiltered loader. In `memory.go`:

```go
// getMemoryByID loads a single memory by id regardless of validity state
// (no currently-valid filter), including its temporal fields. Returns
// (nil, nil) if no such memory exists. Used by reconciliation's apply-time
// re-validation, which must distinguish "missing" from "already invalidated".
func (c *Cortex) getMemoryByID(ctx context.Context, id string) (*Memory, error) {
	var m Memory
	var vat, iat, eat sql.NullTime
	err := c.db.QueryRowContext(ctx,
		`SELECT id, content, source, confidence, created_at, updated_at, valid_at, invalid_at, expired_at
		 FROM memories WHERE id = ?`, id,
	).Scan(&m.ID, &m.Content, &m.Source, &m.Confidence, &m.CreatedAt, &m.UpdatedAt, &vat, &iat, &eat)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cortex: get memory by id: %w", err)
	}
	m.ValidAt = nullTimePtr(vat)
	m.InvalidAt = nullTimePtr(iat)
	m.ExpiredAt = nullTimePtr(eat)
	return &m, nil
}
```

(`sql`, `fmt` already imported in `memory.go`; `nullTimePtr` exists from Tier 2a.)

- [ ] **Step 4: Implement ApplyReconcileReport**

In `reconcile.go`:

```go
// ApplyReconcileReport applies a previously-produced reconcile report WITHOUT
// re-running detection — this is the reviewed-apply path that closes the gap
// where a dry-run and a separate --apply could diverge. Each proposed
// supersession is re-validated against current state before invalidation:
// the stale memory must still be currently valid, the superseding memory must
// still exist, be currently valid, and be strictly newer than the stale one.
// Proposals that no longer hold are skipped and recorded in the returned
// report's Rejected list with a reason. A Skipped input report is returned
// unchanged. The returned report's Proposed list contains only the
// supersessions that were actually applied.
func (c *Cortex) ApplyReconcileReport(ctx context.Context, report ReconcileReport) (ReconcileReport, error) {
	if report.Skipped {
		return report, nil
	}

	out := ReconcileReport{
		EntitiesScanned: report.EntitiesScanned,
		MemoriesScanned: report.MemoriesScanned,
	}

	for _, p := range report.Proposed {
		stale, err := c.getMemoryByID(ctx, p.StaleID)
		if err != nil {
			return out, err
		}
		newer, err := c.getMemoryByID(ctx, p.SupersededByID)
		if err != nil {
			return out, err
		}

		reason, ok := revalidateSupersession(stale, newer)
		if !ok {
			out.Rejected = append(out.Rejected, RejectedPair{
				StaleID: p.StaleID, SupersededByID: p.SupersededByID, Reason: reason,
			})
			continue
		}

		invalidAt := p.InvalidAt
		if err := c.InvalidateMemory(ctx, p.StaleID, &invalidAt); err != nil {
			return out, fmt.Errorf("cortex: apply reviewed supersession (stale %s): %w", p.StaleID, err)
		}
		out.Proposed = append(out.Proposed, p)
	}

	return out, nil
}

// revalidateSupersession re-checks the deterministic gate against current
// memory state. Returns ("", true) when the supersession may be applied,
// or (reason, false) when it must be skipped.
func revalidateSupersession(stale, newer *Memory) (string, bool) {
	if stale == nil {
		return "stale memory no longer exists", false
	}
	if newer == nil {
		return "superseding memory no longer exists", false
	}
	if stale.ExpiredAt != nil || stale.InvalidAt != nil {
		return "stale memory no longer current", false
	}
	if newer.ExpiredAt != nil || newer.InvalidAt != nil {
		return "superseding memory no longer current", false
	}
	if !newer.CreatedAt.After(stale.CreatedAt) {
		return "superseding memory no longer strictly newer", false
	}
	return "", true
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./ -run TestApplyReconcileReport -v`
Expected: PASS (applies-reviewed, skips-stale, skipped-input-unchanged).

- [ ] **Step 6: Run the full suite**

Run: `go test ./...`
Expected: PASS. Ignore `llm/openai` 429.

- [ ] **Step 7: Commit**

```bash
git add reconcile.go memory.go reconcile_test.go
git commit -m "feat: ApplyReconcileReport applies a reviewed report with re-validation"
```

---

### Task 3: CLI --json (emit) and --from (reviewed apply)

**Files:**
- Modify: `cmd/cortex/reconcile.go`
- Test: build + smoke (CLI has no unit harness; mirror existing patterns)

**Interfaces:**
- Consumes: `Reconcile`, `ApplyReconcile`, `ApplyReconcileReport`, `RenderReconcileMarkdown`, the JSON-tagged `ReconcileReport`.
- Produces: extended `cortex reconcile`:
  - (unchanged) no flags → dry-run, render markdown to stdout.
  - `--out <file>` → write markdown report to file (existing).
  - **new** `--json <file>` → write the dry-run report as JSON to file (machine-readable, for later `--from`).
  - **new** `--from <file>` with `--apply` → read the JSON report and apply it via `ApplyReconcileReport` (no detection). Render the applied-outcome report.
  - (unchanged) `--apply` alone → one-shot detect-and-apply via `ApplyReconcile`.
  - `--from` without `--apply` → error (a saved report is only meaningful to apply).

- [ ] **Step 1: Extend the CLI handler**

Rewrite `cmdReconcile` in `cmd/cortex/reconcile.go` to parse the new flags. Full replacement:

```go
func cmdReconcile() {
	apply := false
	out := ""      // markdown out
	jsonOut := ""  // machine-readable report out (dry-run)
	from := ""     // saved report to apply
	usage := "usage: cortex reconcile [--apply] [--out <file>] [--json <file>] [--from <file>]\n" +
		"  (no flags)        dry-run: detect contradictions, print report\n" +
		"  --json <file>     dry-run: also write the report as JSON for later --from\n" +
		"  --apply           one-shot: detect and apply in this run\n" +
		"  --apply --from f  apply a previously saved JSON report (re-validated, no re-detection)\n" +
		"  --out <file>      write the markdown report to a file"
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--apply":
			apply = true
		case "--out":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, usage)
				os.Exit(1)
			}
			i++
			out = args[i]
		case "--json":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, usage)
				os.Exit(1)
			}
			i++
			jsonOut = args[i]
		case "--from":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, usage)
				os.Exit(1)
			}
			i++
			from = args[i]
		default:
			fmt.Fprintln(os.Stderr, usage)
			os.Exit(1)
		}
	}

	if from != "" && !apply {
		fmt.Fprintln(os.Stderr, "error: --from requires --apply (a saved report is only used to apply)")
		fmt.Fprintln(os.Stderr, usage)
		os.Exit(1)
	}

	cx := openCortex()
	defer cx.Close()
	ctx := context.Background()

	var report cortex.ReconcileReport
	var err error
	switch {
	case apply && from != "":
		// Reviewed apply: read the saved report, apply without re-detecting.
		var data []byte
		data, err = os.ReadFile(from)
		if err != nil {
			fmt.Fprintf(os.Stderr, "read report: %v\n", err)
			os.Exit(1)
		}
		var saved cortex.ReconcileReport
		if err = json.Unmarshal(data, &saved); err != nil {
			fmt.Fprintf(os.Stderr, "parse report %s: %v\n", from, err)
			os.Exit(1)
		}
		report, err = cx.ApplyReconcileReport(ctx, saved)
	case apply:
		// One-shot detect-and-apply.
		report, err = cx.ApplyReconcile(ctx)
	default:
		// Dry-run.
		report, err = cx.Reconcile(ctx)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile: %v\n", err)
		os.Exit(1)
	}

	// Emit machine-readable JSON when requested (typically with a dry-run).
	if jsonOut != "" {
		data, mErr := json.MarshalIndent(report, "", "  ")
		if mErr != nil {
			fmt.Fprintf(os.Stderr, "encode report: %v\n", mErr)
			os.Exit(1)
		}
		if wErr := os.WriteFile(jsonOut, data, 0o644); wErr != nil {
			fmt.Fprintf(os.Stderr, "write json report: %v\n", wErr)
			os.Exit(1)
		}
		fmt.Printf("wrote JSON report to %s\n", jsonOut)
	}

	md := cortex.RenderReconcileMarkdown(report)
	if out != "" {
		if wErr := os.WriteFile(out, []byte(md), 0o644); wErr != nil {
			fmt.Fprintf(os.Stderr, "write report: %v\n", wErr)
			os.Exit(1)
		}
		fmt.Printf("wrote reconcile report to %s\n", out)
		return
	}
	fmt.Print(md)
}
```

Add `"encoding/json"` to the imports of `cmd/cortex/reconcile.go`.

- [ ] **Step 2: Build to verify it compiles**

Run: `go build ./...`
Expected: success.

- [ ] **Step 3: Smoke-test the round trip on an empty DB**

Run (from repo root, using the project's DB env convention — check how other subcommands resolve the path, e.g. `CORTEX_DB`):

```bash
CORTEX_DB=/tmp/sm_2c.db go run ./cmd/cortex reconcile --json /tmp/rep.json
CORTEX_DB=/tmp/sm_2c.db go run ./cmd/cortex reconcile --apply --from /tmp/rep.json
```

Expected: first writes `/tmp/rep.json` and prints a report; second reads it, applies (zero proposals on an empty DB), prints the applied report. No crash. Also verify `cortex reconcile --from /tmp/rep.json` (without `--apply`) errors with the `--from requires --apply` message.

- [ ] **Step 4: Full suite**

Run: `go build ./... && go test ./...`
Expected: pass. Ignore `llm/openai` 429.

- [ ] **Step 5: Commit**

```bash
git add cmd/cortex/reconcile.go
git commit -m "feat: reconcile --json emit and --from reviewed-apply CLI"
```

---

### Task 4: Document the review→apply workflow

**Files:**
- Modify: `cmd/cortex/CORTEX.md.template`
- Modify: `README.md`
- Test: build only

**Interfaces:** none new.

- [ ] **Step 1: Update the agent contract**

In `cmd/cortex/CORTEX.md.template`, in the reconciliation subsection added in Tier 2b, add 2-3 lines describing the reviewed-apply workflow: `cortex reconcile --json report.json` to capture a dry-run, review it, then `cortex reconcile --apply --from report.json` to apply exactly those proposals (re-validated against current state, no re-detection). Note that `--apply` alone still does one-shot detect-and-apply. Match surrounding style.

- [ ] **Step 2: Update the README**

In `README.md`, update the `### cortex reconcile` subsection and the CLI reference listing to include `--json` and `--from`. Briefly contrast the two apply modes: one-shot (`--apply`) vs reviewed (`--apply --from <file>`, which re-validates and never re-detects). Keep accurate: re-validation skips proposals that no longer hold.

- [ ] **Step 3: Build (template embedded) + suite**

Run: `go build ./... && go test ./...`
Expected: pass.

- [ ] **Step 4: Commit**

```bash
git add cmd/cortex/CORTEX.md.template README.md
git commit -m "docs: document reconcile review-then-apply workflow"
```

---

## Self-Review

**Spec coverage** (Phase 2c = close the dry-run/apply TOCTOU via JSON report + re-validating reviewed-apply, keeping one-shot):
- JSON serialization of the report: Task 1. ✅
- `ApplyReconcileReport` with re-validation (skip-stale): Task 2. ✅
- CLI `--json` emit + `--from` reviewed apply, one-shot `--apply` preserved, `--from` requires `--apply`: Task 3. ✅
- Docs: Task 4. ✅
- Re-validate-don't-trust-blindly (decided): enforced by `revalidateSupersession` (Task 2), tested by `TestApplyReconcileReport_SkipsStaleProposal`. ✅
- Keep both apply modes (decided): one-shot `--apply` untouched; new `--apply --from` added (Task 3). ✅
- No change to detection/gate/`Reconcile` (constraint): only additive code. ✅

**Placeholder scan:** All code steps show complete code. The one env detail (CLI DB path var for the smoke test) is flagged to confirm against existing subcommands, not assumed. No TBD/"handle errors"/"similar to". ✅

**Type consistency:**
- JSON tags (Task 1) — relied on by the CLI marshal/unmarshal (Task 3).
- `getMemoryByID(ctx, id) (*Memory, error)` returning `(nil,nil)` on missing (Task 2) — consumed by `ApplyReconcileReport`'s re-validation; uses `nullTimePtr` (Tier 2a).
- `ApplyReconcileReport(ctx, ReconcileReport) (ReconcileReport, error)` (Task 2) — called by the CLI `--from` path (Task 3).
- `revalidateSupersession(stale, newer *Memory) (string, bool)` (Task 2) — mirrors the Tier 2b gate's strictly-newer rule, applied to current state.

**Known scope edges (documented, not gaps):**
- **A saved report can name memories that were since deleted (`Forget`)** — `getMemoryByID` returns nil → re-validation skips with "no longer exists". Handled.
- **`InvalidAt` comes from the saved report**, not recomputed — it's the superseding memory's created_at captured at dry-run time, which doesn't change; re-validation confirms the superseding memory still exists and is strictly newer, so the captured `InvalidAt` remains correct. Noted.
- **One-shot `--apply` still re-detects** (Tier 2b behavior) — intentionally kept for autonomous use; the reviewed path is the TOCTOU-safe option. Documented.
- **No schema change** this phase — only struct tags + a new read query. Backward compatible.

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-06-23-tier2c-reviewed-apply.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — execute tasks in this session with checkpoints.

**Which approach?**
