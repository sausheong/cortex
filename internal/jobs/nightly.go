package jobs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
)

// PurgeWindow is how long soft-deleted rows and deleted accounts are kept.
const PurgeWindow = 30 * 24 * time.Hour

// NightlyOptions configures Nightly.
type NightlyOptions struct {
	// Enqueue queues a background job (web.Options.Enqueue shape). When nil,
	// phase 1 never enqueues a re-embed.
	Enqueue func(ctx context.Context, userID, kind string) (string, error)
	// MaintainOpts are passed to every user's Maintain.
	MaintainOpts []cortex.MaintainOption
	// Resolve builds a user's handle. Nil means st.ResolveUser(root, userID, "", nil).
	Resolve func(ctx context.Context, userID string) (*cortex.Cortex, error)
	// PurgeAfter is the retention window (default PurgeWindow).
	PurgeAfter time.Duration
	// Now is the clock (default time.Now).
	Now func() time.Time

	beforeMaintain func(ctx context.Context, userID string, cx *cortex.Cortex) // test hook
}

// NightlyReport counts what a run did.
type NightlyReport struct {
	Maintained, Skipped, Failed int
	UsersPurged                 int
	Errors                      map[string]string // userID -> recorded error
}

func (o *NightlyOptions) now() time.Time {
	if o.Now != nil {
		return o.Now().UTC()
	}
	return time.Now().UTC()
}

// Nightly runs the two independent nightly phases and returns the first
// phase-2 (purge) error, if any. Phase 1 errors are recorded per user and
// never abort the run.
func Nightly(ctx context.Context, root *cortex.Cortex, st *accounts.Store, o NightlyOptions) (NightlyReport, error) {
	rep := NightlyReport{Errors: map[string]string{}}
	users, err := root.ListUsers(ctx)
	if err != nil {
		return rep, err
	}
	// Phase 1: maintenance.
	for _, u := range users {
		if ctx.Err() != nil {
			break
		}
		if u.DeletedAt != nil {
			rep.Skipped++
			continue
		}
		switch err := maintainUser(ctx, root, st, u.ID, &o); {
		case err == nil:
			rep.Maintained++
		case errors.Is(err, errSkipped):
			rep.Skipped++
		default:
			rep.Failed++
			rep.Errors[u.ID] = err.Error()
		}
	}
	// Phase 2: purge, independent of phase 1.
	perr := purgeAll(ctx, root, st, &o, &rep)
	return rep, perr
}

var errSkipped = errors.New("jobs: skipped")

// Nightly embedding backfill bounds: at most backfillRounds*backfillBatch
// rows per user per night; the rest wait for the next run.
const (
	backfillBatch  = 64
	backfillRounds = 50
)

// MaintainUser runs phase 1 for one user (the --user admin path). A user
// without valid settings or a deleted user returns an error.
func MaintainUser(ctx context.Context, root *cortex.Cortex, st *accounts.Store, userID string, o NightlyOptions) error {
	err := maintainUser(ctx, root, st, userID, &o)
	if errors.Is(err, errSkipped) {
		return errors.New("user is deleted or has no valid provider settings")
	}
	return err
}

