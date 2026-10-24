package acceptance

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/jobs"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/testutil"
	"github.com/sausheong/cortex/internal/web"
	"github.com/sausheong/cortex/internal/workset"
)

var bg = context.Background()

// corpusTokens are distinctive strings seeded through every write path. None
// may appear anywhere in a dump of an encrypted user's rows.
var corpusTokens = []string{
	"zxqentity", "zxqattr", "zxqalias", "zxqedgetype", "zxqedgesrc", "zxqmemory", "zxqchunk", "zxqmeta",
	"zxqdupone", "zxqduptwo", "zxqcompact", "zxqpayload", "zxqcursor", "zxqprovider", "zxqapikey", "zxqchatmodel",
	"zxqentsource", "zxqspeaker", "zxqmemsrc", "zxqedgetype2",
}

// populate writes a corpus through the public API: entities with attributes,
// aliases, edges, memories, chunks, an embedding and a merge (undo snapshot).
// It returns ids that later tests need.
type corpus struct {
	AliceID, BobID string
	MemID          string
}

func populate(t *testing.T, cx *cortex.Cortex, tag string) corpus {
	t.Helper()
	var c corpus
	a := &cortex.Entity{Type: "person", Name: "Zxqentity " + tag + " Ann", Source: "zxqentsource", Attributes: map[string]any{"role": "zxqattr " + tag}}
	b := &cortex.Entity{Type: "organization", Name: "Zxqentity " + tag + " Org"}
	for _, e := range []*cortex.Entity{a, b} {
		if err := cx.PutEntity(bg, e); err != nil {
			t.Fatal(err)
		}
	}
	c.AliceID, c.BobID = a.ID, b.ID
	if err := cx.AddAlias(bg, a.ID, "email", "zxqalias."+tag+"@example.org"); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutRelationship(bg, &cortex.Relationship{SourceID: a.ID, TargetID: b.ID, Type: "zxqedgetype", Source: "zxqedgesrc", Attributes: map[string]any{"k": "zxqmeta"}}); err != nil {
		t.Fatal(err)
	}
	m := &cortex.Memory{Content: "zxqmemory fact number one " + tag, Speaker: "zxqspeaker", Source: "zxqmemsrc", EntityIDs: []string{a.ID}}
	if err := cx.PutMemory(bg, m); err != nil {
		t.Fatal(err)
	}
	c.MemID = m.ID
	m2 := &cortex.Memory{Content: "zxqmemory fact number two " + tag, Speaker: "zxqspeaker", Source: "zxqmemsrc"}
	if err := cx.PutMemory(bg, m2); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutRelationship(bg, &cortex.Relationship{SourceID: b.ID, TargetID: a.ID, Type: "zxqedgetype2", Source: "zxqedgesrc", Attributes: map[string]any{"k": "zxqmeta"}}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := cx.PutChunk(bg, &cortex.Chunk{Content: fmt.Sprintf("zxqchunk body text %s %d", tag, i), EntityID: a.ID, Metadata: map[string]any{"source": fmt.Sprintf("zxqmeta doc %s %d", tag, i)}}); err != nil {
			t.Fatal(err)
		}
	}
	gen, err := cx.ActiveGeneration(bg)
	if err != nil {
		t.Fatal(err)
	}
	vec := make([]float32, 512)
	vec[0], vec[1] = 1, 0.5
	for _, id := range []string{m.ID, m2.ID} {
		if err := cx.PutEmbeddingGen(bg, id, "memory", gen, vec); err != nil {
			t.Fatal(err)
		}
	}
	// A merge writes an undo snapshot holding the dropped entity's content.
	d1 := &cortex.Entity{Type: "person", Name: "Zxqdupone " + tag}
	d2 := &cortex.Entity{Type: "person", Name: "Zxqduptwo " + tag, Attributes: map[string]any{"x": "zxqattr"}}
	for _, e := range []*cortex.Entity{d1, d2} {
		if err := cx.PutEntity(bg, e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := cx.MergeEntities(bg, d1.ID, d2.ID); err != nil {
		t.Fatal(err)
	}
	if err := cx.SetSyncState(bg, "gmail", "zxqcursor "+tag); err != nil {
		t.Fatal(err)
	}
	if _, err := cx.RefreshUsage(bg); err != nil {
		t.Fatalf("usage: %v", err)
	}
	return c
}

func TestDumpGrepFindsNoPlaintext(t *testing.T) {
	e := newEnv(t)
	uid := e.user("zxqowner")
	en := e.enroll(uid)
	if en.State != "encrypted" {
		t.Fatalf("state = %q", en.State)
	}
	uk := en.Res.UK
	cx := e.keyed(uid, uk, "encrypted")
	populate(t, cx, "a")

	// Provider settings, sessions, an agent token, a compaction proposal and a
	// job payload, all under the user key.
	if _, err := e.st.PutSettings(bg, uid, accounts.Settings{BaseURL: "https://zxqprovider.example/v1", APIKey: "sk-zxqapikey", ChatModel: "zxqchatmodel", EmbedModel: "zxqembedmodel", EmbedDims: 512}, uk); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.st.CreateAgentToken(bg, uid, "agent", uk); err != nil {
		t.Fatal(err)
	}
	sessions := web.NewSessions(e.st, nil)
	sid, _, err := sessions.Create(bg, uid)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := sessions.Unlock(bg, sid, uk); err != nil {
		t.Fatal(err)
	}
	e.exec(`INSERT INTO compactions (id, user_id, status) VALUES ('c_acc', $1, 'proposing')`, uid)
	if err := e.st.StoreCompactionProposal(bg, uid, "c_acc", uk, []model.Change{{Kind: "merge", SourceIDs: []string{"a", "b"}, Result: "zxqcompact result", Sources: []string{"zxqcompact one"}}}); err != nil {
		t.Fatal(err)
	}
	pool := jobs.NewPool(1, func(context.Context, jobs.Job) error { return nil }).WithDB(e.db)
	if _, err := pool.EnqueueWithKey(bg, uid, "remember", "zxqpayload body", uk); err != nil {
		t.Fatal(err)
	}

	dump := e.dump()
	for _, tok := range corpusTokens {
		if strings.Contains(dump, tok) {
			i := strings.Index(dump, tok)
			lo, hi := max(0, i-80), min(len(dump), i+80)
			t.Errorf("dump contains %q near: %s", tok, dump[lo:hi])
		}
		if hx := fmt.Sprintf("%x", tok); strings.Contains(dump, hx) {
			t.Errorf("dump contains hex of %q", tok)
		}
	}
	// The owner entity carries the display name; the users table holds the
	// account identity (name, email) in the clear by design, so that table is
	// excluded for this one token.
	for _, line := range strings.Split(dump, "\n") {
		if !strings.HasPrefix(line, "users: ") && strings.Contains(line, "zxqowner") {
			t.Errorf("owner display name in plaintext: %.200s", line)
		}
	}
	// And the owner can still read it all back.
	es, err := cx.FindEntities(bg, cortex.EntityFilter{})
	if err != nil || len(es) < 4 {
		t.Fatalf("read back entities: %d, %v", len(es), err)
	}
}

func TestWrongOrAbsentSecretCannotRead(t *testing.T) {
	e := newEnv(t)
	uid := e.user("zxqowner")
	en := e.enroll(uid)
	uk := en.Res.UK
	populate(t, e.keyed(uid, uk, "encrypted"), "a")
	tok, _, err := e.st.CreateAgentToken(bg, uid, "agent", uk)
	if err != nil {
		t.Fatal(err)
	}

	// Wrong PRF output.
	wrong := make([]byte, 32)
	if got, err := e.unlock(uid, en.Dev, wrong); err == nil || got != nil {
		t.Fatal("wrong PRF unlocked")
	}
	// Right passkey, right PRF works (control).
	if got, err := e.unlock(uid, en.Dev, nil); err != nil {
		t.Fatalf("control unlock: %v", err)
	} else {
		got.Zero()
	}
	// Wrong recovery key (valid shape, wrong value).
	groups := strings.Split(en.Res.Recovery, "-")
	bad := append([]string{}, groups...)
	bad[0] = flip(bad[0])
	if got, err := e.st.UnlockWithRecovery(bg, uid, strings.Join(bad, "-")); err == nil || got != nil {
		t.Fatal("wrong recovery key unlocked")
	}
	// Wrong or absent session secret.
	sessions := web.NewSessions(e.st, nil)
	sid, _, _ := sessions.Create(bg, uid)
	if _, _, err := sessions.Unlock(bg, sid, uk); err != nil {
		t.Fatal(err)
	}
	if su, err := sessions.Lookup(bg, sid, "wrong-secret"); err != nil || su.Unlocked || su.Key != nil {
		t.Fatalf("wrong session secret unlocked: %+v %v", su, err)
	}
	if su, err := sessions.Lookup(bg, sid, ""); err != nil || su.Unlocked || su.Key != nil {
		t.Fatalf("absent session secret unlocked: %+v %v", su, err)
	}
	// Wrong token.
	if _, _, got, err := e.st.ResolveBearer(bg, tok+"x"); err == nil || got != nil {
		t.Fatal("wrong token resolved")
	}
	// Absent key: every read is ErrLocked, never an empty result.
	locked := e.keyed(uid, nil, "encrypted")
	checkLocked := func(name string, err error) {
		t.Helper()
		if !errors.Is(err, model.ErrLocked) {
			t.Errorf("%s without key = %v, want ErrLocked", name, err)
		}
	}
	_, _, err = locked.ListMemories(bg, cortex.MemoryListFilter{}, 10, 0)
	checkLocked("ListMemories", err)
	_, err = locked.FindEntities(bg, cortex.EntityFilter{})
	checkLocked("FindEntities", err)
	_, err = locked.Recall(bg, "zxqmemory")
	checkLocked("Recall", err)
	_, err = locked.SearchKeyword(bg, "zxqchunk", 5)
	checkLocked("SearchKeyword", err)
	checkLocked("PutMemory", locked.PutMemory(bg, &cortex.Memory{Content: "x"}))
	// A random wrong key reads nothing and says so with an error.
	other, _ := keys.NewUK()
	defer other.Zero()
	wk := e.keyed(uid, other, "encrypted")
	if ms, _, err := wk.ListMemories(bg, cortex.MemoryListFilter{}, 10, 0); err == nil || len(ms) != 0 {
		t.Fatalf("wrong UK read %d memories, err %v", len(ms), err)
	}
	if es, err := wk.FindEntities(bg, cortex.EntityFilter{}); err == nil || len(es) != 0 {
		t.Fatalf("wrong UK read %d entities, err %v", len(es), err)
	}
}

func flip(g string) string {
	if g == "" {
		return g
	}
	c := byte('A')
	if g[0] == 'A' {
		c = 'B'
	}
	return string(c) + g[1:]
}

// ctColumns lists the bytea *_ct columns of table.
func (e *env) ctColumns(table string) []string {
	rows, err := e.db.Query(`SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 AND column_name LIKE '%\_ct' ORDER BY 1`, table)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		rows.Scan(&c)
		out = append(out, c)
	}
	return out
}

func TestCiphertextMovedAcrossRowOrUserFailsAAD(t *testing.T) {
	e := newEnv(t)
	ua, ub := e.user("zxqa"), e.user("zxqb")
	e.exec(`UPDATE users SET encryption_state = 'encrypted' WHERE id IN ($1,$2)`, ua, ub)
	e.exec(`DELETE FROM entities WHERE enc IS NOT TRUE`)
	// Both users share one key: AAD is then the only thing that stops a
	// ciphertext moved between rows or users from decrypting.
	uk, _ := keys.NewUK()
	defer uk.Zero()

	type loader struct {
		table string
		load  func(cx *cortex.Cortex) error
	}
	loaders := []loader{
		{"entities", func(cx *cortex.Cortex) error { _, err := cx.FindEntities(bg, cortex.EntityFilter{}); return err }},
		{"entity_aliases", func(cx *cortex.Cortex) error {
			es, err := cx.FindEntities(bg, cortex.EntityFilter{})
			if err != nil {
				return err
			}
			for _, en := range es {
				if _, err := cx.Aliases(bg, en.ID); err != nil {
					return err
				}
			}
			return nil
		}},
		{"relationships", func(cx *cortex.Cortex) error { _, err := cx.AllRelationships(bg); return err }},
		{"memories", func(cx *cortex.Cortex) error {
			_, _, err := cx.ListMemories(bg, cortex.MemoryListFilter{}, 50, 0)
			return err
		}},
		{"chunks", func(cx *cortex.Cortex) error { _, err := cx.SearchKeyword(bg, "zxqchunk", 10); return err }},
		{"embeddings", func(cx *cortex.Cortex) error { _, err := cx.SearchMemoryVector(bg, "zxqmemory", 5); return err }},
	}

	reset := func() (*cortex.Cortex, *cortex.Cortex) {
		for _, tb := range []string{"entity_aliases", "relationships", "embeddings", "chunks", "memories", "memory_edges", "delete_batch_changes", "delete_batches", "entities"} {
			e.exec(`DELETE FROM `+tb+` WHERE user_id IN ($1,$2)`, ua, ub)
		}
		ca, cb := e.keyed(ua, uk, "encrypted"), e.keyed(ub, uk, "encrypted")
		populate(t, ca, "a")
		populate(t, cb, "b")
		return ca, cb
	}

	for _, l := range loaders {
		cols := e.ctColumns(l.table)
		if len(cols) == 0 {
			t.Fatalf("table %s has no ciphertext columns", l.table)
		}
		for _, col := range cols {
			for _, mode := range []string{"row", "user"} {
				name := l.table + "." + col + "/" + mode
				t.Run(name, func(t *testing.T) {
					ca, _ := reset()
					workset.Drop(ua)
					if err := l.load(ca); err != nil {
						t.Fatalf("baseline load fails (%v)", err)
					}
					idq := `SELECT id FROM ` + l.table + ` WHERE user_id = $1 AND ` + col + ` IS NOT NULL ORDER BY id`
					pick := func(user string) []string {
						rows, err := e.db.Query(idq, user)
						if err != nil {
							t.Fatal(err)
						}
						defer rows.Close()
						var ids []string
						for rows.Next() {
							var id string
							rows.Scan(&id)
							ids = append(ids, id)
						}
						return ids
					}
					var r1, r2 string
					if mode == "row" {
						ids := pick(ua)
						if len(ids) < 2 {
							t.Fatalf("fewer than two %s rows with %s", l.table, col)
						}
						r1, r2 = ids[0], ids[1]
					} else {
						ia, ib := pick(ua), pick(ub)
						if len(ia) < 1 || len(ib) < 1 {
							t.Fatalf("no %s rows with %s", l.table, col)
						}
						r1, r2 = ia[0], ib[0]
					}
					// Swap the two ciphertexts.
					e.exec(`WITH a AS (SELECT `+col+` AS v FROM `+l.table+` WHERE id = $1), b AS (SELECT `+col+` AS v FROM `+l.table+` WHERE id = $2)
					        UPDATE `+l.table+` t SET `+col+` = CASE WHEN t.id = $1 THEN (SELECT v FROM b) ELSE (SELECT v FROM a) END WHERE t.id IN ($1,$2)`, r1, r2)
					if e.count(`SELECT count(*) FROM `+l.table+` WHERE id = $1 AND `+col+` = (SELECT `+col+` FROM `+l.table+` WHERE id = $2)`, r1, r2) != 0 {
						t.Skipf("ciphertexts were identical, swap is a no-op")
					}
					// Evict the cached working set so the load decrypts the
					// tampered rows (a real attacker waits for eviction).
					workset.Drop(ua)
					workset.Drop(ub)
					if err := l.load(e.keyed(ua, uk, "encrypted")); err == nil {
						t.Fatalf("a %s.%s ciphertext moved between %ss decrypted", l.table, col, mode)
					}
				})
			}
		}
	}
}

func TestSessionWrapsDeletedOnSignOut(t *testing.T) {
	e := newEnv(t)
	uid := e.user("zxqowner")
	en := e.enroll(uid)
	sessions := web.NewSessions(e.st, nil)
	sid, _, err := sessions.Create(bg, uid)
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := sessions.Unlock(bg, sid, en.Res.UK)
	if err != nil {
		t.Fatal(err)
	}
	if e.count(`SELECT count(*) FROM user_key_wraps WHERE user_id=$1 AND kind='session'`, uid) != 1 {
		t.Fatal("no session wrap after unlock")
	}
	if err := sessions.Delete(bg, sid); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM user_key_wraps WHERE user_id=$1 AND kind='session'`, uid); n != 0 {
		t.Fatalf("%d session wraps left after sign-out", n)
	}
	if _, err := sessions.Lookup(bg, sid, secret); err == nil {
		t.Fatal("signed-out session still resolves")
	}
	// The passkey and recovery wraps stay: the user can unlock again.
	if n := e.count(`SELECT count(*) FROM user_key_wraps WHERE user_id=$1 AND kind IN ('passkey','recovery')`, uid); n != 2 {
		t.Fatalf("passkey/recovery wraps = %d", n)
	}
}

func TestSoftDeletedAccountSessionCannotUnlock(t *testing.T) {
	e := newEnv(t)
	uid := e.user("zxqowner")
	en := e.enroll(uid)
	sessions := web.NewSessions(e.st, nil)
	sid, _, _ := sessions.Create(bg, uid)
	secret, _, err := sessions.Unlock(bg, sid, en.Res.UK)
	if err != nil {
		t.Fatal(err)
	}
	if su, err := sessions.Lookup(bg, sid, secret); err != nil || !su.Unlocked {
		t.Fatalf("control lookup: %+v %v", su, err)
	} else {
		su.Key.Zero()
	}
	if err := e.st.DeleteAccount(bg, uid); err != nil {
		t.Fatal(err)
	}
	su, err := sessions.Lookup(bg, sid, secret)
	if err == nil || su.Unlocked || su.Key != nil {
		t.Fatalf("soft-deleted account session unlocked: %+v %v", su, err)
	}
	if n := e.count(`SELECT count(*) FROM user_key_wraps WHERE user_id=$1 AND kind='session'`, uid); n != 0 {
		t.Fatalf("%d session wraps survive soft delete", n)
	}
	// Restoring does not resurrect the session either.
	if err := e.st.RestoreAccount(bg, uid); err != nil {
		t.Fatal(err)
	}
	if su, err := sessions.Lookup(bg, sid, secret); su.Unlocked || (err == nil && su.Key != nil) {
		t.Fatalf("restored account session unlocked: %+v %v", su, err)
	}
}

func TestOldRecoveryKeyFailsAfterRegenerate(t *testing.T) {
	e := newEnv(t)
	uid := e.user("zxqowner")
	en := e.enroll(uid)
	old := en.Res.Recovery
	fresh, err := e.st.RegenerateRecovery(bg, uid, en.Res.UK)
	if err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM user_key_wraps WHERE user_id=$1 AND kind='recovery'`, uid); n != 1 {
		t.Fatalf("recovery wrap rows = %d, want exactly 1", n)
	}
	if got, err := e.st.UnlockWithRecovery(bg, uid, old); err == nil || got != nil {
		t.Fatal("old recovery key still unlocks")
	}
	got, err := e.st.UnlockWithRecovery(bg, uid, fresh)
	if err != nil {
		t.Fatalf("new recovery key: %v", err)
	}
	if !got.Equal(en.Res.UK) {
		t.Fatal("new recovery key unwraps a different key")
	}
	got.Zero()
}

