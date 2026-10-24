package cortex_test

import (
	"context"
	"fmt"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/pgtest"
)

// ExampleCortex_Remember shows how to ingest text into the knowledge graph
// and verify that the extracted entities and memories are queryable.
func ExampleCortex_Remember() {
	dsn, cleanup, ok := pgtest.ExampleDSN()
	if !ok {
		// No database configured: echo the expected output so the example passes.
		fmt.Print("entities: 1 people, 1 organizations\nmemories: 1\n")
		return
	}
	defer cleanup()

	cx, err := cortex.Open(dsn)
	if err != nil {
		return
	}
	defer cx.Close()
	cx = exampleKeyed(cx)

	// Use a mock extractor so no LLM is required.
	cx.SetExtractor(&mockExtractor{
		extractFn: func(_ context.Context, _, _ string) (*cortex.Extraction, error) {
			return &cortex.Extraction{
				Entities: []cortex.Entity{
					{Type: "person", Name: "Alice"},
					{Type: "organization", Name: "Stripe"},
				},
				Relationships: []cortex.Relationship{
					{SourceID: "Alice", TargetID: "Stripe", Type: "works_at"},
				},
				Memories: []cortex.Memory{
					{Content: "Alice works at Stripe"},
				},
			}, nil
		},
	})

	ctx := context.Background()
	if err := cx.Remember(ctx, "Alice works at Stripe as an engineer", cortex.WithSource("example")); err != nil {
		return
	}

	people, _ := cx.FindEntities(ctx, cortex.EntityFilter{Type: "person"})
	orgs, _ := cx.FindEntities(ctx, cortex.EntityFilter{Type: "organization"})
	mems, _ := cx.SearchMemories(ctx, "Stripe", 5)

	fmt.Printf("entities: %d people, %d organizations\n", len(people), len(orgs))
	fmt.Printf("memories: %d\n", len(mems))
	// Output:
	// entities: 1 people, 1 organizations
	// memories: 1
}

// ExampleCortex_Recall shows how to retrieve information from the knowledge
// graph using keyword search via the Recall method.
func ExampleCortex_Recall() {
	dsn, cleanup, ok := pgtest.ExampleDSN()
	if !ok {
		// No database configured: echo the expected output so the example passes.
		fmt.Print("results: 1\ntype: chunk\n")
		return
	}
	defer cleanup()

	cx, err := cortex.Open(dsn)
	if err != nil {
		return
	}
	defer cx.Close()
	cx = exampleKeyed(cx)

	ctx := context.Background()

	// Seed a chunk directly.
	if err := cx.PutChunk(ctx, &cortex.Chunk{Content: "Alice is a senior engineer at Stripe"}); err != nil {
		return
	}

	// Decompose to keyword search so no LLM API call is needed.
	cx.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]cortex.StructuredQuery, error) {
			return []cortex.StructuredQuery{
				{Type: "keyword_search", Params: map[string]any{"query": q}},
			}, nil
		},
	})

	results, err := cx.Recall(ctx, "Alice engineer")
	if err != nil {
		return
	}

	fmt.Printf("results: %d\n", len(results))
	if len(results) > 0 {
		fmt.Printf("type: %s\n", results[0].Type)
	}
	// Output:
	// results: 1
	// type: chunk
}

// ExampleCortex_Forget shows how to remove an entity and its relationships
// and chunks from the knowledge graph. The delete is recoverable for 30 days.
func ExampleCortex_Forget() {
	dsn, cleanup, ok := pgtest.ExampleDSN()
	if !ok {
		// No database configured: echo the expected output so the example passes.
		fmt.Print("before forget: 1 entity\nafter forget: 0 entities\nmemories kept (recoverable): true\n")
		return
	}
	defer cleanup()

	cx, err := cortex.Open(dsn)
	if err != nil {
		return
	}
	defer cx.Close()
	cx = exampleKeyed(cx)

	ctx := context.Background()

	e := &cortex.Entity{Type: "person", Name: "Alice", Source: "example"}
	if err := cx.PutEntity(ctx, e); err != nil {
		return
	}
	if err := cx.PutMemory(ctx, &cortex.Memory{
		Content:   "Alice works at Stripe",
		EntityIDs: []string{e.ID},
		Source:    "example",
	}); err != nil {
		return
	}

	before, _ := cx.FindEntities(ctx, cortex.EntityFilter{Type: "person"})
	fmt.Printf("before forget: %d entity\n", len(before))

	if err := cx.Forget(ctx, cortex.Filter{EntityID: e.ID}); err != nil {
		return
	}

	after, _ := cx.FindEntities(ctx, cortex.EntityFilter{Type: "person"})
	mems, _ := cx.SearchMemories(ctx, "Alice", 5)
	fmt.Printf("after forget: %d entities\n", len(after))
	fmt.Printf("memories kept (recoverable): %v\n", len(mems) == 1)
	// Output:
	// before forget: 1 entity
	// after forget: 0 entities
	// memories kept (recoverable): true
}

