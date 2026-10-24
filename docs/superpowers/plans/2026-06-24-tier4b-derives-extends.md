# Tier 4 Phase 4b: Derives/Extends Edges Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete the facts-on-facts graph (gap G5) by detecting **non-contradicting** memory→memory relations: `extends` (one memory adds detail to another) and `derives` (one memory is inferred/derived from another). Phase 4a recorded `supersedes` edges from reconciliation; this phase adds the other two edge types via a dedicated LLM detection pass with the same **detect (LLM) / gate (deterministic) / record (edge)** discipline that reconciliation uses. The result is a richer memory graph like supermemory's `updates`/`extends`/`derives` model — built in the open on cortex's primitives.

**Architecture:** Five tasks, mirroring the reconcile pattern. (1) A `RelationDetector` optional interface (`DetectRelations`) + a `MemoryRelation` carrier type, parallel to `Reconciler`/`ConflictPair`. (2) Both providers implement `DetectRelations` (prompt + parse), parallel to `DetectConflicts`. (3) A `BuildMemoryEdges` method on `Cortex`: per-entity, fetch currently-valid memories, call the detector, run a deterministic gate (valid type, distinct ids, both currently-valid, not self-loop, no inverse-duplicate), record `derives`/`extends` edges via `PutMemoryEdge`; returns a report. (4) A dry-run/report mode + render. (5) CLI surface (`cortex edges` or fold into reconcile? — decided: a focused `cortex relate` command, or wire into the Phase 4d `maintain` pass; this phase exposes the method + a thin CLI).

**Tech Stack:** Go 1.25.1, `modernc.org/sqlite` (pure-Go, no CGo), Anthropic + OpenAI providers. Tests use `openTestDB`, `mockLLM`.

## Global Constraints

