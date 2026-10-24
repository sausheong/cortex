package jobs

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/model"
)

// Job is one queued unit of background work.
type Job struct {
	ID, UserID, Kind string
	// Payload is the job's input text, loaded when a worker claims the job.
	Payload string
	// Key is the user's key for a job of an encrypted user, supplied by the
	// pool's key source for the duration of the run and zeroed afterwards.
	Key *keys.UK
}

// RememberKind is the kind of a background "remember" job (see remember.go).
const RememberKind = "remember"

// maxAttempts is how many times a retryable job runs before it is marked
// failed; retryDelays are the waits after the first, second and third failure.
const maxAttempts = 4

var retryDelays = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute}

// retryable reports whether a failed job of this kind is run again. Only
// remember jobs are: the others are re-triggered by the user or nightly.
func retryable(kind string) bool { return kind == RememberKind }

// Pool runs jobs on a fixed number of workers. Jobs live in the jobs table;
// the in-memory queue only wakes workers.
type Pool struct {
	n   int
	run func(ctx context.Context, j Job) error
	db  *sql.DB
	// keyFor returns a clone of the user's key, or nil when none is held. A
	// job whose payload is sealed and has no key stays queued (parked).
	keyFor func(userID string) *keys.UK

	// attached holds the per-user key copies of keyed requests (AttachKey).
	// Each is the pool's own clone, zeroed at its ttl or when replaced.
	keysMu   sync.Mutex
	attached map[string]*attachedKey

	queue  chan Job
	mu     sync.Mutex
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewPool returns a pool of n workers calling run for each job. Attach a
// database with WithDB before Enqueue or Start.
func NewPool(n int, run func(ctx context.Context, j Job) error) *Pool {
	if n < 1 {
		n = 1
	}
	return &Pool{n: n, run: run, queue: make(chan Job, 4096)}
}

// WithDB sets the database holding the jobs and users tables.
func (p *Pool) WithDB(db *sql.DB) *Pool {
	p.db = db
	return p
}

// WithKeyFunc sets the source of user keys for jobs of encrypted users. The
// function returns a clone the pool zeroes after the job.
func (p *Pool) WithKeyFunc(f func(userID string) *keys.UK) *Pool {
	p.keyFor = f
	return p
}

type attachedKey struct {
	uk    *keys.UK
	timer *time.Timer
}

// AttachKey lets background jobs of an encrypted user run: it keeps a clone of
// uk for at most ttl (the caller keeps and zeroes its own copy), then resumes
// the user's parked jobs. Attaching again replaces the clone and restarts the
// ttl. The clone is zeroed when the ttl fires, on Detach and on Stop.
func (p *Pool) AttachKey(userID string, uk *keys.UK, ttl time.Duration) {
	if uk == nil || ttl <= 0 {
		return
	}
	c := uk.Clone()
	if c == nil {
		return
	}
	e := &attachedKey{uk: c}
	// The timer closure holds the entry, never the caller's key.
	e.timer = time.AfterFunc(ttl, func() { p.detach(userID, e) })
	p.keysMu.Lock()
	if p.attached == nil {
		p.attached = map[string]*attachedKey{}
	}
	old := p.attached[userID]
	p.attached[userID] = e
	p.keysMu.Unlock()
	if old != nil {
		old.timer.Stop()
		old.uk.Zero()
	}
	p.Resume(userID)
}

func (p *Pool) detach(userID string, e *attachedKey) {
	p.keysMu.Lock()
	if p.attached[userID] == e {
		delete(p.attached, userID)
	}
	p.keysMu.Unlock()
	e.timer.Stop()
	e.uk.Zero()
}

// Detach zeroes and forgets the key attached for userID.
func (p *Pool) Detach(userID string) {
	p.keysMu.Lock()
	e := p.attached[userID]
	delete(p.attached, userID)
	p.keysMu.Unlock()
	if e != nil {
		e.timer.Stop()
		e.uk.Zero()
	}
}

// key returns a clone of the user's key for a job (the caller zeroes it), or
// nil when none is attached.
func (p *Pool) key(userID string) *keys.UK {
	p.keysMu.Lock()
	e := p.attached[userID]
	var c *keys.UK
	if e != nil {
		c = e.uk.Clone()
	}
	p.keysMu.Unlock()
	if c != nil {
		return c
	}
	if p.keyFor != nil {
		return p.keyFor(userID)
	}
	return nil
}

// Resume wakes the user's queued jobs that are due, including ones parked for
// want of a key (a keyed request just attached one).
func (p *Pool) Resume(userID string) {
	if p.db == nil {
		return
	}
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := p.db.QueryContext(c,
		`SELECT id, kind FROM jobs WHERE user_id = $1 AND status = 'queued' AND run_after <= now() ORDER BY created_at LIMIT 100`, userID)
	if err != nil {
		return
	}
	var pending []Job
	for rows.Next() {
		j := Job{UserID: userID}
		if rows.Scan(&j.ID, &j.Kind) == nil {
			pending = append(pending, j)
		}
	}
	rows.Close()
	for _, j := range pending {
		select {
		case p.queue <- j:
		default:
		}
	}
}

func newJobID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return "j_" + hex.EncodeToString(b)
}

