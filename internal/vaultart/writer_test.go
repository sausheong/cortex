package vaultart_test

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
	"github.com/sausheong/cortex/internal/graph"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/memory"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
	"github.com/sausheong/cortex/internal/testutil"
	"github.com/sausheong/cortex/internal/vaultart"
	"github.com/sausheong/cortex/internal/workset"
)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

type wenv struct {
	t     *testing.T
	root  *cortex.Cortex
	llm   *testutil.MockLLM
	clk   *clock
	w     *vaultart.Writer
	owner *wuser
}

type wuser struct {
	e    *wenv
	id   string
	uk   *keys.UK
	cx   *cortex.Cortex
	h    *store.Handle
	user vaultart.User
}

// okArticle cites the first offered memory in the lead and one section.
func okArticle(in cortex.ArticleInput) cortex.Article {
	src := []string{}
	if len(in.Memories) > 0 {
		src = []string{in.Memories[0].ID}
	}
	return cortex.Article{
		Lead:        "A short lead.",
		LeadSources: src,
		Sections: []cortex.ArticleSection{{Heading: "Status", Items: []cortex.ArticleItem{
			{Text: "Something sourced.", Sources: src},
		}}},
	}
}

func newWenv(t *testing.T, mut func(*vaultart.Deps)) *wenv {
	t.Helper()
	workset.Reset()
	t.Cleanup(workset.Reset)
	llm := &testutil.MockLLM{}
	llm.WriteArticleFn = func(ctx context.Context, system string, in cortex.ArticleInput, length string) (cortex.Article, cortex.ArticleUsage, error) {
		return okArticle(in), cortex.ArticleUsage{InputTokens: 10, OutputTokens: 5}, nil
	}
	cx, uk := testutil.OpenEncryptedWith(t, cortex.WithLLM(llm), cortex.WithEmbedder(&testutil.MockEmbedder{}))
	e := &wenv{t: t, llm: llm, clk: &clock{t: time.Unix(1_800_000_000, 0)}}
	e.root = cx
	e.owner = &wuser{e: e, id: cx.UserID(), uk: uk, cx: cx, h: testutil.H(cx)}
	e.owner.user = vaultart.User{ID: e.owner.id, Key: uk, Cx: cx}
	d := vaultart.Deps{Root: cx, Now: e.clk.Now, KeyTTL: time.Minute}
	if mut != nil {
		mut(&d)
	}
	e.w = vaultart.NewWriter(d)
	return e
}

// otherUser creates another enrolled user sharing the same database.
func (e *wenv) otherUser(name string) *wuser {
	e.t.Helper()
	u, err := e.root.CreateUser(context.Background(), name, name+"@x.test", name, true)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.root.DB().Exec(`UPDATE users SET encryption_state = 'encrypted' WHERE id = $1`, u.ID); err != nil {
		e.t.Fatal(err)
	}
	uk, err := keys.NewUK()
	if err != nil {
		e.t.Fatal(err)
	}
	e.t.Cleanup(uk.Zero)
	cx := e.root.ForUser(u.ID).WithKey(uk, "encrypted")
	e.t.Cleanup(cx.Release)
	wu := &wuser{e: e, id: u.ID, uk: uk, cx: cx, h: testutil.H(cx)}
	wu.user = vaultart.User{ID: u.ID, Key: uk, Cx: cx}
	return wu
}

func (u *wuser) subject(name string, nmem int) vaultart.Subject {
	u.e.t.Helper()
	ent := &model.Entity{Type: "person", Name: name}
	if err := graph.New(u.h).PutEntity(bg, ent); err != nil {
		u.e.t.Fatal(err)
	}
	for i := 0; i < nmem; i++ {
		u.addMem(ent.ID, fmt.Sprintf("%s fact %d", name, i))
	}
	return vaultart.Subject{ID: ent.ID, Group: vaultart.GroupPeople, Kind: "entity", Name: name}
}

func (u *wuser) addMem(entityID, text string) string {
	u.e.t.Helper()
	m := &model.Memory{Content: text, Source: "t", Confidence: 0.8, EntityIDs: []string{entityID}}
	if err := memory.New(u.h).PutMemory(bg, m); err != nil {
		u.e.t.Fatal(err)
	}
	workset.Invalidate(u.id)
	return m.ID
}

