package cortex

import (
	"context"
	"time"

	"github.com/sausheong/cortex/internal/query"
)

// WhoIs resolves a mention (a name, alias, email, phone or handle) and
// returns the entity with its aliases, current relationships (named) and
// current memories, static ones first. When the mention does not resolve
// confidently, the card is empty and Candidates lists the closest entities.
func (c *Cortex) WhoIs(ctx context.Context, mention string) (*EntityCard, error) {
	return query.New(c.h).WhoIs(ctx, mention)
}

// Describe returns the EntityCard of a known entity.
func (c *Cortex) Describe(ctx context.Context, entityID string) (*EntityCard, error) {
	return query.New(c.h).Describe(ctx, entityID)
}

// Path returns a shortest chain of current relationships linking two
// entities (in either direction), at most maxHops long (default 4, capped at
// 6), or nil when they are not connected within that distance. The owner is
// only used as an end, never as a stepping stone: everything connects
// through the owner, which says nothing.
func (c *Cortex) Path(ctx context.Context, fromID, toID string, maxHops int) ([]NamedEdge, error) {
	return query.New(c.h).Path(ctx, fromID, toID, maxHops)
}

// Timeline returns an entity's history, oldest first: its memories (dated by
// when they became true, else when they were recorded), and the start and
// end of its relationships. since, when non-zero, drops earlier events;
// limit (default 100) keeps the most recent. Retired memories are included,
// so the timeline shows what used to be true.
func (c *Cortex) Timeline(ctx context.Context, entityID string, since time.Time, limit int) ([]TimelineEvent, error) {
	return query.New(c.h).Timeline(ctx, entityID, since, limit)
}

// WhatChanged lists what was added to or retired from the graph since t:
// new entities, memories and relationships (by when they were recorded),
// memories that stopped being current, and relationships whose end falls
// after t (by when they ended, which for a back-dated end may be before it
// was recorded). Each list holds at most limit items (default 50), newest
// first.
func (c *Cortex) WhatChanged(ctx context.Context, since time.Time, limit int) (*Changes, error) {
	return query.New(c.h).WhatChanged(ctx, since, limit)
}

// Link records a relationship known for certain (from structured data such
// as an email header), without free-text extraction. Each end is an entity
// ID, or a name or identifier resolved like an ingested mention; one that
// resolves to nothing is created with the given type (default person), and
// an identifier used as its name is kept as an alias. The verb is mapped
// onto the curated vocabulary as in Remember.
func (c *Cortex) Link(ctx context.Context, r LinkRequest) (*NamedEdge, error) {
	return query.New(c.h).Link(ctx, r)
}
