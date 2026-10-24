package cortex

import (
	"context"
	"time"

	"github.com/sausheong/cortex/internal/tenancy"
)

// CreateUser inserts a user and its person entity (Source "owner"), whose ID
// is stored in users.owner_entity_id.
func (c *Cortex) CreateUser(ctx context.Context, googleSub, email, name string, emailVerified bool) (*User, error) {
	return tenancy.New(c.h).CreateUser(ctx, googleSub, email, name, emailVerified)
}

// UserByGoogleSub returns the user with the given Google subject, or ErrNotFound.
func (c *Cortex) UserByGoogleSub(ctx context.Context, sub string) (*User, error) {
	return tenancy.New(c.h).UserByGoogleSub(ctx, sub)
}

// ListUsers returns every user, oldest first.
func (c *Cortex) ListUsers(ctx context.Context) ([]User, error) {
	return tenancy.New(c.h).ListUsers(ctx)
}

// BindBootstrap attaches a Google identity to the bootstrap user, keeping
// its existing data. It fails if the bootstrap user is already bound.
func (c *Cortex) BindBootstrap(ctx context.Context, googleSub, email, name string) (*User, error) {
	return tenancy.New(c.h).BindBootstrap(ctx, googleSub, email, name)
}

// ResolveUserRef resolves an admin reference: a user ID, else a verified
// email. Several matches is an error listing the IDs; none is ErrNotFound.
func (c *Cortex) ResolveUserRef(ctx context.Context, ref string) (*User, error) {
	return tenancy.New(c.h).ResolveUserRef(ctx, ref)
}

// OwnerEntityID returns the ID of the handle's user's owner (person) entity,
// or ErrNotFound when the user has none.
func (c *Cortex) OwnerEntityID(ctx context.Context) (string, error) {
	return tenancy.New(c.h).OwnerEntityID(ctx)
}

// ComputeUsage measures this user's rows now. Bytes is the sum of
// pg_column_size over every row the user owns, including embeddings of all
// generations and soft-deleted rows (they still occupy space until purged).
func (c *Cortex) ComputeUsage(ctx context.Context) (Usage, error) {
	return tenancy.New(c.h).ComputeUsage(ctx)
}

// RefreshUsage computes the usage and stores it in users.usage_json (the
// nightly job calls this).
func (c *Cortex) RefreshUsage(ctx context.Context) (Usage, error) {
	return tenancy.New(c.h).RefreshUsage(ctx)
}

// CachedUsage returns the figures stored by the nightly job. ok is false
// when none are cached yet; it never measures, so it is cheap on a page load.
func (c *Cortex) CachedUsage(ctx context.Context) (u Usage, ok bool, err error) {
	return tenancy.New(c.h).CachedUsage(ctx)
}

// Usage returns the cached figures, or computes them live when none are cached.
func (c *Cortex) Usage(ctx context.Context) (Usage, error) {
	return tenancy.New(c.h).Usage(ctx)
}

// NewMemoriesSince groups this user's live memories created after since by
// the connection that wrote them, with each group's newest perGroup
// memories. Groups are ordered by their newest memory, newest first. It is
// one indexed query and never calls a model.
func (c *Cortex) NewMemoriesSince(ctx context.Context, since time.Time, perGroup int) ([]ConnectionNew, error) {
	return tenancy.New(c.h).NewMemoriesSince(ctx, since, perGroup)
}

// WrittenSince counts this user's live memories created after since, per
// writing connection ID ("" for none).
func (c *Cortex) WrittenSince(ctx context.Context, since time.Time) (map[string]int, error) {
	return tenancy.New(c.h).WrittenSince(ctx, since)
}

// ListMemories pages this user's live memories, newest first unless
// f.Oldest, and returns the total number matching the filter.
func (c *Cortex) ListMemories(ctx context.Context, f MemoryListFilter, limit, offset int) ([]Memory, int, error) {
	return tenancy.New(c.h).ListMemories(ctx, f, limit, offset)
}

// AppliedMigrations returns the versions recorded in schema_migrations, ascending.
func (c *Cortex) AppliedMigrations(ctx context.Context) ([]int, error) {
	return c.h.AppliedMigrations(ctx)
}
