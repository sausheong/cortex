package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	oai "github.com/sashabaranov/go-openai"
	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/connector/markdown"
	"github.com/sausheong/cortex/extractor/deterministic"
	"github.com/sausheong/cortex/extractor/hybrid"
	llmext "github.com/sausheong/cortex/extractor/llmext"
	anthropicllm "github.com/sausheong/cortex/llm/anthropic"
	oaillm "github.com/sausheong/cortex/llm/openai"
)

const defaultDB = "brain.db"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	cmd := os.Args[1]

	switch cmd {
	case "init":
		cmdInit()
	case "remember":
		cmdRemember()
	case "recall":
		cmdRecall()
	case "sync":
		cmdSync()
	case "entity":
		cmdEntity()
	case "forget":
		cmdForget()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `Usage: cortex <command> [arguments]

Commands:
  init                           Create a new brain.db
  remember <text>                Remember text
  recall <query>                 Recall and print results
  sync markdown <dir>            Sync markdown directory
  sync gmail                     Sync Gmail (requires OAuth2, see README)
  sync calendar                  Sync Google Calendar (requires OAuth2, see README)
  entity list [--type <type>]    List entities
  entity get <id>                Show entity details + relationships
  forget --source <src>          Forget by source
  forget --entity <id>           Forget by entity ID`)
}

func openCortex() *cortex.Cortex {
	provider := os.Getenv("LLM_PROVIDER") // "openai" (default) or "anthropic"
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

	default: // "openai" or empty
		apiKey := os.Getenv("OPENAI_API_KEY")
		if apiKey == "" {
			opts = append(opts, cortex.WithExtractor(deterministic.New()))
			cx, err := cortex.Open(defaultDB, opts...)
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

	// Embedder — uses OpenAI-compatible API (works with Ollama, vLLM, etc.)
	embedder := configureEmbedder(embModel, embDimsStr)
	det := deterministic.New()
	llmExtractor := llmext.New(llm)
	ext := hybrid.New(det, llmExtractor)

	opts = append(opts,
		cortex.WithLLM(llm),
		cortex.WithEmbedder(embedder),
		cortex.WithExtractor(ext),
	)

	cx, err := cortex.Open(defaultDB, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening database: %v\n", err)
		os.Exit(1)
	}
	return cx
}

func configureEmbedder(embModel, embDimsStr string) cortex.Embedder {
	// Embedding API key and base URL can differ from the LLM provider.
	// Falls back to OPENAI_API_KEY / OPENAI_BASE_URL if not set.
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

func cmdInit() {
	cx, err := cortex.Open(defaultDB)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating database: %v\n", err)
		os.Exit(1)
	}
	cx.Close()
	fmt.Println("Initialized brain.db")
}

func cmdRemember() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: cortex remember <text>")
		os.Exit(1)
	}

	text := strings.Join(os.Args[2:], " ")
	cx := openCortex()
	defer cx.Close()

	ctx := context.Background()
	if err := cx.Remember(ctx, text); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Remembered.")
}

func cmdRecall() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: cortex recall <query>")
		os.Exit(1)
	}

	query := strings.Join(os.Args[2:], " ")
	cx := openCortex()
	defer cx.Close()

	ctx := context.Background()
	results, err := cx.Recall(ctx, query)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if len(results) == 0 {
		fmt.Println("No results found.")
		return
	}

	for i, r := range results {
		fmt.Printf("[%d] (%s, score=%.4f) %s\n", i+1, r.Type, r.Score, r.Content)
		if r.Source != "" {
			fmt.Printf("    source: %s\n", r.Source)
		}
	}
}

