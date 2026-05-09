// Package cortex_test exercises the Cortex API using the fictional Acme Corp
// dataset. These tests serve as integration-style examples showing realistic
// query patterns against a fully seeded knowledge graph.
package cortex_test

import (
	"context"
	"testing"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/testutil"
)

// openDB returns a Cortex instance backed by a temp file with mock LLM,
// embedder, and extractor wired up. Closed automatically when the test ends.
func openDB(t *testing.T) *cortex.Cortex {
	t.Helper()
	return testutil.OpenTestDB(t)
}

// mustSeed seeds the team dataset and fails the test immediately on error.
func mustSeed(t *testing.T, cx *cortex.Cortex) *testutil.Team {
	t.Helper()
	team, err := testutil.SeedTeam(context.Background(), cx)
	if err != nil {
		t.Fatalf("SeedTeam: %v", err)
	}
	return team
}

// TestTeamDataset_EntityCounts verifies total entity counts by type after seeding.
func TestTeamDataset_EntityCounts(t *testing.T) {
	cx := openDB(t)
	mustSeed(t, cx)
	ctx := context.Background()

	people, err := cx.FindEntities(ctx, cortex.EntityFilter{Type: "person"})
	if err != nil {
		t.Fatalf("FindEntities(person): %v", err)
	}
	if len(people) != 5 {
		t.Errorf("expected 5 people, got %d", len(people))
	}

	orgs, err := cx.FindEntities(ctx, cortex.EntityFilter{Type: "organization"})
	if err != nil {
		t.Fatalf("FindEntities(organization): %v", err)
	}
	if len(orgs) != 2 {
		t.Errorf("expected 2 organizations (Acme Corp, WidgetCo), got %d", len(orgs))
	}
}

// TestTeamDataset_RelationshipsFromAlice verifies Alice's total relationship count.
func TestTeamDataset_RelationshipsFromAlice(t *testing.T) {
	cx := openDB(t)
	team := mustSeed(t, cx)
	ctx := context.Background()

	rels, err := cx.GetRelationships(ctx, team.Alice.ID)
	if err != nil {
		t.Fatalf("GetRelationships(Alice): %v", err)
	}
	// Alice: works_at Acme, knows Eve, worked_at WidgetCo, Bob reports_to Alice (4 total).
	if len(rels) != 4 {
		t.Errorf("expected 4 relationships for Alice, got %d", len(rels))
		for _, r := range rels {
			t.Logf("  %s → %s (%s)", r.SourceID, r.TargetID, r.Type)
		}
	}
}

// TestTeamDataset_WorksAtFilter verifies filtering relationships by type.
func TestTeamDataset_WorksAtFilter(t *testing.T) {
	cx := openDB(t)
	team := mustSeed(t, cx)
	ctx := context.Background()

	worksAt, err := cx.GetRelationships(ctx, team.Acme.ID, cortex.RelTypeFilter("works_at"))
	if err != nil {
		t.Fatalf("GetRelationships(Acme, works_at): %v", err)
	}
	// Alice, Bob, Carol, Dave all have works_at → Acme.
	if len(worksAt) != 4 {
		t.Errorf("expected 4 works_at relationships for Acme Corp, got %d", len(worksAt))
	}
}

// TestTeamDataset_TraverseAliceDepth1 verifies that depth-1 traversal from Alice
// reaches all direct connections: Acme Corp, Eve, WidgetCo, and Bob (via reports_to).
func TestTeamDataset_TraverseAliceDepth1(t *testing.T) {
	cx := openDB(t)
	team := mustSeed(t, cx)
	ctx := context.Background()

	graph, err := cx.Traverse(ctx, team.Alice.ID, cortex.WithDepth(1))
	if err != nil {
		t.Fatalf("Traverse(Alice, depth=1): %v", err)
	}
	// Alice + Acme + Eve + WidgetCo + Bob = 5 entities.
	if len(graph.Entities) != 5 {
		t.Errorf("depth=1: expected 5 entities, got %d", len(graph.Entities))
		for _, e := range graph.Entities {
			t.Logf("  %s (%s)", e.Name, e.Type)
		}
	}
}

