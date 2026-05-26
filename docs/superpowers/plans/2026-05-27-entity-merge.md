# Entity Merge Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `cortex merge <keep-id> <drop-id>` (and Go API `MergeEntities`) that re-targets every reference to the drop entity onto the keep entity, then deletes the drop row — atomically, with dry-run preview and `merged_from` provenance.

**Architecture:** Single new method on `*Cortex` that runs the entire merge in one SQLite transaction (`BEGIN/COMMIT` or `BEGIN/ROLLBACK` for dry-run). Re-targets `chunks`, `memory_entities`, `relationships` with collision dedup; drops the stale entity embedding; unions attributes (keep wins on duplicates); appends a `merged_from` record to the keep entity's attributes; deletes the drop row. No schema changes. New CLI command wires into the existing switch in `cmd/cortex/main.go`.

**Tech Stack:** Go 1.25.1, `database/sql`, `modernc.org/sqlite`, existing CLI patterns.

**Spec:** `docs/superpowers/specs/2026-05-27-entity-merge-design.md`

---

## File Structure

**New files:** none.

**Modified files:**

| File | Change |
|---|---|
| `types.go` | Add `MergeStats` struct + `mergeRecord` (for `merged_from` provenance) |
| `entity.go` | Add `MergeEntities(ctx, keepID, dropID) (MergeStats, error)` + private helpers |
| `entity_test.go` | Integration tests for all merge scenarios |
| `cmd/cortex/main.go` | Add `cmdMerge()`, switch case `"merge"`, usage entry |
| `cmd/cortex/main_test.go` (or new `merge_test.go`) | CLI flag parsing tests |
| `README.md` | New `### cortex merge` subsection in CLI Reference |
| `docs/CORTEX.md` + `cmd/cortex/CORTEX.md.template` | One paragraph about merging duplicates |

If `entity.go` grows past ~400 lines after the merge code lands, split it into a new `merge.go` file in the same package. Keep it together unless it gets unwieldy.

---

## Task 1: `MergeEntities` — full algorithm, tested

**Files:**
- Modify: `types.go`
- Modify: `entity.go`
- Modify: `entity_test.go`

This is the heaviest task — the whole backend merge logic in one cohesive commit. The CLI wiring (Task 2) and docs (Task 3) come after.

### Step 1: Add types to `types.go`

Append at the bottom of `types.go`:

```go
// MergeStats reports what MergeEntities did (or would do, under dry-run).
type MergeStats struct {
	Relationships int // re-targeted (after dedup)
	Memories      int // memory_entities rows re-targeted
	Chunks        int // re-targeted
	Embeddings    int // dropped (stale embedding for drop entity)
	DupesDropped  int // duplicate relationships + memory_entity rows removed during dedup
	AttrConflicts int // count of attributes where keep already had a value (keep won)
}

// mergeRecord is one entry in an entity's `merged_from` attribute array.
// It snapshots the dropped entity so a merge is recoverable in principle.
type mergeRecord struct {
	ID       string         `json:"id"`
	Name     string         `json:"name"`
	Type     string         `json:"type"`
	Source   string         `json:"source,omitempty"`
	Attrs    map[string]any `json:"attrs,omitempty"`
	MergedAt time.Time      `json:"merged_at"`
}
```

(`time` is already imported.)

### Step 2: Write failing tests in `entity_test.go`

Append the following tests. Use the existing test helper (the prior tasks established `openTestDB(t)` — check the file and use whatever helper exists; if the helper has a different name, adapt). Add `context` and `time` imports if missing.

