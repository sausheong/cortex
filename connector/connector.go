package connector

import (
	"context"

	"github.com/sausheong/cortex"
)

// Connector defines the interface for syncing external data sources
// into a Cortex knowledge graph.
type Connector interface {
	Sync(ctx context.Context, c *cortex.Cortex) error
}
