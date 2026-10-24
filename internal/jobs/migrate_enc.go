package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/graph"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/memory"
	"github.com/sausheong/cortex/internal/store"
)

// Owner encryption migration.
//
// A legacy user who enrolled a passkey is in state "migrating": new writes are
// already encrypted (dual-mode lookups), and this job rewrites every legacy
// row as ciphertext, batch by batch, under the user's key. It runs only while
// a key is attached to the pool (a keyed request), is idempotent (rows with
// enc = TRUE are skipped) and resumable (progress is derived from the rows
// still in legacy form), and it takes each row batch under
// FOR UPDATE SKIP LOCKED so concurrent writers are never blocked or lost.
//
// When nothing legacy remains, PostCheck flips the state to "encrypted" and
// VacuumUser rewrites the heaps and indexes so plaintext also leaves the
// disk. Backups, dumps and WAL taken before the migration still hold
// plaintext; see docs/ops/deploy.md.

// BatchSize is the number of rows migrated per transaction.
var BatchSize = 200

// encBatchHook, when set (tests), runs inside each batch transaction after the
// rows were rewritten and before the commit; an error rolls the batch back.
var encBatchHook func(table string, rows int) error

// maxStalledPasses bounds the retries when rows stay locked by writers.
const maxStalledPasses = 20

// ErrMigrationIncomplete is returned when rows remain in legacy form after the
// bounded retries (they stayed locked). The job is re-queued on the next keyed
// request.
var ErrMigrationIncomplete = errors.New("jobs: encryption migration incomplete")

// PostCheckReport is the outcome of PostCheck. It never carries content.
type PostCheckReport struct {
	// Leftover counts, per table, the rows still holding plaintext.
	Leftover map[string]int
	Total    int
	// Encrypted is true when the state was flipped to "encrypted".
	Encrypted bool
}

// leftoverPred says, per table, how a row still holding plaintext is found.
var leftoverPred = []struct{ table, pred string }{
	{"entities", `enc IS NOT TRUE OR name IS NOT NULL OR type IS NOT NULL OR source IS NOT NULL OR attributes IS NOT NULL`},
	{"entity_aliases", `enc IS NOT TRUE OR value IS NOT NULL OR value_norm IS NOT NULL OR source IS NOT NULL`},
	{"relationships", `enc IS NOT TRUE OR type IS NOT NULL OR source IS NOT NULL OR attributes IS NOT NULL`},
	{"memories", `enc IS NOT TRUE OR content IS NOT NULL OR source IS NOT NULL OR speaker IS NOT NULL OR tsv IS NOT NULL`},
	{"memory_edges", `enc IS NOT TRUE OR source IS NOT NULL`},
	{"chunks", `enc IS NOT TRUE OR content IS NOT NULL OR metadata IS NOT NULL OR tsv IS NOT NULL`},
	{"embeddings", `enc IS NOT TRUE OR embedding IS NOT NULL`},
	{"compactions", `proposal_json IS NOT NULL`},
	{"delete_batch_changes", `enc IS NOT TRUE OR row_json IS NOT NULL`},
	{"jobs", `COALESCE(payload, '') <> ''`},
	{"user_settings", `enc IS NOT TRUE`},
	{"sync_state", `enc IS NOT TRUE OR state IS NOT NULL`},
}

