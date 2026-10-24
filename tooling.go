package cortex

import (
	"database/sql"

	"github.com/sausheong/cortex/internal/memory"
)

// DB returns the underlying database handle. It is intended for tooling
// (such as the SQLite importer); normal callers should use the Cortex API.
func (c *Cortex) DB() *sql.DB { return c.h.DB.DB }

// Embedder returns the configured embedder, or nil. Intended for tooling.
func (c *Cortex) Embedder() Embedder { return c.h.Cfg.Embedder }

// AugmentForEmbedding builds the text embedded for a memory: its content
// prefixed with the names of the entities it concerns. Intended for tooling.
func AugmentForEmbedding(content string, entityNames []string) string {
	return memory.AugmentForEmbedding(content, entityNames)
}
