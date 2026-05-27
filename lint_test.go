package cortex

import (
	"context"
	"strings"
	"testing"
)

// --- Test 1: Empty graph ---

func TestLint_EmptyGraph(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	r, err := cx.Lint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r.EntityCount != 0 || r.RelationshipCount != 0 || r.MemoryCount != 0 {
		t.Errorf("expected zero counts, got %+v", r)
	}
	if len(r.Orphans) != 0 || len(r.EntitiesNoMemories) != 0 ||
		len(r.NearDuplicates) != 0 || len(r.DeadSources) != 0 ||
		len(r.UnlinkedMemories) != 0 || len(r.LowConfidenceMemories) != 0 {
		t.Errorf("expected no findings on empty graph: %+v", r)
	}
}

// --- Test 2: Healthy graph has no findings ---

func TestLint_HealthyGraph(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	alice := &Entity{Type: "person", Name: "Alice Healthy"}
	stripe := &Entity{Type: "organization", Name: "Stripe Healthy"}
	if err := cx.PutEntity(ctx, alice); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutEntity(ctx, stripe); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutRelationship(ctx, &Relationship{
		SourceID: alice.ID, TargetID: stripe.ID, Type: "works_at",
	}); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutMemory(ctx, &Memory{
		Content:   "alice works at stripe",
		EntityIDs: []string{alice.ID, stripe.ID},
		Source:    "notes/healthy.md",
	}); err != nil {
		t.Fatal(err)
	}

	r, err := cx.Lint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Orphans) != 0 {
		t.Errorf("Orphans should be empty, got %v", r.Orphans)
	}
	if len(r.EntitiesNoMemories) != 0 {
		t.Errorf("EntitiesNoMemories should be empty, got %v", r.EntitiesNoMemories)
	}
	if len(r.NearDuplicates) != 0 {
		t.Errorf("NearDuplicates should be empty, got %v", r.NearDuplicates)
	}
	if len(r.UnlinkedMemories) != 0 {
		t.Errorf("UnlinkedMemories should be empty, got %v", r.UnlinkedMemories)
	}
}

// --- Test 3: Orphan detection ---

func TestLint_OrphanDetected(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	orphan := &Entity{Type: "concept", Name: "Floating"}
	connected := &Entity{Type: "person", Name: "Connected"}
	other := &Entity{Type: "organization", Name: "Other"}
	for _, e := range []*Entity{orphan, connected, other} {
		if err := cx.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := cx.PutRelationship(ctx, &Relationship{
		SourceID: connected.ID, TargetID: other.ID, Type: "knows",
	}); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutMemory(ctx, &Memory{
		Content: "connected does things", EntityIDs: []string{connected.ID},
	}); err != nil {
		t.Fatal(err)
	}

	r, err := cx.Lint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foundOrphan := false
	for _, o := range r.Orphans {
		if o.ID == orphan.ID {
			foundOrphan = true
		}
		if o.ID == connected.ID {
			t.Errorf("connected entity should NOT be orphan")
		}
	}
	if !foundOrphan {
		t.Errorf("orphan %s not in report: %+v", orphan.ID, r.Orphans)
	}
}

// --- Test 4: EntitiesNoMemories ---

func TestLint_EntityWithoutMemories(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	a := &Entity{Type: "person", Name: "HasRelsNoMems"}
	b := &Entity{Type: "person", Name: "Target"}
	for _, e := range []*Entity{a, b} {
		if err := cx.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	if err := cx.PutRelationship(ctx, &Relationship{
		SourceID: a.ID, TargetID: b.ID, Type: "knows",
	}); err != nil {
		t.Fatal(err)
	}
	// No memory links to either.

	r, err := cx.Lint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foundA := false
	for _, e := range r.EntitiesNoMemories {
		if e.ID == a.ID {
			foundA = true
		}
	}
	if !foundA {
		t.Errorf("entity with rels but no memories not surfaced: %+v", r.EntitiesNoMemories)
	}
	// And NOT in Orphans (it has a relationship).
	for _, o := range r.Orphans {
		if o.ID == a.ID {
			t.Errorf("entity with rels should not be in Orphans")
		}
	}
}

// --- Test 5: Near-duplicate names (case-insensitive, same type) ---

func TestLint_NearDuplicateNames(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	a := &Entity{Type: "person", Name: "Alice Chen"}
	b := &Entity{Type: "person", Name: "alice chen"}
	// Different-type but same name shouldn't pair.
	c := &Entity{Type: "organization", Name: "Alice Chen"}
	for _, e := range []*Entity{a, b, c} {
		if err := cx.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	r, err := cx.Lint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.NearDuplicates) != 1 {
		t.Fatalf("expected 1 near-duplicate pair, got %d: %+v", len(r.NearDuplicates), r.NearDuplicates)
	}
	pair := r.NearDuplicates[0]
	if pair.Type != "person" {
		t.Errorf("pair type = %q, want person", pair.Type)
	}
	// Verify both a and b are in the pair (in some order).
	ids := map[string]bool{pair.A.ID: true, pair.B.ID: true}
	if !ids[a.ID] || !ids[b.ID] {
		t.Errorf("pair should contain both alice entities: got %s and %s", pair.A.ID, pair.B.ID)
	}
}

// --- Test 6: Self-pair guard ---

func TestLint_NearDuplicateNoSelfOrReversePair(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	a := &Entity{Type: "concept", Name: "X"}
	b := &Entity{Type: "concept", Name: "x"}
	if err := cx.PutEntity(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutEntity(ctx, b); err != nil {
		t.Fatal(err)
	}

	r, err := cx.Lint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.NearDuplicates) != 1 {
		t.Errorf("expected exactly 1 pair, got %d", len(r.NearDuplicates))
	}
}

// --- Test 7: Dead source ---

func TestLint_DeadSource(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "Live", Source: "live.md"}
	if err := cx.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	// Direct INSERT to simulate orphan-source state. cx.db is the private
	// *sql.DB field on Cortex; tests in this package can access it.
	if _, err := cx.db.ExecContext(ctx,
		`INSERT INTO memories (id, content, source) VALUES (?, ?, ?)`,
		"mem_dead", "an orphan memory", "old-import.md"); err != nil {
		t.Fatal(err)
	}

	r, err := cx.Lint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foundDead := false
	for _, s := range r.DeadSources {
		if s == "old-import.md" {
			foundDead = true
		}
		if s == "live.md" {
			t.Errorf("live.md should NOT be in DeadSources")
		}
	}
	if !foundDead {
		t.Errorf("dead source not surfaced: %v", r.DeadSources)
	}
}

// --- Test 8: Unlinked memory ---

func TestLint_UnlinkedMemory(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	m := &Memory{Content: "floating memory", Source: "notes.md"}
	if err := cx.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}

	r, err := cx.Lint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, mr := range r.UnlinkedMemories {
		if mr.ID == m.ID {
			found = true
			if !strings.Contains(mr.Content, "floating memory") {
				t.Errorf("content = %q, want to contain 'floating memory'", mr.Content)
			}
		}
	}
	if !found {
		t.Errorf("unlinked memory not surfaced: %+v", r.UnlinkedMemories)
	}
}

