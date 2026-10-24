package cortex

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestMaintain_SurvivesLLMFailureAndRunsLaterPasses(t *testing.T) {
	forEachMode(t, testMaintain_SurvivesLLMFailureAndRunsLaterPasses)
}

func testMaintain_SurvivesLLMFailureAndRunsLaterPasses(t *testing.T) {
	fastBackoff(t)
	c := openTestDB(t)
	ctx := context.Background()
	owner := &Entity{Type: "person", Name: "Me", Source: "owner"}
	if err := c.PutEntity(ctx, owner); err != nil {
		t.Fatal(err)
	}
	markOwner(t, c, owner.ID)
	for i := 0; i < 2; i++ {
		if err := c.PutMemory(ctx, &Memory{Content: fmt.Sprintf("fact %d", i), EntityIDs: []string{owner.ID}}); err != nil {
			t.Fatal(err)
		}
	}
	c.SetLLM(&mockLLM{
		detectConflictsFn: func(context.Context, []Memory) ([]ConflictPair, error) {
			return nil, errors.New("net/http: timeout awaiting response headers")
		},
		detectRelationsFn: func(context.Context, []Memory) ([]MemoryRelation, error) {
			return nil, errors.New("net/http: timeout awaiting response headers")
		},
		summarizeFn: func(context.Context, []string) (string, error) {
			return "", errors.New("net/http: timeout awaiting response headers")
		},
	})
	rep, err := c.Maintain(ctx)
	if err != nil {
		t.Fatalf("maintain aborted: %v", err)
	}
	if rep.Decay == nil || rep.Expire == nil || rep.Profile == nil {
		t.Fatalf("later passes did not run: %+v", rep)
	}
	if rep.Reconcile.EntitiesSkipped != 1 || rep.Relate.EntitiesSkipped != 1 || rep.Profile.Undistilled != 1 || rep.SkippedItems() != 3 {
		t.Fatalf("reports: %+v %+v %+v", rep.Reconcile, rep.Relate, rep.Profile)
	}
}

func TestProfile_StaticSelectionIsDiverse(t *testing.T) {
	forEachMode(t, testProfile_StaticSelectionIsDiverse)
}

func testProfile_StaticSelectionIsDiverse(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	owner := &Entity{Type: "person", Name: "Me", Source: "owner"}
	sam := &Entity{Type: "person", Name: "Sam"}
	for _, e := range []*Entity{owner, sam} {
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	markOwner(t, c, owner.ID)
	// 20 recent, near-identical static memories about one acquaintance.
	for i := 0; i < 20; i++ {
		m := &Memory{Content: fmt.Sprintf("Sam Tan had coffee with the owner at the Starbucks near work on day %d", i),
			EntityIDs: []string{owner.ID, sam.ID}, Static: true, Confidence: 1}
		if err := c.PutMemory(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	varied := []string{
		"The owner is a software engineer who founded a startup",
		"The owner grew up in Singapore and studied computer science at NUS",
		"The owner prefers concise answers and dark mode",
		"The owner is skilled at Go and distributed systems",
		"The owner is married with two daughters",
		"The owner enjoys long-distance running",
	}
	for _, v := range varied {
		if err := c.PutMemory(ctx, &Memory{Content: v, EntityIDs: []string{owner.ID}, Static: true, Confidence: 0.8}); err != nil {
			t.Fatal(err)
		}
	}
	var input []string
	c.SetLLM(&mockLLM{summarizeFn: func(_ context.Context, texts []string) (string, error) {
		if strings.Contains(texts[0], "stable, long-term") {
			input = append([]string(nil), texts[1:]...)
		}
		return "- bullet", nil
	}})
	p, err := c.Profile(ctx, owner.ID)
	if err != nil || !p.Distilled {
		t.Fatalf("%+v %v", p, err)
	}
	joined := strings.Join(input, "\n")
	for _, v := range varied {
		if !strings.Contains(joined, v) {
			t.Errorf("missing varied memory %q", v)
		}
	}
	dups := 0
	for _, s := range input {
		if strings.Contains(s, "Sam Tan") {
			dups++
		}
	}
	if dups < 1 || dups > 3 {
		t.Fatalf("duplicates in distill input = %d, want 1..3", dups)
	}
}
