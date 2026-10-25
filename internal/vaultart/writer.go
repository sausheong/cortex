package vaultart

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
	"github.com/sausheong/cortex/internal/workset"
)

// Writer defaults.
const (
	DefaultMaxUser    = 2
	DefaultMaxProcess = 4
	DefaultKeyTTL     = 5 * time.Minute

	// inflightGrace is how long past KeyTTL an in-flight entry may live
	// before the next Open treats it as dead and clears it.
	inflightGrace = 30 * time.Second
	// failureMemory is how long a failed write is remembered so a refreshing
	// page does not start a fresh attempt every few seconds.
	failureMemory = 2 * time.Minute
)

// Fixed user-facing failure messages. None carries model output, memory text
// or an error string from the provider.
const (
	MsgCannotWrite = "Your chat model cannot write articles. Choose another in Settings."
	MsgTruncated   = "The model's reply was cut off. Retry, or choose a shorter length in Settings."
	MsgGeneric     = "The article could not be written just now. Retry in a moment."
	MsgTooLong     = "This is taking too long. Retry."
	MsgRateLimited = "A lot of articles have been started recently. Try again in a little while."
)

// State is the outcome of Open.
type State int

// Open outcomes.
const (
	Ready State = iota
	Writing
	Busy
	Failed
	Locked
	RateLimited
)

func (s State) String() string {
	switch s {
	case Ready:
		return "ready"
	case Writing:
		return "writing"
	case Busy:
		return "busy"
	case Failed:
		return "failed"
	case Locked:
		return "locked"
	case RateLimited:
		return "rate-limited"
	}
	return "unknown"
}

// User is the caller of Open. Cx is the caller's keyed handle (used to read
// the cache and the working set on the request path); Key is the key the
// writer clones for its own use. A nil Key means a locked session.
type User struct {
	ID  string
	Key *keys.UK
	Cx  *cortex.Cortex
}

// Result is what Open reports. Row is the stored article when one exists,
// including an out-of-date one shown while a rewrite runs. Stale says Row is
// out of date. CouldntUpdate says a rewrite failed and Row is the previous
// version. Err carries a fixed, content-free sentence for Failed, Busy
// timeouts and RateLimited.
type Result struct {
	State         State
	Row           *store.ArticleRow
	Err           error
	Stale         bool
	CouldntUpdate bool
}

// Deps configures a Writer. Zero values take the documented defaults.
type Deps struct {
	Root       *cortex.Cortex
	Now        func() time.Time
	Logger     *slog.Logger
	MaxUser    int           // concurrent writes per user, default 2
	MaxProcess int           // concurrent writes per process, default 4
	KeyTTL     time.Duration // life of a writer's key copy, default 5 minutes

	RewriteLimiter *accounts.Limiter // default 6 per hour, burst 6
	CorrectLimiter *accounts.Limiter // default 30 per hour, burst 10
	StartLimiter   *accounts.Limiter // default 40 per hour, burst 15
}

type entry struct {
	userID  string
	started time.Time
}

type failure struct {
	msg   string
	until time.Time
}

// Writer runs article writes in bounded background goroutines. All state is
// in-process, consistent with the single-process working set.
type Writer struct {
	d Deps

	mu       sync.Mutex
	inflight map[string]*entry
	busy     map[string]time.Time
	failed   map[string]failure

	wg sync.WaitGroup

	// Test hooks, set only from export_test.go.
	keyHook   func(*keys.UK)
	beforeRun func()
}

// NewWriter returns a Writer with the defaults filled in.
func NewWriter(d Deps) *Writer {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Logger == nil {
		d.Logger = slog.Default()
	}
	if d.MaxUser <= 0 {
		d.MaxUser = DefaultMaxUser
	}
	if d.MaxProcess <= 0 {
		d.MaxProcess = DefaultMaxProcess
	}
	if d.KeyTTL <= 0 {
		d.KeyTTL = DefaultKeyTTL
	}
	if d.RewriteLimiter == nil {
		d.RewriteLimiter = accounts.NewHourlyLimiter(6, 6, d.Now)
	}
	if d.CorrectLimiter == nil {
		d.CorrectLimiter = accounts.NewHourlyLimiter(30, 10, d.Now)
	}
	if d.StartLimiter == nil {
		d.StartLimiter = accounts.NewHourlyLimiter(40, 15, d.Now)
	}
	return &Writer{
		d:        d,
		inflight: map[string]*entry{},
		busy:     map[string]time.Time{},
		failed:   map[string]failure{},
	}
}