// TestTeamDataset_TraverseAcmeDepth2 verifies that a depth-2 traversal from Acme
// reaches the full dataset (all 7 entities) via Alice's secondary connections.
func TestTeamDataset_TraverseAcmeDepth2(t *testing.T) {
	cx := openDB(t)
	team := mustSeed(t, cx)
	ctx := context.Background()

	graph, err := cx.Traverse(ctx, team.Acme.ID, cortex.WithDepth(2))
	if err != nil {
		t.Fatalf("Traverse(Acme, depth=2): %v", err)
	}

	// At depth 2, Acme's employees (depth 1) bring in their connections:
	// Alice → Eve and Alice → WidgetCo, making all 7 entities reachable.
	names := make(map[string]bool)
	for _, e := range graph.Entities {
		names[e.Name] = true
	}
	for _, want := range []string{"Alice Chen", "Bob Kim", "Carol Reyes", "Dave Patel", "Eve Morgan", "WidgetCo"} {
		if !names[want] {
			t.Errorf("expected %q in depth-2 traversal from Acme, not found", want)
		}
	}
}

// TestTeamDataset_TraverseWorksAtOnly verifies that edge-type filtering restricts
// traversal to works_at edges, excluding knows, worked_at, and reports_to.
func TestTeamDataset_TraverseWorksAtOnly(t *testing.T) {
	cx := openDB(t)
	team := mustSeed(t, cx)
	ctx := context.Background()

	graph, err := cx.Traverse(ctx, team.Alice.ID, cortex.WithDepth(1), cortex.WithEdgeTypes("works_at"))
	if err != nil {
		t.Fatalf("Traverse(Alice, works_at only): %v", err)
	}
	// Only Alice + Acme Corp — knows Eve, worked_at WidgetCo, reports_to are excluded.
	if len(graph.Entities) != 2 {
		t.Errorf("expected 2 entities with works_at filter, got %d", len(graph.Entities))
		for _, e := range graph.Entities {
			t.Logf("  %s", e.Name)
		}
	}
}

// TestTeamDataset_KeywordSearchPayments verifies the project-update chunk is
// returned when searching for "payments".
func TestTeamDataset_KeywordSearchPayments(t *testing.T) {
	cx := openDB(t)
	mustSeed(t, cx)
	ctx := context.Background()

	results, err := cx.SearchKeyword(ctx, "payments", 10)
	if err != nil {
		t.Fatalf("SearchKeyword(payments): %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least one chunk matching 'payments', got 0")
	}

	found := false
	for _, ch := range results {
		if ch.Metadata["type"] == "project-update" {
			found = true
		}
	}
	if !found {
		t.Error("expected project-update chunk in 'payments' search results")
	}
}

// TestTeamDataset_KeywordSearchByName verifies chunks mentioning a person's name
// are all returned.
func TestTeamDataset_KeywordSearchByName(t *testing.T) {
	cx := openDB(t)
	mustSeed(t, cx)
	ctx := context.Background()

	// "Dave" appears in ChunkProjectStatus and ChunkMeetingNotes.
	results, err := cx.SearchKeyword(ctx, "Dave", 10)
	if err != nil {
		t.Fatalf("SearchKeyword(Dave): %v", err)
	}
	if len(results) < 2 {
		t.Errorf("expected Dave to appear in at least 2 chunks, got %d", len(results))
	}
}

// TestTeamDataset_MemorySearchRoadmap verifies the roadmap memory is returned.
func TestTeamDataset_MemorySearchRoadmap(t *testing.T) {
	cx := openDB(t)
	mustSeed(t, cx)
	ctx := context.Background()

	mems, err := cx.SearchMemories(ctx, "roadmap", 5)
	if err != nil {
		t.Fatalf("SearchMemories(roadmap): %v", err)
	}
	if len(mems) != 1 {
		t.Fatalf("expected 1 memory matching 'roadmap', got %d", len(mems))
	}
	want := "Dave Patel is coordinating the Q3 product roadmap and aligning it with engineering capacity."
	if mems[0].Content != want {
		t.Errorf("unexpected memory content:\n  got:  %q\n  want: %q", mems[0].Content, want)
	}
}

