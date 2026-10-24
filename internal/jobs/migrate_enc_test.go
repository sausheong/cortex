package jobs

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/testutil"
)

// migSeeds are strings that must not survive on disk after the migration.
var migSeeds = []string{
	"Alice Chen", "Acme Corp", "WidgetCo", "team-seed", "Stripe", "SECRETCHUNKTEXT", "SECRETPAYLOAD",
	"SECRETCURSOR", "SECRETPROPOSAL", "alice.secret@example.com", "sk-secret-3f9a", "zedowner", "SECRETEDGE",
}

type migEnv struct {
	*env
	uid  string
	uk   *keys.UK
	team *testutil.Team
}

// legacyUser seeds a user in the legacy (plaintext) form with every table
// populated, then marks it migrating.
func (e *env) legacyUser(t *testing.T, sub string) *migEnv {
	t.Helper()
	ctx := t.Context()
	uid := e.user(t, sub)
	emb := &stub{dims: 256}
	cx := e.handle(uid, emb, 0)
	team, err := testutil.SeedTeam(ctx, cx)
	if err != nil {
		t.Fatal(err)
	}
	if err := cx.AddAlias(ctx, team.Alice.ID, "email", "alice.secret@example.com"); err != nil {
		t.Fatal(err)
	}
	addMemory(t, cx, emb, "A memory with an embedding about Stripe")
	addChunk(t, cx, emb, "SECRETCHUNKTEXT lives here")
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO sync_state (user_id, connector, state) VALUES ($1, 'gmail', 'SECRETCURSOR')`, []any{uid}},
		{`INSERT INTO compactions (id, user_id, status, proposal_json) VALUES ('c_mig', $1, 'ready', '[{"text":"SECRETPROPOSAL"}]'::jsonb)`, []any{uid}},
		{`INSERT INTO jobs (id, user_id, kind, status, payload) VALUES ('j_mig', $1, 'remember', 'queued', 'SECRETPAYLOAD')`, []any{uid}},
		{`INSERT INTO delete_batches (id, user_id, kind) VALUES ('b_mig', $1, 'merge')`, []any{uid}},
		{`INSERT INTO delete_batch_changes (batch_id, user_id, table_name, op, row_json) VALUES ('b_mig', $1, 'relationships', 'update', '{"type":"SECRETEDGE","enc":false}'::jsonb)`, []any{uid}},
	} {
		if _, err := e.db.ExecContext(ctx, q.sql, q.args...); err != nil {
			t.Fatalf("%s: %v", q.sql, err)
		}
	}
	e.mustExec(t, `UPDATE users SET encryption_state = 'migrating' WHERE id = $1`, uid)
	uk, err := keys.NewUK()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(uk.Zero)
	return &migEnv{env: e, uid: uid, uk: uk, team: team}
}

func (m *migEnv) run(t *testing.T) error {
	t.Helper()
	return EncryptUserJob(t.Context(), m.root, m.st, m.uid, m.uk, nil)
}

func (m *migEnv) state(t *testing.T) string {
	t.Helper()
	var s string
	if err := m.db.QueryRowContext(t.Context(), `SELECT encryption_state FROM users WHERE id = $1`, m.uid).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// keyed returns a handle for the user that holds the key, in the given state.
func (m *migEnv) keyed(t *testing.T, state string) *cortex.Cortex {
	t.Helper()
	cx := m.root.ForUser(m.uid).WithKey(m.uk, state)
	t.Cleanup(cx.Release)
	return cx
}

func (m *migEnv) names(t *testing.T, cx *cortex.Cortex) []string {
	t.Helper()
	es, err := cx.FindEntities(t.Context(), cortex.EntityFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range es {
		out = append(out, e.Type+"/"+e.Name)
	}
	sort.Strings(out)
	return out
}

func (m *migEnv) memories(t *testing.T, cx *cortex.Cortex) []string {
	t.Helper()
	ms, _, err := cx.ListMemories(t.Context(), cortex.MemoryListFilter{}, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, x := range ms {
		out = append(out, x.Content)
	}
	sort.Strings(out)
	return out
}

func TestMigrationEncryptsAllTables(t *testing.T) {
	e := newEnv(t)
	m := e.legacyUser(t, "zedowner")
	beforeNames := m.names(t, e.root.ForUser(m.uid))
	beforeMems := m.memories(t, e.root.ForUser(m.uid))
	if len(beforeNames) < 8 || len(beforeMems) < 5 {
		t.Fatalf("seed too small: %d entities %d memories", len(beforeNames), len(beforeMems))
	}
	var reports []int
	if err := MigrateOwner(t.Context(), e.root, e.st, m.uid, m.uk, func(done, total int) { reports = append(reports, done) }); err != nil {
		t.Fatal(err)
	}
	if len(reports) < 2 || reports[len(reports)-1] == 0 {
		t.Fatalf("progress not reported: %v", reports)
	}
	if got := m.state(t); got != "encrypted" {
		t.Fatalf("state = %q", got)
	}
	rep, err := PostCheck(t.Context(), e.st, m.uid)
	if err != nil || rep.Total != 0 {
		t.Fatalf("post-check = %+v, %v", rep, err)
	}
	if e.count(t, `SELECT count(*) FROM cortex_meta WHERE key = $1`, "enc_migration_done:"+m.uid) != 1 {
		t.Fatal("completion marker missing")
	}
	cx := m.keyed(t, "encrypted")
	if got := m.names(t, cx); strings.Join(got, "|") != strings.Join(beforeNames, "|") {
		t.Fatalf("entities changed:\n%v\n%v", beforeNames, got)
	}
	if got := m.memories(t, cx); strings.Join(got, "|") != strings.Join(beforeMems, "|") {
		t.Fatalf("memories changed:\n%v\n%v", beforeMems, got)
	}
	set, err := e.st.GetSettings(t.Context(), m.uid, m.uk)
	if err != nil || set.APIKey != "sk-secret-3f9a" || set.EmbedDims != 256 {
		t.Fatalf("settings after migration = %+v, %v", set, err)
	}
	if e.count(t, `SELECT count(*) FROM embeddings WHERE user_id = $1 AND (enc IS NOT TRUE OR embedding IS NOT NULL OR embedding_ct IS NULL)`, m.uid) != 0 {
		t.Fatal("embeddings not sealed")
	}
	if e.count(t, `SELECT count(*) FROM entity_aliases WHERE user_id = $1 AND enc AND value_ct IS NOT NULL`, m.uid) < 1 {
		t.Fatal("alias not sealed")
	}
	// Reads of the migrated alias still resolve through the blind index.
	if _, err := cx.ResolveEntity(t.Context(), "email", "Alice.Secret@example.com"); err != nil {
		t.Fatalf("alias lookup after migration: %v", err)
	}
}

func TestMigrationIdempotent(t *testing.T) {
	e := newEnv(t)
	m := e.legacyUser(t, "zedowner")
	if err := m.run(t); err != nil {
		t.Fatal(err)
	}
	snap := func() string {
		var s string
		if err := e.db.QueryRowContext(t.Context(), `SELECT
			(SELECT md5(string_agg(encode(name_ct,'hex'), ',' ORDER BY id)) FROM entities WHERE user_id = $1) || '/' ||
			(SELECT md5(string_agg(encode(content_ct,'hex'), ',' ORDER BY id)) FROM memories WHERE user_id = $1) || '/' ||
			(SELECT md5(string_agg(encode(embedding_ct,'hex'), ',' ORDER BY id)) FROM embeddings WHERE user_id = $1)`, m.uid).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	first := snap()
	var moved int
	if err := EncryptUserJob(t.Context(), e.root, e.st, m.uid, m.uk, func(done, _ int) { moved = done }); err != nil {
		t.Fatal(err)
	}
	if moved != 0 || snap() != first {
		t.Fatalf("second run changed rows (moved %d)", moved)
	}
}

func TestMigrationResumable(t *testing.T) {
	e := newEnv(t)
	m := e.legacyUser(t, "zedowner")
	old := BatchSize
	BatchSize = 3
	defer func() { BatchSize = old; encBatchHook = nil }()
	before := m.names(t, e.root.ForUser(m.uid))
	beforeMems := m.memories(t, e.root.ForUser(m.uid))

	ctx, cancel := context.WithCancel(t.Context())
	batches := 0
	encBatchHook = func(string, int) error {
		batches++
		if batches == 2 {
			cancel()
		}
		return nil
	}
	err := EncryptUserJob(ctx, e.root, e.st, m.uid, m.uk, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("interrupted run = %v", err)
	}
	if e.count(t, `SELECT count(*) FROM entities WHERE user_id = $1 AND enc`, m.uid) == 0 {
		t.Fatal("no progress was kept")
	}
	if rep, _ := PostCheck(t.Context(), e.st, m.uid); rep.Encrypted || m.state(t) != "migrating" {
		t.Fatal("half-migrated user flipped to encrypted")
	}
	encBatchHook = nil
	if err := MigrateOwner(t.Context(), e.root, e.st, m.uid, m.uk, nil); err != nil {
		t.Fatal(err)
	}
	cx := m.keyed(t, "encrypted")
	if got := m.names(t, cx); strings.Join(got, "|") != strings.Join(before, "|") {
		t.Fatalf("entities after resume:\n%v\n%v", before, got)
	}
	if got := m.memories(t, cx); strings.Join(got, "|") != strings.Join(beforeMems, "|") {
		t.Fatal("memories lost or duplicated by resume")
	}
}

func TestMigrationWithConcurrentWrites(t *testing.T) {
	e := newEnv(t)
	m := e.legacyUser(t, "zedowner")
	old := BatchSize
	BatchSize = 2
	defer func() { BatchSize = old; encBatchHook = nil }()
	encBatchHook = func(string, int) error { time.Sleep(3 * time.Millisecond); return nil }

	cx := m.keyed(t, "migrating")
	var wg sync.WaitGroup
	var migErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		migErr = m.run(t)
	}()
	for i := 0; i < 15; i++ {
		ent := &cortex.Entity{Type: "person", Name: fmt.Sprintf("Concurrent %d", i), Source: "live"}
		if err := cx.PutEntity(t.Context(), ent); err != nil {
			t.Fatalf("write during migration: %v", err)
		}
		// Re-assert a legacy entity during the run (dual-mode lookup).
		if i%5 == 0 {
			if err := cx.PutEntity(t.Context(), &cortex.Entity{Type: "person", Name: "Alice Chen", Source: "live"}); err != nil {
				t.Fatalf("legacy re-assert: %v", err)
			}
		}
		if err := cx.PutMemory(t.Context(), &cortex.Memory{Content: fmt.Sprintf("live memory %d", i), Source: "live"}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	if migErr != nil {
		t.Fatal(migErr)
	}
	encBatchHook = nil
	if err := MigrateOwner(t.Context(), e.root, e.st, m.uid, m.uk, nil); err != nil {
		t.Fatal(err)
	}
	ec := m.keyed(t, "encrypted")
	names := m.names(t, ec)
	for i := 0; i < 15; i++ {
		if !contains(names, fmt.Sprintf("person/Concurrent %d", i)) {
			t.Fatalf("lost concurrent entity %d", i)
		}
	}
	alices := 0
	for _, n := range names {
		if n == "person/Alice Chen" {
			alices++
		}
	}
	if alices != 1 {
		t.Fatalf("Alice Chen appears %d times", alices)
	}
	mems := m.memories(t, ec)
	for i := 0; i < 15; i++ {
		if !contains(mems, fmt.Sprintf("live memory %d", i)) {
			t.Fatalf("lost concurrent memory %d", i)
		}
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func TestPostCheckFindsLeftoverPlaintext(t *testing.T) {
	e := newEnv(t)
	m := e.legacyUser(t, "zedowner")
	if err := m.run(t); err != nil {
		t.Fatal(err)
	}
	e.mustExec(t, `INSERT INTO entities (id, user_id, type, name) VALUES ('e_left', $1, 'person', 'LEFTOVERNAME')`, m.uid)
	rep, err := PostCheck(t.Context(), e.st, m.uid)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Encrypted || rep.Total != 1 || rep.Leftover["entities"] != 1 {
		t.Fatalf("report = %+v", rep)
	}
	if got := m.state(t); got != "migrating" {
		t.Fatalf("state = %q, want migrating", got)
	}
	if e.count(t, `SELECT count(*) FROM cortex_meta WHERE key = $1`, "enc_migration_done:"+m.uid) != 0 {
		t.Fatal("marker stamped despite leftovers")
	}
	for k := range rep.Leftover {
		if strings.Contains(k, "LEFTOVERNAME") {
			t.Fatal("report carries content")
		}
	}
	// A further run fixes it.
	if err := MigrateOwner(t.Context(), e.root, e.st, m.uid, m.uk, nil); err != nil {
		t.Fatal(err)
	}
	if m.state(t) != "encrypted" {
		t.Fatal("not encrypted after rerun")
	}
}

func TestMigrationHandlesFailureMidBatch(t *testing.T) {
	e := newEnv(t)
	m := e.legacyUser(t, "zedowner")
	old := BatchSize
	BatchSize = 3
	defer func() { BatchSize = old; encBatchHook = nil }()
	calls := 0
	boom := errors.New("injected")
	encBatchHook = func(table string, n int) error {
		calls++
		if calls == 2 {
			return boom
		}
		return nil
	}
	if err := m.run(t); !errors.Is(err, boom) {
		t.Fatalf("run = %v", err)
	}
	// Only the first batch (3 entities) committed; the failed batch rolled back.
	if n := e.count(t, `SELECT count(*) FROM entities WHERE user_id = $1 AND enc`, m.uid); n != 3 {
		t.Fatalf("encrypted entities = %d, want 3", n)
	}
	encBatchHook = nil
	if err := MigrateOwner(t.Context(), e.root, e.st, m.uid, m.uk, nil); err != nil {
		t.Fatal(err)
	}
	if m.state(t) != "encrypted" {
		t.Fatal("not completed after the failure")
	}
}

func TestMigrationResumesOnNextKeyedRequest(t *testing.T) {
	e := newEnv(t)
	m := e.legacyUser(t, "zedowner")
	// ConfirmEnroll leaves a parked job.
	e.mustExec(t, `INSERT INTO jobs (id, user_id, kind, status) VALUES ('j_park', $1, $2, $3)`, m.uid, accounts.MigrateEncKind, accounts.StatusParked)
	pool := NewPool(1, func(ctx context.Context, j Job) error {
		if j.Kind != accounts.MigrateEncKind {
			return nil
		}
		if j.Key == nil {
			return errors.New("no key")
		}
		return MigrateOwner(ctx, e.root, e.st, j.UserID, j.Key, nil)
	}).WithDB(e.db)
	pool.Start(t.Context())
	defer pool.Stop()

	// No key attached: nothing runs, state stays migrating.
	if err := EnsureMigrationJob(t.Context(), pool, m.uid); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if m.state(t) != "migrating" {
		t.Fatal("migration ran without a key")
	}
	if e.count(t, `SELECT count(*) FROM jobs WHERE user_id = $1 AND kind = $2 AND status IN ('queued','running')`, m.uid, accounts.MigrateEncKind) != 1 {
		t.Fatal("parked job was not queued exactly once")
	}
	// The next keyed request attaches the key and the job completes.
	pool.AttachKey(m.uid, m.uk, time.Minute)
	if !waitFor(t, func() bool { return m.state(t) == "encrypted" }) {
		t.Fatal("migration did not resume on the keyed request")
	}
	if !waitFor(t, func() bool {
		return e.count(t, `SELECT count(*) FROM jobs WHERE user_id = $1 AND kind = $2 AND status = 'done'`, m.uid, accounts.MigrateEncKind) == 1
	}) {
		t.Fatal("job not marked done")
	}
	// A completed user gets no new job.
	if err := EnsureMigrationJob(t.Context(), pool, m.uid); err != nil {
		t.Fatal(err)
	}
	if e.count(t, `SELECT count(*) FROM jobs WHERE user_id = $1 AND kind = $2 AND status IN ('queued','running')`, m.uid, accounts.MigrateEncKind) != 0 {
		t.Fatal("job enqueued for an encrypted user")
	}
}

func TestMigrationDumpGrep(t *testing.T) {
	e := newEnv(t)
	m := e.legacyUser(t, "zedowner")
	// Make some dead tuples with plaintext first, as a real history would.
	e.mustExec(t, `UPDATE memories SET confidence = 0.9 WHERE user_id = $1`, m.uid)
	if err := MigrateOwner(t.Context(), e.root, e.st, m.uid, m.uk, nil); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	lower := make([]string, len(migSeeds))
	for i, s := range migSeeds {
		lower[i] = strings.ToLower(s)
	}
	hit := func(where, text string) {
		t.Helper()
		lt := strings.ToLower(text)
		for i, s := range lower {
			if strings.Contains(lt, s) {
				t.Errorf("%s still holds %q", where, migSeeds[i])
			}
		}
	}

	// (a) every row of every migrated table, as text. users.name is account
	// metadata (not content) and is excluded by listing tables explicitly.
	for _, tbl := range vacuumTables {
		rows, err := e.db.QueryContext(ctx, fmt.Sprintf(`SELECT to_jsonb(t)::text FROM %s t WHERE user_id = $1`, tbl), m.uid)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var s string
			rows.Scan(&s)
			hit("table "+tbl, s)
		}
		rows.Close()
	}

	// (b) planner statistics.
	rows, err := e.db.QueryContext(ctx,
		`SELECT tablename, COALESCE(most_common_vals::text,''), COALESCE(histogram_bounds::text,'')
		   FROM pg_stats WHERE schemaname = current_schema() AND tablename = ANY($1)`, vacuumTablesArray())
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var tbl, mcv, hb string
		rows.Scan(&tbl, &mcv, &hb)
		hit("pg_stats mcv of "+tbl, mcv)
		hit("pg_stats histogram of "+tbl, hb)
	}
	rows.Close()

	// (c) index pages through pageinspect, else assert the plaintext btree
	// indexes are partial or gone.
	e.db.ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS pageinspect`)
	var have bool
	e.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pageinspect')`).Scan(&have)
	if have {
		// VACUUM FULL keeps tuples that a concurrent transaction elsewhere in
		// the database can still see (other tests run in parallel), so re-run
		// it a few times until the index pages are clean.
		var leaks []string
		for attempt := 0; attempt < 15; attempt++ {
			leaks = indexLeaks(t, e, lower)
			if len(leaks) == 0 {
				break
			}
			time.Sleep(200 * time.Millisecond)
			if err := VacuumUser(ctx, e.db); err != nil {
				t.Fatal(err)
			}
		}
		for _, l := range leaks {
			t.Error(l)
		}
	} else {
		pr, err := e.db.QueryContext(ctx,
			`SELECT indexname, indexdef FROM pg_indexes WHERE schemaname = current_schema()
			  AND tablename IN ('entities','entity_aliases','relationships','memories','chunks')
			  AND indexdef ~* '\((name|type|source|value_norm|value|content)\)|\(.*, (name|type|value_norm)\)'`)
		if err != nil {
			t.Fatal(err)
		}
		for pr.Next() {
			var n, def string
			pr.Scan(&n, &def)
			if !strings.Contains(def, "enc IS NOT TRUE") {
				t.Errorf("plaintext btree index %s is neither partial nor dropped: %s", n, def)
			}
		}
		pr.Close()
	}
}

