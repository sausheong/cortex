package cortex

import "context"

// Test-only wrappers for methods that moved to internal/store. White-box
// tests in this package call them under their old names.

func (c *Cortex) migrate(ctx context.Context) error { return c.h.Migrate(ctx) }

func (c *Cortex) migrateUpTo(ctx context.Context, max int) error { return c.h.MigrateUpTo(ctx, max) }