// TestTeamDataset_MemoriesByEntity verifies per-entity memory linkage.
func TestTeamDataset_MemoriesByEntity(t *testing.T) {
	cx := openDB(t)
	team := mustSeed(t, cx)
	ctx := context.Background()

	cases := []struct {
		name     string
		entityID string
		want     int
	}{
		{"Alice", team.Alice.ID, 1}, // MemAliceJoined
		{"Bob", team.Bob.ID, 1},     // MemPayments
		{"Carol", team.Carol.ID, 1}, // MemPayments (shared)
		{"Dave", team.Dave.ID, 1},   // MemRoadmap
		{"Eve", team.Eve.ID, 1},     // MemOnboarding
	}
	for _, tc := range cases {
		mems, err := cx.GetMemoriesByEntity(ctx, tc.entityID)
		if err != nil {
			t.Fatalf("GetMemoriesByEntity(%s): %v", tc.name, err)
		}
		if len(mems) != tc.want {
			t.Errorf("%s: expected %d memories, got %d", tc.name, tc.want, len(mems))
		}
	}
}

// TestTeamDataset_VectorSearch seeds content via Remember (which embeds
// automatically), then verifies vector search returns relevant chunks.
func TestTeamDataset_VectorSearch(t *testing.T) {
	cx := openDB(t) // MockEmbedder wired by OpenTestDB
	ctx := context.Background()

	// Use empty extractor — we only want chunks + embeddings, not entity extraction.
	cx.SetExtractor(&testutil.MockExtractor{})

	contents := []string{
		"Alice Chen leads Acme Corp engineering with 15 years of distributed systems experience.",
		"The payments feature integrates the Stripe API for processing transactions.",
		"Q3 planning meeting covered roadmap priorities and engineering capacity.",
	}
	for _, content := range contents {
		if err := cx.Remember(ctx, content); err != nil {
			t.Fatalf("Remember: %v", err)
		}
	}

	results, err := cx.SearchVector(ctx, "engineering leadership distributed systems", 3)
	if err != nil {
		t.Fatalf("SearchVector: %v", err)
	}
	if len(results) == 0 {
		t.Error("expected vector search results, got 0")
	}
}

// TestTeamDataset_ForgetBySource removes all "team-seed" data and verifies
// entities and orphaned memories are cleaned up.
func TestTeamDataset_ForgetBySource(t *testing.T) {
	cx := openDB(t)
	mustSeed(t, cx)
	ctx := context.Background()

	before, err := cx.FindEntities(ctx, cortex.EntityFilter{Source: "team-seed"})
	if err != nil {
		t.Fatalf("FindEntities before forget: %v", err)
	}
	if len(before) != 7 {
		t.Fatalf("expected 7 team-seed entities before forget, got %d", len(before))
	}

	if err := cx.Forget(ctx, cortex.Filter{Source: "team-seed"}); err != nil {
		t.Fatalf("Forget(source=team-seed): %v", err)
	}

	remaining, err := cx.FindEntities(ctx, cortex.EntityFilter{})
	if err != nil {
		t.Fatalf("FindEntities after forget: %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("expected 0 entities after Forget, got %d", len(remaining))
	}

	// All four memories were linked only to team-seed entities; after forgetting
	// those entities, memories become orphaned and are removed.
	mems, err := cx.SearchMemories(ctx, "Alice Bob Carol Dave Eve", 20)
	if err != nil {
		t.Fatalf("SearchMemories after forget: %v", err)
	}
	if len(mems) != 0 {
		t.Errorf("expected 0 memories after Forget, got %d", len(mems))
	}
}