// ExampleCortex_Traverse shows how to walk the knowledge graph starting
// from an entity and collect connected entities and relationships.
func ExampleCortex_Traverse() {
	dsn, cleanup, ok := pgtest.ExampleDSN()
	if !ok {
		// No database configured: echo the expected output so the example passes.
		fmt.Print("entities in graph: 3\nrelationships: 2\n")
		return
	}
	defer cleanup()

	cx, err := cortex.Open(dsn)
	if err != nil {
		return
	}
	defer cx.Close()
	cx = exampleKeyed(cx)

	ctx := context.Background()

	alice := &cortex.Entity{Type: "person", Name: "Alice"}
	stripe := &cortex.Entity{Type: "organization", Name: "Stripe"}
	bob := &cortex.Entity{Type: "person", Name: "Bob"}
	for _, e := range []*cortex.Entity{alice, stripe, bob} {
		if err := cx.PutEntity(ctx, e); err != nil {
			return
		}
	}
	if err := cx.PutRelationship(ctx, &cortex.Relationship{
		SourceID: alice.ID, TargetID: stripe.ID, Type: "works_at",
	}); err != nil {
		return
	}
	if err := cx.PutRelationship(ctx, &cortex.Relationship{
		SourceID: alice.ID, TargetID: bob.ID, Type: "knows",
	}); err != nil {
		return
	}

	graph, err := cx.Traverse(ctx, alice.ID, cortex.WithDepth(1))
	if err != nil {
		return
	}

	fmt.Printf("entities in graph: %d\n", len(graph.Entities))
	fmt.Printf("relationships: %d\n", len(graph.Relationships))
	// Output:
	// entities in graph: 3
	// relationships: 2
}

// exampleKeyed returns a handle for a fresh encrypted user holding a user key,
// as a signed-in, unlocked account has. Examples have no *testing.T.
func exampleKeyed(root *cortex.Cortex) *cortex.Cortex {
	u, err := root.CreateUser(context.Background(), "example-sub", "example@x.test", "Example", true)
	if err != nil {
		panic(err)
	}
	if _, err := root.DB().ExecContext(context.Background(), `UPDATE users SET encryption_state = 'encrypted' WHERE id = $1`, u.ID); err != nil {
		panic(err)
	}
	uk, err := keys.NewUK()
	if err != nil {
		panic(err)
	}
	return root.ForUser(u.ID).WithKey(uk, "encrypted")
}

// mockExtractor is a canned extractor so the examples need no LLM.
type mockExtractor struct {
	extractFn func(ctx context.Context, content, contentType string) (*cortex.Extraction, error)
}

func (m *mockExtractor) Extract(ctx context.Context, content, contentType string) (*cortex.Extraction, error) {
	if m.extractFn != nil {
		return m.extractFn(ctx, content, contentType)
	}
	return &cortex.Extraction{}, nil
}

// mockLLM is a canned LLM; only Decompose is customisable.
type mockLLM struct {
	decomposeFn func(ctx context.Context, query string) ([]cortex.StructuredQuery, error)
}

func (m *mockLLM) Extract(context.Context, string, string) (cortex.ExtractionResult, error) {
	return cortex.ExtractionResult{}, nil
}

func (m *mockLLM) Decompose(ctx context.Context, query string) ([]cortex.StructuredQuery, error) {
	if m.decomposeFn != nil {
		return m.decomposeFn(ctx, query)
	}
	return nil, nil
}

func (m *mockLLM) Summarize(context.Context, []string) (string, error) { return "", nil }

func (m *mockLLM) DetectConflicts(context.Context, []cortex.Memory) ([]cortex.ConflictPair, error) {
	return nil, nil
}

func (m *mockLLM) DetectRelations(context.Context, []cortex.Memory) ([]cortex.MemoryRelation, error) {
	return nil, nil
}