// CanWrite reports whether the handle's chat model can write articles.
func CanWrite(cx *cortex.Cortex) bool {
	if cx == nil {
		return false
	}
	_, ok := cx.LLM().(model.ArticleWriter)
	return ok
}

// AllowRewrite consumes one Rewrite token for the user (6 per hour).
func (w *Writer) AllowRewrite(userID string) bool { return w.d.RewriteLimiter.Allow(userID) }

// AllowCorrect consumes one Correct token for the user (30 per hour).
func (w *Writer) AllowCorrect(userID string) bool { return w.d.CorrectLimiter.Allow(userID) }

// ClearFailure forgets a remembered failure and busy timer for the subject so
// the next Open may start a fresh attempt.
func (w *Writer) ClearFailure(userID, subjectID string) {
	k := userID + "|" + subjectID
	w.mu.Lock()
	delete(w.failed, k)
	delete(w.busy, k)
	w.mu.Unlock()
}

// Wait blocks until every started writer goroutine has finished. It is a
// test hook.
func (w *Writer) Wait() { w.wg.Wait() }

// Usage summarises the recent write attempts of a user.
type Usage struct {
	Articles int // successful writes among the recent attempts
	Failed   int
	Tokens   int
}

// RecentUsage totals the user's recent attempts (the log keeps the last 50).
func (w *Writer) RecentUsage(ctx context.Context, cx *cortex.Cortex) (Usage, error) {
	return w.RecentUsageLast(ctx, cx, 50)
}

// RecentUsageLast totals the user's last n attempts (newest first). The
// attempt log, not the per-article tokens column, is the source: a rewrite
// replaces an article's tokens but never erases an attempt.
func (w *Writer) RecentUsageLast(ctx context.Context, cx *cortex.Cortex, n int) (Usage, error) {
	recs, err := cx.StoreHandle().RecentWrites(ctx, n)
	if err != nil {
		return Usage{}, err
	}
	var u Usage
	for _, r := range recs {
		u.Tokens += r.Tokens
		if r.OK {
			u.Articles++
		} else {
			u.Failed++
		}
	}
	return u, nil
}

// Open returns the article for sub, starting a background write when there is
// no current one. It never blocks on the model: a missing or out-of-date
// article yields Writing and the caller polls. force rewrites a current
// article (the caller meters it with AllowRewrite).
func (w *Writer) Open(ctx context.Context, u User, sub Subject, force bool) Result {
	if u.Cx == nil {
		return Result{State: Locked}
	}
	h := u.Cx.StoreHandle()

	var row *store.ArticleRow
	if r, err := h.GetArticle(ctx, sub.ID); err == nil {
		row = r
	} else if errors.Is(err, model.ErrLocked) || errors.Is(err, model.ErrEnrollRequired) {
		return Result{State: Locked}
	} else if !errors.Is(err, store.ErrArticleCorrupt) {
		return w.fail(row, MsgGeneric)
	} // a corrupt row counts as absent

	set, err := workset.For(ctx, h)
	if err != nil {
		if errors.Is(err, model.ErrLocked) || errors.Is(err, model.ErrEnrollRequired) {
			return Result{State: Locked}
		}
		return w.fail(row, MsgGeneric)
	}
	settings, err := LoadSettings(ctx, h)
	if err != nil {
		return w.fail(row, MsgGeneric)
	}
	ownerName := w.ownerName(ctx, u.Cx, set)
	fp := Fingerprint(h, set, sub, ownerName, settings)

	k := u.ID + "|" + sub.ID
	if row != nil && !row.Stale && !force && len(fp) > 0 && string(row.Fingerprint) == string(fp) {
		w.mu.Lock()
		delete(w.busy, k)
		w.mu.Unlock()
		return Result{State: Ready, Row: row}
	}
	if u.Key == nil {
		return Result{State: Locked}
	}
	if !CanWrite(u.Cx) {
		return w.fail(row, MsgCannotWrite)
	}

	now := w.d.Now()
	w.mu.Lock()
	// Entries past KeyTTL plus grace are dead (a wedged goroutine); clear them.
	for ek, e := range w.inflight {
		if now.Sub(e.started) > w.d.KeyTTL+inflightGrace {
			delete(w.inflight, ek)
		}
	}
	if _, ok := w.inflight[k]; ok {
		w.mu.Unlock()
		return Result{State: Writing, Row: row, Stale: row != nil}
	}
	if force {
		delete(w.failed, k)
	} else if f, ok := w.failed[k]; ok {
		if now.Before(f.until) {
			w.mu.Unlock()
			return w.fail(row, f.msg)
		}
		delete(w.failed, k)
	}
	userN := 0
	for _, e := range w.inflight {
		if e.userID == u.ID {
			userN++
		}
	}
	if userN >= w.d.MaxUser || len(w.inflight) >= w.d.MaxProcess {
		first, ok := w.busy[k]
		if !ok {
			w.busy[k] = now
		} else if now.Sub(first) >= w.d.KeyTTL {
			delete(w.busy, k)
			w.mu.Unlock()
			return w.fail(row, MsgTooLong)
		}
		w.mu.Unlock()
		return Result{State: Busy, Row: row, Stale: row != nil}
	}
	if !force && !w.d.StartLimiter.Allow(u.ID) {
		w.mu.Unlock()
		return Result{State: RateLimited, Row: row, Stale: row != nil, Err: errors.New(MsgRateLimited)}
	}
	e := &entry{userID: u.ID, started: now}
	w.inflight[k] = e
	delete(w.busy, k)
	// The writer's key copy is taken here, before Open returns, so the goroutine
	// never reads the caller's key.
	wcx := u.Cx.WithKey(u.Key, h.EncState)
	w.wg.Add(1)
	w.mu.Unlock()

	if w.keyHook != nil {
		w.keyHook(wcx.StoreHandle().Key)
	}
	go w.run(wcx, e, k, u.ID, sub, force)
	return Result{State: Writing, Row: row, Stale: row != nil}
}