func cmdSync() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: cortex sync <markdown|gmail|calendar> [args]")
		os.Exit(1)
	}

	subCmd := os.Args[2]

	switch subCmd {
	case "markdown":
		if len(os.Args) < 4 {
			fmt.Fprintln(os.Stderr, "usage: cortex sync markdown <dir>")
			os.Exit(1)
		}
		dir := os.Args[3]
		cx := openCortex()
		defer cx.Close()

		conn := markdown.New(dir)
		ctx := context.Background()
		if err := conn.Sync(ctx, cx); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Synced markdown files from %s\n", dir)
	case "gmail":
		fmt.Fprintln(os.Stderr, "Gmail sync requires Google OAuth2 credentials.")
		fmt.Fprintln(os.Stderr, "Set up OAuth2 and pass credentials programmatically via the Go API.")
		fmt.Fprintln(os.Stderr, "See README for details.")
		os.Exit(1)
	case "calendar":
		fmt.Fprintln(os.Stderr, "Calendar sync requires Google OAuth2 credentials.")
		fmt.Fprintln(os.Stderr, "Set up OAuth2 and pass credentials programmatically via the Go API.")
		fmt.Fprintln(os.Stderr, "See README for details.")
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "unknown sync type: %s\n", subCmd)
		os.Exit(1)
	}
}

func cmdEntity() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: cortex entity list [--type <type>] | cortex entity get <id>")
		os.Exit(1)
	}

	subCmd := os.Args[2]

	switch subCmd {
	case "list":
		cmdEntityList()
	case "get":
		cmdEntityGet()
	default:
		fmt.Fprintf(os.Stderr, "unknown entity subcommand: %s\n", subCmd)
		os.Exit(1)
	}
}

func cmdEntityList() {
	cx := openCortex()
	defer cx.Close()

	filter := cortex.EntityFilter{}

	// Parse --type flag.
	for i := 3; i < len(os.Args); i++ {
		if os.Args[i] == "--type" && i+1 < len(os.Args) {
			filter.Type = os.Args[i+1]
			i++
		}
	}

	ctx := context.Background()
	entities, err := cx.FindEntities(ctx, filter)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if len(entities) == 0 {
		fmt.Println("No entities found.")
		return
	}

	for _, e := range entities {
		fmt.Printf("%-26s  %-12s  %s\n", e.ID, e.Type, e.Name)
	}
}

func cmdEntityGet() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: cortex entity get <id>")
		os.Exit(1)
	}

	id := os.Args[3]
	cx := openCortex()
	defer cx.Close()

	ctx := context.Background()

	entity, err := cx.GetEntity(ctx, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("ID:      %s\n", entity.ID)
	fmt.Printf("Name:    %s\n", entity.Name)
	fmt.Printf("Type:    %s\n", entity.Type)
	fmt.Printf("Source:  %s\n", entity.Source)
	if len(entity.Attributes) > 0 {
		fmt.Println("Attributes:")
		for k, v := range entity.Attributes {
			fmt.Printf("  %s: %v\n", k, v)
		}
	}

	// Show relationships.
	rels, err := cx.GetRelationships(ctx, id)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading relationships: %v\n", err)
		return
	}

	if len(rels) > 0 {
		fmt.Println("Relationships:")
		for _, r := range rels {
			direction := "→"
			otherID := r.TargetID
			if r.TargetID == id {
				direction = "←"
				otherID = r.SourceID
			}
			fmt.Printf("  %s %s %s\n", direction, r.Type, otherID)
		}
	}
}

func cmdForget() {
	if len(os.Args) < 4 {
		fmt.Fprintln(os.Stderr, "usage: cortex forget --source <src> | --entity <id>")
		os.Exit(1)
	}

	cx := openCortex()
	defer cx.Close()

	ctx := context.Background()
	var filter cortex.Filter

	flag := os.Args[2]
	value := os.Args[3]

	switch flag {
	case "--source":
		filter.Source = value
	case "--entity":
		filter.EntityID = value
	default:
		fmt.Fprintf(os.Stderr, "unknown flag: %s\nusage: cortex forget --source <src> | --entity <id>\n", flag)
		os.Exit(1)
	}

	if err := cx.Forget(ctx, filter); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("Forgotten.")
}
