package openai

import (
	"context"
	"os"
	"testing"
)

func TestEmbedIntegration(t *testing.T) {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		t.Skip("OPENAI_API_KEY not set")
	}

	embedder := NewEmbedder(key)
	ctx := context.Background()

	texts := []string{
		"Alice is a software engineer.",
		"Bob manages the engineering team.",
	}

	vecs, err := embedder.Embed(ctx, texts)
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}

	if len(vecs) != 2 {
		t.Fatalf("expected 2 vectors, got %d", len(vecs))
	}

	for i, v := range vecs {
		if len(v) == 0 {
			t.Errorf("vector %d is empty", i)
		}
		t.Logf("Vector %d: %d dimensions", i, len(v))
	}
}

func TestEmbedderDimensions(t *testing.T) {
	embedder := NewEmbedder("dummy-key")
	if d := embedder.Dimensions(); d != 1536 {
		t.Errorf("expected 1536 dimensions, got %d", d)
	}
}