// remaining counts the rows still holding plaintext, per table, for userID.
func remaining(ctx context.Context, db *sql.DB, userID string) (map[string]int, int, error) {
	out := map[string]int{}
	total := 0
	for _, t := range leftoverPred {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT count(*) FROM `+t.table+` WHERE user_id = $1 AND (`+t.pred+`)`, userID).Scan(&n); err != nil {
			return nil, 0, fmt.Errorf("jobs: count %s: %w", t.table, err)
		}
		if n > 0 {
			out[t.table] = n
			total += n
		}
	}
	return out, total, nil
}

// PostCheck verifies that no plaintext remains for userID. On zero it sets
// encryption_state to "encrypted" and stamps cortex_meta
// enc_migration_done:<userID>; otherwise the user stays "migrating" and the
// per-table counts are returned (never content).
func PostCheck(ctx context.Context, st *accounts.Store, userID string) (PostCheckReport, error) {
	left, total, err := remaining(ctx, st.DB, userID)
	if err != nil {
		return PostCheckReport{}, err
	}
	rep := PostCheckReport{Leftover: left, Total: total}
	if total > 0 {
		return rep, nil
	}
	tx, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return rep, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET encryption_state = 'encrypted' WHERE id = $1 AND encryption_state = 'migrating'`, userID); err != nil {
		return rep, err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO cortex_meta (key, value) VALUES ($1, $2)
		 ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`,
		"enc_migration_done:"+userID, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return rep, err
	}
	if err := tx.Commit(); err != nil {
		return rep, err
	}
	rep.Encrypted = true
	return rep, nil
}

// vacuumTables are rewritten after a successful post-check.
var vacuumTables = []string{
	"entities", "entity_aliases", "relationships", "memories", "memory_edges", "chunks", "embeddings",
	"compactions", "delete_batch_changes", "jobs", "user_settings", "sync_state",
}

// VacuumUser runs VACUUM (FULL) and ANALYZE on every migrated table, one
// table at a time and outside any transaction. VACUUM FULL rewrites the heap
// and rebuilds every index (btree, GIN tsvector, HNSW) so dead plaintext tuples
// and stale index entries are gone. It takes an ACCESS EXCLUSIVE lock on the
// table for the duration, which blocks every user of the instance on that
// table; the tables are processed one at a time to bound that.
func VacuumUser(ctx context.Context, db *sql.DB) error {
	for _, t := range vacuumTables {
		if _, err := db.ExecContext(ctx, `VACUUM (FULL) `+t); err != nil {
			return fmt.Errorf("jobs: vacuum %s: %w", t, err)
		}
		if _, err := db.ExecContext(ctx, `ANALYZE `+t); err != nil {
			return fmt.Errorf("jobs: analyze %s: %w", t, err)
		}
	}
	return nil
}

// MigrateOwner is the whole migration: EncryptUserJob, PostCheck and, when the
// check passes, VacuumUser. It returns ErrMigrationIncomplete when plaintext
// remains.
func MigrateOwner(ctx context.Context, root *cortex.Cortex, st *accounts.Store, userID string, uk *keys.UK, report func(done, total int)) error {
	if err := EncryptUserJob(ctx, root, st, userID, uk, report); err != nil {
		return err
	}
	rep, err := PostCheck(ctx, st, userID)
	if err != nil {
		return err
	}
	if !rep.Encrypted {
		return ErrMigrationIncomplete
	}
	return VacuumUser(ctx, st.DB)
}

// EnsureMigrationJob makes sure a migrating user has a runnable migration
// job: a parked one is queued, and when none is queued or running a new one is
// enqueued. Call it on every keyed request, before attaching the key.
func EnsureMigrationJob(ctx context.Context, p *Pool, userID string) error {
	if p == nil || p.db == nil {
		return nil
	}
	var state string
	if err := p.db.QueryRowContext(ctx,
		`SELECT encryption_state FROM users WHERE id = $1 AND deleted_at IS NULL`, userID).Scan(&state); err != nil || state != "migrating" {
		return nil
	}
	if _, err := p.db.ExecContext(ctx,
		`UPDATE jobs SET status = 'queued', updated_at = now() WHERE user_id = $1 AND kind = $2 AND status = $3`,
		userID, accounts.MigrateEncKind, accounts.StatusParked); err != nil {
		return err
	}
	var n int
	if err := p.db.QueryRowContext(ctx,
		`SELECT count(*) FROM jobs WHERE user_id = $1 AND kind = $2 AND status IN ('queued','running')`,
		userID, accounts.MigrateEncKind).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, err := p.Enqueue(ctx, userID, accounts.MigrateEncKind)
	return err
}

// EncryptUserJob rewrites every legacy row of userID as ciphertext under uk.
// report receives (done, total): rows rewritten so far and the number that
// were legacy when the run started.
func EncryptUserJob(ctx context.Context, _ *cortex.Cortex, st *accounts.Store, userID string, uk *keys.UK, report func(done, total int)) error {
	if uk == nil || st == nil || st.DB == nil {
		return errors.New("jobs: encryption migration needs a key")
	}
	_, total, err := remaining(ctx, st.DB, userID)
	if err != nil {
		return err
	}
	if report == nil {
		report = func(int, int) {}
	}
	done := 0
	report(done, total)
	m := &encMigrator{st: st, userID: userID, uk: uk}
	stalled := 0
	for {
		moved := 0
		for _, s := range m.steps() {
			for {
				if err := ctx.Err(); err != nil {
					return err
				}
				n, err := m.batch(ctx, s)
				if err != nil {
					return err
				}
				if n == 0 {
					break
				}
				moved += n
				done += n
				if done > total {
					total = done
				}
				report(done, total)
			}
		}
		_, left, err := remaining(ctx, st.DB, userID)
		if err != nil {
			return err
		}
		if left == 0 {
			return nil
		}
		if moved == 0 { // the rest is locked by writers: wait and retry
			stalled++
			if stalled >= maxStalledPasses {
				return ErrMigrationIncomplete
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(50 * time.Millisecond):
			}
		}
	}
}

type encMigrator struct {
	st     *accounts.Store
	userID string
	uk     *keys.UK
}

type encStep struct {
	table string
	run   func(ctx context.Context, tx *sql.Tx, limit int) (int, error)
}

func (m *encMigrator) seal(table, rowID, col, plain string) ([]byte, error) {
	return m.uk.Seal(m.userID, table, rowID, col, []byte(plain))
}

// batch migrates one transaction's worth of rows of s and returns how many.
func (m *encMigrator) batch(ctx context.Context, s encStep) (int, error) {
	tx, err := m.st.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	n, err := s.run(ctx, tx, BatchSize)
	if err != nil {
		return 0, fmt.Errorf("jobs: encrypt %s: %w", s.table, err)
	}
	if n == 0 {
		return 0, tx.Commit()
	}
	if encBatchHook != nil {
		if err := encBatchHook(s.table, n); err != nil {
			return 0, err
		}
	}
	return n, tx.Commit()
}

// collect runs a locking SELECT and scans every row with scan into a slice,
// closing the cursor before any update runs on the same transaction.
func collect[T any](ctx context.Context, tx *sql.Tx, q string, args []any, scan func(*sql.Rows) (T, error)) ([]T, error) {
	rows, err := tx.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []T
	for rows.Next() {
		v, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (m *encMigrator) steps() []encStep {
	return []encStep{
		{"entities", m.entities}, {"entity_aliases", m.aliases}, {"relationships", m.relationships},
		{"memories", m.memories}, {"memory_edges", m.memoryEdges}, {"chunks", m.chunks},
		{"embeddings", m.embeddings}, {"compactions", m.compactions},
		{"delete_batch_changes", m.changes}, {"jobs", m.jobs},
		{"user_settings", m.settings}, {"sync_state", m.syncState},
	}
}

type idText struct{ id, a, b, c, d string }

func scanIDText(r *sql.Rows) (idText, error) {
	var v idText
	err := r.Scan(&v.id, &v.a, &v.b, &v.c, &v.d)
	return v, err
}

func (m *encMigrator) entities(ctx context.Context, tx *sql.Tx, limit int) (int, error) {
	rows, err := collect(ctx, tx,
		`SELECT id, COALESCE(name,''), COALESCE(type,''), COALESCE(source,''), COALESCE(attributes::text,'null')
		   FROM entities WHERE user_id = $1 AND enc IS NOT TRUE ORDER BY id LIMIT $2 FOR UPDATE SKIP LOCKED`,
		[]any{m.userID, limit}, scanIDText)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		name, typ, src, attrs := r.a, r.b, r.c, r.d
		var cts [4][]byte
		for i, p := range [][2]string{{"name", name}, {"type", typ}, {"source", src}, {"attributes", attrs}} {
			if cts[i], err = m.seal(store.TblEntities, r.id, p[0], p[1]); err != nil {
				return 0, err
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE entities SET enc = TRUE, name = NULL, type = NULL, source = NULL, attributes = NULL,
			   name_ct = $3, type_ct = $4, source_ct = $5, attributes_ct = $6, name_blind = $7, type_blind = $8
			 WHERE id = $1 AND user_id = $2`,
			r.id, m.userID, cts[0], cts[1], cts[2], cts[3],
			m.uk.Blind(store.NormName(name)), m.uk.Blind(store.NormName(typ))); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}

