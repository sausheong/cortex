package main

import (
	"fmt"
	"os"
	"strconv"

	"github.com/mark3labs/mcp-go/server"
	oai "github.com/sashabaranov/go-openai"
	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/extractor/deterministic"
	"github.com/sausheong/cortex/extractor/hybrid"
	llmext "github.com/sausheong/cortex/extractor/llmext"
	anthropicllm "github.com/sausheong/cortex/llm/anthropic"
	oaillm "github.com/sausheong/cortex/llm/openai"
)

func main() {
	cx := openCortex()
	defer cx.Close()

	s := server.NewMCPServer("cortex", "1.0.0", server.WithToolCapabilities(false))
	registerTools(s, cx)

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "mcp server error: %v\n", err)
		os.Exit(1)
	}
}

func openCortex() *cortex.Cortex {
	dbPath := os.Getenv("CORTEX_DB")
	if dbPath == "" {
		dbPath = "brain.db"
	}

	provider := os.Getenv("LLM_PROVIDER")
	modelName := os.Getenv("LLM_MODEL")
	embModel := os.Getenv("EMBEDDING_MODEL")
	embDimsStr := os.Getenv("EMBEDDING_DIMS")

	var opts []cortex.Option
	var llm cortex.LLM

	switch provider {
	case "anthropic":
		apiKey := os.Getenv("ANTHROPIC_API_KEY")
		if apiKey == "" {
			fmt.Fprintln(os.Stderr, "ANTHROPIC_API_KEY is required when LLM_PROVIDER=anthropic")
			os.Exit(1)
		}
		var llmOpts []anthropicllm.LLMOption
		if baseURL := os.Getenv("ANTHROPIC_BASE_URL"); baseURL != "" {
			llmOpts = append(llmOpts, anthropicllm.WithBaseURL(baseURL))
		}
		if modelName != "" {
			llmOpts = append(llmOpts, anthropicllm.WithModel(modelName))
		}
		llm = anthropicllm.NewLLM(apiKey, llmOpts...)

	default:
		apiKey := os.Getenv("OPENAI_API_KEY")
		if apiKey == "" {
			opts = append(opts, cortex.WithExtractor(deterministic.New()))
			cx, err := cortex.Open(dbPath, opts...)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error opening database: %v\n", err)
				os.Exit(1)
			}
			return cx
		}
		var llmOpts []oaillm.LLMOption
		if baseURL := os.Getenv("OPENAI_BASE_URL"); baseURL != "" {
			llmOpts = append(llmOpts, oaillm.WithBaseURL(baseURL))
		}
		if modelName != "" {
			llmOpts = append(llmOpts, oaillm.WithModel(modelName))
		}
		llm = oaillm.NewLLM(apiKey, llmOpts...)
	}

	embedder := configureEmbedder(embModel, embDimsStr)
	det := deterministic.New()
	llmExtractor := llmext.New(llm)
	ext := hybrid.New(det, llmExtractor)

	opts = append(opts,
		cortex.WithLLM(llm),
		cortex.WithEmbedder(embedder),
		cortex.WithExtractor(ext),
	)

	cx, err := cortex.Open(dbPath, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening database: %v\n", err)
		os.Exit(1)
	}
	return cx
}

func configureEmbedder(embModel, embDimsStr string) cortex.Embedder {
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
	if embModel != "" {
		dims := 1536
		if embDimsStr != "" {
			if d, err := strconv.Atoi(embDimsStr); err == nil {
				dims = d
			}
		}
		embOpts = append(embOpts, oaillm.WithEmbeddingModel(oai.EmbeddingModel(embModel), dims))
	}

	return oaillm.NewEmbedder(apiKey, embOpts...)
}
