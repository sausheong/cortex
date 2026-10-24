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

// TestExistingSuiteForEncryptedUser proves the ForEachMode encrypted
// parameterisation is not silently skipped: it runs a probe and checks the
// registry counted the arm. Together with the
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
	for _, arm := range []string{"encrypted"} {
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