func TestParkedJobResumesOnNextVisit(t *testing.T) {
	e := newEnv(t)
	uid := e.user("zxqowner")
	en := e.enroll(uid)
	uk := en.Res.UK
	var mu sync.Mutex
	ran := map[string]string{}
	pool := jobs.NewPool(1, func(ctx context.Context, j jobs.Job) error {
		mu.Lock()
		ran[j.ID] = j.Payload
		mu.Unlock()
		return nil
	}).WithDB(e.db)
	id, err := pool.EnqueueWithKey(bg, uid, "x", "zxqpayload body", uk)
	if err != nil {
		t.Fatal(err)
	}
	pool.Start(bg)
	defer pool.Stop()
	time.Sleep(400 * time.Millisecond)
	mu.Lock()
	_, early := ran[id]
	mu.Unlock()
	if early {
		t.Fatal("job ran without a key attached")
	}
	if e.count(`SELECT count(*) FROM jobs WHERE id=$1 AND status='queued'`, id) != 1 {
		t.Fatal("parked job is not still queued")
	}
	// The next key-bearing request attaches the key and the job runs.
	pool.AttachKey(uid, uk, time.Minute)
	pool.Resume(uid)
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		p, ok := ran[id]
		mu.Unlock()
		if ok {
			if p != "zxqpayload body" {
				t.Fatalf("payload = %q", p)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("parked job did not resume on the next keyed request")
}

// legacyOwner builds a plaintext owner with a seeded team and settings, then
// enrolls it (state migrating, a parked migration job) and returns the key.
func legacyOwner(t *testing.T, e *env) (uid string, en *enrolled) {
	t.Helper()
	// Seed a pre-enrollment account: drop the "encryption required" cohort
	// marker so legacy writes are allowed, as for the existing owner.
	e.exec(`DELETE FROM cortex_meta WHERE key = 'encryption_required_since'`)
	uid = e.user("zxqowner")
	cx := e.root.ForUser(uid)
	if _, err := testutil.SeedTeam(bg, cx); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutMemory(bg, &cortex.Memory{Content: "zxqmemory legacy fact"}); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutChunk(bg, &cortex.Chunk{Content: "zxqchunk legacy body", Metadata: map[string]any{"source": "zxqmeta legacy"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.PutSettings(bg, uid, accounts.Settings{BaseURL: "https://zxqprovider.example/v1", APIKey: "sk-zxqapikey", ChatModel: "zxqchatmodel", EmbedModel: "zxqembedmodel", EmbedDims: 256}, nil); err != nil {
		t.Fatal(err)
	}
	en = e.enroll(uid)
	if en.State != "migrating" {
		t.Fatalf("owner with data enrolled to %q, want migrating", en.State)
	}
	return uid, en
}

func TestMigrationIdempotentAndResumable(t *testing.T) {
	e := newEnv(t)
	uid, en := legacyOwner(t, e)
	uk := en.Res.UK

	// Interrupt after the first batch, then resume to completion.
	old := jobs.BatchSize
	jobs.BatchSize = 3
	defer func() { jobs.BatchSize = old }()
	ctx, cancel := context.WithCancel(bg)
	cancel()
	if err := jobs.EncryptUserJob(ctx, e.root, e.st, uid, uk, nil); err == nil {
		t.Fatal("cancelled run reported success")
	}
	if e.state(uid) != "migrating" {
		t.Fatal("interrupted migration changed state")
	}
	if err := jobs.MigrateOwner(bg, e.root, e.st, uid, uk, nil); err != nil {
		t.Fatal(err)
	}
	if e.state(uid) != "encrypted" {
		t.Fatalf("state = %q after resume", e.state(uid))
	}
	snap := func() string {
		var s string
		if err := e.db.QueryRow(`SELECT
			(SELECT md5(string_agg(encode(name_ct,'hex'), ',' ORDER BY id)) FROM entities WHERE user_id = $1) || '/' ||
			(SELECT md5(string_agg(encode(content_ct,'hex'), ',' ORDER BY id)) FROM memories WHERE user_id = $1) || '/' ||
			(SELECT md5(string_agg(encode(content_ct,'hex'), ',' ORDER BY id)) FROM chunks WHERE user_id = $1)`, uid).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	first := snap()
	moved := -1
	if err := jobs.EncryptUserJob(bg, e.root, e.st, uid, uk, func(done, _ int) { moved = done }); err != nil {
		t.Fatal(err)
	}
	if moved > 0 || snap() != first {
		t.Fatalf("second run changed rows (moved %d)", moved)
	}
	// Everything is still readable.
	cx := e.keyed(uid, uk, "encrypted")
	es, err := cx.FindEntities(bg, cortex.EntityFilter{})
	if err != nil || len(es) < 7 {
		t.Fatalf("entities after migration: %d, %v", len(es), err)
	}
}

func TestPlaintextPostCheckFindsNothing(t *testing.T) {
	e := newEnv(t)
	uid, en := legacyOwner(t, e)
	uk := en.Res.UK
	// Before the migration the dump is full of plaintext (control).
	if !strings.Contains(e.dump(), "zxqmemory") {
		t.Fatal("control: seeded plaintext absent before migration")
	}
	if err := jobs.MigrateOwner(bg, e.root, e.st, uid, uk, nil); err != nil {
		t.Fatal(err)
	}
	rep, err := jobs.PostCheck(bg, e.st, uid)
	if err != nil || rep.Total != 0 {
		t.Fatalf("post-check = %+v, %v", rep, err)
	}
	dump := e.dump()
	for _, tok := range append([]string{"alice chen", "acme corp", "widgetco", "team-seed", "zxqmemory", "zxqchunk", "zxqprovider", "zxqapikey", "zxqchatmodel"}, corpusTokens...) {
		if strings.Contains(dump, strings.ToLower(tok)) {
			i := strings.Index(dump, strings.ToLower(tok))
			t.Errorf("plaintext %q survives migration near: %s", tok, dump[max(0, i-60):min(len(dump), i+60)])
		}
	}
}

func TestEncFlagTamperRejected(t *testing.T) {
	e := newEnv(t)
	uid := e.user("zxqowner")
	en := e.enroll(uid)
	cx := e.keyed(uid, en.Res.UK, "encrypted")
	populate(t, cx, "a")
	if _, err := cx.FindEntities(bg, cortex.EntityFilter{}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		table string
		plant string // the plaintext an attacker must supply to satisfy the *_plain_check
		load  func(c *cortex.Cortex) error
	}{
		{"entities", "name = 'planted', type = 'person'", func(c *cortex.Cortex) error { _, err := c.FindEntities(bg, cortex.EntityFilter{}); return err }},
		{"memories", "content = 'planted'", func(c *cortex.Cortex) error {
			_, _, err := c.ListMemories(bg, cortex.MemoryListFilter{}, 50, 0)
			return err
		}},
		{"chunks", "content = 'planted'", func(c *cortex.Cortex) error { _, err := c.SearchKeyword(bg, "zxqchunk", 5); return err }},
		{"relationships", "type = 'planted'", func(c *cortex.Cortex) error { _, err := c.AllRelationships(bg); return err }},
	} {
		t.Run(tc.table, func(t *testing.T) {
			workset.Drop(uid)
			e.exec(`UPDATE `+tc.table+` SET enc = FALSE, `+tc.plant+` WHERE id = (SELECT id FROM `+tc.table+` WHERE user_id = $1 AND enc ORDER BY id LIMIT 1)`, uid)
			// A fresh handle so no cached working set masks the flip.
			fresh := e.keyed(uid, en.Res.UK, "encrypted")
			err := tc.load(fresh)
			if !errors.Is(err, model.ErrPlaintextRow) {
				t.Fatalf("flipped enc flag on %s: err = %v, want ErrPlaintextRow", tc.table, err)
			}
			if err != nil && strings.Contains(strings.ToLower(err.Error()), "zxq") {
				t.Fatalf("error leaks content: %v", err)
			}
			e.exec(`DELETE FROM `+tc.table+` WHERE user_id = $1 AND enc IS NOT TRUE`, uid)
		})
	}
}

func TestMigratingWriteNoDuplicates(t *testing.T) {
	e := newEnv(t)
	uid := e.user("zxqowner")
	uk, _ := keys.NewUK()
	defer uk.Zero()
	// Plaintext rows written while the user is still legacy.
	legacy := e.root.ForUser(uid)
	ent := &cortex.Entity{Type: "person", Name: "Old Friend"}
	org := &cortex.Entity{Type: "organization", Name: "Old Org"}
	for _, x := range []*cortex.Entity{ent, org} {
		if err := legacy.PutEntity(bg, x); err != nil {
			t.Fatal(err)
		}
	}
	if err := legacy.AddAlias(bg, ent.ID, "email", "Old@X.test"); err != nil {
		t.Fatal(err)
	}
	if err := legacy.PutRelationship(bg, &cortex.Relationship{SourceID: ent.ID, TargetID: org.ID, Type: "works_at", Confidence: 0.5}); err != nil {
		t.Fatal(err)
	}
	if err := legacy.PutChunk(bg, &cortex.Chunk{Content: "zxqchunk legacy", Metadata: map[string]any{"source": "Old Doc"}}); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE users SET encryption_state = 'migrating' WHERE id = $1`, uid)
	cx := e.keyed(uid, uk, "migrating")

	// The same things written again, with different case and spacing.
	again := &cortex.Entity{Type: "person", Name: "  old FRIEND "}
	if err := cx.PutEntity(bg, again); err != nil {
		t.Fatal(err)
	}
	if again.ID != ent.ID {
		t.Fatalf("entity forked: %s vs %s", again.ID, ent.ID)
	}
	if err := cx.AddAlias(bg, ent.ID, "email", "OLD@x.test"); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutRelationship(bg, &cortex.Relationship{SourceID: ent.ID, TargetID: org.ID, Type: "works_at", Confidence: 0.7}); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM entities WHERE user_id = $1 AND NOT is_owner`, uid); n != 2 {
		t.Errorf("entities = %d, want 2", n)
	}
	if n := e.count(`SELECT count(*) FROM entity_aliases WHERE user_id = $1`, uid); n != 1 {
		t.Errorf("aliases = %d, want 1", n)
	}
	if n := e.count(`SELECT count(*) FROM relationships WHERE user_id = $1 AND deleted_at IS NULL`, uid); n != 1 {
		t.Errorf("edges = %d, want 1", n)
	}
	// Chunks have no write-side uniqueness; the mixed (legacy plus encrypted)
	// reads must show each chunk once.
	if err := cx.PutChunk(bg, &cortex.Chunk{Content: "zxqchunk fresh", Metadata: map[string]any{"source": "Old Doc"}}); err != nil {
		t.Fatal(err)
	}
	got, err := cx.SearchKeyword(bg, "zxqchunk", 10)
	if err != nil || len(got) != 2 {
		t.Fatalf("chunks across both forms = %d, %v", len(got), err)
	}
	ids := map[string]bool{}
	for _, c := range got {
		if ids[c.ID] {
			t.Fatalf("chunk %s returned twice", c.ID)
		}
		ids[c.ID] = true
	}
	// After migration the counts are unchanged.
	if err := jobs.MigrateOwner(bg, e.root, e.st, uid, uk, nil); err != nil {
		t.Fatal(err)
	}
	if n := e.count(`SELECT count(*) FROM entities WHERE user_id = $1 AND NOT is_owner`, uid); n != 2 {
		t.Errorf("entities after migration = %d", n)
	}
	if n := e.count(`SELECT count(*) FROM chunks WHERE user_id = $1`, uid); n != 2 {
		t.Errorf("chunks after migration = %d", n)
	}
}

// TestExistingSuiteForEncryptedUser proves the ForEachMode encrypted
// parameterisation is not silently skipped: it runs a probe through both
// modes and checks the registry counted each arm. Together with the
// per-package ForEachMode users (recall, search modes) this keeps "the whole
// suite passes for an encrypted user" honest.
func TestExistingSuiteForEncryptedUser(t *testing.T) {
	before := testutil.ModeCounts()
	testutil.ForEachMode(t, func(t *testing.T, cx *cortex.Cortex) {
		m := &cortex.Memory{Content: "probe memory about kumquats"}
		if err := cx.PutMemory(bg, m); err != nil {
			t.Fatal(err)
		}
		ms, _, err := cx.ListMemories(bg, cortex.MemoryListFilter{}, 10, 0)
		if err != nil || len(ms) != 1 || ms[0].Content != m.Content {
			t.Fatalf("probe read: %v %v", ms, err)
		}
	})
	after := testutil.ModeCounts()
	for _, arm := range []string{"legacy", "encrypted"} {
		if after[arm] != before[arm]+1 {
			t.Fatalf("ForEachMode arm %q ran %d times, want 1", arm, after[arm]-before[arm])
		}
	}
	if cx, _ := testutil.OpenEncrypted(t); !cx.Encrypted() {
		t.Fatal("OpenEncrypted returned a handle that is not encrypted")
	}
}

// TestCloseoutChecklist pins the operator closeout steps in the migration
// runbook so they cannot be dropped from the docs unnoticed.
func TestCloseoutChecklist(t *testing.T) {
	b, err := readDoc("deploy.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Encryption closeout checklist", "Purge the pre-migration backups", "Purge WAL", "pg_dump", "CORTEX_ENCRYPTION_ENROLL", "gated cleanup migration 012"} {
		if !strings.Contains(b, want) {
			t.Errorf("deploy.md is missing %q", want)
		}
	}
	if strings.ContainsRune(b, '\u2014') {
		t.Error("deploy.md contains an em dash")
	}
}
