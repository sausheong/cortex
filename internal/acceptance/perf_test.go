//go:build perf

// Performance measurement for the encrypted working set.
//
// Run: go test -tags perf ./internal/acceptance -run Perf -v
//
// Targets (spec Acceptance), measured on a synthetic corpus of 2000 entities,
// 3000 relationships, 1300 memories with 512-dimension embeddings and 500
// chunks, all under a user key:
//
//	TestPerfColdLoad          first read after the working set is evicted < 300 ms
//	TestPerfRecallWarm        recall with the working set cached        < 150 ms
//	TestPerfVaultAndGraphPages /vault warm                              < 300 ms
//	                           /graph and /graph/data                   < 500 ms each
//
// Each test prints the measured numbers for the review. /vault is measured on
// an owner-sized graph (1,600 entities, 4,000 memories): "cold" is the first
// request after the working set and the article index are evicted, "warm" is
// the median of repeat requests (working set and index cached on the set
// version). Only warm is bounded. /graph and /graph/data are the first request
// of a freshly built page set (working set warm), with the repeat after
// working-set eviction printed alongside.
//
// Owner's data: set CORTEX_PERF_DATABASE_URL, CORTEX_PERF_USER (an encrypted
// user id) and CORTEX_PERF_RECOVERY_KEY and the same checks run against that
// database (read only, nothing is written; recall uses the keyword path
// because the owner's embedder settings are sealed under CORTEX_SECRET_KEY).
//
// Droplet decision rule: resize the droplet only if a WARM target misses
// (recall, Vault, Graph) on the owner's data. A cold-load miss alone is not a
// reason to resize: it is paid once per ten idle minutes, and the load path
// can be tuned first.
package acceptance

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"os"
	"sort"
	"testing"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/vaultart"
	"github.com/sausheong/cortex/internal/web"
	"github.com/sausheong/cortex/internal/workset"
)

const (
	coldTarget   = 300 * time.Millisecond
	recallTarget = 150 * time.Millisecond
	pageTarget   = 500 * time.Millisecond
)

type perfEnv struct {
	o    *oauthEnv
	uid  string
	uk   *keys.UK
	cx   *cortex.Cortex
	cook []*http.Cookie
	// recall is the warm-recall operation under test.
	recall func(cx *cortex.Cortex) error
	// coldRead is the first read after eviction.
	coldRead func(cx *cortex.Cortex) error
	label    string
}

var perfShared *perfEnv

func synthetic(t *testing.T) *perfEnv {
	t.Helper()
	o := newOAuthEnv(t)
	uid := o.user("perfowner")
	en := o.enroll(uid)
	uk := en.Res.UK
	cx := o.keyed(uid, uk, "encrypted")
	seedPerf(t, cx)
	p := &perfEnv{o: o, uid: uid, uk: uk, cx: cx, label: "synthetic 2000 entities / 1300 memories / 512 dims"}
	p.recall = func(c *cortex.Cortex) error {
		_, err := c.Recall(context.Background(), "project roadmap payments onboarding")
		return err
	}
	p.coldRead = p.recall
	p.session(t)
	return p
}

func (p *perfEnv) session(t *testing.T) {
	t.Helper()
	sessions := web.NewSessions(p.o.st, nil)
	sid, _, err := sessions.Create(context.Background(), p.uid)
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := sessions.Unlock(context.Background(), sid, p.uk)
	if err != nil {
		t.Fatal(err)
	}
	p.cook = []*http.Cookie{{Name: "cortex_session", Value: sid}, {Name: "cortex_unlock", Value: secret}}
}

func owner(t *testing.T) *perfEnv {
	t.Helper()
	dsn, uid, rec := os.Getenv("CORTEX_PERF_DATABASE_URL"), os.Getenv("CORTEX_PERF_USER"), os.Getenv("CORTEX_PERF_RECOVERY_KEY")
	if dsn == "" || uid == "" || rec == "" {
		return nil
	}
	root, err := cortex.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st := &accounts.Store{DB: db}
	uk, err := st.UnlockWithRecovery(context.Background(), uid, rec)
	if err != nil {
		t.Fatalf("owner unlock: %v", err)
	}
	t.Cleanup(uk.Zero)
	o := serve(t, &env{t: t, dsn: dsn, root: root, db: db, st: st})
	cx := root.ForUser(uid).WithKey(uk, "encrypted")
	t.Cleanup(cx.Release)
	p := &perfEnv{o: o, uid: uid, uk: uk, cx: cx, label: "owner data"}
	p.recall = func(c *cortex.Cortex) error { _, err := c.SearchKeyword(context.Background(), "the", 10); return err }
	p.coldRead = func(c *cortex.Cortex) error {
		_, _, err := c.ListMemories(context.Background(), cortex.MemoryListFilter{}, 50, 0)
		return err
	}
	p.session(t)
	return p
}

