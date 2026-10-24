package jobs

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/keys"
)

func waitDone(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	if ch == nil {
		t.Fatal("maintenance did not start")
	}
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("maintenance did not finish")
	}
}

func TestMaintenanceStartsOnKeyedRequestWhenStale(t *testing.T) {
	e := newEnv(t)
	uid, uk := e.encUser(t, "m1")
	var calls sync.Map
	o := NightlyOptions{Resolve: e.resolver(&calls, nil)}
	waitDone(t, maintainOnUse(e.root, e.st, uk, uid, o))
	if callsOf(&calls, uid) != 1 {
		t.Fatalf("resolve calls = %d", callsOf(&calls, uid))
	}
	if e.count(t, `SELECT count(*) FROM users WHERE id=$1 AND last_maintenance_at IS NOT NULL`, uid) != 1 {
		t.Fatal("last_maintenance_at not recorded")
	}
	// A run older than 24 hours is stale again.
	e.mustExec(t, `UPDATE users SET last_maintenance_at = now() - interval '25 hours' WHERE id=$1`, uid)
	waitDone(t, maintainOnUse(e.root, e.st, uk, uid, o))
	if callsOf(&calls, uid) != 2 {
		t.Fatalf("stale run did not start: %d", callsOf(&calls, uid))
	}
}

func TestMaintenanceSkippedWhenFresh(t *testing.T) {
	e := newEnv(t)
	uid, uk := e.encUser(t, "m2")
	e.mustExec(t, `UPDATE users SET last_maintenance_at = now() - interval '1 hour' WHERE id=$1`, uid)
	var calls sync.Map
	if ch := maintainOnUse(e.root, e.st, uk, uid, NightlyOptions{Resolve: e.resolver(&calls, nil)}); ch != nil {
		t.Fatal("fresh user maintained")
	}
	if callsOf(&calls, uid) != 0 {
		t.Fatal("resolved for a fresh user")
	}
	// No key (legacy user or locked request): nothing starts.
	e.mustExec(t, `UPDATE users SET last_maintenance_at = NULL WHERE id=$1`, uid)
	if ch := maintainOnUse(e.root, e.st, nil, uid, NightlyOptions{Resolve: e.resolver(&calls, nil)}); ch != nil {
		t.Fatal("started without a key")
	}
}

func TestMaintenanceSingleFlightPerUser(t *testing.T) {
	e := newEnv(t)
	uid, uk := e.encUser(t, "m3")
	var calls sync.Map
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	o := NightlyOptions{Resolve: e.resolver(&calls, nil),
		beforeMaintain: func(context.Context, string, *cortex.Cortex) {
			once.Do(func() { close(started) })
			<-release
		}}
	first := maintainOnUse(e.root, e.st, uk, uid, o)
	<-started
	if second := maintainOnUse(e.root, e.st, uk, uid, o); second != nil {
		t.Fatal("second concurrent run started")
	}
	// The advisory lock is held on another connection while the run is active.
	conn, err := e.db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var got bool
	if err := conn.QueryRowContext(t.Context(), `SELECT pg_try_advisory_lock($1)`, advisoryKey(uid)).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got {
		t.Fatal("advisory lock was free during a run")
	}
	conn.Close()
	close(release)
	waitDone(t, first)
	if callsOf(&calls, uid) != 1 {
		t.Fatalf("runs = %d, want 1", callsOf(&calls, uid))
	}
	// Released after the run.
	conn, _ = e.db.Conn(t.Context())
	defer conn.Close()
	conn.QueryRowContext(t.Context(), `SELECT pg_try_advisory_lock($1)`, advisoryKey(uid)).Scan(&got)
	if !got {
		t.Fatal("advisory lock not released")
	}
	conn.ExecContext(t.Context(), `SELECT pg_advisory_unlock($1)`, advisoryKey(uid))
}

func TestMaintenanceZeroesKeyAfterRun(t *testing.T) {
	e := newEnv(t)
	uid, uk := e.encUser(t, "m4")
	var clone *keys.UK
	o := NightlyOptions{Resolve: e.resolver(new(sync.Map), nil), onClone: func(c *keys.UK) { clone = c }}
	waitDone(t, maintainOnUse(e.root, e.st, uk, uid, o))
	if clone == nil || clone == uk {
		t.Fatal("run did not get its own clone")
	}
	if clone.Bytes() != nil {
		t.Fatal("clone alive after the run")
	}
	if uk.Bytes() == nil {
		t.Fatal("caller's key was zeroed")
	}
}

func TestMaintenanceCapZeroesKey(t *testing.T) {
	old := maintainCap
	maintainCap = 80 * time.Millisecond
	defer func() { maintainCap = old }()
	e := newEnv(t)
	uid, uk := e.encUser(t, "m5")
	var clone atomic.Pointer[keys.UK]
	o := NightlyOptions{Resolve: e.resolver(new(sync.Map), nil),
		onClone: func(c *keys.UK) { clone.Store(c) },
		beforeMaintain: func(ctx context.Context, _ string, _ *cortex.Cortex) {
			<-ctx.Done() // a run that never finishes by itself
		}}
	waitDone(t, maintainOnUse(e.root, e.st, uk, uid, o))
	if c := clone.Load(); c == nil || c.Bytes() != nil {
		t.Fatal("clone alive after the cap")
	}
}

func TestKeyZeroedWhenRequestEndsDoesNotAffectBackgroundClone(t *testing.T) {
	e := newEnv(t)
	uid, uk := e.encUser(t, "m6")
	sealed, err := uk.Seal(uid, "t", "r", "c", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	reqEnded := make(chan struct{})
	var opened atomic.Bool
	o := NightlyOptions{Resolve: e.resolver(new(sync.Map), nil),
		beforeMaintain: func(context.Context, string, *cortex.Cortex) {}, onClone: func(c *keys.UK) {
			<-reqEnded
			if b, err := c.Open(uid, "t", "r", "c", sealed); err == nil && string(b) == "hello" {
				opened.Store(true)
			}
		}}
	done := maintainOnUse(e.root, e.st, uk, uid, o)
	uk.Zero() // the request ends: the handler's key is zeroed
	close(reqEnded)
	waitDone(t, done)
	if !opened.Load() {
		t.Fatal("background clone died with the request key")
	}
}

func TestNoClosureRetainsKeyAfterCap(t *testing.T) {
	old := maintainCap
	maintainCap = 50 * time.Millisecond
	defer func() { maintainCap = old }()
	e := newEnv(t)
	uid, uk := e.encUser(t, "m7")
	var clone atomic.Pointer[keys.UK]
	o := NightlyOptions{Resolve: e.resolver(new(sync.Map), nil),
		onClone:        func(c *keys.UK) { clone.Store(c) },
		beforeMaintain: func(ctx context.Context, _ string, _ *cortex.Cortex) { <-ctx.Done() }}
	waitDone(t, maintainOnUse(e.root, e.st, uk, uid, o))
	// After the cap the run holds nothing: the clone is dead, and the
	// request's own key was neither used up nor zeroed by the run.
	if clone.Load().Bytes() != nil {
		t.Fatal("clone retained after cap")
	}
	if uk.Bytes() == nil {
		t.Fatal("root key zeroed by the background run")
	}
	// No leftover handle: the pool-style timer for a later run is independent.
	if ch := maintainOnUse(e.root, e.st, uk, uid, o); ch != nil {
		// last_maintenance_at was just written, so the user is fresh.
		t.Fatal("run restarted right after a capped run")
	}
}
