package openai

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	oai "github.com/sashabaranov/go-openai"
)

// EmbedderOption configures the Embedder.
type EmbedderOption func(*Embedder)

// WithEmbedderBaseURL sets a custom base URL for OpenAI-compatible APIs.
func WithEmbedderBaseURL(url string) EmbedderOption {
	return func(e *Embedder) {
		e.baseURL = url
	}
}

// WithEmbeddingModel sets the embedding model and its dimensions.
func WithEmbeddingModel(model oai.EmbeddingModel, dims int) EmbedderOption {
	return func(e *Embedder) {
		e.model = model
		e.dims = dims
	}
}

// Embedder implements the cortex.Embedder interface using OpenAI's embeddings API.
// Works with any OpenAI-compatible API by setting a custom base URL.
type Embedder struct {
	client  *oai.Client
	model   oai.EmbeddingModel
	dims    int
	baseURL string

	httpClient *http.Client
}

// WithEmbedderHTTPClient makes the embedder use hc for every request.
func WithEmbedderHTTPClient(hc *http.Client) EmbedderOption {
	return func(e *Embedder) { e.httpClient = hc }
}

// NewEmbedder creates a new OpenAI Embedder. Defaults to text-embedding-3-small (512 dims).
// Use WithEmbedderBaseURL to point at any OpenAI-compatible API.
func NewEmbedder(apiKey string, opts ...EmbedderOption) *Embedder {
	e := &Embedder{
		model: oai.SmallEmbedding3,
		dims:  512,
	}
	for _, o := range opts {
		o(e)
	}
	cfg := oai.DefaultConfig(apiKey)
	if e.baseURL != "" {
		cfg.BaseURL = e.baseURL
	}
	if e.httpClient != nil {
		cfg.HTTPClient = e.httpClient
	}
	e.client = oai.NewClientWithConfig(cfg)
	return e
}

// Embed generates embedding vectors for the given texts using OpenAI's API.
func (e *Embedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	req := oai.EmbeddingRequestStrings{
		Input: texts,
		Model: e.model,
	}
	// Only text-embedding-3-* models accept the dimensions parameter.
	if strings.HasPrefix(string(e.model), "text-embedding-3") {
		req.Dimensions = e.dims
	}
	resp, err := e.client.CreateEmbeddings(ctx, req)
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
