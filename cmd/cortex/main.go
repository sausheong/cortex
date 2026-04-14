package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	oai "github.com/sashabaranov/go-openai"
	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/connector/files"
	"github.com/sausheong/cortex/extractor/deterministic"
	"github.com/sausheong/cortex/extractor/hybrid"
	llmext "github.com/sausheong/cortex/extractor/llmext"
	anthropicllm "github.com/sausheong/cortex/llm/anthropic"
	oaillm "github.com/sausheong/cortex/llm/openai"
)

var dbPath = "brain.db"

func main() {
	// Resolve database path: --db flag > CORTEX_DB env > default "brain.db".
	if envDB := os.Getenv("CORTEX_DB"); envDB != "" {
		dbPath = envDB
	}

	// Strip --db flag from os.Args before command parsing.
	var filtered []string
	for i := 0; i < len(os.Args); i++ {
		if os.Args[i] == "--db" && i+1 < len(os.Args) {
			dbPath = os.Args[i+1]
			i++ // skip the value
		} else {
			filtered = append(filtered, os.Args[i])
		}
	}
	os.Args = filtered

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
	case "config":
		cmdConfig()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `Usage: cortex [--db <path>] <command> [arguments]

Global options:
  --db <path>                    Path to brain.db (default: brain.db, or CORTEX_DB env var)

Commands:
  init                           Create a new brain.db
  remember <text>                Remember text
  recall <query>                 Recall and print results
  sync <dir>                     Sync text files from a directory (.md, .csv, .yaml, .json, .txt, etc.)
  entity list [--type <type>]    List entities
  entity get <id>                Show entity details + relationships
  forget --source <src>          Forget by source
  forget --entity <id>           Forget by entity ID
  config                         Show owner identity
  config --name <name>           Update owner name
  config --nickname <nick>       Update owner nickname
  config --email <email>         Add an email address`)
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

	cx, err := cortex.Open(dbPath, opts...)
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
	cx, err := cortex.Open(dbPath, cortex.WithExtractor(deterministic.New()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "error creating database: %v\n", err)
		os.Exit(1)
	}
	defer cx.Close()
	fmt.Println("Initialized brain.db")

	// Prompt for owner identity.
	reader := bufio.NewReader(os.Stdin)

	fmt.Print("Enter your name: ")
	name, _ := reader.ReadString('\n')
	name = strings.TrimSpace(name)
	if name == "" {
		fmt.Println("Skipping owner setup.")
		return
	}

	fmt.Print("Enter nickname (optional): ")
	nickname, _ := reader.ReadString('\n')
	nickname = strings.TrimSpace(nickname)

	var emails []string
	for {
		fmt.Print("Enter email address (or press Enter to finish): ")
		email, _ := reader.ReadString('\n')
		email = strings.TrimSpace(email)
		if email == "" {
			break
		}
		emails = append(emails, email)
	}

	storeOwner(cx, name, nickname, emails)
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
		fmt.Fprintln(os.Stderr, "usage: cortex sync <dir>")
		os.Exit(1)
	}

	dir := os.Args[2]
	cx := openCortex()
	defer cx.Close()

	conn := files.New(dir)
	ctx := context.Background()
	if err := conn.Sync(ctx, cx); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Synced files from %s\n", dir)
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

func storeOwner(cx *cortex.Cortex, name, nickname string, emails []string) {
	ctx := context.Background()

	// Forget previous owner data first (entity + memories), then recreate.
	_ = cx.Forget(ctx, cortex.Filter{Source: "owner"})

	attrs := map[string]any{}
	if nickname != "" {
		attrs["nickname"] = nickname
	}
	if len(emails) > 0 {
		attrs["emails"] = strings.Join(emails, ", ")
	}

	e := &cortex.Entity{
		Type:       "person",
		Name:       name,
		Source:     "owner",
		Attributes: attrs,
	}
	if err := cx.PutEntity(ctx, e); err != nil {
		fmt.Fprintf(os.Stderr, "error storing owner entity: %v\n", err)
		os.Exit(1)
	}

	summary := buildOwnerSummary(name, nickname, emails)
	if err := cx.Remember(ctx, summary, cortex.WithSource("owner")); err != nil {
		fmt.Fprintf(os.Stderr, "error storing owner memory: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Owner set: %s\n", name)
}

func buildOwnerSummary(name, nickname string, emails []string) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("My name is %s.", name))
	if nickname != "" {
		parts = append(parts, fmt.Sprintf("My nickname is %s.", nickname))
	}
	if len(emails) > 0 {
		parts = append(parts, fmt.Sprintf("My email addresses are %s.", strings.Join(emails, ", ")))
	}
	return strings.Join(parts, " ")
}

func cmdConfig() {
	cx := openCortex()
	defer cx.Close()

	ctx := context.Background()

	// If no flags, show current owner info.
	if len(os.Args) < 4 {
		entities, err := cx.FindEntities(ctx, cortex.EntityFilter{Type: "person", Source: "owner"})
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		if len(entities) == 0 {
			fmt.Println("No owner configured. Run 'cortex init' or 'cortex config --name <name>' to set up.")
			return
		}
		e := entities[0]
		fmt.Printf("Name:     %s\n", e.Name)
		if nick, ok := e.Attributes["nickname"]; ok && nick != "" {
			fmt.Printf("Nickname: %v\n", nick)
		}
		if em, ok := e.Attributes["emails"]; ok && em != "" {
			fmt.Printf("Emails:   %v\n", em)
		}
		return
	}

	// Parse flags to update owner.
	// First, load existing owner entity to preserve fields not being updated.
	entities, err := cx.FindEntities(ctx, cortex.EntityFilter{Type: "person", Source: "owner"})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	var name, nickname string
	var emails []string

	if len(entities) > 0 {
		e := entities[0]
		name = e.Name
		if n, ok := e.Attributes["nickname"]; ok {
			nickname = fmt.Sprintf("%v", n)
		}
		if em, ok := e.Attributes["emails"]; ok {
			emailStr := fmt.Sprintf("%v", em)
			if emailStr != "" {
				for _, addr := range strings.Split(emailStr, ", ") {
					addr = strings.TrimSpace(addr)
					if addr != "" {
						emails = append(emails, addr)
					}
				}
			}
		}
	}

	// Apply flags.
	for i := 2; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--name":
			if i+1 >= len(os.Args) {
				fmt.Fprintln(os.Stderr, "missing value for --name")
				os.Exit(1)
			}
			i++
			name = os.Args[i]
		case "--nickname":
			if i+1 >= len(os.Args) {
				fmt.Fprintln(os.Stderr, "missing value for --nickname")
				os.Exit(1)
			}
			i++
			nickname = os.Args[i]
		case "--email":
			if i+1 >= len(os.Args) {
				fmt.Fprintln(os.Stderr, "missing value for --email")
				os.Exit(1)
			}
			i++
			newEmail := os.Args[i]
			// Add only if not already present.
			found := false
			for _, e := range emails {
				if strings.EqualFold(e, newEmail) {
					found = true
					break
				}
			}
			if !found {
				emails = append(emails, newEmail)
			}
		default:
			fmt.Fprintf(os.Stderr, "unknown config flag: %s\n", os.Args[i])
			os.Exit(1)
		}
	}

	if name == "" {
		fmt.Fprintln(os.Stderr, "name is required. Use --name <name>")
		os.Exit(1)
	}

	storeOwner(cx, name, nickname, emails)
}