// --- Test 9: Low-conf default off ---

func TestLint_LowConfidenceDefaultOff(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	m := &Memory{Content: "uncertain claim", Confidence: 0.2}
	if err := cx.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}

	r, err := cx.Lint(ctx) // no options
	if err != nil {
		t.Fatal(err)
	}
	if len(r.LowConfidenceMemories) != 0 {
		t.Errorf("LowConfidenceMemories should be empty by default: %+v", r.LowConfidenceMemories)
	}
}

// --- Test 10: Low-conf opt-in ---

func TestLint_LowConfidenceOptIn(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	low := &Memory{Content: "uncertain claim", Confidence: 0.2}
	high := &Memory{Content: "solid fact", Confidence: 0.9}
	for _, m := range []*Memory{low, high} {
		if err := cx.PutMemory(ctx, m); err != nil {
			t.Fatal(err)
		}
	}

	r, err := cx.Lint(ctx, WithLowConfidence())
	if err != nil {
		t.Fatal(err)
	}
	foundLow := false
	for _, mr := range r.LowConfidenceMemories {
		if mr.ID == low.ID {
			foundLow = true
		}
		if mr.ID == high.ID {
			t.Errorf("high-confidence memory should NOT surface")
		}
	}
	if !foundLow {
		t.Errorf("low-confidence memory not surfaced: %+v", r.LowConfidenceMemories)
	}
}

// --- Test 11: Custom threshold ---

func TestLint_LowConfidenceCustomThreshold(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	m := &Memory{Content: "mid-confidence", Confidence: 0.5}
	if err := cx.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}

	r, err := cx.Lint(ctx, WithLowConfidenceThreshold(0.6))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, mr := range r.LowConfidenceMemories {
		if mr.ID == m.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("memory at 0.5 should surface with threshold 0.6: %+v", r.LowConfidenceMemories)
	}

	r2, err := cx.Lint(ctx, WithLowConfidenceThreshold(0.4))
	if err != nil {
		t.Fatal(err)
	}
	for _, mr := range r2.LowConfidenceMemories {
		if mr.ID == m.ID {
			t.Errorf("memory at 0.5 should NOT surface with threshold 0.4")
		}
	}
}

// --- Test 12: Counts populated ---