```go
// --- Basic merge ---

func TestMergeEntities_BasicMerge_ReTargetsEverything(t *testing.T) {
	cx := openTestDB(t)
	defer cx.Close()
	ctx := context.Background()

	keep := &Entity{Type: "person", Name: "Alice Keep"}
	drop := &Entity{Type: "person", Name: "Alice Drop"}
	other := &Entity{Type: "organization", Name: "Stripe"}
	for _, e := range []*Entity{keep, drop, other} {
		if err := cx.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	// drop has a relationship and a memory.
	rel := &Relationship{SourceID: drop.ID, TargetID: other.ID, Type: "works_at"}
	if err := cx.PutRelationship(ctx, rel); err != nil {
		t.Fatal(err)
	}
	mem := &Memory{Content: "drop memory content", EntityIDs: []string{drop.ID}}
	if err := cx.PutMemory(ctx, mem); err != nil {
		t.Fatal(err)
	}
	// Also a chunk linked to drop.
	ch := &Chunk{EntityID: drop.ID, Content: "drop chunk content"}
	if err := cx.PutChunk(ctx, ch); err != nil {
		t.Fatal(err)
	}

	stats, err := cx.MergeEntities(ctx, keep.ID, drop.ID)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if stats.Relationships != 1 {
		t.Errorf("Relationships = %d, want 1", stats.Relationships)
	}
	if stats.Memories != 1 {
		t.Errorf("Memories = %d, want 1", stats.Memories)
	}
	if stats.Chunks != 1 {
		t.Errorf("Chunks = %d, want 1", stats.Chunks)
	}

	// drop entity is gone.
	if _, err := cx.GetEntity(ctx, drop.ID); err == nil {
		t.Error("drop entity should be deleted")
	}
	// keep entity's relationships now include the re-targeted one.
	rels, err := cx.GetRelationships(ctx, keep.ID)
	if err != nil {
		t.Fatal(err)
	}
	foundReTargeted := false
	for _, r := range rels {
		if r.SourceID == keep.ID && r.TargetID == other.ID && r.Type == "works_at" {
			foundReTargeted = true
		}
	}
	if !foundReTargeted {
		t.Error("re-targeted relationship not found on keep entity")
	}
}

// --- Validation errors ---

func TestMergeEntities_SelfMergeError(t *testing.T) {
	cx := openTestDB(t)
	defer cx.Close()
	ctx := context.Background()
	e := &Entity{Type: "person", Name: "Solo"}
	if err := cx.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	_, err := cx.MergeEntities(ctx, e.ID, e.ID)
	if err == nil {
		t.Fatal("expected error for self-merge")
	}
	if !strings.Contains(err.Error(), "itself") {
		t.Errorf("error %q should mention 'itself'", err.Error())
	}
}

func TestMergeEntities_KeepNotFound(t *testing.T) {
	cx := openTestDB(t)
	defer cx.Close()
	ctx := context.Background()
	drop := &Entity{Type: "person", Name: "Drop"}
	if err := cx.PutEntity(ctx, drop); err != nil {
		t.Fatal(err)
	}
	_, err := cx.MergeEntities(ctx, "ent_doesnt_exist", drop.ID)
	if err == nil {
		t.Fatal("expected error for missing keep")
	}
	if !strings.Contains(err.Error(), "keep entity not found") {
		t.Errorf("error %q should mention 'keep entity not found'", err.Error())
	}
}

func TestMergeEntities_DropNotFound(t *testing.T) {
	cx := openTestDB(t)
	defer cx.Close()
	ctx := context.Background()
	keep := &Entity{Type: "person", Name: "Keep"}
	if err := cx.PutEntity(ctx, keep); err != nil {
		t.Fatal(err)
	}
	_, err := cx.MergeEntities(ctx, keep.ID, "ent_doesnt_exist")
	if err == nil {
		t.Fatal("expected error for missing drop")
	}
	if !strings.Contains(err.Error(), "drop entity not found") {
		t.Errorf("error %q should mention 'drop entity not found'", err.Error())
	}
}

func TestMergeEntities_TypeMismatch(t *testing.T) {
	cx := openTestDB(t)
	defer cx.Close()
	ctx := context.Background()
	person := &Entity{Type: "person", Name: "Stripe"}
	org := &Entity{Type: "organization", Name: "Stripe Inc"}
	if err := cx.PutEntity(ctx, person); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutEntity(ctx, org); err != nil {
		t.Fatal(err)
	}
	_, err := cx.MergeEntities(ctx, person.ID, org.ID)
	if err == nil {
		t.Fatal("expected error for type mismatch")
	}
	if !strings.Contains(err.Error(), "cannot merge across types") {
		t.Errorf("error %q should mention 'cannot merge across types'", err.Error())
	}
}

// --- Dedup ---

func TestMergeEntities_RelationshipDedup(t *testing.T) {
	cx := openTestDB(t)
	defer cx.Close()
	ctx := context.Background()
	keep := &Entity{Type: "person", Name: "K"}
	drop := &Entity{Type: "person", Name: "D"}
	stripe := &Entity{Type: "organization", Name: "Stripe"}
	for _, e := range []*Entity{keep, drop, stripe} {
		if err := cx.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	// Both keep and drop work at stripe.
	for _, e := range []*Entity{keep, drop} {
		if err := cx.PutRelationship(ctx, &Relationship{SourceID: e.ID, TargetID: stripe.ID, Type: "works_at"}); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := cx.MergeEntities(ctx, keep.ID, drop.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.DupesDropped == 0 {
		t.Error("expected duplicate relationship to be dropped")
	}
	// keep should now have exactly one works_at stripe relationship.
	rels, _ := cx.GetRelationships(ctx, keep.ID, RelTypeFilter("works_at"))
	count := 0
	for _, r := range rels {
		if r.TargetID == stripe.ID {
			count++
		}
	}
	if count != 1 {
		t.Errorf("keep has %d works_at stripe relationships, want 1", count)
	}
}

func TestMergeEntities_MemoryEntitiesDedup(t *testing.T) {
	cx := openTestDB(t)
	defer cx.Close()
	ctx := context.Background()
	keep := &Entity{Type: "person", Name: "K"}
	drop := &Entity{Type: "person", Name: "D"}
	for _, e := range []*Entity{keep, drop} {
		if err := cx.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	// One memory linked to both.
	mem := &Memory{Content: "shared memory", EntityIDs: []string{keep.ID, drop.ID}}
	if err := cx.PutMemory(ctx, mem); err != nil {
		t.Fatal(err)
	}
	stats, err := cx.MergeEntities(ctx, keep.ID, drop.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.DupesDropped == 0 {
		t.Error("expected duplicate memory link to be dropped")
	}
	// keep should still have the memory linked exactly once.
	mems, err := cx.GetMemoriesByEntity(ctx, keep.ID)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, m := range mems {
		if m.Content == "shared memory" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected 1 link to shared memory, got %d", count)
	}
}

// --- Self-loop cleanup ---

func TestMergeEntities_SelfLoopRemoved(t *testing.T) {
	cx := openTestDB(t)
	defer cx.Close()
	ctx := context.Background()
	keep := &Entity{Type: "person", Name: "K"}
	drop := &Entity{Type: "person", Name: "D"}
	for _, e := range []*Entity{keep, drop} {
		if err := cx.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	// drop has an edge pointing AT keep.
	if err := cx.PutRelationship(ctx, &Relationship{SourceID: drop.ID, TargetID: keep.ID, Type: "knows"}); err != nil {
		t.Fatal(err)
	}
	if _, err := cx.MergeEntities(ctx, keep.ID, drop.ID); err != nil {
		t.Fatal(err)
	}
	// keep should NOT have a self-loop now.
	rels, _ := cx.GetRelationships(ctx, keep.ID)
	for _, r := range rels {
		if r.SourceID == keep.ID && r.TargetID == keep.ID {
			t.Errorf("found self-loop on keep entity: %+v", r)
		}
	}
}

// --- Attributes ---

func TestMergeEntities_AttributeUnion_KeepWins(t *testing.T) {
	cx := openTestDB(t)
	defer cx.Close()
	ctx := context.Background()
	keep := &Entity{
		Type:       "person",
		Name:       "K",
		Attributes: map[string]any{"role": "engineer"},
	}
	drop := &Entity{
		Type:       "person",
		Name:       "D",
		Attributes: map[string]any{"role": "developer", "team": "payments"},
	}
	for _, e := range []*Entity{keep, drop} {
		if err := cx.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := cx.MergeEntities(ctx, keep.ID, drop.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.AttrConflicts != 1 {
		t.Errorf("AttrConflicts = %d, want 1", stats.AttrConflicts)
	}
	got, err := cx.GetEntity(ctx, keep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Attributes["role"] != "engineer" {
		t.Errorf("role = %v, want engineer (keep wins)", got.Attributes["role"])
	}
	if got.Attributes["team"] != "payments" {
		t.Errorf("team = %v, want payments (unioned in)", got.Attributes["team"])
	}
	// merged_from is now present.
	mf, ok := got.Attributes["merged_from"].([]any)
	if !ok {
		t.Fatalf("merged_from missing or wrong type: %T %v", got.Attributes["merged_from"], got.Attributes["merged_from"])
	}
	if len(mf) != 1 {
		t.Errorf("merged_from len = %d, want 1", len(mf))
	}
}

func TestMergeEntities_MergedFromAppends(t *testing.T) {
	cx := openTestDB(t)
	defer cx.Close()
	ctx := context.Background()
	keep := &Entity{Type: "person", Name: "K"}
	drop1 := &Entity{Type: "person", Name: "D1"}
	drop2 := &Entity{Type: "person", Name: "D2"}
	for _, e := range []*Entity{keep, drop1, drop2} {
		if err := cx.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := cx.MergeEntities(ctx, keep.ID, drop1.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := cx.MergeEntities(ctx, keep.ID, drop2.ID); err != nil {
		t.Fatal(err)
	}
	got, err := cx.GetEntity(ctx, keep.ID)
	if err != nil {
		t.Fatal(err)
	}
	mf, ok := got.Attributes["merged_from"].([]any)
	if !ok {
		t.Fatalf("merged_from missing: %v", got.Attributes)
	}
	if len(mf) != 2 {
		t.Errorf("merged_from len = %d, want 2", len(mf))
	}
}

// --- Embeddings ---
// We can verify embedding cleanup by direct SQL query — there's no public
// API for "does this entity have an embedding" so we use the db handle.
// (Helper at bottom of this test file.)

func TestMergeEntities_StaleEmbeddingDropped(t *testing.T) {
	cx := openTestDB(t)
	defer cx.Close()
	ctx := context.Background()
	keep := &Entity{Type: "person", Name: "K"}
	drop := &Entity{Type: "person", Name: "D"}
	for _, e := range []*Entity{keep, drop} {
		if err := cx.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	// Seed a fake embedding row for drop.
	if _, err := cx.db.ExecContext(ctx,
		`INSERT INTO embeddings (id, ref_id, ref_type, vector) VALUES (?, ?, 'entity', ?)`,
		"emb_drop", drop.ID, []byte{0x00}); err != nil {
		t.Fatal(err)
	}
	stats, err := cx.MergeEntities(ctx, keep.ID, drop.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Embeddings != 1 {
		t.Errorf("Embeddings = %d, want 1", stats.Embeddings)
	}
	// Verify it's actually gone.
	var count int
	if err := cx.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM embeddings WHERE ref_id = ?`, drop.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("embedding for drop entity still exists (count=%d)", count)
	}
}

