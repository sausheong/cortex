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

// Connector ingests conversation messages into a Cortex knowledge graph.
type Connector struct{}

// New creates a new conversation Connector.
func New() *Connector { return &Connector{} }

// Ingest processes a slice of conversation messages, concatenates them into
// a single text block, and stores them in the knowledge graph via Remember.
func (c *Connector) Ingest(ctx context.Context, cx *cortex.Cortex, messages []Message) error {
	if len(messages) == 0 {
		return nil
	}

	var parts []string
	for _, m := range messages {
		parts = append(parts, fmt.Sprintf("%s: %s", m.Role, m.Content))
	}
	text := strings.Join(parts, "\n\n")

	return cx.Remember(ctx, text, cortex.WithSource("conversation"), cortex.WithContentType("conversation"))
}