func seedPerf(t *testing.T, cx *cortex.Cortex) {
	t.Helper()
	ctx := context.Background()
	words := []string{"project", "roadmap", "payments", "onboarding", "billing", "search", "latency", "design", "review", "launch", "incident", "budget"}
	ents := make([]*cortex.Entity, 2000)
	for i := range ents {
		typ := []string{"person", "organization", "project", "place"}[i%4]
		ents[i] = &cortex.Entity{Type: typ, Name: fmt.Sprintf("%s %s %d", words[i%len(words)], words[(i/7)%len(words)], i), Attributes: map[string]any{"note": words[i%5]}}
		if err := cx.PutEntity(ctx, ents[i]); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 3000; i++ {
		a, b := ents[i%len(ents)], ents[(i*7+13)%len(ents)]
		if a.ID == b.ID {
			continue
		}
		if err := cx.PutRelationship(ctx, &cortex.Relationship{SourceID: a.ID, TargetID: b.ID, Type: []string{"works_at", "knows", "part_of", "located_in"}[i%4]}); err != nil {
			t.Fatal(err)
		}
	}
	gen, err := cx.ActiveGeneration(ctx)
	if err != nil {
		t.Fatal(err)
	}
	emb := bagEmbedder{dims: 512}
	for i := 0; i < 1300; i++ {
		text := fmt.Sprintf("%s %s %s note %d about %s", words[i%len(words)], words[(i*3)%len(words)], words[(i*5)%len(words)], i, ents[i%len(ents)].Name)
		m := &cortex.Memory{Content: text, EntityIDs: []string{ents[i%len(ents)].ID}}
		if err := cx.PutMemory(ctx, m); err != nil {
			t.Fatal(err)
		}
		v, _ := emb.Embed(ctx, []string{text})
		if err := cx.PutEmbeddingGen(ctx, m.ID, "memory", gen, v[0]); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 500; i++ {
		if err := cx.PutChunk(ctx, &cortex.Chunk{Content: fmt.Sprintf("document %d on %s and %s", i, words[i%len(words)], words[(i*7)%len(words)]), Metadata: map[string]any{"source": fmt.Sprintf("doc-%d", i%50)}}); err != nil {
			t.Fatal(err)
		}
	}
}

func timeit(f func() error) (time.Duration, error) {
	t0 := time.Now()
	err := f()
	return time.Since(t0), err
}

func median(ds []time.Duration) time.Duration {
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	return ds[len(ds)/2]
}

func (p *perfEnv) page(t *testing.T, path string) time.Duration {
	t.Helper()
	d, err := timeit(func() error {
		resp, body := p.o.do("GET", path, p.cook, nil, "")
		if resp.StatusCode != 200 {
			return fmt.Errorf("GET %s: %d %.200s", path, resp.StatusCode, body)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func forEach(t *testing.T, f func(t *testing.T, p *perfEnv)) {
	t.Helper()
	t.Run("synthetic", func(t *testing.T) { f(t, synthetic(t)) })
	if p := owner(t); p != nil {
		t.Run("owner", func(t *testing.T) { f(t, p) })
	}
}

func TestPerfColdLoad(t *testing.T) {
	forEach(t, func(t *testing.T, p *perfEnv) {
		var ds []time.Duration
		for i := 0; i < 5; i++ {
			workset.Drop(p.uid)
			d, err := timeit(func() error { return p.coldRead(p.cx) })
			if err != nil {
				t.Fatal(err)
			}
			ds = append(ds, d)
		}
		m := median(ds)
		t.Logf("PERF %s cold working-set load (first read after eviction): median %v, runs %v (target < %v)", p.label, m, ds, coldTarget)
		if m >= coldTarget {
			t.Errorf("cold load %v >= %v", m, coldTarget)
		}
	})
}

func TestPerfRecallWarm(t *testing.T) {
	forEach(t, func(t *testing.T, p *perfEnv) {
		if err := p.recall(p.cx); err != nil { // warm
			t.Fatal(err)
		}
		var ds []time.Duration
		for i := 0; i < 15; i++ {
			d, err := timeit(func() error { return p.recall(p.cx) })
			if err != nil {
				t.Fatal(err)
			}
			ds = append(ds, d)
		}
		m := median(ds)
		t.Logf("PERF %s warm recall: median %v, max %v (target < %v)", p.label, m, ds[len(ds)-1], recallTarget)
		if m >= recallTarget {
			t.Errorf("warm recall %v >= %v", m, recallTarget)
		}
	})
}

func TestPerfVaultAndGraphPages(t *testing.T) {
	forEach(t, func(t *testing.T, p *perfEnv) {
		_ = p.coldRead(p.cx) // working set warm
		for _, path := range []string{"/graph", "/graph/data"} {
			warm := p.page(t, path)
			workset.Drop(p.uid)
			cold := p.page(t, path)
			t.Logf("PERF %s %s: first request %v, repeat after working-set eviction %v (target < %v)", p.label, path, warm, cold, pageTarget)
			if warm >= pageTarget {
				t.Errorf("%s %v >= %v", path, warm, pageTarget)
			}
		}
	})
	t.Run("vault", func(t *testing.T) {
		forEachVault(t, func(t *testing.T, p *perfEnv) {
			workset.Drop(p.uid)
			vaultart.ResetIndexes()
			cold := p.page(t, "/vault")
			var ds []time.Duration
			for i := 0; i < 9; i++ {
				ds = append(ds, p.page(t, "/vault"))
			}
			m := median(ds)
			t.Logf("PERF %s /vault: cold %v, warm median %v, max %v (target warm < %v)", p.label, cold, m, ds[len(ds)-1], vaultTarget)
			if m >= vaultTarget {
				t.Errorf("/vault warm %v >= %v", m, vaultTarget)
			}
		})
	})
}

const vaultTarget = 300 * time.Millisecond

func forEachVault(t *testing.T, f func(t *testing.T, p *perfEnv)) {
	t.Helper()
	t.Run("synthetic", func(t *testing.T) {
		o := newOAuthEnv(t)
		uid := o.user("vaultperf")
		en := o.enroll(uid)
		cx := o.keyed(uid, en.Res.UK, "encrypted")
		seedVaultGraph(t, cx)
		p := &perfEnv{o: o, uid: uid, uk: en.Res.UK, cx: cx, label: "synthetic 1600 entities / 4000 memories"}
		p.session(t)
		f(t, p)
	})
	if p := owner(t); p != nil {
		t.Run("owner", func(t *testing.T) { f(t, p) })
	}
}

// seedVaultGraph builds an owner-sized graph: 1,600 entities of mixed types
// and 4,000 memories, each tied to one or two entities, so that a large share
// of the people, projects and organisations qualify for an article and the
// concepts cluster into topics.
func seedVaultGraph(t *testing.T, cx *cortex.Cortex) {
	t.Helper()
	ctx := context.Background()
	types := []string{"person", "person", "organization", "project", "concept", "concept", "place", "event"}
	ents := make([]*cortex.Entity, 1600)
	for i := range ents {
		ents[i] = &cortex.Entity{Type: types[i%len(types)], Name: fmt.Sprintf("Name%d Surname%d", i, i/9)}
		if err := cx.PutEntity(ctx, ents[i]); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2400; i++ {
		a, b := ents[i%len(ents)], ents[(i*7+13)%len(ents)]
		if a.ID == b.ID {
			continue
		}
		if err := cx.PutRelationship(ctx, &cortex.Relationship{SourceID: a.ID, TargetID: b.ID, Type: []string{"works_at", "knows", "part_of", "located_in"}[i%4]}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 4000; i++ {
		ids := []string{ents[i%900].ID}
		if i%3 == 0 {
			ids = append(ids, ents[(i*5+1)%len(ents)].ID)
		}
		if err := cx.PutMemory(ctx, &cortex.Memory{Content: fmt.Sprintf("Note %d about %s", i, ents[i%900].Name), EntityIDs: ids, Confidence: 1}); err != nil {
			t.Fatal(err)
		}
	}
}