// --- Idempotency / second-call behavior ---

func TestMergeEntities_SecondCallReturnsDropNotFound(t *testing.T) {
	cx := openTestDB(t)
	defer cx.Close()
	ctx := context.Background()
	keep := &Entity{Type: "person", Name: "K"}
	drop := &Entity{Type: "person", Name: "D"}
	for _, e := range []*Entity{keep, drop} {
		if err := cx.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := cx.MergeEntities(ctx, keep.ID, drop.ID); err != nil {
		t.Fatal(err)
	}
	// Second call: drop no longer exists.
	_, err := cx.MergeEntities(ctx, keep.ID, drop.ID)
	if err == nil {
		t.Fatal("expected error on second merge")
	}
	if !strings.Contains(err.Error(), "drop entity not found") {
		t.Errorf("error = %v, want 'drop entity not found'", err)
	}
}
```

### Step 3: Run tests to verify they fail

Run: `go test -run "TestMergeEntities" -v .`
Expected: compile error — `MergeEntities` undefined.

### Step 4: Implement `MergeEntities` in `entity.go`

Append to `entity.go`:

```go
// MergeEntities merges the drop entity into the keep entity, atomically.
// All references to dropID (relationships, memory_entities, chunks,
// embeddings) are re-targeted to keepID; duplicates that would arise
// from the re-target are collapsed; the drop entity's attributes are
// unioned into the keep entity (keep wins on conflicts); a `merged_from`
// provenance record is appended to keep's attributes; finally the drop
// entity row is deleted. On any error, all changes are rolled back.
func (c *Cortex) MergeEntities(ctx context.Context, keepID, dropID string) (MergeStats, error) {
	var stats MergeStats

	if keepID == dropID {
		return stats, fmt.Errorf("cortex: cannot merge an entity into itself")
	}

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return stats, fmt.Errorf("cortex: begin tx: %w", err)
	}
	defer tx.Rollback() // no-op after Commit

	keep, err := getEntityTx(ctx, tx, keepID)
	if err != nil {
		return stats, fmt.Errorf("cortex: keep entity not found: %s", keepID)
	}
	drop, err := getEntityTx(ctx, tx, dropID)
	if err != nil {
		return stats, fmt.Errorf("cortex: drop entity not found: %s", dropID)
	}
	if keep.Type != drop.Type {
		return stats, fmt.Errorf("cortex: cannot merge across types: %s (keep) vs %s (drop)", keep.Type, drop.Type)
	}

	// Step 2: Re-target chunks (no collision possible).
	res, err := tx.ExecContext(ctx,
		`UPDATE chunks SET entity_id = ? WHERE entity_id = ?`, keepID, dropID)
	if err != nil {
		return stats, fmt.Errorf("cortex: re-target chunks: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.Chunks = int(n)
	}

	// Step 3: Re-target memory_entities, with dedup.
	res, err = tx.ExecContext(ctx,
		`DELETE FROM memory_entities
		 WHERE entity_id = ?
		   AND memory_id IN (SELECT memory_id FROM memory_entities WHERE entity_id = ?)`,
		dropID, keepID)
	if err != nil {
		return stats, fmt.Errorf("cortex: dedup memory_entities: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.DupesDropped += int(n)
	}
	res, err = tx.ExecContext(ctx,
		`UPDATE memory_entities SET entity_id = ? WHERE entity_id = ?`, keepID, dropID)
	if err != nil {
		return stats, fmt.Errorf("cortex: re-target memory_entities: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.Memories = int(n)
	}

	// Step 4: Re-target relationships, with dedup on (source, target, type).
	// First dedup source_id collisions, then re-target.
	res, err = tx.ExecContext(ctx,
		`DELETE FROM relationships
		 WHERE source_id = ?
		   AND EXISTS (
			 SELECT 1 FROM relationships k
			 WHERE k.source_id = ?
			   AND k.target_id = relationships.target_id
			   AND k.type      = relationships.type
		   )`,
		dropID, keepID)
	if err != nil {
		return stats, fmt.Errorf("cortex: dedup source rels: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.DupesDropped += int(n)
	}
	res, err = tx.ExecContext(ctx,
		`UPDATE relationships SET source_id = ? WHERE source_id = ?`, keepID, dropID)
	if err != nil {
		return stats, fmt.Errorf("cortex: re-target source rels: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.Relationships += int(n)
	}

	// Same for target_id.
	res, err = tx.ExecContext(ctx,
		`DELETE FROM relationships
		 WHERE target_id = ?
		   AND EXISTS (
			 SELECT 1 FROM relationships k
			 WHERE k.target_id = ?
			   AND k.source_id = relationships.source_id
			   AND k.type      = relationships.type
		   )`,
		dropID, keepID)
	if err != nil {
		return stats, fmt.Errorf("cortex: dedup target rels: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.DupesDropped += int(n)
	}
	res, err = tx.ExecContext(ctx,
		`UPDATE relationships SET target_id = ? WHERE target_id = ?`, keepID, dropID)
	if err != nil {
		return stats, fmt.Errorf("cortex: re-target target rels: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.Relationships += int(n)
	}

	// Drop self-loops created by the merge.
	res, err = tx.ExecContext(ctx,
		`DELETE FROM relationships WHERE source_id = ? AND target_id = ?`, keepID, keepID)
	if err != nil {
		return stats, fmt.Errorf("cortex: drop self-loops: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.DupesDropped += int(n)
	}

	// Step 5: Drop the stale entity embedding.
	res, err = tx.ExecContext(ctx,
		`DELETE FROM embeddings WHERE ref_id = ? AND ref_type = 'entity'`, dropID)
	if err != nil {
		return stats, fmt.Errorf("cortex: drop embedding: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		stats.Embeddings = int(n)
	}

	// Step 6: Union attributes (keep wins) + record provenance.
	if keep.Attributes == nil {
		keep.Attributes = map[string]any{}
	}
	for k, v := range drop.Attributes {
		if _, exists := keep.Attributes[k]; exists {
			stats.AttrConflicts++
			continue
		}
		keep.Attributes[k] = v
	}
	// Append the merge record.
	record := mergeRecord{
		ID:       drop.ID,
		Name:     drop.Name,
		Type:     drop.Type,
		Source:   drop.Source,
		Attrs:    drop.Attributes,
		MergedAt: time.Now().UTC(),
	}
	// merged_from might be missing (first merge) or present from a prior merge.
	// JSON deserialization gives us []any with map[string]any elements; we just
	// append and let it round-trip.
	var existing []any
	if raw, ok := keep.Attributes["merged_from"]; ok {
		if asArr, isArr := raw.([]any); isArr {
			existing = asArr
		}
	}
	existing = append(existing, record)
	keep.Attributes["merged_from"] = existing

	attrsJSON, err := json.Marshal(keep.Attributes)
	if err != nil {
		return stats, fmt.Errorf("cortex: marshal merged attributes: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE entities SET attributes = ?, updated_at = ? WHERE id = ?`,
		string(attrsJSON), time.Now().UTC(), keepID); err != nil {
		return stats, fmt.Errorf("cortex: update keep attributes: %w", err)
	}

	// Step 7: Delete drop entity.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM entities WHERE id = ?`, dropID); err != nil {
		return stats, fmt.Errorf("cortex: delete drop entity: %w", err)
	}

	// Step 8: Commit.
	if err := tx.Commit(); err != nil {
		return stats, fmt.Errorf("cortex: commit merge: %w", err)
	}
	return stats, nil
}

// getEntityTx loads an entity by ID using the supplied transaction.
// Returns sql.ErrNoRows if not found.
func getEntityTx(ctx context.Context, tx *sql.Tx, id string) (*Entity, error) {
	var e Entity
	var attrsJSON sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT id, type, name, attributes, source, confidence, created_at, updated_at
		 FROM entities WHERE id = ?`, id,
	).Scan(&e.ID, &e.Type, &e.Name, &attrsJSON, &e.Source, &e.Confidence, &e.CreatedAt, &e.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if attrsJSON.Valid && attrsJSON.String != "" {
		if err := json.Unmarshal([]byte(attrsJSON.String), &e.Attributes); err != nil {
			return nil, fmt.Errorf("cortex: unmarshal attributes: %w", err)
		}
	}
	return &e, nil
}
```

### Step 5: Run tests

Run: `go test -run TestMergeEntities -v -count=1 .`
Expected: all 11 merge tests PASS.

Run: `go test ./... -count=1`
Expected: full hermetic suite still passes. Pre-existing live-network `llm/openai` 429 failures are unrelated.

### Step 6: Commit

```bash
git add types.go entity.go entity_test.go
git commit -m "$(cat <<'EOF'
feat(cortex): MergeEntities — atomic merge of two entities

Re-targets relationships, memory_entities, chunks, and the entity-level
embedding from drop → keep, with dedup on (memory_id, entity_id) and
(source, target, type). Self-loops created by the merge are dropped.
Attributes are unioned, keep wins on duplicate keys; AttrConflicts
stat tracks how many keys collided. A `merged_from` record (full
snapshot of drop's attrs) is appended to keep's attributes so multi-
step merges remain recoverable.

Validation: self-merge, missing keep, missing drop, type mismatch all
return typed errors before any mutation.

Whole operation runs in one SQLite transaction. On any error, ROLLBACK.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: CLI command + dry-run

**Files:**
- Modify: `cmd/cortex/main.go`
- Modify: `cmd/cortex/main_test.go` (or create if missing — check first)

### Step 1: Write failing CLI flag-parsing test

Append to (or create) `cmd/cortex/main_test.go` (check if the file already exists; the prior `export_test.go` and `init_schema_test.go` are in the same package):

```go
package main

import (
	"testing"
)

func TestParseMergeArgs_Defaults(t *testing.T) {
	opts, err := parseMergeArgs([]string{"ent_keep", "ent_drop"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.KeepID != "ent_keep" {
		t.Errorf("KeepID = %q, want ent_keep", opts.KeepID)
	}
	if opts.DropID != "ent_drop" {
		t.Errorf("DropID = %q, want ent_drop", opts.DropID)
	}
	if opts.DryRun {
		t.Error("DryRun should default to false")
	}
}

func TestParseMergeArgs_DryRun(t *testing.T) {
	opts, err := parseMergeArgs([]string{"ent_keep", "ent_drop", "--dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.DryRun {
		t.Error("DryRun should be true")
	}
	// Order shouldn't matter.
	opts2, err := parseMergeArgs([]string{"--dry-run", "ent_keep", "ent_drop"})
	if err != nil {
		t.Fatal(err)
	}
	if !opts2.DryRun || opts2.KeepID != "ent_keep" || opts2.DropID != "ent_drop" {
		t.Errorf("flag-first parse wrong: %+v", opts2)
	}
}

func TestParseMergeArgs_MissingIDs(t *testing.T) {
	_, err := parseMergeArgs([]string{"ent_keep"})
	if err == nil {
		t.Error("expected error for single positional arg")
	}
	_, err = parseMergeArgs([]string{})
	if err == nil {
		t.Error("expected error for no args")
	}
}

func TestParseMergeArgs_UnknownFlag(t *testing.T) {
	_, err := parseMergeArgs([]string{"ent_keep", "ent_drop", "--unknown"})
	if err == nil {
		t.Error("expected error for unknown flag")
	}
}
```

### Step 2: Run tests to verify they fail

Run: `go test -run TestParseMergeArgs -v ./cmd/cortex/...`
Expected: build error — `parseMergeArgs` undefined.

### Step 3: Implement `cmdMerge` in `cmd/cortex/main.go`

Add the following near the other command functions (e.g. next to `cmdForget`):

```go
type mergeOptions struct {
	KeepID string
	DropID string
	DryRun bool
}

func parseMergeArgs(args []string) (mergeOptions, error) {
	var opts mergeOptions
	var positional []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--dry-run":
			opts.DryRun = true
		default:
			if strings.HasPrefix(args[i], "--") {
				return opts, fmt.Errorf("unknown flag: %s", args[i])
			}
			positional = append(positional, args[i])
		}
	}
	if len(positional) != 2 {
		return opts, fmt.Errorf("merge requires <keep-id> <drop-id>")
	}
	opts.KeepID = positional[0]
	opts.DropID = positional[1]
	return opts, nil
}

func cmdMerge() {
	opts, err := parseMergeArgs(os.Args[2:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		fmt.Fprintln(os.Stderr, "usage: cortex merge <keep-id> <drop-id> [--dry-run]")
		os.Exit(1)
	}

	cx := openCortex()
	defer cx.Close()
	ctx := context.Background()

	// Look up names for display (best-effort).
	keepName := opts.KeepID
	keepType := ""
	if e, err := cx.GetEntity(ctx, opts.KeepID); err == nil {
		keepName = e.Name
		keepType = e.Type
	}

	verb := "Merging"
	if opts.DryRun {
		verb = "Dry-run: would merge"
	}
	if keepType != "" {
		fmt.Printf("%s %s → %s (%s, %s)\n", verb, opts.DropID, opts.KeepID, keepName, keepType)
	} else {
		fmt.Printf("%s %s → %s\n", verb, opts.DropID, opts.KeepID)
	}

	var stats cortex.MergeStats
	if opts.DryRun {
		stats, err = runMergeDryRun(ctx, cx, opts.KeepID, opts.DropID)
	} else {
		stats, err = cx.MergeEntities(ctx, opts.KeepID, opts.DropID)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	printMergeStats(stats, opts.DryRun)

	if opts.DryRun {
		fmt.Println("No changes written.")
	} else {
		fmt.Printf("Merge complete. %s deleted.\n", opts.DropID)
	}
}

// runMergeDryRun runs MergeEntities inside a manually-managed transaction
// that is always rolled back. Returns the same MergeStats a real merge
// would have produced.
//
// Implementation note: We can't simply call MergeEntities and "undo" it,
// because MergeEntities owns its own transaction. We expose a sibling
// function in the cortex package that performs the merge against a
// caller-supplied tx — see cortex.MergeEntitiesTx.
func runMergeDryRun(ctx context.Context, cx *cortex.Cortex, keepID, dropID string) (cortex.MergeStats, error) {
	return cx.MergeEntitiesDryRun(ctx, keepID, dropID)
}

func printMergeStats(s cortex.MergeStats, dryRun bool) {
	verb := func(action string) string {
		if dryRun {
			return action + " would be"
		}
		return action
	}
	fmt.Printf("  %d relationships %s re-targeted", s.Relationships, verb(""))
	if s.DupesDropped > 0 {
		fmt.Printf(" (%d duplicates dropped)", s.DupesDropped)
	}
	fmt.Println()
	fmt.Printf("  %d memory links %s re-targeted\n", s.Memories, verb(""))
	fmt.Printf("  %d chunks %s re-targeted\n", s.Chunks, verb(""))
	fmt.Printf("  %d stale embedding%s %s removed\n", s.Embeddings, plural(s.Embeddings), verb(""))
	fmt.Printf("  %d attribute conflict%s (keep value preserved)\n", s.AttrConflicts, plural(s.AttrConflicts))
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
```

The `runMergeDryRun` wrapper delegates to a new `MergeEntitiesDryRun` method that we need to add to the cortex package. Add it in `entity.go` (near `MergeEntities`):

```go
// MergeEntitiesDryRun runs the merge algorithm but always ROLLBACKs the
// transaction, returning the same MergeStats a real run would produce
// without writing any changes. Used by `cortex merge --dry-run`.
//
// Implementation: extract the merge body into a helper that takes a
// *sql.Tx, then call it from both MergeEntities (which COMMITs) and
// this function (which ROLLBACKs).
func (c *Cortex) MergeEntitiesDryRun(ctx context.Context, keepID, dropID string) (MergeStats, error) {
	var stats MergeStats
	if keepID == dropID {
		return stats, fmt.Errorf("cortex: cannot merge an entity into itself")
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return stats, fmt.Errorf("cortex: begin tx: %w", err)
	}
	defer tx.Rollback()
	return mergeEntitiesTx(ctx, tx, keepID, dropID)
}
```

And refactor `MergeEntities` to delegate to the same helper:

```go
func (c *Cortex) MergeEntities(ctx context.Context, keepID, dropID string) (MergeStats, error) {
	var stats MergeStats
	if keepID == dropID {
		return stats, fmt.Errorf("cortex: cannot merge an entity into itself")
	}
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return stats, fmt.Errorf("cortex: begin tx: %w", err)
	}
	defer tx.Rollback()
	stats, err = mergeEntitiesTx(ctx, tx, keepID, dropID)
	if err != nil {
		return stats, err
	}
	if err := tx.Commit(); err != nil {
		return stats, fmt.Errorf("cortex: commit merge: %w", err)
	}
	return stats, nil
}

// mergeEntitiesTx performs the merge against the provided transaction.
// Caller is responsible for Commit or Rollback. The function returns
// MergeStats reflecting what was done.
func mergeEntitiesTx(ctx context.Context, tx *sql.Tx, keepID, dropID string) (MergeStats, error) {
	// ... body of original MergeEntities (without the BeginTx / Commit) ...
}
```

**For Task 2, refactor `MergeEntities` to extract `mergeEntitiesTx`**, then add `MergeEntitiesDryRun` next to it. The body (from "Step 2: Re-target chunks" through "Step 7: Delete drop entity" — but NOT the BeginTx/Commit wrapper) becomes the body of `mergeEntitiesTx`. Replace every `tx.ExecContext` etc. with the same — they already use `tx` from the outer scope, so the move is mechanical.

Existing TestMergeEntities tests continue to pass because `MergeEntities` still works end-to-end.

### Step 4: Wire `cmdMerge` into the command switch

In `cmd/cortex/main.go`, find the existing switch statement (~line 49 region, alongside `init`, `remember`, `recall`, `sync`, `entity`, `forget`, `config`, `export`, `init-schema`) and add:

```go
	case "merge":
		cmdMerge()
```

Place before `default:`.

### Step 5: Update `printUsage`

Add a line under the existing commands:

```
  merge <keep-id> <drop-id> [--dry-run]
                                 Merge drop entity into keep entity; re-target all references
```

Match the formatting and indentation of nearby usage entries.

### Step 6: Write and run an integration test for dry-run

Append to `entity_test.go`:

```go
func TestMergeEntitiesDryRun_NoChanges(t *testing.T) {
	cx := openTestDB(t)
	defer cx.Close()
	ctx := context.Background()
	keep := &Entity{Type: "person", Name: "K"}
	drop := &Entity{Type: "person", Name: "D"}
	for _, e := range []*Entity{keep, drop} {
		if err := cx.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := cx.PutRelationship(ctx, &Relationship{SourceID: drop.ID, TargetID: keep.ID, Type: "knows"}); err != nil {
		t.Fatal(err)
	}
	stats, err := cx.MergeEntitiesDryRun(ctx, keep.ID, drop.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Relationships == 0 {
		t.Error("dry-run should report what would change")
	}
	// drop entity is STILL present.
	if _, err := cx.GetEntity(ctx, drop.ID); err != nil {
		t.Errorf("drop entity should still exist after dry-run: %v", err)
	}
}
```

### Step 7: Run tests + build

Run: `go test -run "TestMergeEntities|TestParseMergeArgs" -v -count=1 ./...`
Expected: all merge tests + CLI parse tests PASS.

Run: `go build ./...`
Expected: clean build.

Smoke test (optional):

```bash
go build -o /tmp/cortex-test ./cmd/cortex
echo -e "Test\n\n" | /tmp/cortex-test --db /tmp/merge-smoke.db init
/tmp/cortex-test --db /tmp/merge-smoke.db remember "Alice Chen joined Stripe"
# Find the entity ID:
/tmp/cortex-test --db /tmp/merge-smoke.db entity list
# Use two of the IDs for a dry-run:
/tmp/cortex-test --db /tmp/merge-smoke.db merge <some-id> <another-id> --dry-run
rm /tmp/merge-smoke.db /tmp/cortex-test
```

### Step 8: Commit

```bash
git add entity.go entity_test.go cmd/cortex/main.go cmd/cortex/main_test.go
git commit -m "$(cat <<'EOF'
feat(cli): add cortex merge command with --dry-run

CLI takes two entity IDs; optional --dry-run runs the full algorithm
inside a transaction that ROLLBACKs at the end, so dry-run stats are
identical to a real run without writing anything.

Refactored MergeEntities into a transaction-agnostic
mergeEntitiesTx helper so MergeEntities (commits) and the new
MergeEntitiesDryRun (rolls back) share the same body.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Documentation

**Files:**
- Modify: `README.md`
- Modify: `docs/CORTEX.md`
- Modify: `cmd/cortex/CORTEX.md.template`

### Step 1: Add `### cortex merge` to README

Find the existing `### cortex export` / `### cortex init-schema` subsections in the CLI Reference section. Add a new subsection (place it near `cortex forget` since they're both destructive operations, or near `cortex entity` since they share the entity ID concept — match what's natural):

```markdown
### `cortex merge`

Merge a duplicate entity into the canonical one.

```bash
cortex merge <keep-id> <drop-id> [--dry-run]
```

Re-targets every reference to the drop entity onto the keep entity: relationships, memory links, chunks, and the entity's vector embedding. Duplicate relationships and memory links that would arise from the re-target are collapsed. Self-loops (an edge from drop pointing at keep, which would become keep→keep after merge) are removed. The drop entity is then deleted.

Attributes from the drop entity are unioned into the keep entity. **Keep wins** on duplicate keys — its existing attribute values are preserved. A `merged_from` record (containing the drop entity's name, type, source, and full attribute snapshot at merge time) is appended to the keep entity's attributes for provenance. Multiple merges into the same entity append to the same `merged_from` array.

The whole operation runs in a single SQLite transaction. On any error, all changes are rolled back. `--dry-run` runs the same algorithm and reports identical stats, but always rolls back — useful for previewing a merge before committing to it.

```bash
$ cortex merge ent_01HKEEP ent_01HDROP --dry-run
Dry-run: would merge ent_01HDROP → ent_01HKEEP (Alice Chen, person)
  4 relationships would be re-targeted (1 duplicate dropped)
  ...
No changes written.
```

Errors out (exit 1) if either entity is missing, the IDs are identical, or the entities have different types (e.g. trying to merge a `person` into an `organization`).
```

### Step 2: Add a paragraph to `docs/CORTEX.md`

Find a sensible spot in the "Workflow loop" or "What NOT to remember" area (use your judgment based on the existing flow):

```markdown
### Cleaning up duplicates

Over time you'll notice the same real-world entity stored as two separate cortex entities (different spellings, different sources). When you spot one, suggest a merge:

```
cortex merge <keep-id> <drop-id> --dry-run   # preview
cortex merge <keep-id> <drop-id>             # commit
```

Always preview with `--dry-run` first. Merge re-targets every reference (relationships, memories, chunks) and deletes the drop entity; the operation is atomic.
```

### Step 3: Mirror to the embedded template

```bash
head -5 cmd/cortex/CORTEX.md.template > /tmp/header.md
cat /tmp/header.md > cmd/cortex/CORTEX.md.template
echo "" >> cmd/cortex/CORTEX.md.template
cat docs/CORTEX.md >> cmd/cortex/CORTEX.md.template
rm /tmp/header.md
```

(Verify the head count by reading the first lines of the template first — adjust if the header isn't exactly 5 lines.)

### Step 4: Verify

Run: `go test ./...`
Expected: hermetic tests still pass (this is docs-only, but cmd/cortex embeds the template so a test pass guards against template-embed regressions).

Visual check:

```bash
head -100 docs/CORTEX.md  # new merge section visible?
head -100 cmd/cortex/CORTEX.md.template  # same content after the header?
```

### Step 5: Commit

```bash
git add README.md docs/CORTEX.md cmd/cortex/CORTEX.md.template
git commit -m "$(cat <<'EOF'
docs: document cortex merge command

README gains a `### cortex merge` subsection in CLI Reference.
CORTEX.md template gains a short paragraph on cleaning up duplicates
with merge + --dry-run.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Done

After Task 3, callers can:

```go
stats, err := cx.MergeEntities(ctx, keepID, dropID)
// or preview:
stats, err := cx.MergeEntitiesDryRun(ctx, keepID, dropID)
```

And via CLI:

```bash
cortex merge ent_01HKEEP ent_01HDROP --dry-run
cortex merge ent_01HKEEP ent_01HDROP
cortex entity get ent_01HKEEP  # shows merged_from attribute
```
