package cortex

import (
	"database/sql"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/memory"
	"github.com/sausheong/cortex/internal/store"
	// Linked for its init: installs the store.OnWrite cache-invalidation hook.
	_ "github.com/sausheong/cortex/internal/workset"
)

// Cortex is the main knowledge graph store backed by Postgres.
type Cortex struct {
	h *store.Handle
}

// Open connects to the Postgres database at dsn and applies pending migrations.
// Options are applied before schema setup.
func Open(dsn string, opts ...Option) (*Cortex, error) {
	h, err := store.Connect(dsn, opts...)
	if err != nil {
		return nil, err
	}
	c := &Cortex{h: h}
	mctx, mcancel := h.MigrationContext()
	defer mcancel()
	if err := h.Migrate(mctx); err != nil {
		h.Close()
		return nil, err
	}
	if err := memory.New(c.h).EnsureVectorSchema(mctx); err != nil {
		h.Close()
		return nil, err
	}
	return c, nil
}

// Close closes the underlying database connection.
func (c *Cortex) Close() error { return c.h.Close() }

// UserID returns the ID of the user this handle is bound to.
func (c *Cortex) UserID() string { return c.h.UserID }

// ForUser returns a handle bound to another user. It shares the database
// connection but has its own configuration; opts override the LLM, embedder
// and extractor.
func (c *Cortex) ForUser(id string, opts ...Option) *Cortex {
	h2 := *c.h
	h2.UserID = id
	for _, o := range opts {
		o(&h2.Cfg)
	}
	return &Cortex{h: &h2}
}

// WithConnection returns a copy of the handle whose writes record connID as
// written_by_connection_id.
func (c *Cortex) WithConnection(connID string) *Cortex {
	h2 := *c.h
	h2.ConnID = connID
	return &Cortex{h: &h2}
}

// ConnectionID is the connection this handle stamps on its writes, "" when none.
func (c *Cortex) ConnectionID() string { return c.h.ConnID }

// WithKey returns a copy of the handle holding its own clone of uk, with the
// user's encryption state. A background goroutine that outlives the request
// must call WithKey itself before the request ends.
func (c *Cortex) WithKey(uk *keys.UK, state string) *Cortex {
	return &Cortex{h: c.h.WithKey(uk, state)}
}

// Release zeroes this handle's own key copy.
func (c *Cortex) Release() { c.h.Release() }

// Encrypted reports whether this handle's user is enrolled in encryption.
func (c *Cortex) Encrypted() bool { return c.h.Encrypted() }

// CloneKey returns a copy of this handle's user key for a caller that must
// seal data outside the handle (for example a queued job payload). The caller
// zeroes it. Nil when the handle holds no key.
func (c *Cortex) CloneKey() *keys.UK {
	if c.h.Key == nil {
		return nil
	}
	return c.h.Key.Clone()
}

// SetExtractor sets the extractor on the Cortex instance.
func (c *Cortex) SetExtractor(e Extractor) { c.h.Cfg.Extractor = e }

// SetLLM sets the LLM on the Cortex instance.
func (c *Cortex) SetLLM(l LLM) { c.h.Cfg.LLM = l }

// SetEmbedder sets the embedder on the Cortex instance.
func (c *Cortex) SetEmbedder(e Embedder) { c.h.Cfg.Embedder = e }

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
