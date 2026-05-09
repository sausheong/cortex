package testutil

import (
	"context"
	"fmt"

	"github.com/sausheong/cortex"
)

// Team holds every object seeded by SeedTeam, keyed by logical name so
// tests can reference entities and relationships by field rather than by ID.
type Team struct {
	// People
	Alice cortex.Entity
	Bob   cortex.Entity
	Carol cortex.Entity
	Dave  cortex.Entity
	Eve   cortex.Entity

	// Organizations
	Acme     cortex.Entity
	WidgetCo cortex.Entity

	// Relationships
	AliceAtAcme        cortex.Relationship // Alice works_at Acme
	BobAtAcme          cortex.Relationship // Bob works_at Acme
	CarolAtAcme        cortex.Relationship // Carol works_at Acme
	DaveAtAcme         cortex.Relationship // Dave works_at Acme
	AliceKnowsEve      cortex.Relationship // Alice knows Eve
	AliceWorkedAtWidget cortex.Relationship // Alice worked_at WidgetCo
	BobReportsToAlice  cortex.Relationship // Bob reports_to Alice

	// Memories
	MemAliceJoined  cortex.Memory // Alice joined Acme after WidgetCo
	MemPayments     cortex.Memory // Bob and Carol on payments feature
	MemRoadmap      cortex.Memory // Dave coordinating Q3 roadmap
	MemOnboarding   cortex.Memory // Eve designed onboarding flow

	// Text chunks (keyword / vector searchable)
	ChunkAliceBio      cortex.Chunk // Alice's biography
	ChunkProjectStatus cortex.Chunk // Project Alpha status update
	ChunkMeetingNotes  cortex.Chunk // Q3 planning meeting notes
}