func (u *wuser) article(sub vaultart.Subject) *store.ArticleRow {
	u.e.t.Helper()
	r, err := u.h.GetArticle(bg, sub.ID)
	if err != nil {
		u.e.t.Fatal(err)
	}
	return r
}

// blockingLLM makes every WriteArticle wait until release is called and announce itself on started.
func (e *wenv) blockingLLM() (release func(), started chan struct{}) {
	gate := make(chan struct{})
	started = make(chan struct{}, 64)
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	e.t.Cleanup(release)
	e.llm.WriteArticleFn = func(ctx context.Context, system string, in cortex.ArticleInput, length string) (cortex.Article, cortex.ArticleUsage, error) {
		started <- struct{}{}
		<-gate
		return okArticle(in), cortex.ArticleUsage{}, nil
	}
	return release, started
}

func waitStarted(t *testing.T, started chan struct{}, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			t.Fatalf("writer %d did not start", i)
		}
	}
}

func TestOpenWritesOnceAndCaches(t *testing.T) {
	e := newWenv(t, nil)
	u := e.owner
	sub := u.subject("Ada", 3)

	r := e.w.Open(bg, u.user, sub, false)
	if r.State != vaultart.Writing || r.Row != nil {
		t.Fatalf("first open: %+v", r)
	}
	e.w.Wait()
	r = e.w.Open(bg, u.user, sub, false)
	if r.State != vaultart.Ready || r.Row == nil || r.Row.Article.Lead != "A short lead." || r.Stale || r.CouldntUpdate {
		t.Fatalf("second open: %+v", r)
	}
	if r.Row.Slug == "" || r.Row.ShortID != vaultart.ShortID(sub.ID) || r.Row.Kind != "entity" {
		t.Fatalf("row metadata: %+v", r.Row)
	}
	for i := 0; i < 3; i++ {
		if r = e.w.Open(bg, u.user, sub, false); r.State != vaultart.Ready {
			t.Fatalf("open %d: %+v", i, r)
		}
	}
	e.w.Wait()
	if e.llm.Calls != 1 {
		t.Fatalf("calls = %d, want 1", e.llm.Calls)
	}
	if e.w.InflightCount() != 0 {
		t.Fatal("inflight left over")
	}
}

func TestOpenRewritesAfterNewMemory(t *testing.T) {
	e := newWenv(t, nil)
	u := e.owner
	sub := u.subject("Ada", 3)
	e.w.Open(bg, u.user, sub, false)
	e.w.Wait()
	u.addMem(sub.ID, "Ada moved to Lisbon")
	r := e.w.Open(bg, u.user, sub, false)
	if r.State != vaultart.Writing || r.Row == nil || !r.Stale {
		t.Fatalf("want Writing with the old row: %+v", r)
	}
	e.w.Wait()
	if r = e.w.Open(bg, u.user, sub, false); r.State != vaultart.Ready || r.Stale {
		t.Fatalf("after rewrite: %+v", r)
	}
	if e.llm.Calls != 2 {
		t.Fatalf("calls = %d, want 2", e.llm.Calls)
	}
}

func TestOpenInvalidatedByMergeOrDelete(t *testing.T) {
	t.Run("merge", func(t *testing.T) {
		e := newWenv(t, nil)
		u := e.owner
		sub := u.subject("Ada", 3)
		other := u.subject("Ada L", 2)
		e.w.Open(bg, u.user, sub, false)
		e.w.Wait()
		if _, err := u.cx.MergeEntities(bg, sub.ID, other.ID); err != nil {
			t.Fatal(err)
		}
		workset.Invalidate(u.id)
		if r := e.w.Open(bg, u.user, sub, false); r.State != vaultart.Writing {
			t.Fatalf("after merge: %+v", r)
		}
		e.w.Wait()
		if e.llm.Calls != 2 {
			t.Fatalf("calls = %d", e.llm.Calls)
		}
	})
	t.Run("delete", func(t *testing.T) {
		e := newWenv(t, nil)
		u := e.owner
		sub := u.subject("Ada", 3)
		id := u.addMem(sub.ID, "to be removed")
		e.w.Open(bg, u.user, sub, false)
		e.w.Wait()
		now := time.Now()
		if err := memory.New(u.h).InvalidateMemory(bg, id, &now); err != nil {
			t.Fatal(err)
		}
		workset.Invalidate(u.id)
		if r := e.w.Open(bg, u.user, sub, false); r.State != vaultart.Writing {
			t.Fatalf("after delete: %+v", r)
		}
		e.w.Wait()
		if e.llm.Calls != 2 {
			t.Fatalf("calls = %d", e.llm.Calls)
		}
	})
}