- **Module path:** `github.com/sausheong/cortex` (root package is `cortex`).
- **No CGo, single binary.** Only existing dependencies.
- **Detect vs decide (load-bearing, same as reconcile):** the LLM only *proposes* relations (fuzzy); a **deterministic gate in Go** decides what is recorded. The gate rejects: unknown edge types (only `derives`/`extends` allowed — `supersedes` is reconcile's job, not this pass), ids not in the candidate set, self-loops (`source==target`), and pairs where an edge already exists in either direction for that type. An LLM hallucinating a relation between unrelated memories is bounded by the candidate set (only memories sharing an entity are compared).
- **Optional capability (same as Reconciler):** `RelationDetector` is a separate interface from `LLM`. `BuildMemoryEdges` type-asserts the configured LLM for it and **skips gracefully** (returns a Skipped report) when absent — never errors.
- **Additive & idempotent:** edges are recorded via the existing idempotent `PutMemoryEdge` (Phase 4a). Re-running `BuildMemoryEdges` does not stack edges. It never modifies or deletes memories — purely edge-additive.
- **Direction convention (from 4a):** `source <type> target`. For `extends`: source extends target (source adds detail to the target it builds on). For `derives`: source is derived from target. The LLM is prompted to return `(source_id, target_id, type)` in this subject-verb-object order; the gate does not reinterpret direction.
- **No contradiction here:** `derives`/`extends` are for memories that are *consistent* with each other. Contradictions remain reconcile's domain. If the LLM flags a contradiction as `extends`, that's acceptable noise — the edge is harmless metadata; reconcile handles actual supersession separately.
- **Confidence / bi-temporal invariants:** unchanged. Only currently-valid memories are candidates (same as reconcile's `GetMemoriesByEntity`).
- **Commit trailer:** do NOT add a `Co-Authored-By` trailer to commits.
- **Test command:** `go test ./...` must pass. Hermetic only. (`llm/openai` live-429 pre-existing, out of scope.)
- **TDD:** failing test first, watch fail, implement, watch pass, commit.

---

## File Structure

| File | Responsibility | Change |
|------|----------------|--------|
| `types.go` | interfaces/types | `RelationDetector` interface; `MemoryRelation` type; `RelationReport`/`RelationProposal`/`RejectedRelation`; `RelateOption` |
| `relate.go` | new file | `BuildMemoryEdges` (detect → gate → record); `allEntityIDs` reuse |
| `relate_render.go` | new file | `RenderRelationMarkdown` |
| `llm/anthropic/llm.go`, `llm/openai/llm.go` | providers | `DetectRelations` + prompt + parse + interface assertion |
| `cmd/cortex/relate.go` | new file | `cmdRelate` (`cortex relate` dry-run/--apply) |
| `cmd/cortex/main.go` | dispatch | `case "relate"` |
| `*_test.go` | tests | per task |

---

### Task 1: RelationDetector interface and report types

**Files:**
- Modify: `types.go`
- Test: `types_test.go` (interface-satisfaction compile check via mockLLM, mirroring `TestMockLLM_SatisfiesReconciler`)

**Interfaces:**
- Produces:
  - `RelationDetector interface { DetectRelations(ctx, memories []Memory) ([]MemoryRelation, error) }` — optional capability parallel to `Reconciler`.
  - `MemoryRelation struct { SourceID, TargetID, Type, Reason string }` — an LLM-proposed relation (Type is `derives` or `extends`).
  - `RelationProposal struct { SourceID, SourceContent, TargetID, TargetContent, Type, Reason string }` — a gate-passed proposal.
  - `RejectedRelation struct { SourceID, TargetID, Type, Reason string }` — gate-rejected, with why.
  - `RelationReport struct { EntitiesScanned, MemoriesScanned int; Proposed []RelationProposal; Rejected []RejectedRelation; Skipped bool; SkipReason string }`.
  - `RelateOption func(*relateConfig)` + empty `relateConfig` (room to grow), mirroring `ReconcileOption`.

- [ ] **Step 1: Write the failing test**

Add to `types_test.go` (or wherever `TestMockLLM_SatisfiesReconciler` lives — `recall_test.go:301`; put this beside it):

```go
func TestMockLLM_SatisfiesRelationDetector(t *testing.T) {
	var d RelationDetector = &mockLLM{
		detectRelationsFn: func(_ context.Context, mems []Memory) ([]MemoryRelation, error) {
			return []MemoryRelation{{SourceID: "a", TargetID: "b", Type: EdgeExtends, Reason: "test"}}, nil
		},
	}
	got, err := d.DetectRelations(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Type != EdgeExtends {
		t.Fatalf("unexpected: %+v", got)
	}
}
```

This requires adding a `detectRelationsFn` field + `DetectRelations` method to the test `mockLLM` (in `recall_test.go`). Add them mirroring `detectConflictsFn`/`DetectConflicts` (`recall_test.go:270,294-299`):

```go
// in mockLLM struct:
	detectRelationsFn func(ctx context.Context, memories []Memory) ([]MemoryRelation, error)

// method:
func (m *mockLLM) DetectRelations(ctx context.Context, memories []Memory) ([]MemoryRelation, error) {
	if m.detectRelationsFn != nil {
		return m.detectRelationsFn(ctx, memories)
	}
	return nil, nil
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestMockLLM_SatisfiesRelationDetector -v`
Expected: FAIL — `RelationDetector`, `MemoryRelation`, `detectRelationsFn` undefined.

- [ ] **Step 3: Add the types**

In `types.go`, after the `Reconciler`/`ConflictPair` block (`types.go:126-136`), add:

```go
// RelationDetector is an optional capability a provider may implement to
// detect NON-contradicting relations among memories — one memory extending
// or deriving from another. It is separate from LLM (and from Reconciler) so
// the core interface stays stable and providers opt in. BuildMemoryEdges
// (relate.go) type-asserts the configured LLM for this interface and no-ops
// gracefully when it is absent.
//
// DetectRelations receives memories (typically all currently-valid memories
// linked to one entity) and returns proposed derives/extends relations. The
// implementation only PROPOSES; the deterministic gate in relate.go decides
// what is recorded.
type RelationDetector interface {
	DetectRelations(ctx context.Context, memories []Memory) ([]MemoryRelation, error)
}

// MemoryRelation is an LLM-proposed non-contradicting relation: SourceID
// <Type> TargetID, where Type is EdgeDerives or EdgeExtends. Reads
// subject-verb-object: for extends, source adds detail to target; for
// derives, source is inferred from target.
type MemoryRelation struct {
	SourceID string `json:"source_id"`
	TargetID string `json:"target_id"`
	Type     string `json:"type"`
	Reason   string `json:"reason"`
}

// RelationProposal is a gate-passed relation ready to record as an edge.
type RelationProposal struct {
	SourceID      string `json:"source_id"`
	SourceContent string `json:"source_content"`
	TargetID      string `json:"target_id"`
	TargetContent string `json:"target_content"`
	Type          string `json:"type"`
	Reason        string `json:"reason"`
}

// RejectedRelation is an LLM-proposed relation the deterministic gate rejected.
type RejectedRelation struct {
	SourceID string `json:"source_id"`
	TargetID string `json:"target_id"`
	Type     string `json:"type"`
	Reason   string `json:"reason"`
}

// RelationReport summarizes a BuildMemoryEdges run.
type RelationReport struct {
	EntitiesScanned int                `json:"entities_scanned"`
	MemoriesScanned int                `json:"memories_scanned"`
	Proposed        []RelationProposal `json:"proposed"`
	Rejected        []RejectedRelation `json:"rejected"`
	Skipped         bool               `json:"skipped"`
	SkipReason      string             `json:"skip_reason,omitempty"`
}

// RelateOption configures a BuildMemoryEdges run.
type RelateOption func(*relateConfig)

type relateConfig struct{}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./ -run TestMockLLM_SatisfiesRelationDetector -v`
Expected: PASS.

- [ ] **Step 5: Run the full suite + commit**

Run: `go test ./...` → PASS. `go vet ./...` → clean.

```bash
git add types.go recall_test.go
git commit -m "feat: RelationDetector interface and relation report types"
```

---

### Task 2: Providers implement DetectRelations

**Files:**
- Modify: `llm/anthropic/llm.go`, `llm/openai/llm.go`
- Test: `llm/anthropic/conflicts_test.go` (or a new `relations_test.go`), same for openai — parse-level tests

**Interfaces:**
- Consumes: `cortex.MemoryRelation`.
- Produces: both providers gain `DetectRelations(ctx, []cortex.Memory) ([]cortex.MemoryRelation, error)` + a `detectRelationsPrompt` + a `parseRelationsJSON` + `var _ cortex.RelationDetector = (*LLM)(nil)`. Mirrors `DetectConflicts` exactly.

- [ ] **Step 1: Write the failing test (anthropic)**

Inspect `llm/anthropic/conflicts_test.go` for the parse-test pattern. Add a `parseRelationsJSON` test:

```go
func TestParseRelationsJSON(t *testing.T) {
	raw := `{"relations":[
		{"source_id":"a","target_id":"b","type":"extends","reason":"adds detail"},
		{"source_id":"c","target_id":"d","type":"derives","reason":"inferred"}
	]}`
	got, err := parseRelationsJSON(raw)
	if err != nil {
		t.Fatalf("parseRelationsJSON: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 relations, got %d", len(got))
	}
	if got[0].Type != "extends" || got[1].Type != "derives" {
		t.Fatalf("unexpected types: %+v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./llm/anthropic/ -run TestParseRelationsJSON -v`
Expected: FAIL — `parseRelationsJSON` undefined.

- [ ] **Step 3: Implement in anthropic** (`llm/anthropic/llm.go`, after the conflicts block `llm.go:168-222`)

```go
const detectRelationsPrompt = `You are given a list of memories about the same subject, each with an ID.
Identify pairs where one memory EXTENDS another (adds detail without contradicting)
or DERIVES from another (is inferred/follows from it). Do NOT flag contradictions
or simple restatements — only genuine elaboration or derivation.

Return ONLY valid JSON, no markdown:
{"relations":[{"source_id":"<id of the memory that extends/derives>","target_id":"<id of the base memory>","type":"extends|derives","reason":"<short reason>"}]}

For "extends", source adds detail to target. For "derives", source is inferred from target.
If there are none, return {"relations":[]}.`

// DetectRelations implements cortex.RelationDetector.
func (l *LLM) DetectRelations(ctx context.Context, memories []cortex.Memory) ([]cortex.MemoryRelation, error) {
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
		System:    []anthropic.TextBlockParam{{Text: detectRelationsPrompt}},
		Messages:  []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(sb.String()))},
	})
	if err != nil {
		return nil, fmt.Errorf("anthropic: detect relations: %w", err)
	}
	return parseRelationsJSON(extractText(msg))
}

func parseRelationsJSON(raw string) ([]cortex.MemoryRelation, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "```") {
		lines := strings.Split(raw, "\n")
		if len(lines) > 2 {
			raw = strings.Join(lines[1:len(lines)-1], "\n")
		}
	}
	var parsed struct {
		Relations []cortex.MemoryRelation `json:"relations"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("anthropic: parse relations: %w", err)
	}
	return parsed.Relations, nil
}

var _ cortex.RelationDetector = (*LLM)(nil)
```

- [ ] **Step 4: Implement in openai** — same additions in `llm/openai/llm.go` (after its conflicts block), using the OpenAI chat-completion call shape (mirror `DetectConflicts` there, `llm/openai/llm.go:195-220`), the identical `detectRelationsPrompt`, `parseRelationsJSON`, and `var _ cortex.RelationDetector = (*LLM)(nil)`. Add the same parse test to the openai package.

- [ ] **Step 5: Run tests + full suite + commit**

Run: `go test ./llm/anthropic/ ./llm/openai/ -run TestParseRelationsJSON -v` → PASS (hermetic). `go test ./...` → root + providers pass (live-429 out of scope). `go vet ./...` clean.

```bash
git add llm/anthropic/llm.go llm/anthropic/*_test.go llm/openai/llm.go llm/openai/*_test.go
git commit -m "feat: providers implement DetectRelations for derives/extends"
```

---

### Task 3: BuildMemoryEdges (detect → gate → record)

**Files:**
- Create: `relate.go`
- Test: `relate_test.go`

**Interfaces:**
- Consumes: `RelationDetector` (Task 1), `PutMemoryEdge` + `GetMemoryEdgesByType` (4a), `GetMemoriesByEntity`, `allEntityIDs` (reconcile.go:128).
- Produces: `BuildMemoryEdges(ctx, opts ...RelateOption) (RelationReport, error)` — per-entity: fetch currently-valid memories; if <2 skip; call `DetectRelations`; for each proposed relation run the deterministic gate; record passing ones as edges; collect a report. Skipped report when no `RelationDetector` configured. Idempotent (PutMemoryEdge + a `seen` dedup across entity passes).

**The deterministic gate** rejects a proposed relation when:
1. Type is not `EdgeDerives` or `EdgeExtends` (reason: "unsupported relation type").
2. `SourceID == TargetID` (reason: "self-loop").
3. Either id is not in the current candidate set (reason: "id not in candidate set").
4. An edge of this type already exists between the two memories in EITHER direction (reason: "edge already exists") — checked via `GetMemoryEdgesByType` + a `seen` set within the run.

- [ ] **Step 1: Write the failing test**

Add to `relate_test.go` (new file):

```go
package cortex

import (
	"context"
	"testing"
)

func TestBuildMemoryEdges_RecordsExtends(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Name: "Project Y", Type: "project"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	base := &Memory{Content: "Project Y uses Postgres", EntityIDs: []string{e.ID}}
	detail := &Memory{Content: "Project Y uses Postgres 16 with pgvector", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, base); err != nil {
		t.Fatal(err)
	}
	if err := c.PutMemory(ctx, detail); err != nil {
		t.Fatal(err)
	}

	c.SetLLM(&mockLLM{
		detectRelationsFn: func(_ context.Context, mems []Memory) ([]MemoryRelation, error) {
			return []MemoryRelation{
				{SourceID: detail.ID, TargetID: base.ID, Type: EdgeExtends, Reason: "adds version detail"},
			}, nil
		},
	})

	report, err := c.BuildMemoryEdges(ctx)
	if err != nil {
		t.Fatalf("BuildMemoryEdges: %v", err)
	}
	if report.Skipped {
		t.Fatalf("unexpected skip: %s", report.SkipReason)
	}
	if len(report.Proposed) != 1 {
		t.Fatalf("expected 1 proposed relation, got %d (%+v)", len(report.Proposed), report.Proposed)
	}

	edges, err := c.GetMemoryEdgesByType(ctx, detail.ID, EdgeExtends)
	if err != nil {
		t.Fatal(err)
	}
	if len(edges) != 1 || edges[0].SourceID != detail.ID || edges[0].TargetID != base.ID {
		t.Fatalf("expected extends edge detail->base, got %+v", edges)
	}
}

func TestBuildMemoryEdges_GateRejectsSelfLoopAndBadType(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Name: "Z", Type: "thing"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	m1 := &Memory{Content: "fact one", EntityIDs: []string{e.ID}}
	m2 := &Memory{Content: "fact two", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, m1); err != nil {
		t.Fatal(err)
	}
	if err := c.PutMemory(ctx, m2); err != nil {
		t.Fatal(err)
	}

	c.SetLLM(&mockLLM{
		detectRelationsFn: func(_ context.Context, mems []Memory) ([]MemoryRelation, error) {
			return []MemoryRelation{
				{SourceID: m1.ID, TargetID: m1.ID, Type: EdgeExtends, Reason: "self"},         // self-loop
				{SourceID: m1.ID, TargetID: m2.ID, Type: "supersedes", Reason: "wrong type"},   // unsupported type
				{SourceID: m1.ID, TargetID: "ghost", Type: EdgeDerives, Reason: "unknown id"},  // not in set
			}, nil
		},
	})

	report, err := c.BuildMemoryEdges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Proposed) != 0 {
		t.Fatalf("expected 0 proposed (all rejected), got %+v", report.Proposed)
	}
	if len(report.Rejected) != 3 {
		t.Fatalf("expected 3 rejected, got %d (%+v)", len(report.Rejected), report.Rejected)
	}
}

func TestBuildMemoryEdges_SkipsWithoutDetector(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	// No LLM set → no RelationDetector → Skipped.
	report, err := c.BuildMemoryEdges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Skipped {
		t.Fatalf("expected Skipped report, got %+v", report)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestBuildMemoryEdges -v`
Expected: FAIL — `BuildMemoryEdges` undefined.

- [ ] **Step 3: Implement BuildMemoryEdges**

Create `relate.go`, modeled on `Reconcile` (`reconcile.go:17-100`):

```go
package cortex

import (
	"context"
	"fmt"
)

// BuildMemoryEdges scans the graph per-entity for non-contradicting relations
// (derives/extends) among currently-valid memories and records them as memory
// edges. Detection is delegated to the configured LLM if it implements
// RelationDetector; if it does not (or none is configured), the report is
// marked Skipped. The record-vs-reject decision is made by a deterministic
// gate here, not by the LLM. Edge writes are idempotent (PutMemoryEdge), so
// re-running does not stack edges. This never modifies or deletes memories.
func (c *Cortex) BuildMemoryEdges(ctx context.Context, opts ...RelateOption) (RelationReport, error) {
	cfg := &relateConfig{}
	for _, o := range opts {
		o(cfg)
	}

	det, ok := c.cfg.llm.(RelationDetector)
	if c.cfg.llm == nil || !ok {
		return RelationReport{
			Skipped:    true,
			SkipReason: "no RelationDetector-capable LLM configured",
		}, nil
	}

	var report RelationReport

	// Dedup proposals across entity passes; keyed by an order-insensitive
	// pair+type signature so an (a,b,extends) seen under one shared entity
	// isn't re-proposed under another.
	seen := map[string]bool{}

	entityIDs, err := c.allEntityIDs(ctx)
	if err != nil {
		return RelationReport{}, err
	}

	for _, eid := range entityIDs {
		mems, err := c.GetMemoriesByEntity(ctx, eid) // currently-valid only
		if err != nil {
			return RelationReport{}, err
		}
		if len(mems) < 2 {
			continue
		}
		report.EntitiesScanned++
		report.MemoriesScanned += len(mems)

		rels, err := det.DetectRelations(ctx, mems)
		if err != nil {
			return RelationReport{}, fmt.Errorf("cortex: detect relations for entity %s: %w", eid, err)
		}

		inSet := make(map[string]Memory, len(mems))
		for _, m := range mems {
			inSet[m.ID] = m
		}

		for _, r := range rels {
			reason, okGate := c.gateRelation(ctx, r, inSet, seen)
			if !okGate {
				report.Rejected = append(report.Rejected, RejectedRelation{
					SourceID: r.SourceID, TargetID: r.TargetID, Type: r.Type, Reason: reason,
				})
				continue
			}
			edge := &MemoryEdge{SourceID: r.SourceID, TargetID: r.TargetID, Type: r.Type}
			if err := c.PutMemoryEdge(ctx, edge); err != nil {
				return report, fmt.Errorf("cortex: record %s edge: %w", r.Type, err)
			}
			src := inSet[r.SourceID]
			tgt := inSet[r.TargetID]
			report.Proposed = append(report.Proposed, RelationProposal{
				SourceID: r.SourceID, SourceContent: src.Content,
				TargetID: r.TargetID, TargetContent: tgt.Content,
				Type: r.Type, Reason: r.Reason,
			})
		}
	}
	return report, nil
}

// gateRelation is the deterministic gate. Returns ("", true) to record, or
// (reason, false) to reject.
func (c *Cortex) gateRelation(ctx context.Context, r MemoryRelation, inSet map[string]Memory, seen map[string]bool) (string, bool) {
	if r.Type != EdgeDerives && r.Type != EdgeExtends {
		return "unsupported relation type", false
	}
	if r.SourceID == r.TargetID {
		return "self-loop", false
	}
	if _, ok := inSet[r.SourceID]; !ok {
		return "source id not in candidate set", false
	}
	if _, ok := inSet[r.TargetID]; !ok {
		return "target id not in candidate set", false
	}
	// Order-insensitive signature for this type, so (a,b) and (b,a) collide.
	a, b := r.SourceID, r.TargetID
	if a > b {
		a, b = b, a
	}
	sig := a + "\x00" + b + "\x00" + r.Type
	if seen[sig] {
		return "duplicate relation in this run", false
	}
	// Already-persisted edge of this type in either direction?
	existing, err := c.GetMemoryEdgesByType(ctx, r.SourceID, r.Type)
	if err == nil {
		for _, e := range existing {
			if (e.SourceID == r.SourceID && e.TargetID == r.TargetID) ||
				(e.SourceID == r.TargetID && e.TargetID == r.SourceID) {
				return "edge already exists", false
			}
		}
	}
	seen[sig] = true
	return "", true
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./ -run TestBuildMemoryEdges -v`
Expected: PASS (all three).

- [ ] **Step 5: Run the full suite + commit**

Run: `go test ./...` → PASS. `go vet ./...` clean.

```bash
git add relate.go relate_test.go
git commit -m "feat: BuildMemoryEdges detects and records derives/extends edges"
```

---

### Task 4: Relation report rendering

**Files:**
- Create: `relate_render.go`
- Test: `relate_render_test.go`

**Interfaces:**
- Consumes: `RelationReport`.
- Produces: `RenderRelationMarkdown(r RelationReport) string` — mirrors `RenderReconcileMarkdown` (`reconcile_render.go`): header, scanned counts, proposed/rejected sections, Skipped handling.

- [ ] **Step 1: Write the failing test**

Add to `relate_render_test.go`:

```go
package cortex

import (
	"strings"
	"testing"
)

func TestRenderRelationMarkdown(t *testing.T) {
	r := RelationReport{
		EntitiesScanned: 2,
		MemoriesScanned: 5,
		Proposed: []RelationProposal{
			{SourceID: "d", SourceContent: "detail", TargetID: "b", TargetContent: "base", Type: "extends", Reason: "adds detail"},
		},
		Rejected: []RejectedRelation{
			{SourceID: "x", TargetID: "x", Type: "extends", Reason: "self-loop"},
		},
	}
	out := RenderRelationMarkdown(r)
	for _, want := range []string{"Relation", "extends", "detail", "base", "self-loop"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected output to contain %q, got:\n%s", want, out)
		}
	}

	skipped := RenderRelationMarkdown(RelationReport{Skipped: true, SkipReason: "no detector"})
	if !strings.Contains(skipped, "no detector") {
		t.Fatalf("expected skip reason in output, got:\n%s", skipped)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./ -run TestRenderRelationMarkdown -v`
Expected: FAIL — undefined.

- [ ] **Step 3: Implement** (`relate_render.go`, mirror `reconcile_render.go:22-...`)

```go
package cortex

import (
	"fmt"
	"strings"
)

// RenderRelationMarkdown renders a RelationReport as human-readable markdown,
// mirroring RenderReconcileMarkdown.
func RenderRelationMarkdown(r RelationReport) string {
	var b strings.Builder
	b.WriteString("# Relation Report\n\n")

	if r.Skipped {
		fmt.Fprintf(&b, "Skipped: %s\n", r.SkipReason)
		return b.String()
	}

	fmt.Fprintf(&b, "Entities scanned: %d\n", r.EntitiesScanned)
	fmt.Fprintf(&b, "Memories scanned: %d\n", r.MemoriesScanned)
	fmt.Fprintf(&b, "Proposed relations: %d\n", len(r.Proposed))
	fmt.Fprintf(&b, "Rejected (gate): %d\n\n", len(r.Rejected))

	if len(r.Proposed) > 0 {
		b.WriteString("## Proposed Relations\n\n")
		for _, p := range r.Proposed {
			fmt.Fprintf(&b, "- %q (%s) **%s** %q (%s)\n", p.SourceContent, p.SourceID, p.Type, p.TargetContent, p.TargetID)
			fmt.Fprintf(&b, "  reason: %s\n\n", p.Reason)
		}
	}

	if len(r.Rejected) > 0 {
		b.WriteString("## Rejected by gate\n\n")
		for _, rj := range r.Rejected {
			fmt.Fprintf(&b, "- %s %s %s: %s\n", rj.SourceID, rj.Type, rj.TargetID, rj.Reason)
		}
	}

	return b.String()
}
```

- [ ] **Step 4: Run test to verify it passes + full suite + commit**

Run: `go test ./ -run TestRenderRelationMarkdown -v` → PASS. `go test ./...` → PASS.

```bash
git add relate_render.go relate_render_test.go
git commit -m "feat: render relation report as markdown"
```

---

### Task 5: cortex relate CLI command

**Files:**
- Create: `cmd/cortex/relate.go`
- Modify: `cmd/cortex/main.go` (dispatch + usage)
- Test: build (CLI orchestration; the engine is unit-tested)

**Interfaces:**
- Consumes: `cortex.BuildMemoryEdges`, `cortex.RenderRelationMarkdown`.
- Produces: `cortex relate` — runs `BuildMemoryEdges` and prints the markdown report. Flags: `--out <file>` (write markdown). Since `BuildMemoryEdges` records edges as it goes (it's not a pure dry-run like reconcile's default), document that `relate` applies by default — OR add a dry-run. **Decision:** to match reconcile's safety ergonomics, `BuildMemoryEdges` as specified records immediately; for the CLI, keep it simple this phase — `relate` records and reports (edges are additive, idempotent, harmless, and reversible by ignoring them). Note in help that it records derives/extends edges. (A pure dry-run mode can be a Phase 4d refinement when folded into `maintain`.)

> **Resolve before coding:** `BuildMemoryEdges` records as it detects (unlike `Reconcile` which is dry-run by default). That's acceptable because edges are additive/idempotent/non-destructive — but the CLI help MUST say so plainly ("records derives/extends edges between related memories"). Do not imply it's a preview.

- [ ] **Step 1: Implement cmdRelate** (`cmd/cortex/relate.go`, mirror `cmd/cortex/reconcile.go` structure but simpler)

```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/sausheong/cortex"
)

func cmdRelate() {
	out := ""
	usage := "usage: cortex relate [--out <file>]\n" +
		"  Detects and records non-contradicting derives/extends edges between\n" +
		"  related memories (requires an LLM provider that supports relation\n" +
		"  detection). Edges are additive and idempotent. Prints a report."
	args := os.Args[2:]
	for i := 0; i < len(args); i++ {
		switch args[i] {
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

	report, err := cx.BuildMemoryEdges(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "relate: %v\n", err)
		os.Exit(1)
	}

	md := cortex.RenderRelationMarkdown(report)
	if out != "" {
		if err := os.WriteFile(out, []byte(md), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write report: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Wrote relation report to %s\n", out)
		return
	}
	fmt.Print(md)
}
```

Confirm `openCortex()` is the shared helper used by `cmdReconcile` (`cmd/cortex/reconcile.go`). Match it.

- [ ] **Step 2: Wire dispatch**

In `cmd/cortex/main.go`, add `case "relate": cmdRelate()` next to `case "reconcile":` (`main.go:72`), and add `relate` to `printUsage`.

- [ ] **Step 3: Build + full suite + commit**

Run: `go build ./...` → success. `go test ./...` → PASS.

```bash
git add cmd/cortex/relate.go cmd/cortex/main.go
git commit -m "feat: cortex relate command records derives/extends edges"
```

---

## Self-Review

**Spec coverage** (Phase 4b = derives/extends edges, completing G5):
- Interface + types (Task 1), provider detection (Task 2), detect→gate→record engine (Task 3), render (Task 4), CLI (Task 5). ✅

**Placeholder scan:** No TBD/TODO — complete code at every step. ✅

**Type / linkage consistency:**
- `RelationDetector`/`MemoryRelation` (Task 1) implemented by providers (Task 2), consumed by `BuildMemoryEdges` (Task 3). Mirrors `Reconciler`/`ConflictPair` exactly. ✅
- Edges recorded via 4a's idempotent `PutMemoryEdge`; gate uses 4a's `GetMemoryEdgesByType`. ✅
- `mockLLM` gains `detectRelationsFn`/`DetectRelations` (Task 1) so it satisfies `RelationDetector` for Task 3's tests. ✅
- Edge-type vocabulary (`EdgeDerives`/`EdgeExtends`) defined in 4a, now actually written. ✅

**Detect/decide discipline:** LLM proposes; deterministic gate (type allowlist excluding `supersedes`, self-loop, candidate-set membership, inverse-duplicate) decides. Same as reconcile. ✅

**Backward compatibility:** purely additive — new interface (opt-in), new method, new command, new edge types written into the existing 4a table. No schema change, no behavior change to existing paths. ✅

**Known scope edges (documented):**
- `BuildMemoryEdges` records as it detects (not dry-run-by-default like reconcile) — justified because edges are additive/idempotent/non-destructive; CLI help says so. A dry-run mode is a possible Phase 4d refinement.
- Edges still not surfaced in recall/traverse/export — substrate continues; Phase 4d's `maintain` report and a recall/traverse follow-up surface them.
- `derives` vs `extends` distinction is the LLM's judgment; the gate doesn't validate semantic correctness, only structural validity. Acceptable (same trust model as extraction).

## Execution Handoff

**Plan complete and saved to `docs/superpowers/plans/2026-06-24-tier4b-derives-extends.md`.**

Phase 4b of Tier 4. After merge: Phase 4c (confidence decay + auto-prune), Phase 4d (`cortex maintain` orchestration folding reconcile + relate + decay together, + cron docs). Execute subagent-driven, then write Phase 4c.
