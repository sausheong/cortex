package jobs

import (
	"context"
	"database/sql"
	"hash/fnv"
	"sync"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/keys"
)

// Maintenance during use: an encrypted user's content cannot be maintained by
// the server-wide nightly (no key), so the first keyed request after the last
// run goes stale starts one, in the background, under a private clone of the
// user's key that is zeroed when the run ends or after maintainCap, whichever
// comes first.

// maintainCap bounds how long a background run may hold the key.
var maintainCap = 15 * time.Minute

// maintainStale is how old last_maintenance_at must be for a run to start.
const maintainStale = 24 * time.Hour

// maintainLocks holds the in-process single-flight mutex per user.
var maintainLocks sync.Map // userID -> *sync.Mutex

// MaintainOnUse starts a maintenance run for userID when it is stale and none
// is active (in this process, or in another one: Postgres advisory lock). It
// returns at once. uk is cloned before MaintainOnUse returns, so the caller
// may zero its copy when the request ends; the clone lives only inside the
// run. A nil uk (legacy user) or a fresh last run does nothing.
func MaintainOnUse(root *cortex.Cortex, st *accounts.Store, uk *keys.UK, userID string, o NightlyOptions) {
	maintainOnUse(root, st, uk, userID, o)
}

// maintainOnUse is MaintainOnUse returning a channel closed when the run
// (and the zeroing of its key) is over, or nil when nothing started.
func maintainOnUse(root *cortex.Cortex, st *accounts.Store, uk *keys.UK, userID string, o NightlyOptions) <-chan struct{} {
	if uk == nil || st == nil || st.DB == nil {
		return nil
	}
	ctx := context.Background()
	var stale bool
	err := st.DB.QueryRowContext(ctx,
		`SELECT deleted_at IS NULL AND encryption_state <> 'none'
		        AND (last_maintenance_at IS NULL OR last_maintenance_at < $2)
		   FROM users WHERE id = $1`, userID, o.now().Add(-maintainStale)).Scan(&stale)
	if err != nil || !stale {
		return nil
	}
	mu, _ := maintainLocks.LoadOrStore(userID, new(sync.Mutex))
	m := mu.(*sync.Mutex)
	if !m.TryLock() {
		return nil
	}
	clone := uk.Clone()
	if clone == nil {
		m.Unlock()
		return nil
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer m.Unlock()
		runMaintainClone(ctx, root, st, clone, userID, o)
	}()
	return done
}

// runMaintainClone owns clone: it zeroes it when the run ends or at the cap.
func runMaintainClone(ctx context.Context, root *cortex.Cortex, st *accounts.Store, clone *keys.UK, userID string, o NightlyOptions) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	// The cap timer and the deferred zero reference only the clone.
	capTimer := time.AfterFunc(maintainCap, func() { cancel(); clone.Zero() })
	defer func() { capTimer.Stop(); clone.Zero() }()

	// One run per user across processes: a session-level advisory lock on a
	// dedicated connection, released when the run ends.
	conn, err := st.DB.Conn(ctx)
	if err != nil {
		return
	}
	defer conn.Close()
	var got bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, advisoryKey(userID)).Scan(&got); err != nil || !got {
		return
	}
	defer unlock(conn, userID)

	// Another process may have finished a run between the cheap check and
	// the lock; do not rerun right after it.
	var stale bool
	if err := st.DB.QueryRowContext(ctx,
		`SELECT last_maintenance_at IS NULL OR last_maintenance_at < $2 FROM users WHERE id = $1`,
		userID, o.now().Add(-maintainStale)).Scan(&stale); err != nil || !stale {
		return
	}

	if o.onClone != nil {
		o.onClone(clone)
	}
	_ = maintainUser(ctx, root, st, userID, &o, clone)
}

func unlock(conn *sql.Conn, userID string) {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = conn.ExecContext(c, `SELECT pg_advisory_unlock($1)`, advisoryKey(userID))
}

// advisoryKey maps a user id to a 64-bit advisory lock key.
func advisoryKey(userID string) int64 {
	h := fnv.New64a()
	h.Write([]byte("cortex/maintain/" + userID))
	return int64(h.Sum64())
}