// maintainUser maintains one user and records the outcome in
// users.last_maintenance_error / last_maintenance_at. Fixed texts only.
func maintainUser(ctx context.Context, root *cortex.Cortex, st *accounts.Store, userID string, o *NightlyOptions) error {
	var deleted bool
	if err := st.DB.QueryRowContext(ctx, `SELECT deleted_at IS NOT NULL FROM users WHERE id = $1`, userID).Scan(&deleted); err != nil {
		return err
	}
	if deleted {
		return errSkipped
	}
	if ok, err := st.HasSettings(ctx, userID); err == nil && !ok {
		return errSkipped
	}
	resolve := o.Resolve
	if resolve == nil {
		resolve = func(ctx context.Context, id string) (*cortex.Cortex, error) {
			return st.ResolveUser(ctx, root, id, "", nil)
		}
	}
	record := func(msg string) {
		var m any
		if msg != "" {
			m = msg
		}
		_, _ = st.DB.ExecContext(context.WithoutCancel(ctx),
			`UPDATE users SET last_maintenance_at = $2, last_maintenance_error = $3 WHERE id = $1`, userID, o.now(), m)
	}
	cx, err := resolve(ctx, userID)
	if err != nil {
		if errors.Is(err, accounts.ErrNoProvider) {
			return errSkipped
		}
		record("provider settings could not be loaded")
		return errors.New("provider settings could not be loaded")
	}
	if o.beforeMaintain != nil {
		o.beforeMaintain(ctx, userID, cx)
	}
	o.maybeEnqueueReembed(ctx, st, userID)
	rep, err := cx.Maintain(ctx, o.MaintainOpts...)
	if err != nil {
		msg := "maintenance failed: " + accounts.ProviderError(err).Error()
		record(msg)
		return errors.New(msg)
	}
	if _, err := cx.RefreshUsage(ctx); err != nil {
		record("maintenance failed: usage refresh")
		return errors.New("maintenance failed: usage refresh")
	}
	// Embed anything Remember stored without a vector (provider errors).
	pending := 0
	for i := 0; i < backfillRounds; i++ {
		n, err := cx.BackfillEmbeddings(ctx, backfillBatch)
		if err != nil {
			pending = -1
			break
		}
		if n < backfillBatch {
			break
		}
	}
	if pending < 0 {
		record("maintenance completed; some items still wait for an embedding")
		return nil
	}
	if n := rep.SkippedItems(); n > 0 {
		// Degraded, not failed: the run completed and skipped items are
		// retried by the next run.
		record(fmt.Sprintf("maintenance completed; %d item(s) skipped after provider errors", n))
		return nil
	}
	record("")
	return nil
}

// maybeEnqueueReembed retries a failed or never-started re-embed: when the
// desired embedder settings differ from the active ones and no reembed job
// is queued or running, it enqueues one.
func (o *NightlyOptions) maybeEnqueueReembed(ctx context.Context, st *accounts.Store, userID string) {
	if o.Enqueue == nil {
		return
	}
	want, err := st.GetSettings(ctx, userID, nil) // a locked encrypted user is skipped
	if err != nil {
		return
	}
	act, err := st.GetActiveEmbed(ctx, userID, nil)
	if err != nil {
		return
	}
	if want.EmbedModel == act.EmbedModel && want.EmbedDims == act.EmbedDims && want.EffectiveEmbedURL() == act.EmbedBaseURL {
		return
	}
	var n int
	if err := st.DB.QueryRowContext(ctx,
		`SELECT count(*) FROM jobs WHERE user_id = $1 AND kind = 'reembed' AND status IN ('queued','running')`, userID).Scan(&n); err != nil || n > 0 {
		return
	}
	_, _ = o.Enqueue(ctx, userID, "reembed")
}

// purgeAll is phase 2: it covers every user, including settings-less and
// deleted ones.
func purgeAll(ctx context.Context, root *cortex.Cortex, st *accounts.Store, o *NightlyOptions, rep *NightlyReport) error {
	window := o.PurgeAfter
	if window <= 0 {
		window = PurgeWindow
	}
	now := o.now()
	var first error
	keep := func(err error) {
		if err != nil && first == nil {
			first = err
		}
	}
	users, err := root.ListUsers(ctx)
	if err != nil {
		return err
	}
	cutoff := now.Add(-window)
	for _, u := range users {
		if u.DeletedAt != nil && u.DeletedAt.Before(cutoff) {
			res, err := st.DB.ExecContext(ctx, `DELETE FROM users WHERE id = $1 AND deleted_at < $2`, u.ID, cutoff)
			keep(err)
			if err == nil {
				if n, _ := res.RowsAffected(); n > 0 {
					rep.UsersPurged++
				}
			}
			continue
		}
		_, err := root.ForUser(u.ID).PurgeDeleted(ctx, window)
		keep(err)
	}
	keep(st.PurgeEnrollments(ctx, now))
	for _, q := range []string{
		`DELETE FROM oauth_clients WHERE kind = 'dcr' AND authorized_at IS NULL AND (expires_at < $1 OR created_at < $1::timestamptz - interval '24 hours')`,
		`DELETE FROM authorize_requests WHERE expires_at < $1`,
		`DELETE FROM oauth_codes WHERE expires_at < $1`,
		`DELETE FROM login_attempts WHERE expires_at < $1`,
		`DELETE FROM sessions WHERE expires_at < $1`,
	} {
		_, err := st.DB.ExecContext(ctx, q, now)
		keep(err)
	}
	return first
}