func TestLint_CountsPopulated(t *testing.T) {
	cx := openTestDB(t)
	ctx := context.Background()

	a := &Entity{Type: "person", Name: "A"}
	b := &Entity{Type: "person", Name: "B"}
	if err := cx.PutEntity(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutEntity(ctx, b); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutRelationship(ctx, &Relationship{
		SourceID: a.ID, TargetID: b.ID, Type: "knows",
	}); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutMemory(ctx, &Memory{Content: "memcontent", EntityIDs: []string{a.ID}}); err != nil {
		t.Fatal(err)
	}

	r, err := cx.Lint(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if r.EntityCount != 2 {
		t.Errorf("EntityCount = %d, want 2", r.EntityCount)
	}
	if r.RelationshipCount != 1 {
		t.Errorf("RelationshipCount = %d, want 1", r.RelationshipCount)
	}
	if r.MemoryCount != 1 {
		t.Errorf("MemoryCount = %d, want 1", r.MemoryCount)
	}
}

// --- Test 13: Render markdown ---

func TestLint_RenderMarkdown(t *testing.T) {
	r := LintReport{
		EntityCount:       10,
		RelationshipCount: 5,
		MemoryCount:       7,
		Orphans: []EntityRef{
			{ID: "ent_01", Name: "Floating", Type: "concept"},
		},
		NearDuplicates: []DuplicatePair{
			{Type: "person", A: EntityRef{ID: "ent_02", Name: "Alice Chen", Type: "person"}, B: EntityRef{ID: "ent_03", Name: "alice chen", Type: "person"}},
		},
		UnlinkedMemories: []MemoryRef{
			{ID: "mem_01", Content: "loose memory", Source: "slack-export", Confidence: 1.0},
		},
	}
	got := renderLintMarkdown(r)

	if !strings.Contains(got, "Scanned 10 entities, 5 relationships, 7 memories.") {
		t.Errorf("missing header summary, got:\n%s", got)
	}
	if !strings.Contains(got, "## Orphan entities (1)") {
		t.Errorf("missing orphans heading, got:\n%s", got)
	}
	if !strings.Contains(got, "ent_01") {
		t.Errorf("missing orphan ID, got:\n%s", got)
	}
	if !strings.Contains(got, "## Near-duplicate entity names (1 pair") {
		t.Errorf("missing near-duplicates heading, got:\n%s", got)
	}
	if !strings.Contains(got, "ent_02") || !strings.Contains(got, "ent_03") {
		t.Errorf("missing duplicate IDs, got:\n%s", got)
	}
	if !strings.Contains(got, "## Memories with no entity links (1)") {
		t.Errorf("missing unlinked memories heading, got:\n%s", got)
	}
	if !strings.Contains(got, "loose memory") {
		t.Errorf("missing memory content, got:\n%s", got)
	}
	if strings.Contains(got, "## Dead sources") {
		t.Errorf("empty section should be omitted, got:\n%s", got)
	}
	if strings.Contains(got, "## Entities without memories") {
		t.Errorf("empty section should be omitted, got:\n%s", got)
	}
}

func TestLint_RenderMarkdown_HealthyGraph(t *testing.T) {
	r := LintReport{EntityCount: 100, RelationshipCount: 50, MemoryCount: 200}
	got := renderLintMarkdown(r)

	if !strings.Contains(got, "Scanned 100 entities") {
		t.Errorf("missing summary: %s", got)
	}
	if strings.Contains(got, "## Orphan") || strings.Contains(got, "## Near-duplicate") ||
		strings.Contains(got, "## Memories") || strings.Contains(got, "## Dead") {
		t.Errorf("healthy graph should have no finding sections, got:\n%s", got)
	}
}

func TestLint_RenderMarkdown_LowConfidenceSkippedNote(t *testing.T) {
	r := LintReport{EntityCount: 10, RelationshipCount: 5, MemoryCount: 7}
	got := renderLintMarkdown(r)
	if !strings.Contains(got, "Low-confidence memories (skipped") {
		t.Errorf("expected skipped note, got:\n%s", got)
	}
}

func TestLint_RenderMarkdown_LowConfidenceWithFindings(t *testing.T) {
	r := LintReport{
		EntityCount:       10,
		RelationshipCount: 5,
		MemoryCount:       7,
		LowConfidenceMemories: []MemoryRef{
			{ID: "mem_lc", Content: "shaky claim", Source: "slack", Confidence: 0.2},
		},
	}
	got := renderLintMarkdown(r)
	if !strings.Contains(got, "## Low-confidence memories (1)") {
		t.Errorf("missing low-confidence section, got:\n%s", got)
	}
	if !strings.Contains(got, "mem_lc") {
		t.Errorf("missing memory id, got:\n%s", got)
	}
	if strings.Contains(got, "skipped") {
		t.Errorf("should not show skipped note when section populated, got:\n%s", got)
	}
}