func (m *encMigrator) aliases(ctx context.Context, tx *sql.Tx, limit int) (int, error) {
	rows, err := collect(ctx, tx,
		`SELECT id, kind, COALESCE(value,''), COALESCE(source,''), ''
		   FROM entity_aliases WHERE user_id = $1 AND enc IS NOT TRUE ORDER BY id LIMIT $2 FOR UPDATE SKIP LOCKED`,
		[]any{m.userID, limit}, scanIDText)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		kind, value, src := r.a, r.b, r.c
		valueCT, err := m.seal(store.TblAliases, r.id, "value", value)
		if err != nil {
			return 0, err
		}
		var srcCT []byte
		if src != "" {
			if srcCT, err = m.seal(store.TblAliases, r.id, "source", src); err != nil {
				return 0, err
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE entity_aliases SET enc = TRUE, value = NULL, value_norm = NULL, source = NULL,
			   value_ct = $3, value_blind = $4, source_ct = $5
			 WHERE id = $1 AND user_id = $2`,
			r.id, m.userID, valueCT, m.uk.Blind(graph.NormalizeAlias(kind, value)), srcCT); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}

func (m *encMigrator) relationships(ctx context.Context, tx *sql.Tx, limit int) (int, error) {
	rows, err := collect(ctx, tx,
		`SELECT id, COALESCE(type,''), COALESCE(source,''), COALESCE(attributes::text,'null'), ''
		   FROM relationships WHERE user_id = $1 AND enc IS NOT TRUE ORDER BY id LIMIT $2 FOR UPDATE SKIP LOCKED`,
		[]any{m.userID, limit}, scanIDText)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		typ, src, attrs := r.a, r.b, r.c
		var cts [3][]byte
		for i, p := range [][2]string{{"type", typ}, {"source", src}, {"attributes", attrs}} {
			if cts[i], err = m.seal(store.TblRelationships, r.id, p[0], p[1]); err != nil {
				return 0, err
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE relationships SET enc = TRUE, type = NULL, source = NULL, attributes = NULL,
			   type_ct = $3, source_ct = $4, attributes_ct = $5, type_blind = $6
			 WHERE id = $1 AND user_id = $2`,
			r.id, m.userID, cts[0], cts[1], cts[2], m.uk.Blind(store.NormName(typ))); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}

func (m *encMigrator) memories(ctx context.Context, tx *sql.Tx, limit int) (int, error) {
	rows, err := collect(ctx, tx,
		`SELECT id, COALESCE(content,''), COALESCE(source,''), COALESCE(speaker,''), ''
		   FROM memories WHERE user_id = $1 AND enc IS NOT TRUE ORDER BY id LIMIT $2 FOR UPDATE SKIP LOCKED`,
		[]any{m.userID, limit}, scanIDText)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		contentCT, err := m.seal(store.TblMemories, r.id, "content", r.a)
		if err != nil {
			return 0, err
		}
		sourceCT, err := m.seal(store.TblMemories, r.id, "source", r.b)
		if err != nil {
			return 0, err
		}
		var speakerCT []byte
		if r.c != "" {
			if speakerCT, err = m.seal(store.TblMemories, r.id, "speaker", r.c); err != nil {
				return 0, err
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE memories SET enc = TRUE, content = NULL, source = NULL, speaker = NULL,
			   content_ct = $3, source_ct = $4, speaker_ct = $5
			 WHERE id = $1 AND user_id = $2`,
			r.id, m.userID, contentCT, sourceCT, speakerCT); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}

func (m *encMigrator) memoryEdges(ctx context.Context, tx *sql.Tx, limit int) (int, error) {
	rows, err := collect(ctx, tx,
		`SELECT id, COALESCE(source,''), '', '', ''
		   FROM memory_edges WHERE user_id = $1 AND enc IS NOT TRUE ORDER BY id LIMIT $2 FOR UPDATE SKIP LOCKED`,
		[]any{m.userID, limit}, scanIDText)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		var ct []byte
		if r.a != "" {
			if ct, err = m.seal(store.TblMemoryEdges, r.id, "source", r.a); err != nil {
				return 0, err
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE memory_edges SET enc = TRUE, source = NULL, source_ct = $3 WHERE id = $1 AND user_id = $2`,
			r.id, m.userID, ct); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}

func (m *encMigrator) chunks(ctx context.Context, tx *sql.Tx, limit int) (int, error) {
	rows, err := collect(ctx, tx,
		`SELECT id, COALESCE(content,''), COALESCE(metadata::text,'null'), '', ''
		   FROM chunks WHERE user_id = $1 AND enc IS NOT TRUE ORDER BY id LIMIT $2 FOR UPDATE SKIP LOCKED`,
		[]any{m.userID, limit}, scanIDText)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		contentCT, err := m.seal(store.TblChunks, r.id, "content", r.a)
		if err != nil {
			return 0, err
		}
		metaCT, err := m.seal(store.TblChunks, r.id, "metadata", r.b)
		if err != nil {
			return 0, err
		}
		var blind []byte
		var meta map[string]any
		if json.Unmarshal([]byte(r.b), &meta) == nil {
			if src, _ := meta["source"].(string); src != "" {
				blind = m.uk.Blind(store.NormName(src))
			}
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE chunks SET enc = TRUE, content = NULL, metadata = NULL,
			   content_ct = $3, metadata_ct = $4, source_blind = $5
			 WHERE id = $1 AND user_id = $2`,
			r.id, m.userID, contentCT, metaCT, blind); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}

func (m *encMigrator) embeddings(ctx context.Context, tx *sql.Tx, limit int) (int, error) {
	rows, err := collect(ctx, tx,
		`SELECT id, COALESCE(embedding::text,''), '', '', ''
		   FROM embeddings WHERE user_id = $1 AND enc IS NOT TRUE ORDER BY id LIMIT $2 FOR UPDATE SKIP LOCKED`,
		[]any{m.userID, limit}, scanIDText)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		vec, err := memory.ParseVectorLiteral(r.a)
		if err != nil {
			return 0, err
		}
		ct, err := m.uk.Seal(m.userID, store.TblEmbeddings, r.id, "embedding", store.EncodeVec(vec))
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE embeddings SET enc = TRUE, embedding = NULL, embedding_ct = $3 WHERE id = $1 AND user_id = $2`,
			r.id, m.userID, ct); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}

func (m *encMigrator) compactions(ctx context.Context, tx *sql.Tx, limit int) (int, error) {
	rows, err := collect(ctx, tx,
		`SELECT id, proposal_json::text, '', '', ''
		   FROM compactions WHERE user_id = $1 AND enc IS NOT TRUE AND proposal_json IS NOT NULL
		  ORDER BY id LIMIT $2 FOR UPDATE SKIP LOCKED`,
		[]any{m.userID, limit}, scanIDText)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		ct, err := m.seal("compactions", r.id, "proposal_json", r.a)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE compactions SET enc = TRUE, proposal_json = NULL, proposal_ct = $3 WHERE id = $1 AND user_id = $2`,
			r.id, m.userID, ct); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}

// changes seals the undo snapshots with the same row id the graph package
// uses (batch id and change id). A snapshot taken before the migration is the
// legacy row; undoing such a batch restores that legacy form, which the row
// form guard rejects for an encrypted user (documented limitation).
func (m *encMigrator) changes(ctx context.Context, tx *sql.Tx, limit int) (int, error) {
	type ch struct {
		id            int64
		batch, rowTxt string
	}
	rows, err := collect(ctx, tx,
		`SELECT id, batch_id, row_json::text FROM delete_batch_changes
		  WHERE user_id = $1 AND enc IS NOT TRUE AND row_json IS NOT NULL
		  ORDER BY id LIMIT $2 FOR UPDATE SKIP LOCKED`,
		[]any{m.userID, limit}, func(r *sql.Rows) (ch, error) {
			var c ch
			err := r.Scan(&c.id, &c.batch, &c.rowTxt)
			return c, err
		})
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		ct, err := m.seal("delete_batch_changes", fmt.Sprintf("%s/%d", r.batch, r.id), "row_json", r.rowTxt)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE delete_batch_changes SET enc = TRUE, row_json = NULL, row_ct = $2 WHERE id = $1`, r.id, ct); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}

func (m *encMigrator) jobs(ctx context.Context, tx *sql.Tx, limit int) (int, error) {
	// Error texts of finished jobs can echo content: replace with fixed text.
	if _, err := tx.ExecContext(ctx,
		`UPDATE jobs SET error = 'job failed' WHERE user_id = $1 AND error <> '' AND error NOT IN ('job failed', 'locked')`,
		m.userID); err != nil {
		return 0, err
	}
	rows, err := collect(ctx, tx,
		`SELECT id, payload, '', '', '' FROM jobs
		  WHERE user_id = $1 AND enc IS NOT TRUE AND COALESCE(payload,'') <> ''
		  ORDER BY id LIMIT $2 FOR UPDATE SKIP LOCKED`,
		[]any{m.userID, limit}, scanIDText)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		ct, err := m.seal("jobs", r.id, "payload", r.a)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE jobs SET enc = TRUE, payload = '', payload_ct = $3 WHERE id = $1 AND user_id = $2`,
			r.id, m.userID, ct); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}

func (m *encMigrator) settings(ctx context.Context, tx *sql.Tx, limit int) (int, error) {
	ids, err := collect(ctx, tx,
		`SELECT user_id FROM user_settings WHERE user_id = $1 AND enc IS NOT TRUE LIMIT $2 FOR UPDATE SKIP LOCKED`,
		[]any{m.userID, limit}, func(r *sql.Rows) (string, error) {
			var s string
			return s, r.Scan(&s)
		})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, id := range ids {
		ok, err := m.st.SealLegacySettingsTx(ctx, tx, id, m.uk)
		if err != nil {
			return 0, err
		}
		if ok {
			n++
		}
	}
	return n, nil
}

func (m *encMigrator) syncState(ctx context.Context, tx *sql.Tx, limit int) (int, error) {
	rows, err := collect(ctx, tx,
		`SELECT connector, COALESCE(state,''), '', '', ''
		   FROM sync_state WHERE user_id = $1 AND (enc IS NOT TRUE OR state IS NOT NULL)
		  ORDER BY connector LIMIT $2 FOR UPDATE SKIP LOCKED`,
		[]any{m.userID, limit}, scanIDText)
	if err != nil {
		return 0, err
	}
	for _, r := range rows {
		ct, err := m.seal("sync_state", r.id, "state", r.a)
		if err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE sync_state SET enc = TRUE, state = NULL, state_ct = $3 WHERE connector = $1 AND user_id = $2`,
			r.id, m.userID, ct); err != nil {
			return 0, err
		}
	}
	return len(rows), nil
}
