package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/sausheong/cortex"
	anthropicllm "github.com/sausheong/cortex/llm/anthropic"
	oaillm "github.com/sausheong/cortex/llm/openai"

	oai "github.com/sashabaranov/go-openai"
)

// dbPath resolves the database path from CORTEX_DB (no default — the
// benchmark must run against a real, named graph).
func dbPath() string {
	p := os.Getenv("CORTEX_DB")
	if p == "" {
		fmt.Fprintln(os.Stderr, "CORTEX_DB is required (path to the cortex database to benchmark)")
		os.Exit(1)
	}
	return p
}

// newEmbedder builds the embedder exactly as the main cortex binary does, so
// query embeddings match the stored ones. Defaults to text-embedding-3-small
// (1536 dims). Honors EMBEDDING_MODEL/EMBEDDING_DIMS/EMBEDDING_API_KEY/
// EMBEDDING_BASE_URL with OPENAI_* fallbacks.
func newEmbedder() cortex.Embedder {
	apiKey := os.Getenv("EMBEDDING_API_KEY")
	if apiKey == "" {
		apiKey = os.Getenv("OPENAI_API_KEY")
	}
	if apiKey == "" {
		fmt.Fprintln(os.Stderr, "EMBEDDING_API_KEY or OPENAI_API_KEY is required for embeddings")
		os.Exit(1)
	}
	var embOpts []oaillm.EmbedderOption
	baseURL := os.Getenv("EMBEDDING_BASE_URL")
	if baseURL == "" {
		baseURL = os.Getenv("OPENAI_BASE_URL")
	}
	if baseURL != "" {
		embOpts = append(embOpts, oaillm.WithEmbedderBaseURL(baseURL))
	}
	if embModel := os.Getenv("EMBEDDING_MODEL"); embModel != "" {
		dims := 1536
		if s := os.Getenv("EMBEDDING_DIMS"); s != "" {
			if d, err := strconv.Atoi(s); err == nil {
				dims = d
			}
		}
		embOpts = append(embOpts, oaillm.WithEmbeddingModel(oai.EmbeddingModel(embModel), dims))
	}
	return oaillm.NewEmbedder(apiKey, embOpts...)
}

// newLLM builds the question-generation LLM (only needed by `generate`).
// Mirrors the main binary's provider selection.
func newLLM() cortex.LLM {
	provider := os.Getenv("LLM_PROVIDER")
	modelName := os.Getenv("LLM_MODEL")
	switch provider {
	case "anthropic":
		apiKey := os.Getenv("ANTHROPIC_API_KEY")
		if apiKey == "" {
			apiKey = os.Getenv("ANTHROPIC_AUTH_TOKEN")
		}
		if apiKey == "" {
			fmt.Fprintln(os.Stderr, "ANTHROPIC_API_KEY required for LLM_PROVIDER=anthropic")
			os.Exit(1)
		}
		var opts []anthropicllm.LLMOption
		if b := os.Getenv("ANTHROPIC_BASE_URL"); b != "" {
			opts = append(opts, anthropicllm.WithBaseURL(b))
		}
		if modelName != "" {
			opts = append(opts, anthropicllm.WithModel(modelName))
		}
		return anthropicllm.NewLLM(apiKey, opts...)
	default:
		apiKey := os.Getenv("OPENAI_API_KEY")
		if apiKey == "" {
			fmt.Fprintln(os.Stderr, "OPENAI_API_KEY required to generate questions")
			os.Exit(1)
		}
		var opts []oaillm.LLMOption
		if b := os.Getenv("OPENAI_BASE_URL"); b != "" {
			opts = append(opts, oaillm.WithBaseURL(b))
		}
		if modelName != "" {
			opts = append(opts, oaillm.WithModel(modelName))
		}
		return oaillm.NewLLM(apiKey, opts...)
	}
}

// openForRun opens the graph with just an embedder (run scores retrieval; it
// swaps in its own fixed decomposition LLM per config, so no real LLM needed).
// A logger is installed so embedder failures (e.g. an over-quota key) surface
// loudly instead of silently zeroing the vector path.
func openForRun() *cortex.Cortex {
	cx, err := cortex.Open(dbPath(),
		cortex.WithEmbedder(newEmbedder()),
		cortex.WithLogger(func(format string, args ...any) {
			fmt.Fprintf(os.Stderr, "[cortex] "+format+"\n", args...)
		}),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open db: %v\n", err)
		os.Exit(1)
	}
	return cx
}