// fail builds the result for a failure: Ready with CouldntUpdate when a
// previous version exists, else Failed.
func (w *Writer) fail(row *store.ArticleRow, msg string) Result {
	if row != nil {
		return Result{State: Ready, Row: row, Stale: true, CouldntUpdate: true, Err: errors.New(msg)}
	}
	return Result{State: Failed, Err: errors.New(msg)}
}

func (w *Writer) ownerName(ctx context.Context, cx *cortex.Cortex, set *workset.Set) string {
	id, err := cx.OwnerEntityID(ctx)
	if err != nil || id == "" {
		return ""
	}
	if e, ok := set.EntityByID(id); ok {
		return e.Name
	}
	return ""
}

func (w *Writer) remember(k, msg string) {
	w.mu.Lock()
	w.failed[k] = failure{msg: msg, until: w.d.Now().Add(failureMemory)}
	w.mu.Unlock()
}

// run is the writer goroutine. It owns wcx, which holds its own clone of the
// key: the clone is zeroed when run returns, however it returns.
func (w *Writer) run(wcx *cortex.Cortex, e *entry, k, userID string, sub Subject, force bool) {
	defer w.wg.Done()
	defer func() {
		w.mu.Lock()
		if w.inflight[k] == e { // a cleared dead entry may have been replaced
			delete(w.inflight, k)
		}
		w.mu.Unlock()
	}()
	defer wcx.Release()
	defer func() {
		if r := recover(); r != nil {
			// Content-free: the panic value could carry user text.
			w.d.Logger.Error("vault article writer panicked", "user", userID, "subject", sub.ID)
			w.remember(k, MsgGeneric)
			w.record(wcx, 0, false)
		}
	}()
	if w.beforeRun != nil {
		w.beforeRun()
	}

	ctx, cancel := context.WithTimeout(context.Background(), w.d.KeyTTL)
	defer cancel()
	h := wcx.StoreHandle()

	// One snapshot: the input, the valid source ids and the stored fingerprint
	// all come from this set, so the fingerprint matches what the model saw.
	set, err := workset.For(ctx, h)
	if err != nil {
		w.remember(k, MsgGeneric)
		return
	}
	settings, err := LoadSettings(ctx, h)
	if err != nil {
		w.remember(k, MsgGeneric)
		return
	}
	ownerName := w.ownerName(ctx, wcx, set)
	fp := Fingerprint(h, set, sub, ownerName, settings)
	if !force {
		// A write that finished between the caller's check and our start.
		if row, err := h.GetArticle(ctx, sub.ID); err == nil && row != nil && !row.Stale &&
			len(fp) > 0 && string(row.Fingerprint) == string(fp) {
			return
		}
	}

	ownerID, _ := wcx.OwnerEntityID(ctx)
	cands := Qualify(set, ownerID, settings, w.d.Now())
	var topics []Topic
	if t, err := LoadTopics(ctx, h, set, settings); err == nil {
		topics = t
	}
	in, _, _ := BuildInput(set, sub, ownerName, cands, settings, DefaultBudgetTokens)
	valid := MemoryIDs(set, sub)
	slug, shortID := w.slugFor(sub, topics)
	link := linkTargets(set, cands, topics, sub)

	writer, ok := wcx.LLM().(model.ArticleWriter)
	if !ok {
		w.remember(k, MsgCannotWrite)
		return
	}
	system := SystemPrompt(promptKind(sub), settings.Length)

	type outcome struct {
		art   model.Article
		usage model.ArticleUsage
		err   error
	}
	ch := make(chan outcome, 1)
	// The provider call holds only plaintext input, never the key, so it may
	// outlive this goroutine if the provider ignores ctx.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- outcome{err: errors.New("provider panicked")}
			}
		}()
		a, u, err := writer.WriteArticle(ctx, system, in, settings.Length)
		ch <- outcome{a, u, err}
	}()

	var res outcome
	select {
	case res = <-ch:
	case <-ctx.Done():
		w.remember(k, MsgTooLong)
		w.record(wcx, 0, false)
		return
	}
	tokens := res.usage.InputTokens + res.usage.OutputTokens
	if ctx.Err() != nil { // expired while the result arrived: write nothing
		w.remember(k, MsgTooLong)
		w.record(wcx, tokens, false)
		return
	}
	if res.err != nil {
		msg := MsgGeneric
		if errors.Is(res.err, model.ErrArticleTruncated) {
			msg = MsgTruncated
		}
		w.remember(k, msg)
		w.record(wcx, tokens, false)
		return
	}
	var opts []Option
	if sub.Kind == "topic" {
		opts = append(opts, ForTopic())
	}
	rendered, err := Validate(res.art, valid, link, opts...)
	if err != nil {
		w.remember(k, MsgGeneric)
		w.record(wcx, tokens, false)
		return
	}
	row := store.ArticleRow{
		SubjectID:   sub.ID,
		Kind:        kindOf(sub),
		ShortID:     shortID,
		Slug:        slug,
		Fingerprint: fp,
		WrittenAt:   w.d.Now(),
		Stale:       false,
		Tokens:      tokens,
		Article:     res.art,
	}
	if err := h.PutArticle(ctx, row); err != nil {
		w.remember(k, MsgGeneric)
		w.record(wcx, tokens, false)
		return
	}
	if sub.Kind == "topic" && rendered.Title != "" {
		_ = h.SetTopicWrittenTitle(ctx, sub.ID, rendered.Title)
	}
	w.mu.Lock()
	delete(w.failed, k)
	w.mu.Unlock()
	w.record(wcx, tokens, true)
}

