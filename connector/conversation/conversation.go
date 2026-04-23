package conversation

import (
	"context"
	"fmt"
	"strings"

	"github.com/sausheong/cortex"
)

// Message represents a single message in a conversation.
type Message struct {
	Role    string
	Content string
}

// defaultMaxChunkChars caps each stored chunk so it fits within the context
// window of common embedding models (nomic-embed-text, bge-*, OpenAI
// text-embedding-3-*). ~6000 chars ≈ 1500 tokens.
const defaultMaxChunkChars = 6000

// Connector ingests conversation messages into a Cortex knowledge graph.
//
// MaxChunkChars overrides the default per-chunk character cap used when
// embedding long threads. Zero (the default) uses defaultMaxChunkChars; set
// to a negative value to disable splitting entirely.
type Connector struct {
	MaxChunkChars int
}

// New creates a new conversation Connector with default settings.
func New() *Connector { return &Connector{} }

// Ingest processes a slice of conversation messages, concatenates them into
// a single text block, and stores them in the knowledge graph via Remember.
// Long threads are split into multiple chunks so each fits within the
// embedder's context window.
func (c *Connector) Ingest(ctx context.Context, cx *cortex.Cortex, messages []Message) error {
	if len(messages) == 0 {
		return nil
	}

	var parts []string
	for _, m := range messages {
		parts = append(parts, fmt.Sprintf("%s: %s", m.Role, m.Content))
	}
	text := strings.Join(parts, "\n\n")

	max := c.MaxChunkChars
	if max == 0 {
		max = defaultMaxChunkChars
	} else if max < 0 {
		max = 0 // disable splitting
	}

	return cx.Remember(ctx, text,
		cortex.WithSource("conversation"),
		cortex.WithContentType("conversation"),
		cortex.WithMaxChunkChars(max),
	)
}
