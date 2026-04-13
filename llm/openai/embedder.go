package openai

import (
	"context"
	"fmt"

	oai "github.com/sashabaranov/go-openai"
)

// Embedder implements the cortex.Embedder interface using OpenAI's embeddings API.
type Embedder struct {
	client *oai.Client
	model  oai.EmbeddingModel
	dims   int
}

// NewEmbedder creates a new OpenAI Embedder with text-embedding-3-small (1536 dims).
func NewEmbedder(apiKey string) *Embedder {
	return &Embedder{
		client: oai.NewClient(apiKey),
		model:  oai.SmallEmbedding3,
		dims:   1536,
	}
}

// Embed generates embedding vectors for the given texts using OpenAI's API.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	resp, err := e.client.CreateEmbeddings(ctx, oai.EmbeddingRequestStrings{
		Input: texts,
		Model: e.model,
	})
	if err != nil {
		return nil, fmt.Errorf("openai: create embeddings: %w", err)
	}

	result := make([][]float32, len(resp.Data))
	for i, d := range resp.Data {
		result[i] = d.Embedding
	}
	return result, nil
}

// Dimensions returns the dimensionality of the embedding vectors.
func (e *Embedder) Dimensions() int {
	return e.dims
}
