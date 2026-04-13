package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/extractor/deterministic"
	"github.com/sausheong/cortex/extractor/hybrid"
	llmext "github.com/sausheong/cortex/extractor/llmext"
	oaillm "github.com/sausheong/cortex/llm/openai"
)

var cx *cortex.Cortex

func main() {
	cx = openCortex()
	defer cx.Close()

	port := os.Getenv("CORTEX_PORT")
	if port == "" {
		port = "8080"
	}

	mux := http.NewServeMux()

	mux.HandleFunc("POST /remember", handleRemember)
	mux.HandleFunc("GET /recall", handleRecall)
	mux.HandleFunc("DELETE /forget", handleForget)
	mux.HandleFunc("GET /entity/{id}", handleGetEntity)
	mux.HandleFunc("GET /entities", handleFindEntities)
	mux.HandleFunc("GET /relationships/{entity_id}", handleGetRelationships)
	mux.HandleFunc("GET /traverse/{entity_id}", handleTraverse)
	mux.HandleFunc("GET /search", handleSearch)

	fmt.Fprintf(os.Stderr, "cortex-http listening on :%s\n", port)
	if err := http.ListenAndServe(":"+port, mux); err != nil {
		fmt.Fprintf(os.Stderr, "server error: %v\n", err)
		os.Exit(1)
	}
}

func openCortex() *cortex.Cortex {
	dbPath := os.Getenv("CORTEX_DB")
	if dbPath == "" {
		dbPath = "brain.db"
	}

	apiKey := os.Getenv("OPENAI_API_KEY")

	var opts []cortex.Option
	if apiKey != "" {
		llm := oaillm.NewLLM(apiKey)
		embedder := oaillm.NewEmbedder(apiKey)
		det := deterministic.New()
		llmExtractor := llmext.New(llm)
		ext := hybrid.New(det, llmExtractor)

		opts = append(opts,
			cortex.WithLLM(llm),
			cortex.WithEmbedder(embedder),
			cortex.WithExtractor(ext),
		)
	} else {
		opts = append(opts, cortex.WithExtractor(deterministic.New()))
	}

	c, err := cortex.Open(dbPath, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error opening database: %v\n", err)
		os.Exit(1)
	}
	return c
}

// --- Handlers ---

func handleRemember(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Content string `json:"content"`
		Source  string `json:"source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if body.Content == "" {
		writeError(w, http.StatusBadRequest, "content is required")
		return
	}

	var opts []cortex.RememberOption
	if body.Source != "" {
		opts = append(opts, cortex.WithSource(body.Source))
	}

	ctx := r.Context()
	if err := cx.Remember(ctx, body.Content, opts...); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "remembered"})
}

func handleRecall(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		writeError(w, http.StatusBadRequest, "q parameter is required")
		return
	}

	var opts []cortex.RecallOption
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		limit, err := strconv.Atoi(limitStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid limit: "+err.Error())
			return
		}
		opts = append(opts, cortex.WithLimit(limit))
	}

	ctx := r.Context()
	results, err := cx.Recall(ctx, query, opts...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, results)
}

func handleForget(w http.ResponseWriter, r *http.Request) {
	entityID := r.URL.Query().Get("entity_id")
	source := r.URL.Query().Get("source")

	if entityID == "" && source == "" {
		writeError(w, http.StatusBadRequest, "entity_id or source parameter is required")
		return
	}

	ctx := r.Context()
	filter := cortex.Filter{EntityID: entityID, Source: source}
	if err := cx.Forget(ctx, filter); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "forgotten"})
}

func handleGetEntity(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "id is required")
		return
	}

	ctx := r.Context()
	entity, err := cx.GetEntity(ctx, id)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, entity)
}

func handleFindEntities(w http.ResponseWriter, r *http.Request) {
	filter := cortex.EntityFilter{
		Type:     r.URL.Query().Get("type"),
		NameLike: r.URL.Query().Get("name"),
		Source:   r.URL.Query().Get("source"),
	}

	ctx := r.Context()
	entities, err := cx.FindEntities(ctx, filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if entities == nil {
		entities = []cortex.Entity{}
	}
	writeJSON(w, http.StatusOK, entities)
}

func handleGetRelationships(w http.ResponseWriter, r *http.Request) {
	entityID := r.PathValue("entity_id")
	if entityID == "" {
		writeError(w, http.StatusBadRequest, "entity_id is required")
		return
	}

	var filters []cortex.RelFilter
	if relType := r.URL.Query().Get("type"); relType != "" {
		filters = append(filters, cortex.RelTypeFilter(relType))
	}

	ctx := r.Context()
	rels, err := cx.GetRelationships(ctx, entityID, filters...)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rels == nil {
		rels = []cortex.Relationship{}
	}
	writeJSON(w, http.StatusOK, rels)
}

func handleTraverse(w http.ResponseWriter, r *http.Request) {
	entityID := r.PathValue("entity_id")
	if entityID == "" {
		writeError(w, http.StatusBadRequest, "entity_id is required")
		return
	}

	var opts []cortex.TraverseOption
	if depthStr := r.URL.Query().Get("depth"); depthStr != "" {
		depth, err := strconv.Atoi(depthStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid depth: "+err.Error())
			return
		}
		opts = append(opts, cortex.WithDepth(depth))
	}
	if edgeTypesStr := r.URL.Query().Get("edge_types"); edgeTypesStr != "" {
		types := strings.Split(edgeTypesStr, ",")
		for i := range types {
			types[i] = strings.TrimSpace(types[i])
		}
		opts = append(opts, cortex.WithEdgeTypes(types...))
	}

	ctx := r.Context()
	graph, err := cx.Traverse(ctx, entityID, opts...)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			writeError(w, http.StatusNotFound, err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, graph)
}

func handleSearch(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	if query == "" {
		writeError(w, http.StatusBadRequest, "q parameter is required")
		return
	}
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "keyword"
	}

	limit := 10
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		var err error
		limit, err = strconv.Atoi(limitStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid limit: "+err.Error())
			return
		}
	}

	ctx := r.Context()
	switch mode {
	case "keyword":
		chunks, err := cx.SearchKeyword(ctx, query, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if chunks == nil {
			chunks = []cortex.Chunk{}
		}
		writeJSON(w, http.StatusOK, chunks)
	case "vector":
		chunks, err := cx.SearchVector(ctx, query, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if chunks == nil {
			chunks = []cortex.Chunk{}
		}
		writeJSON(w, http.StatusOK, chunks)
	case "memory":
		memories, err := cx.SearchMemories(ctx, query, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if memories == nil {
			memories = []cortex.Memory{}
		}
		writeJSON(w, http.StatusOK, memories)
	default:
		writeError(w, http.StatusBadRequest, "invalid mode: "+mode+"; must be keyword, vector, or memory")
	}
}

// --- Helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
