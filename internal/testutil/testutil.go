package testutil

import (
	"context"
	"math"
	"path/filepath"
	"testing"

	"github.com/sausheong/cortex"
)

// MockLLM is a configurable mock for the cortex.LLM interface.
type MockLLM struct {
	ExtractFn   func(ctx context.Context, text, prompt string) (cortex.ExtractionResult, error)
	DecomposeFn func(ctx context.Context, query string) ([]cortex.StructuredQuery, error)
	SummarizeFn func(ctx context.Context, texts []string) (string, error)
}

func (m *MockLLM) Extract(ctx context.Context, text, prompt string) (cortex.ExtractionResult, error) {
	if m.ExtractFn != nil {
		return m.ExtractFn(ctx, text, prompt)
	}
	return cortex.ExtractionResult{}, nil
}

func (m *MockLLM) Decompose(ctx context.Context, query string) ([]cortex.StructuredQuery, error) {
	if m.DecomposeFn != nil {
		return m.DecomposeFn(ctx, query)
	}
	return nil, nil
}

func (m *MockLLM) Summarize(ctx context.Context, texts []string) (string, error) {
	if m.SummarizeFn != nil {
		return m.SummarizeFn(ctx, texts)
	}
	return "", nil
}

// MockEmbedder is a configurable mock for the cortex.Embedder interface.
// By default it generates deterministic 8-dimensional normalized vectors from text bytes.
type MockEmbedder struct {
	EmbedFn      func(ctx context.Context, texts []string) ([][]float32, error)
	DimensionsFn func() int
}

func (m *MockEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if m.EmbedFn != nil {
		return m.EmbedFn(ctx, texts)
	}
	// Default: deterministic 8-dim normalized vectors derived from text bytes.
	result := make([][]float32, len(texts))
	for i, text := range texts {
		result[i] = deterministicVector(text)
	}
	return result, nil
}

func (m *MockEmbedder) Dimensions() int {
	if m.DimensionsFn != nil {
		return m.DimensionsFn()
	}
	return 8
}

// deterministicVector generates a deterministic 8-dimensional normalized vector from text.
func deterministicVector(text string) []float32 {
	const dims = 8
	vec := make([]float32, dims)
	for i, b := range []byte(text) {
		vec[i%dims] += float32(b)
	}
	// Normalize to unit length.
	var norm float64
	for _, v := range vec {
		norm += float64(v) * float64(v)
	}
	norm = math.Sqrt(norm)
	if norm > 0 {
		for i := range vec {
			vec[i] = float32(float64(vec[i]) / norm)
		}
	}
	return vec
}

// MockExtractor is a configurable mock for the cortex.Extractor interface.
type MockExtractor struct {
	ExtractFn func(ctx context.Context, content, contentType string) (*cortex.Extraction, error)
}

func (m *MockExtractor) Extract(ctx context.Context, content, contentType string) (*cortex.Extraction, error) {
	if m.ExtractFn != nil {
		return m.ExtractFn(ctx, content, contentType)
	}
	return &cortex.Extraction{}, nil
}

// OpenTestDB creates a temporary Cortex database with all mocks wired up.
// The database is automatically cleaned up when the test finishes.
func OpenTestDB(t *testing.T) *cortex.Cortex {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	llm := &MockLLM{}
	embedder := &MockEmbedder{}
	extractor := &MockExtractor{}

	c, err := cortex.Open(dbPath,
		cortex.WithLLM(llm),
		cortex.WithEmbedder(embedder),
		cortex.WithExtractor(extractor),
	)
	if err != nil {
		t.Fatalf("testutil.OpenTestDB: %v", err)
	}

	t.Cleanup(func() { c.Close() })
	return c
}
