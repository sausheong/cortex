package cortex

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sausheong/cortex/internal/llmutil"
)

func fastBackoff(t *testing.T) {
	t.Helper()
	old := llmutil.Backoff
	llmutil.Backoff = func(int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { llmutil.Backoff = old })
}

func TestReconcile_TimeoutSkipsEntityAndContinues(t *testing.T) {
	forEachMode(t, testReconcile_TimeoutSkipsEntityAndContinues)
}

func testReconcile_TimeoutSkipsEntityAndContinues(t *testing.T) {
	fastBackoff(t)
	c := openTestDB(t)
	ctx := context.Background()
	mk := func(name string) *Entity {
		e := &Entity{Type: "person", Name: name}
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if err := c.PutMemory(ctx, &Memory{Content: fmt.Sprintf("%s fact %d", name, i), EntityIDs: []string{e.ID}}); err != nil {
				t.Fatal(err)
			}
		}
		return e
	}
	mk("Slow")
	mk("Fast")
	var goodCalls, badCalls atomic.Int32
	c.SetLLM(&mockLLM{detectConflictsFn: func(_ context.Context, mems []Memory) ([]ConflictPair, error) {
		if strings.HasPrefix(mems[0].Content, "Slow") {
			badCalls.Add(1)
			return nil, errors.New("openai: detect conflicts: net/http: timeout awaiting response headers")
		}
		goodCalls.Add(1)
		return nil, nil
	}})
	rep, err := c.Reconcile(ctx)
	if err != nil {
		t.Fatalf("reconcile aborted: %v", err)
	}
	if rep.EntitiesSkipped != 1 || len(rep.Errors) != 1 || !strings.Contains(rep.Errors[0], "timeout") {
		t.Fatalf("report: %+v", rep)
	}
	if badCalls.Load() != 3 || goodCalls.Load() != 1 {
		t.Fatalf("calls bad=%d good=%d (want 3 attempts, 1)", badCalls.Load(), goodCalls.Load())
	}
	if !strings.Contains(RenderReconcileMarkdown(rep), "skipped") {
		t.Fatal("render must mention skipped entities")
	}
}

func TestReconcile_429ThenSuccessRetried(t *testing.T) {
	forEachMode(t, testReconcile_429ThenSuccessRetried)
}

func testReconcile_429ThenSuccessRetried(t *testing.T) {
	fastBackoff(t)
	c := openTestDB(t)
	ctx := context.Background()
	e := &Entity{Type: "person", Name: "Alice"}
	if err := c.PutEntity(ctx, e); err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{"a", "b"} {
		if err := c.PutMemory(ctx, &Memory{Content: "Alice " + s, EntityIDs: []string{e.ID}}); err != nil {
			t.Fatal(err)
		}
	}
	var n atomic.Int32
	c.SetLLM(&mockLLM{detectConflictsFn: func(context.Context, []Memory) ([]ConflictPair, error) {
		if n.Add(1) == 1 {
			return nil, errors.New("error, status code: 429, status: 429 Too Many Requests")
		}
		return nil, nil
	}})
	rep, err := c.Reconcile(ctx)
	if err != nil || rep.EntitiesSkipped != 0 || n.Load() != 2 {
		t.Fatalf("rep=%+v err=%v calls=%d", rep, err, n.Load())
	}
}

func TestRelate_FailureSkipsEntityAndContinues(t *testing.T) {
	forEachMode(t, testRelate_FailureSkipsEntityAndContinues)
}

func testRelate_FailureSkipsEntityAndContinues(t *testing.T) {
	fastBackoff(t)
	c := openTestDB(t)
	ctx := context.Background()
	for _, name := range []string{"Slow", "Fast"} {
		e := &Entity{Type: "person", Name: name}
		if err := c.PutEntity(ctx, e); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if err := c.PutMemory(ctx, &Memory{Content: fmt.Sprintf("%s fact %d", name, i), EntityIDs: []string{e.ID}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	var good atomic.Int32
	c.SetLLM(&mockLLM{detectRelationsFn: func(_ context.Context, mems []Memory) ([]MemoryRelation, error) {
		if strings.HasPrefix(mems[0].Content, "Slow") {
			return nil, errors.New("status code: 502")
		}
		good.Add(1)
		return nil, nil
	}})
	rep, err := c.BuildMemoryEdges(ctx)
	if err != nil || rep.EntitiesSkipped != 1 || good.Load() != 1 {
		t.Fatalf("rep=%+v err=%v good=%d", rep, err, good.Load())
	}
}

func TestMaintain_ContextCancelledStillAborts(t *testing.T) {
	forEachMode(t, testMaintain_ContextCancelledStillAborts)
}

func testMaintain_ContextCancelledStillAborts(t *testing.T) {
	c := openTestDB(t)
	e := &Entity{Type: "person", Name: "A"}
	_ = c.PutEntity(context.Background(), e)
	for i := 0; i < 2; i++ {
		_ = c.PutMemory(context.Background(), &Memory{Content: fmt.Sprintf("m%d", i), EntityIDs: []string{e.ID}})
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.SetLLM(&mockLLM{detectConflictsFn: func(context.Context, []Memory) ([]ConflictPair, error) {
		cancel()
		return nil, errors.New("timeout")
	}})
	if _, err := c.Reconcile(ctx); err == nil {
		t.Fatal("cancelled context must abort")
	}
}
