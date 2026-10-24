package cortex

import (
	"context"

	"github.com/sausheong/cortex/internal/maintain"
	"github.com/sausheong/cortex/internal/memory"
	"github.com/sausheong/cortex/internal/model"
)

// Test-only wrappers for methods that moved to internal/store and
// internal/memory. White-box tests in this package call them under their old
// names.

func (c *Cortex) migrate(ctx context.Context) error { return c.h.Migrate(ctx) }

func (c *Cortex) migrateUpTo(ctx context.Context, max int) error { return c.h.MigrateUpTo(ctx, max) }

func (c *Cortex) putEmbedding(ctx context.Context, refID, refType string, v []float32) error {
	return memory.New(c.h).PutEmbedding(ctx, refID, refType, v)
}

func (c *Cortex) getMemoryByID(ctx context.Context, id string) (*Memory, error) {
	return memory.New(c.h).GetMemoryByID(ctx, id)
}

func (c *Cortex) embeddingByRef(ctx context.Context, refID, refType string) []float32 {
	return memory.New(c.h).EmbeddingByRef(ctx, refID, refType)
}

func (c *Cortex) searchVectorRaw(ctx context.Context, queryVec []float32, refType string, limit int) ([]memory.VectorResult, error) {
	return memory.New(c.h).SearchVectorRaw(ctx, queryVec, refType, limit)
}

func (c *Cortex) searchMemoriesMode(ctx context.Context, query string, limit int, mode model.TemporalMode) ([]Memory, error) {
	return memory.New(c.h).SearchMemoriesMode(ctx, query, limit, mode)
}

func (c *Cortex) neighbourhood(ctx context.Context, seeds []string, hops int, mode model.TemporalMode) (map[string]int, error) {
	return memory.New(c.h).Neighbourhood(ctx, seeds, hops, mode)
}

const reembedLockSQL = memory.ReembedLockSQL

func vectorLiteral(v []float32) string { return memory.VectorLiteral(v) }

// Wrappers for internal/maintain.

const (
	attrProfile         = maintain.AttrProfile
	attrProfiled        = maintain.AttrProfiled
	profileStaticPrompt = maintain.ProfileStaticPrompt
)

func (c *Cortex) profileEligibleIDs(ctx context.Context) ([]string, error) {
	return maintain.New(c.h).ProfileEligibleIDs(ctx)
}

func (c *Cortex) distillBucket(ctx context.Context, name, instruction string, mems []Memory) ([]string, bool) {
	return maintain.New(c.h).DistillBucket(ctx, name, instruction, mems)
}
