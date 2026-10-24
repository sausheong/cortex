package jobs

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/model"
)

func (e *env) encUser(t *testing.T, sub string) (string, *keys.UK) {
	t.Helper()
	uid := e.user(t, sub)
	if _, err := e.db.ExecContext(t.Context(), `UPDATE users SET encryption_state = 'encrypted' WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	uk, err := keys.NewUK()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(uk.Zero)
	return uid, uk
}

type seen struct {
	mu sync.Mutex
	m  map[string]string
}

func (s *seen) put(k, v string) { s.mu.Lock(); s.m[k] = v; s.mu.Unlock() }
func (s *seen) get(k string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[k]
	return v, ok
}

func waitFor(t *testing.T, f func() bool) bool {
	t.Helper()
	for i := 0; i < 100; i++ {
		if f() {
			return true
		}
		time.Sleep(30 * time.Millisecond)
	}
	return false
}

func TestJobPayloadEncryptedAndParkedWithoutKey(t *testing.T) {
	e := newEnv(t)
	uid, uk := e.encUser(t, "p")
	sn := &seen{m: map[string]string{}}
	pool := NewPool(1, func(ctx context.Context, j Job) error { sn.put(j.ID, j.Payload); return nil }).WithDB(e.db)
	if _, err := pool.EnqueueWithKey(t.Context(), uid, "x", "PAYLOADSECRET", nil); !errors.Is(err, model.ErrLocked) {
		t.Fatalf("enqueue without key = %v", err)
	}
	id, err := pool.EnqueueWithKey(t.Context(), uid, "x", "PAYLOADSECRET", uk)
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	e.db.QueryRow(`SELECT to_jsonb(t)::text FROM jobs t WHERE id = $1`, id).Scan(&raw)
	if strings.Contains(raw, "PAYLOADSECRET") {
		t.Fatalf("job row leaks: %s", raw)
	}
	if e.count(t, `SELECT count(*) FROM jobs WHERE id = $1 AND enc AND payload_ct IS NOT NULL AND payload = ''`, id) != 1 {
		t.Fatal("payload not sealed")
	}
	// No key source: the job is parked, still queued, never run.
	pool.Start(t.Context())
	time.Sleep(400 * time.Millisecond)
	pool.Stop()
	if _, ran := sn.get(id); ran {
		t.Fatal("keyless pool ran an encrypted job")
	}
	if e.count(t, `SELECT count(*) FROM jobs WHERE id = $1 AND status = 'queued'`, id) != 1 {
		t.Fatal("parked job must stay queued")
	}
	// A key becomes available: the job runs with the decrypted payload.
	pool.WithKeyFunc(func(user string) *keys.UK {
		if user == uid {
			return uk.Clone()
		}
		return nil
	})
	pool.Start(t.Context())
	defer pool.Stop()
	if !waitFor(t, func() bool { _, ok := sn.get(id); return ok }) {
		t.Fatal("job did not run after key arrived")
	}
	if v, _ := sn.get(id); v != "PAYLOADSECRET" {
		t.Fatalf("payload = %q", v)
	}
	waitFor(t, func() bool {
		return e.count(t, `SELECT count(*) FROM jobs WHERE id = $1 AND status = 'done' AND payload_ct IS NULL`, id) == 1
	})
}

func TestSweeperReadsLegacyAndEncryptedPayloads(t *testing.T) {
	e := newEnv(t)
	uid, uk := e.encUser(t, "s")
	lid := e.user(t, "legacy")
	sn := &seen{m: map[string]string{}}
	pool := NewPool(2, func(ctx context.Context, j Job) error { sn.put(j.ID, j.Payload); return nil }).WithDB(e.db)
	pool.WithKeyFunc(func(user string) *keys.UK {
		if user == uid {
			return uk.Clone()
		}
		return nil
	})
	if _, err := e.db.Exec(`INSERT INTO jobs (id, user_id, kind, status, payload) VALUES ('j_legacy', $1, 'x', 'queued', 'LEGACYTEXT')`, lid); err != nil {
		t.Fatal(err)
	}
	eid, err := pool.EnqueueWithKey(t.Context(), uid, "x", "ENCTEXT", uk)
	if err != nil {
		t.Fatal(err)
	}
	pool.Start(t.Context())
	defer pool.Stop()
	if !waitFor(t, func() bool { _, a := sn.get("j_legacy"); _, b := sn.get(eid); return a && b }) {
		t.Fatal("jobs did not run")
	}
	if v, _ := sn.get("j_legacy"); v != "LEGACYTEXT" {
		t.Fatalf("legacy payload = %q", v)
	}
	if v, _ := sn.get(eid); v != "ENCTEXT" {
		t.Fatalf("encrypted payload = %q", v)
	}
}

func TestJobsErrorAndMaintenanceErrorFreeOfContent(t *testing.T) {
	e := newEnv(t)
	uid, uk := e.encUser(t, "err")
	pool := NewPool(1, func(ctx context.Context, j Job) error {
		return errors.New("provider said: SEEDSECRET content leaked")
	}).WithDB(e.db)
	pool.WithKeyFunc(func(string) *keys.UK { return uk.Clone() })
	id, err := pool.EnqueueWithKey(t.Context(), uid, "x", "SEEDSECRET body", uk)
	if err != nil {
		t.Fatal(err)
	}
	rid, err := pool.EnqueueWithKey(t.Context(), uid, RememberKind, "SEEDSECRET body", uk)
	if err != nil {
		t.Fatal(err)
	}
	pool.Start(t.Context())
	defer pool.Stop()
	ok := waitFor(t, func() bool {
		return e.count(t, `SELECT count(*) FROM jobs WHERE id = $1 AND status = 'failed'`, id) == 1 &&
			e.count(t, `SELECT count(*) FROM jobs WHERE id = $1 AND error <> ''`, rid) == 1
	})
	if !ok {
		t.Fatal("jobs did not fail")
	}
	for _, j := range []string{id, rid} {
		var msg string
		e.db.QueryRow(`SELECT error FROM jobs WHERE id = $1`, j).Scan(&msg)
		if strings.Contains(msg, "SEEDSECRET") || strings.Contains(msg, "leaked") {
			t.Fatalf("jobs.error leaks content: %q", msg)
		}
	}
	// Maintenance.
	<-maintainOnUse(e.root, e.st, uk, uid, NightlyOptions{Resolve: func(ctx context.Context, u string) (*cortex.Cortex, error) {
		return nil, errors.New("SEEDSECRET resolve failure")
	}})
	var me *string
	e.db.QueryRow(`SELECT last_maintenance_error FROM users WHERE id = $1`, uid).Scan(&me)
	if me == nil || strings.Contains(*me, "SEEDSECRET") {
		t.Fatalf("last_maintenance_error = %v", me)
	}
}