// Enqueue records a queued job for userID and wakes a worker. It matches
// web.Options.Enqueue.
func (p *Pool) Enqueue(ctx context.Context, userID, kind string) (string, error) {
	return p.EnqueueWith(ctx, userID, kind, "")
}

// EnqueueWith is Enqueue for a job that carries an input payload, stored in
// the jobs row so the job survives a restart. A non-empty payload of an
// encrypted user needs a key: use EnqueueWithKey.
func (p *Pool) EnqueueWith(ctx context.Context, userID, kind, payload string) (string, error) {
	return p.EnqueueWithKey(ctx, userID, kind, payload, nil)
}

// EnqueueWithKey is EnqueueWith for a caller holding the user's key. For an
// encrypted user the payload is sealed into payload_ct (AAD column payload);
// without a key it fails with model.ErrLocked and nothing is queued.
func (p *Pool) EnqueueWithKey(ctx context.Context, userID, kind, payload string, uk *keys.UK) (string, error) {
	if p.db == nil {
		return "", errors.New("jobs: pool has no database")
	}
	id := newJobID()
	plain, ct, enc := payload, []byte(nil), false
	if payload != "" {
		var state string
		if err := p.db.QueryRowContext(ctx, `SELECT encryption_state FROM users WHERE id = $1`, userID).Scan(&state); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("jobs: enqueue: %w", err)
		} else if err == nil && state != "none" {
			if uk == nil {
				return "", model.ErrLocked
			}
			sealed, err := uk.Seal(userID, "jobs", id, "payload", []byte(payload))
			if err != nil {
				return "", err
			}
			plain, ct, enc = "", sealed, true
		}
	}
	if _, err := p.db.ExecContext(ctx,
		`INSERT INTO jobs (id, user_id, kind, status, payload, payload_ct, enc) VALUES ($1,$2,$3,'queued',$4,$5,$6)`,
		id, userID, kind, plain, ct, enc); err != nil {
		return "", fmt.Errorf("jobs: enqueue: %w", err)
	}
	select {
	case p.queue <- Job{ID: id, UserID: userID, Kind: kind}:
	default:
		return id, nil // stays queued in the table; picked up at next Start
	}
	return id, nil
}

// Progress records a running job's progress.
func (p *Pool) Progress(ctx context.Context, jobID string, done, total int) {
	if p.db == nil {
		return
	}
	p.db.ExecContext(ctx, `UPDATE jobs SET progress_done=$2, progress_total=$3, updated_at=now() WHERE id=$1`,
		jobID, done, total)
}

// Start launches the workers and requeues jobs left queued or running by a
// previous process.
func (p *Pool) Start(ctx context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil {
		return
	}
	ctx, p.cancel = context.WithCancel(ctx)
	if p.db != nil {
		rows, err := p.db.QueryContext(ctx,
			`UPDATE jobs SET status='queued' WHERE status IN ('queued','running') RETURNING id, user_id, kind`)
		if err == nil {
			var pending []Job
			for rows.Next() {
				var j Job
				if rows.Scan(&j.ID, &j.UserID, &j.Kind) == nil {
					pending = append(pending, j)
				}
			}
			rows.Close()
			for _, j := range pending {
				select {
				case p.queue <- j:
				default:
				}
			}
		}
	}
	for i := 0; i < p.n; i++ {
		p.wg.Add(1)
		go p.worker(ctx)
	}
	if p.db != nil {
		p.wg.Add(1)
		go p.sweep(ctx)
	}
}

// sweepEvery is how often the pool picks up jobs queued by other processes
// (for example the nightly `cortex maintain` retrying a re-embed).
const sweepEvery = 30 * time.Second

func (p *Pool) sweep(ctx context.Context) {
	defer p.wg.Done()
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pending := p.sweepBatch(ctx)
		for _, j := range pending {
			select {
			case p.queue <- j:
			default:
			}
		}
	}
}

