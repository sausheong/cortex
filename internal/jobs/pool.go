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
)

// Job is one queued unit of background work.
type Job struct{ ID, UserID, Kind string }

// Pool runs jobs on a fixed number of workers. Jobs live in the jobs table;
// the in-memory queue only wakes workers.
type Pool struct {
	n   int
	run func(ctx context.Context, j Job) error
	db  *sql.DB

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
	if p.db == nil {
		return "", errors.New("jobs: pool has no database")
	}
	id := newJobID()
	if _, err := p.db.ExecContext(ctx,
		`INSERT INTO jobs (id, user_id, kind, status) VALUES ($1,$2,$3,'queued')`, id, userID, kind); err != nil {
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
		rows, err := p.db.QueryContext(ctx, `SELECT id, user_id, kind FROM jobs WHERE status='queued' ORDER BY created_at LIMIT 100`)
		if err != nil {
			continue
		}
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
	p.db.ExecContext(c, `UPDATE jobs SET status=$2, error=$3, updated_at=now() WHERE id=$1`, j.ID, status, msg)
}

func (p *Pool) exec(ctx context.Context, j Job) {
	if p.db != nil {
		var deleted bool
		err := p.db.QueryRowContext(ctx,
			`SELECT deleted_at IS NOT NULL FROM users WHERE id = $1`, j.UserID).Scan(&deleted)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && deleted) {
			p.setStatus(j, "skipped", "")
			return
		}
		// Claim atomically: a job may be in the channel and swept twice.
		res, err := p.db.ExecContext(ctx,
			`UPDATE jobs SET status='running', updated_at=now() WHERE id=$1 AND status='queued'`, j.ID)
		if err == nil {
			if n, _ := res.RowsAffected(); n == 0 {
				return
			}
		}
	}
	if err := p.run(ctx, j); err != nil {
		if ctx.Err() != nil {
			// Interrupted by Stop: leave it queued so the next Start resumes it.
			p.setStatus(j, "queued", "")
			return
		}
		p.setStatus(j, "failed", err.Error())
		return
	}
	p.setStatus(j, "done", "")
}