func vacuumTablesArray() []string {
	out := make([]string, len(vacuumTables))
	copy(out, vacuumTables)
	return out
}

// indexLeaks returns a message per seed string found in a btree index page of
// the migrated tables (pageinspect must be installed).
func indexLeaks(t *testing.T, e *env, lower []string) []string {
	t.Helper()
	ctx := t.Context()
	ir, err := e.db.QueryContext(ctx,
		`SELECT c.relname, pg_relation_size(c.oid) / 8192 FROM pg_class c
		   JOIN pg_index i ON i.indexrelid = c.oid JOIN pg_am a ON a.oid = c.relam
		  WHERE c.relnamespace = current_schema()::regnamespace AND a.amname = 'btree'
		    AND i.indrelid::regclass::text = ANY($1)`, vacuumTablesArray())
	if err != nil {
		t.Fatal(err)
	}
	type idx struct {
		name   string
		blocks int
	}
	var idxs []idx
	for ir.Next() {
		var x idx
		ir.Scan(&x.name, &x.blocks)
		idxs = append(idxs, x)
	}
	ir.Close()
	var out []string
	for _, x := range idxs {
		for blk := 1; blk < x.blocks; blk++ {
			pr, err := e.db.QueryContext(ctx, `SELECT data FROM bt_page_items(format('%I.%I', current_schema(), $1::text), $2)`, x.name, blk)
			if err != nil {
				continue
			}
			for pr.Next() {
				var d *string
				pr.Scan(&d)
				if d == nil {
					continue
				}
				if b, err := hex.DecodeString(strings.ReplaceAll(*d, " ", "")); err == nil {
					lt := strings.ToLower(string(b))
					for i, sd := range lower {
						if strings.Contains(lt, sd) {
							out = append(out, fmt.Sprintf("index %s still holds %q", x.name, migSeeds[i]))
						}
					}
				}
			}
			pr.Close()
		}
	}
	return out
}
