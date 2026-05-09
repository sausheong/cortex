package cortex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// ExampleCortex_Remember shows how to ingest text into the knowledge graph
// and verify that the extracted entities and memories are queryable.
func ExampleCortex_Remember() {
	dir, _ := os.MkdirTemp("", "cortex-example-*")
	defer os.RemoveAll(dir)

	cx, err := Open(filepath.Join(dir, "example.db"))
	if err != nil {
		return
	}
	defer cx.Close()

	// Use a mock extractor so no LLM is required.
	cx.SetExtractor(&mockExtractor{
		extractFn: func(_ context.Context, _, _ string) (*Extraction, error) {
			return &Extraction{
				Entities: []Entity{
					{Type: "person", Name: "Alice"},
					{Type: "organization", Name: "Stripe"},
				},
				Relationships: []Relationship{
					{SourceID: "Alice", TargetID: "Stripe", Type: "works_at"},
				},
				Memories: []Memory{
					{Content: "Alice works at Stripe"},
				},
			}, nil
		},
	})

	ctx := context.Background()
	if err := cx.Remember(ctx, "Alice works at Stripe as an engineer", WithSource("example")); err != nil {
		return
	}

	people, _ := cx.FindEntities(ctx, EntityFilter{Type: "person"})
	orgs, _ := cx.FindEntities(ctx, EntityFilter{Type: "organization"})
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
	dir, _ := os.MkdirTemp("", "cortex-example-*")
	defer os.RemoveAll(dir)

	cx, err := Open(filepath.Join(dir, "example.db"))
	if err != nil {
		return
	}
	defer cx.Close()

	ctx := context.Background()

	// Seed a chunk directly.
	if err := cx.PutChunk(ctx, &Chunk{Content: "Alice is a senior engineer at Stripe"}); err != nil {
		return
	}

	// Decompose to keyword search so no LLM API call is needed.
	cx.SetLLM(&mockLLM{
		decomposeFn: func(_ context.Context, q string) ([]StructuredQuery, error) {
			return []StructuredQuery{
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

// ExampleCortex_Forget shows how to remove an entity and all associated
// data (chunks, memories, relationships) from the knowledge graph.
func ExampleCortex_Forget() {
	dir, _ := os.MkdirTemp("", "cortex-example-*")
	defer os.RemoveAll(dir)

	cx, err := Open(filepath.Join(dir, "example.db"))
	if err != nil {
		return
	}
	defer cx.Close()

	ctx := context.Background()

	e := &Entity{Type: "person", Name: "Alice", Source: "example"}
	if err := cx.PutEntity(ctx, e); err != nil {
		return
	}
	if err := cx.PutMemory(ctx, &Memory{
		Content:   "Alice works at Stripe",
		EntityIDs: []string{e.ID},
		Source:    "example",
	}); err != nil {
		return
	}

	before, _ := cx.FindEntities(ctx, EntityFilter{Type: "person"})
	fmt.Printf("before forget: %d entity\n", len(before))

	if err := cx.Forget(ctx, Filter{EntityID: e.ID}); err != nil {
		return
	}

	after, _ := cx.FindEntities(ctx, EntityFilter{Type: "person"})
	mems, _ := cx.SearchMemories(ctx, "Alice", 5)
	fmt.Printf("after forget: %d entities\n", len(after))
	fmt.Printf("orphaned memories removed: %v\n", len(mems) == 0)
	// Output:
	// before forget: 1 entity
	// after forget: 0 entities
	// orphaned memories removed: true
}

// ExampleCortex_Traverse shows how to walk the knowledge graph starting
// from an entity and collect connected entities and relationships.
func ExampleCortex_Traverse() {
	dir, _ := os.MkdirTemp("", "cortex-example-*")
	defer os.RemoveAll(dir)

	cx, err := Open(filepath.Join(dir, "example.db"))
	if err != nil {
		return
	}
	defer cx.Close()

	ctx := context.Background()

	alice := &Entity{Type: "person", Name: "Alice"}
	stripe := &Entity{Type: "organization", Name: "Stripe"}
	bob := &Entity{Type: "person", Name: "Bob"}
	for _, e := range []*Entity{alice, stripe, bob} {
		if err := cx.PutEntity(ctx, e); err != nil {
			return
		}
	}
	if err := cx.PutRelationship(ctx, &Relationship{
		SourceID: alice.ID, TargetID: stripe.ID, Type: "works_at",
	}); err != nil {
		return
	}
	if err := cx.PutRelationship(ctx, &Relationship{
		SourceID: alice.ID, TargetID: bob.ID, Type: "knows",
	}); err != nil {
		return
	}

	graph, err := cx.Traverse(ctx, alice.ID, WithDepth(1))
	if err != nil {
		return
	}

	fmt.Printf("entities in graph: %d\n", len(graph.Entities))
	fmt.Printf("relationships: %d\n", len(graph.Relationships))
	// Output:
	// entities in graph: 3
	// relationships: 2
}