// SeedTeam populates cx with a small fictional Acme Corp dataset and returns
// handles to every created object. It does not require an LLM or embedder —
// all data is inserted directly via Put* methods using the "team-seed" source.
//
// Dataset overview:
//   - 7 entities: Alice, Bob, Carol, Dave, Eve (people) + Acme Corp, WidgetCo (orgs)
//   - 7 relationships: works_at ×4, worked_at, knows, reports_to
//   - 4 memories covering career history, project work, roadmap, and design
//   - 3 text chunks: Alice's bio, a project status doc, meeting notes
func SeedTeam(ctx context.Context, cx *cortex.Cortex) (*Team, error) {
	team := &Team{}

	// ── People ──────────────────────────────────────────────────────────────
	people := []struct {
		dst   *cortex.Entity
		name  string
		attrs map[string]any
	}{
		{&team.Alice, "Alice Chen", map[string]any{"role": "CTO", "joined": "2022"}},
		{&team.Bob, "Bob Kim", map[string]any{"role": "engineer", "team": "payments"}},
		{&team.Carol, "Carol Reyes", map[string]any{"role": "engineer", "team": "payments"}},
		{&team.Dave, "Dave Patel", map[string]any{"role": "product manager"}},
		{&team.Eve, "Eve Morgan", map[string]any{"role": "designer", "kind": "freelancer"}},
	}
	for _, p := range people {
		e := &cortex.Entity{Type: "person", Name: p.name, Attributes: p.attrs, Source: "team-seed"}
		if err := cx.PutEntity(ctx, e); err != nil {
			return nil, fmt.Errorf("seed entity %q: %w", p.name, err)
		}
		*p.dst = *e
	}

	// ── Organizations ────────────────────────────────────────────────────────
	orgs := []struct {
		dst  *cortex.Entity
		name string
	}{
		{&team.Acme, "Acme Corp"},
		{&team.WidgetCo, "WidgetCo"},
	}
	for _, o := range orgs {
		e := &cortex.Entity{Type: "organization", Name: o.name, Source: "team-seed"}
		if err := cx.PutEntity(ctx, e); err != nil {
			return nil, fmt.Errorf("seed org %q: %w", o.name, err)
		}
		*o.dst = *e
	}

	// ── Relationships ────────────────────────────────────────────────────────
	rels := []struct {
		dst    *cortex.Relationship
		srcID  string
		tgtID  string
		relTyp string
	}{
		{&team.AliceAtAcme, team.Alice.ID, team.Acme.ID, "works_at"},
		{&team.BobAtAcme, team.Bob.ID, team.Acme.ID, "works_at"},
		{&team.CarolAtAcme, team.Carol.ID, team.Acme.ID, "works_at"},
		{&team.DaveAtAcme, team.Dave.ID, team.Acme.ID, "works_at"},
		{&team.AliceKnowsEve, team.Alice.ID, team.Eve.ID, "knows"},
		{&team.AliceWorkedAtWidget, team.Alice.ID, team.WidgetCo.ID, "worked_at"},
		{&team.BobReportsToAlice, team.Bob.ID, team.Alice.ID, "reports_to"},
	}
	for _, r := range rels {
		rel := &cortex.Relationship{
			SourceID: r.srcID,
			TargetID: r.tgtID,
			Type:     r.relTyp,
			Source:   "team-seed",
		}
		if err := cx.PutRelationship(ctx, rel); err != nil {
			return nil, fmt.Errorf("seed relationship %s→%s (%s): %w", r.srcID, r.tgtID, r.relTyp, err)
		}
		*r.dst = *rel
	}

	// ── Memories ─────────────────────────────────────────────────────────────
	mems := []struct {
		dst       *cortex.Memory
		content   string
		entityIDs []string
	}{
		{
			&team.MemAliceJoined,
			"Alice Chen joined Acme Corp as CTO in 2022 after leaving WidgetCo where she was VP of Engineering.",
			[]string{team.Alice.ID, team.Acme.ID, team.WidgetCo.ID},
		},
		{
			&team.MemPayments,
			"Bob Kim and Carol Reyes are working on the payments feature integration with Stripe.",
			[]string{team.Bob.ID, team.Carol.ID},
		},
		{
			&team.MemRoadmap,
			"Dave Patel is coordinating the Q3 product roadmap and aligning it with engineering capacity.",
			[]string{team.Dave.ID},
		},
		{
			&team.MemOnboarding,
			"Eve Morgan designed the new user onboarding flow that launched last month.",
			[]string{team.Eve.ID},
		},
	}
	for _, m := range mems {
		mem := &cortex.Memory{Content: m.content, EntityIDs: m.entityIDs, Source: "team-seed"}
		if err := cx.PutMemory(ctx, mem); err != nil {
			return nil, fmt.Errorf("seed memory: %w", err)
		}
		*m.dst = *mem
	}

	// ── Text chunks ───────────────────────────────────────────────────────────
	chunks := []struct {
		dst     *cortex.Chunk
		content string
		meta    map[string]any
	}{
		{
			&team.ChunkAliceBio,
			"Alice Chen is the CTO of Acme Corp. She joined in 2022 after leaving WidgetCo, " +
				"where she was VP of Engineering. Alice has 15 years of experience in distributed " +
				"systems and previously led the platform team at WidgetCo.",
			map[string]any{"type": "bio", "subject": "Alice Chen"},
		},
		{
			&team.ChunkProjectStatus,
			"Project Alpha – Status Update: Bob Kim and Carol Reyes are working on the payments " +
				"feature integration. Target completion is end of Q3. Current blocker: Stripe API " +
				"rate limiting. Dave Patel is tracking this in the product roadmap.",
			map[string]any{"type": "project-update", "project": "alpha"},
		},
		{
			&team.ChunkMeetingNotes,
			"Q3 Planning Meeting – Action items: Dave Patel to finalise the product roadmap by " +
				"Friday. Alice Chen to review engineering capacity. Bob Kim to prototype the new " +
				"checkout flow. Eve Morgan's onboarding designs to be reviewed next week.",
			map[string]any{"type": "meeting-notes", "quarter": "Q3"},
		},
	}
	for _, ch := range chunks {
		c := &cortex.Chunk{Content: ch.content, Metadata: ch.meta}
		if err := cx.PutChunk(ctx, c); err != nil {
			return nil, fmt.Errorf("seed chunk: %w", err)
		}
		*ch.dst = *c
	}

	return team, nil
}