func TestSingleWritePerArticle(t *testing.T) {
	e := newWenv(t, nil)
	u := e.owner
	sub := u.subject("Ada", 3)
	release, started := e.blockingLLM()
	var wg sync.WaitGroup
	states := make([]vaultart.State, 20)
	for i := range states {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			states[i] = e.w.Open(bg, u.user, sub, false).State
		}(i)
	}
	wg.Wait()
	waitStarted(t, started, 1)
	release()
	e.w.Wait()
	for i, s := range states {
		if s != vaultart.Writing {
			t.Fatalf("caller %d saw %v", i, s)
		}
	}
	if e.llm.Calls != 1 {
		t.Fatalf("calls = %d, want 1", e.llm.Calls)
	}
}

func TestPerUserAndProcessCaps(t *testing.T) {
	e := newWenv(t, func(d *vaultart.Deps) { d.MaxUser, d.MaxProcess = 2, 3 })
	_, started := e.blockingLLM()
	a := e.owner
	s1, s2, s3 := a.subject("A1", 3), a.subject("A2", 3), a.subject("A3", 3)
	if r := e.w.Open(bg, a.user, s1, false); r.State != vaultart.Writing {
		t.Fatalf("s1 %v", r.State)
	}
	if r := e.w.Open(bg, a.user, s2, false); r.State != vaultart.Writing {
		t.Fatalf("s2 %v", r.State)
	}
	waitStarted(t, started, 2)
	r := e.w.Open(bg, a.user, s3, false)
	if r.State != vaultart.Busy {
		t.Fatalf("per-user cap: %+v", r)
	}
	b := e.otherUser("b")
	bs := b.subject("B1", 3)
	if r := e.w.Open(bg, b.user, bs, false); r.State != vaultart.Writing {
		t.Fatalf("user b %v", r.State)
	}
	waitStarted(t, started, 1)
	c := e.otherUser("c")
	cs := c.subject("C1", 3)
	if r := e.w.Open(bg, c.user, cs, false); r.State != vaultart.Busy {
		t.Fatalf("process cap: %+v", r)
	}
	if e.w.InflightCount() != 3 {
		t.Fatalf("inflight %d", e.w.InflightCount())
	}
	select {
	case <-started:
		t.Fatal("a refused open started a write")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestCapRefusedThenStartsWhenSlotFrees(t *testing.T) {
	e := newWenv(t, nil)
	release, started := e.blockingLLM()
	a := e.owner
	s1, s2, s3 := a.subject("A1", 3), a.subject("A2", 3), a.subject("A3", 3)
	e.w.Open(bg, a.user, s1, false)
	e.w.Open(bg, a.user, s2, false)
	waitStarted(t, started, 2)
	if r := e.w.Open(bg, a.user, s3, false); r.State != vaultart.Busy {
		t.Fatalf("%+v", r)
	}
	release()
	e.w.Wait()
	if r := e.w.Open(bg, a.user, s3, false); r.State != vaultart.Writing {
		t.Fatalf("after slot freed: %+v", r)
	}
	e.w.Wait()
	if r := e.w.Open(bg, a.user, s3, false); r.State != vaultart.Ready {
		t.Fatalf("after write: %+v", r)
	}
}

func TestProcessCapDoesNotStarveOtherUser(t *testing.T) {
	e := newWenv(t, nil) // 2 per user, 4 per process
	_, started := e.blockingLLM()
	a := e.owner
	e.w.Open(bg, a.user, a.subject("A1", 3), false)
	e.w.Open(bg, a.user, a.subject("A2", 3), false)
	waitStarted(t, started, 2)
	if r := e.w.Open(bg, a.user, a.subject("A3", 3), false); r.State != vaultart.Busy {
		t.Fatalf("a third: %v", r.State)
	}
	b := e.otherUser("b")
	if r := e.w.Open(bg, b.user, b.subject("B1", 3), false); r.State != vaultart.Writing {
		t.Fatalf("b starved: %v", r.State)
	}
	waitStarted(t, started, 1)
}

func TestBusyBecomesFailedAfterTTL(t *testing.T) {
	e := newWenv(t, nil) // KeyTTL one minute on the fake clock
	e.blockingLLM()
	a := e.owner
	e.w.Open(bg, a.user, a.subject("A1", 3), false)
	e.w.Open(bg, a.user, a.subject("A2", 3), false)
	s3 := a.subject("A3", 3)
	if r := e.w.Open(bg, a.user, s3, false); r.State != vaultart.Busy {
		t.Fatalf("%v", r.State)
	}
	e.clk.Advance(30 * time.Second)
	if r := e.w.Open(bg, a.user, s3, false); r.State != vaultart.Busy {
		t.Fatalf("still busy expected: %v", r.State)
	}
	e.clk.Advance(31 * time.Second)
	r := e.w.Open(bg, a.user, s3, false)
	if r.State != vaultart.Failed || r.Err == nil || r.Err.Error() != vaultart.MsgTooLong {
		t.Fatalf("want Failed %q: %+v", vaultart.MsgTooLong, r)
	}
}

func TestPanicInWriterClearsInflightAndFails(t *testing.T) {
	e := newWenv(t, nil)
	u := e.owner
	sub := u.subject("Ada", 3)
	e.w.SetBeforeRun(func() { panic("secret memory text") })
	if r := e.w.Open(bg, u.user, sub, false); r.State != vaultart.Writing {
		t.Fatalf("%v", r.State)
	}
	e.w.Wait()
	if e.w.InflightCount() != 0 {
		t.Fatal("panic left an in-flight entry")
	}
	r := e.w.Open(bg, u.user, sub, false)
	if r.State != vaultart.Failed || r.Err == nil || strings.Contains(r.Err.Error(), "secret") {
		t.Fatalf("%+v", r)
	}
	if e.llm.Calls != 0 {
		t.Fatal("provider called")
	}
}

func TestStaleInflightEntryCleared(t *testing.T) {
	e := newWenv(t, nil)
	u := e.owner
	sub := u.subject("Ada", 3)
	e.w.InjectInflight(u.id, sub.ID, e.clk.Now())
	if r := e.w.Open(bg, u.user, sub, false); r.State != vaultart.Writing {
		t.Fatalf("%v", r.State)
	}
	if e.llm.Calls != 0 {
		t.Fatal("a live in-flight entry must not start a second write")
	}
	e.clk.Advance(time.Minute + 31*time.Second) // past KeyTTL+30s
	if r := e.w.Open(bg, u.user, sub, false); r.State != vaultart.Writing {
		t.Fatalf("%v", r.State)
	}
	e.w.Wait()
	if e.llm.Calls != 1 {
		t.Fatalf("calls = %d, want 1 (dead entry cleared, write started)", e.llm.Calls)
	}
	if r := e.w.Open(bg, u.user, sub, false); r.State != vaultart.Ready {
		t.Fatalf("%+v", r)
	}
}

func TestKeyCloneZeroedAfterWrite(t *testing.T) {
	e := newWenv(t, nil)
	u := e.owner
	sub := u.subject("Ada", 3)
	var clone *keys.UK
	e.w.SetKeyHook(func(k *keys.UK) { clone = k })
	e.w.Open(bg, u.user, sub, false)
	e.w.Wait()
	if clone == nil || clone == u.uk {
		t.Fatal("writer must own a separate clone")
	}
	if clone.Bytes() != nil {
		t.Fatal("writer key clone not zeroed")
	}
	if u.uk.Bytes() == nil {
		t.Fatal("caller's key was zeroed")
	}
}

func TestWriteStopsAtKeyTTLNoRow(t *testing.T) {
	e := newWenv(t, func(d *vaultart.Deps) { d.KeyTTL = 150 * time.Millisecond })
	u := e.owner
	sub := u.subject("Ada", 3)
	release := make(chan struct{})
	e.llm.WriteArticleFn = func(ctx context.Context, system string, in cortex.ArticleInput, length string) (cortex.Article, cortex.ArticleUsage, error) {
		<-release // ignores ctx
		return okArticle(in), cortex.ArticleUsage{InputTokens: 3}, nil
	}
	var once sync.Once
	t.Cleanup(func() { once.Do(func() { close(release) }) })
	var clone *keys.UK
	e.w.SetKeyHook(func(k *keys.UK) { clone = k })
	e.w.Open(bg, u.user, sub, false)
	e.w.Wait() // returns at the TTL although the provider is still blocked
	if clone.Bytes() != nil {
		t.Fatal("key clone alive past the TTL")
	}
	once.Do(func() { close(release) })
	time.Sleep(150 * time.Millisecond)
	if r := u.article(sub); r != nil {
		t.Fatal("a row was written after the key expired")
	}
	r := e.w.Open(bg, u.user, sub, false)
	if r.State != vaultart.Failed {
		t.Fatalf("%+v", r)
	}
}

func TestFingerprintStoredMatchesSnapshotUsed(t *testing.T) {
	e := newWenv(t, nil)
	u := e.owner
	sub := u.subject("Ada", 3)
	first := true
	e.llm.WriteArticleFn = func(ctx context.Context, system string, in cortex.ArticleInput, length string) (cortex.Article, cortex.ArticleUsage, error) {
		if first {
			first = false
			u.addMem(sub.ID, "arrived while the model was writing")
		}
		return okArticle(in), cortex.ArticleUsage{}, nil
	}
	e.w.Open(bg, u.user, sub, false)
	e.w.Wait()
	r := e.w.Open(bg, u.user, sub, false)
	if r.State != vaultart.Writing {
		t.Fatalf("a memory added mid-write must force a rewrite: %+v", r)
	}
	e.w.Wait()
	if r = e.w.Open(bg, u.user, sub, false); r.State != vaultart.Ready {
		t.Fatalf("%+v", r)
	}
}

func TestLockedSessionNeverStartsWrite(t *testing.T) {
	e := newWenv(t, nil)
	u := e.owner
	sub := u.subject("Ada", 3)
	if r := e.w.Open(bg, vaultart.User{ID: u.id, Key: nil, Cx: u.cx}, sub, false); r.State != vaultart.Locked {
		t.Fatalf("nil key: %v", r.State)
	}
	keyless := u.cx.WithKey(nil, "encrypted")
	if r := e.w.Open(bg, vaultart.User{ID: u.id, Key: u.uk, Cx: keyless}, sub, false); r.State != vaultart.Locked {
		t.Fatalf("keyless handle: %v", r.State)
	}
	e.w.Wait()
	if e.llm.Calls != 0 || e.w.InflightCount() != 0 {
		t.Fatal("a locked session started a write")
	}
}

type plainLLM struct{}

func (plainLLM) Extract(context.Context, string, string) (cortex.ExtractionResult, error) {
	return cortex.ExtractionResult{}, nil
}
func (plainLLM) Decompose(context.Context, string) ([]cortex.StructuredQuery, error) { return nil, nil }
func (plainLLM) Summarize(context.Context, []string) (string, error)                 { return "", nil }

func TestProviderCannotWriteArticles(t *testing.T) {
	e := newWenv(t, nil)
	u := e.owner
	sub := u.subject("Ada", 3)
	pcx := e.root.ForUser(u.id, cortex.WithLLM(plainLLM{})).WithKey(u.uk, "encrypted")
	t.Cleanup(pcx.Release)
	if vaultart.CanWrite(pcx) || !vaultart.CanWrite(u.cx) {
		t.Fatal("CanWrite wrong")
	}
	r := e.w.Open(bg, vaultart.User{ID: u.id, Key: u.uk, Cx: pcx}, sub, false)
	if r.State != vaultart.Failed || r.Err == nil || r.Err.Error() != vaultart.MsgCannotWrite {
		t.Fatalf("%+v", r)
	}
	if e.w.InflightCount() != 0 {
		t.Fatal("started a write")
	}
}

func TestTruncatedReplyFailureMessage(t *testing.T) {
	e := newWenv(t, nil)
	u := e.owner
	sub := u.subject("Ada", 3)
	e.llm.WriteArticleFn = func(context.Context, string, cortex.ArticleInput, string) (cortex.Article, cortex.ArticleUsage, error) {
		return cortex.Article{}, cortex.ArticleUsage{}, fmt.Errorf("wrapped: %w", model.ErrArticleTruncated)
	}
	e.w.Open(bg, u.user, sub, false)
	e.w.Wait()
	r := e.w.Open(bg, u.user, sub, false)
	if r.State != vaultart.Failed || r.Err.Error() != vaultart.MsgTruncated {
		t.Fatalf("%+v", r)
	}
}

func TestProviderDownNoCache(t *testing.T) {
	e := newWenv(t, nil)
	u := e.owner
	sub := u.subject("Ada", 3)
	e.llm.WriteArticleFn = func(context.Context, string, cortex.ArticleInput, string) (cortex.Article, cortex.ArticleUsage, error) {
		return cortex.Article{}, cortex.ArticleUsage{}, errors.New("boom: sk-live-secret")
	}
	e.w.Open(bg, u.user, sub, false)
	e.w.Wait()
	r := e.w.Open(bg, u.user, sub, false)
	if r.State != vaultart.Failed || r.Row != nil || r.Err.Error() != vaultart.MsgGeneric {
		t.Fatalf("%+v", r)
	}
	if e.llm.Calls != 1 {
		t.Fatalf("a remembered failure must not retry on every refresh: %d calls", e.llm.Calls)
	}
	// Two minutes later a refresh may try again; Retry (force) always may.
	e.clk.Advance(3 * time.Minute)
	e.llm.WriteArticleFn = func(_ context.Context, _ string, in cortex.ArticleInput, _ string) (cortex.Article, cortex.ArticleUsage, error) {
		return okArticle(in), cortex.ArticleUsage{}, nil
	}
	if r = e.w.Open(bg, u.user, sub, false); r.State != vaultart.Writing {
		t.Fatalf("after memory expired: %+v", r)
	}
	e.w.Wait()
	if r = e.w.Open(bg, u.user, sub, false); r.State != vaultart.Ready {
		t.Fatalf("%+v", r)
	}
}

func TestProviderDownKeepsPreviousVersion(t *testing.T) {
	e := newWenv(t, nil)
	u := e.owner
	sub := u.subject("Ada", 3)
	e.w.Open(bg, u.user, sub, false)
	e.w.Wait()
	u.addMem(sub.ID, "new fact")
	e.llm.WriteArticleFn = func(context.Context, string, cortex.ArticleInput, string) (cortex.Article, cortex.ArticleUsage, error) {
		return cortex.Article{}, cortex.ArticleUsage{}, errors.New("provider down")
	}
	r := e.w.Open(bg, u.user, sub, false)
	if r.State != vaultart.Writing || r.Row == nil {
		t.Fatalf("%+v", r)
	}
	e.w.Wait()
	r = e.w.Open(bg, u.user, sub, false)
	if r.State != vaultart.Ready || !r.CouldntUpdate || r.Row == nil || r.Row.Article.Lead != "A short lead." || !r.Stale {
		t.Fatalf("%+v", r)
	}
	if r.Err == nil || r.Err.Error() != vaultart.MsgGeneric {
		t.Fatalf("err %v", r.Err)
	}
}

func TestRewriteRateLimitSixPerHour(t *testing.T) {
	e := newWenv(t, nil)
	for i := 0; i < 6; i++ {
		if !e.w.AllowRewrite("u") {
			t.Fatalf("rewrite %d refused", i)
		}
	}
	if e.w.AllowRewrite("u") {
		t.Fatal("seventh rewrite allowed")
	}
	if !e.w.AllowRewrite("v") {
		t.Fatal("other user limited")
	}
	e.clk.Advance(11 * time.Minute)
	if !e.w.AllowRewrite("u") {
		t.Fatal("no token after ten minutes")
	}
	for i := 0; i < 10; i++ {
		if !e.w.AllowCorrect("u") {
			t.Fatalf("correct %d refused", i)
		}
	}
	if e.w.AllowCorrect("u") {
		t.Fatal("correct burst exceeded")
	}
}

func TestStartLimiterBlocksScriptedFirstOpens(t *testing.T) {
	e := newWenv(t, func(d *vaultart.Deps) {
		d.MaxUser, d.MaxProcess = 100, 100
		d.StartLimiter = accounts.NewHourlyLimiter(40, 15, func() time.Time { return time.Unix(1_800_000_000, 0) })
	})
	u := e.owner
	var subs []vaultart.Subject
	for i := 0; i < 20; i++ {
		subs = append(subs, u.subject(fmt.Sprintf("P%02d", i), 3))
	}
	writing, limited := 0, 0
	for _, s := range subs {
		switch r := e.w.Open(bg, u.user, s, false); r.State {
		case vaultart.Writing:
			writing++
		case vaultart.RateLimited:
			limited++
			if r.Err == nil || r.Err.Error() != vaultart.MsgRateLimited {
				t.Fatalf("limited without message: %+v", r)
			}
		default:
			t.Fatalf("unexpected %v", r.State)
		}
	}
	e.w.Wait()
	if writing != 15 || limited != 5 || e.llm.Calls != 15 {
		t.Fatalf("writing %d limited %d calls %d", writing, limited, e.llm.Calls)
	}
	// A deliberate Rewrite is metered elsewhere and is not blocked here.
	if r := e.w.Open(bg, u.user, subs[19], true); r.State != vaultart.Writing {
		t.Fatalf("forced open: %v", r.State)
	}
	e.w.Wait()
}

func TestUsageRecordedPerAttemptIncludingFailures(t *testing.T) {
	e := newWenv(t, nil)
	u := e.owner
	sub := u.subject("Ada", 3)
	e.w.Open(bg, u.user, sub, false)
	e.w.Wait()
	us, err := e.w.RecentUsage(bg, u.cx)
	if err != nil || us.Articles != 1 || us.Failed != 0 || us.Tokens != 15 {
		t.Fatalf("%+v %v", us, err)
	}
	e.llm.WriteArticleFn = func(context.Context, string, cortex.ArticleInput, string) (cortex.Article, cortex.ArticleUsage, error) {
		return cortex.Article{}, cortex.ArticleUsage{InputTokens: 40}, errors.New("nope")
	}
	e.w.Open(bg, u.user, sub, true)
	e.w.Wait()
	e.w.ClearFailure(u.id, sub.ID)
	e.w.Open(bg, u.user, sub, true)
	e.w.Wait()
	us, _ = e.w.RecentUsage(bg, u.cx)
	if us.Articles != 1 || us.Failed != 2 || us.Tokens != 15+80 {
		t.Fatalf("%+v", us)
	}
}

func TestNoBackgroundWrites(t *testing.T) {
	e := newWenv(t, nil)
	u := e.owner
	sub := u.subject("Ada", 3)
	// Data changes and a time passes with nobody opening a page.
	u.addMem(sub.ID, "more")
	e.clk.Advance(48 * time.Hour)
	e.w.Wait()
	time.Sleep(50 * time.Millisecond)
	if e.llm.Calls != 0 || e.w.InflightCount() != 0 {
		t.Fatal("writer did work without an Open")
	}
	if rows, _ := u.h.ListArticleMeta(bg); len(rows) != 0 {
		t.Fatal("article stored without an Open")
	}
	// A cache hit starts nothing.
	e.w.Open(bg, u.user, sub, false)
	e.w.Wait()
	before := e.llm.Calls
	for i := 0; i < 3; i++ {
		e.w.Open(bg, u.user, sub, false)
	}
	e.w.Wait()
	if e.llm.Calls != before || e.w.InflightCount() != 0 {
		t.Fatal("cache hits started a write")
	}
}
