package cortex

import (
	"context"
	"testing"
	"time"
)

func TestExpireMemories_RetiresPastDue(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()

	e := &Entity{Type: "person", Name: "Alice"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-24 * time.Hour)
	future := time.Now().UTC().Add(24 * time.Hour)

	expired := &Memory{Content: "Alice meeting yesterday", EntityIDs: []string{e.ID}, ForgetAfter: &past}
	if err := c.PutMemory(ctx, expired); err != nil {
		t.Fatal(err)
	}
	kept := &Memory{Content: "Alice meeting tomorrow", EntityIDs: []string{e.ID}, ForgetAfter: &future}
	if err := c.PutMemory(ctx, kept); err != nil {
		t.Fatal(err)
	}
	noExpiry := &Memory{Content: "Alice is an engineer", EntityIDs: []string{e.ID}}
	if err := c.PutMemory(ctx, noExpiry); err != nil {
		t.Fatal(err)
	}

	rep, err := c.ExpireMemories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Expired != 1 {
		t.Fatalf("Expired = %d, want 1", rep.Expired)
	}
	if len(rep.Changes) != 1 || rep.Changes[0].ID != expired.ID {
		t.Fatalf("expected only %s expired, got %+v", expired.ID, rep.Changes)
	}

	// Default recall (currently-valid) no longer sees the expired memory…
	valid, err := c.GetMemoriesByEntity(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range valid {
		if m.ID == expired.ID {
			t.Error("expired memory should be hidden from default recall")
		}
	}
	if len(valid) != 2 {
		t.Errorf("expected 2 valid memories remaining, got %d", len(valid))
	}
}

func TestExpireMemories_StaticStillExpires(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	e := &Entity{Type: "person", Name: "Bob"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Hour)
	// Static AND past-due: TTL wins.
	m := &Memory{Content: "temp", EntityIDs: []string{e.ID}, Static: true, ForgetAfter: &past}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	rep, err := c.ExpireMemories(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Expired != 1 {
		t.Errorf("static past-due memory should expire (TTL wins); Expired=%d", rep.Expired)
	}
}

func TestExpireMemories_DryRunWritesNothing(t *testing.T) {
	c := openTestDB(t)
	ctx := context.Background()
	e := &Entity{Type: "person", Name: "Carol"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Hour)
	m := &Memory{Content: "old", EntityIDs: []string{e.ID}, ForgetAfter: &past}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	rep, err := c.ExpireMemories(ctx, WithExpireDryRun())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Expired != 1 || !rep.DryRun {
		t.Errorf("dry-run should report 1 would-expire, DryRun=true; got Expired=%d DryRun=%v", rep.Expired, rep.DryRun)
	}
	// Still currently-valid (nothing written).
	valid, err := c.GetMemoriesByEntity(ctx, e.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(valid) != 1 {
		t.Errorf("dry-run must not retire; expected 1 valid, got %d", len(valid))
	}
}