// sweepBatch lists the queued jobs that are due. The select takes row locks
// (SKIP LOCKED) so a migration or another sweeper rewriting a row's payload
// form is never raced; the payload itself is read, in either form, when the
// job runs.
func (p *Pool) sweepBatch(ctx context.Context) []Job {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT j.id, j.user_id, j.kind, u.encryption_state FROM jobs j JOIN users u ON u.id = j.user_id
		WHERE j.status='queued' AND j.run_after <= now()
		ORDER BY j.created_at LIMIT 100 FOR UPDATE OF j SKIP LOCKED`)
	if err != nil {
		return nil
	}
	var pending []Job
	for rows.Next() {
		var j Job
		var state string
		if rows.Scan(&j.ID, &j.UserID, &j.Kind, &state) != nil {
			continue
		}
		// Parked: an encrypted user's job waits, untouched (still queued,
		// no error, no attempt), until a keyed request attaches a key.
		if state != "none" {
			if k := p.key(j.UserID); k == nil {
				continue
			} else {
				k.Zero()
			}
		}
		pending = append(pending, j)
	}
	rows.Close()
	_ = tx.Commit()
	return pending
}

// Stop cancels running jobs and waits for the workers to exit.
func (p *Pool) Stop() {
	p.mu.Lock()
	cancel := p.cancel
	p.cancel = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	p.wg.Wait()
	p.keysMu.Lock()
	all := p.attached
	p.attached = nil
	p.keysMu.Unlock()
	for _, e := range all {
		e.timer.Stop()
		e.uk.Zero()
	}
}

func (p *Pool) worker(ctx context.Context) {
	defer p.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-p.queue:
			p.exec(ctx, j)
		}
	}
}

func (p *Pool) setStatus(j Job, status, msg string) {
	if p.db == nil {
		return
	}
	// A cancelled pool context must still be able to record the outcome.
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// A finished job no longer needs its input text.
	clear := ""
	if status == "done" {
		clear = ", payload='', payload_ct=NULL, enc=FALSE"
	}
	p.db.ExecContext(c, `UPDATE jobs SET status=$2, error=$3, updated_at=now()`+clear+` WHERE id=$1`, j.ID, status, msg)
}

// retryLater puts a failed job back in the queue after a backoff, or reports
// false when it has used all its attempts. The sweep picks it up once
// run_after has passed.
func (p *Pool) retryLater(j Job, attempts int, msg string) bool {
	if p.db == nil || !retryable(j.Kind) || attempts+1 >= maxAttempts {
		return false
	}
	delay := retryDelays[min(attempts, len(retryDelays)-1)]
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := p.db.ExecContext(c,
		`UPDATE jobs SET status='queued', error=$2, attempts=$3, run_after=now()+make_interval(secs => $4), updated_at=now() WHERE id=$1`,
		j.ID, msg, attempts+1, delay.Seconds())
	return err == nil
}

// jobErrText is what a failed job records. For an encrypted user it is a
// fixed text: provider and handler errors can echo content.
func jobErrText(encUser bool, err error) string {
	if !encUser {
		return err.Error()
	}
	if errors.Is(err, model.ErrLocked) {
		return "locked"
	}
	return "job failed"
}

func (p *Pool) exec(ctx context.Context, j Job) {
	encUser := false
	attempts := 0
	if p.db != nil {
		var deleted bool
		var state string
		err := p.db.QueryRowContext(ctx,
			`SELECT deleted_at IS NOT NULL, encryption_state FROM users WHERE id = $1`, j.UserID).Scan(&deleted, &state)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && deleted) {
			p.setStatus(j, "skipped", "")
			return
		}
		encUser = err == nil && state != "none"
		// Read the payload (either form) before claiming: a sealed payload
		// without a key leaves the job queued for a later keyed pass.
		var ct []byte
		var enc bool
		if err := p.db.QueryRowContext(ctx, `SELECT payload, payload_ct, enc, attempts FROM jobs WHERE id=$1 AND status='queued'`, j.ID).
			Scan(&j.Payload, &ct, &enc, &attempts); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return // already claimed or finished
			}
			return
		}
		// An encrypted user's job runs only under a key; without one it is
		// parked: left queued, no error, attempts untouched.
		if encUser || enc {
			uk := p.key(j.UserID)
			if uk == nil {
				return // parked
			}
			defer uk.Zero()
			if enc {
				b, err := uk.Open(j.UserID, "jobs", j.ID, "payload", ct)
				if err != nil {
					p.setStatus(j, "failed", jobErrText(true, err))
					return
				}
				j.Payload = string(b)
			}
			j.Key = uk
		}
		// Claim atomically: a job may be in the channel and swept twice, and
		// a retry waits out its backoff even if a stale copy is still queued.
		res, err := p.db.ExecContext(ctx,
			`UPDATE jobs SET status='running', updated_at=now() WHERE id=$1 AND status='queued' AND run_after <= now()`, j.ID)
		if err == nil {
			if n, _ := res.RowsAffected(); n == 0 {
				return
			}
		}
	}
	if err := p.run(ctx, j); err != nil {
		msg := jobErrText(encUser, err)
		if ctx.Err() == nil && p.retryLater(j, attempts, msg) {
			return
		}
		if ctx.Err() != nil {
			// Interrupted by Stop: leave it queued so the next Start resumes it.
			p.setStatus(j, "queued", "")
			return
		}
		p.setStatus(j, "failed", msg)
		return
	}
	p.setStatus(j, "done", "")
}