// record logs one attempt. It uses its own short context so a timed-out write
// is still counted.
func (w *Writer) record(wcx *cortex.Cortex, tokens int, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := wcx.StoreHandle().RecordWrite(ctx, tokens, ok); err != nil {
		w.d.Logger.Warn("vault article usage not recorded")
	}
}

func promptKind(sub Subject) string {
	switch {
	case sub.Kind == "topic":
		return KindTopic
	case sub.Group == GroupPeople:
		return KindPerson
	}
	return KindProject
}

func kindOf(sub Subject) string {
	if sub.Kind == "topic" {
		return "topic"
	}
	return "entity"
}

// slugFor returns the stored slug and short id: a topic's own persisted pair,
// else derived from the subject's name and id.
func (w *Writer) slugFor(sub Subject, topics []Topic) (slug, shortID string) {
	if sub.Kind == "topic" {
		for _, t := range topics {
			if t.ID == sub.ID && t.Slug != "" {
				return t.Slug, t.ShortID
			}
		}
	}
	return ArticleSlug(sub.Name, sub.ID), ShortID(sub.ID)
}

// linkTargets maps every name and alias of the other qualifying subjects to
// its article, for Validate.
func linkTargets(set *workset.Set, cands []Candidate, topics []Topic, self Subject) map[string]LinkTarget {
	out := map[string]LinkTarget{}
	byID := map[string]LinkTarget{}
	for _, c := range cands {
		if c.ID == self.ID {
			continue
		}
		t := LinkTarget{Group: c.Group, Slug: ArticleSlug(c.Name, c.ID), Name: c.Name}
		byID[c.ID] = t
		out[strings.TrimSpace(c.Name)] = t
	}
	for _, a := range set.Aliases() {
		if t, ok := byID[a.EntityID]; ok {
			out[strings.TrimSpace(a.Value)] = t
		}
	}
	for _, tp := range topics {
		if tp.ID == self.ID || tp.Slug == "" {
			continue
		}
		out[strings.TrimSpace(tp.DisplayTitle())] = LinkTarget{Group: GroupTopics, Slug: tp.Slug, Name: tp.DisplayTitle()}
	}
	return out
}
