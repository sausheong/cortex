package cortex

import (
	"context"
	"fmt"
)

// Reserved entity attribute keys for profiles.
const (
	attrProfile  = "_profile"  // cached digest (cachedProfile, JSON)
	attrProfiled = "_profiled" // tracking marker (bool)
)

// TrackProfile marks an entity for automatic profile refresh during Maintain.
// The owner entity is always eligible regardless of this marker.
func (c *Cortex) TrackProfile(ctx context.Context, entityID string) error {
	e, err := c.GetEntity(ctx, entityID)
	if err != nil {
		return err
	}
	if e.Attributes == nil {
		e.Attributes = map[string]any{}
	}
	e.Attributes[attrProfiled] = true
	return c.PutEntity(ctx, e)
}

// UntrackProfile removes the tracking marker and drops any cached digest.
func (c *Cortex) UntrackProfile(ctx context.Context, entityID string) error {
	e, err := c.GetEntity(ctx, entityID)
	if err != nil {
		return err
	}
	if e.Attributes != nil {
		delete(e.Attributes, attrProfiled)
		delete(e.Attributes, attrProfile)
	}
	return c.PutEntity(ctx, e)
}

// profileEligibleIDs returns the IDs of entities that get auto-refreshed
// profiles: the owner entity (source=owner) plus any entity flagged via
// TrackProfile. Order is owner(s) first, then tracked; duplicates removed.
func (c *Cortex) profileEligibleIDs(ctx context.Context) ([]string, error) {
	seen := map[string]bool{}
	var ids []string

	owners, err := c.FindEntities(ctx, EntityFilter{Source: "owner"})
	if err != nil {
		return nil, fmt.Errorf("cortex: find owner entities: %w", err)
	}
	for _, e := range owners {
		if !seen[e.ID] {
			seen[e.ID] = true
			ids = append(ids, e.ID)
		}
	}

	all, err := c.FindEntities(ctx, EntityFilter{})
	if err != nil {
		return nil, fmt.Errorf("cortex: find entities: %w", err)
	}
	for _, e := range all {
		if v, _ := e.Attributes[attrProfiled].(bool); v && !seen[e.ID] {
			seen[e.ID] = true
			ids = append(ids, e.ID)
		}
	}
	return ids, nil
}
