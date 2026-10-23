package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	oai "github.com/sashabaranov/go-openai"
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
	if d := embedder.Dimensions(); d != 512 {
		t.Errorf("expected 512 dimensions, got %d", d)
	}
}

func embedRequestBody(t *testing.T, model string, opts ...EmbedderOption) map[string]any {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"x"}`))
	}))
	defer srv.Close()
	all := append([]EmbedderOption{WithEmbedderBaseURL(srv.URL)}, opts...)
	if model != "" {
		all = append(all, WithEmbeddingModel(oai.EmbeddingModel(model), 512))
	}
	if _, err := NewEmbedder("k", all...).Embed(context.Background(), []string{"hi"}); err != nil {
		t.Fatalf("Embed: %v", err)
	}
	return got
}

func TestEmbedderSendsDimensions(t *testing.T) {
	body := embedRequestBody(t, "")
	if d, ok := body["dimensions"].(float64); !ok || d != 512 {
		t.Errorf("dimensions = %v, want 512 (body %v)", body["dimensions"], body)
	}
}

func TestEmbedderNoDimensionsForOtherModels(t *testing.T) {
	body := embedRequestBody(t, "nomic-embed-text")
	if _, ok := body["dimensions"]; ok {
		t.Errorf("dimensions should be absent for non text-embedding-3 models: %v", body)
	}
}

// Gateways (LiteLLM, Azure, OpenRouter) prefix the model name with a route,
// e.g. "azure/text-embedding-3-small-eastus". Those are still
// text-embedding-3 models and must get the dimensions parameter, or they
// return full-size vectors.
func TestEmbedderSendsDimensionsForPrefixedModels(t *testing.T) {
	for _, m := range []string{"azure/text-embedding-3-small-eastus", "openai/text-embedding-3-large"} {
		body := embedRequestBody(t, m)
		if d, ok := body["dimensions"].(float64); !ok || d != 512 {
			t.Errorf("%s: dimensions = %v, want 512", m, body["dimensions"])
		}
	}
}
