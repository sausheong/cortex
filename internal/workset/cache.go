package workset

import (
	"context"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
	"github.com/sausheong/cortex/internal/textsearch"
)

const (
	defaultCap  = 96 << 20
	defaultIdle = 10 * time.Minute
)

type globalState struct {
	mu    sync.Mutex
	users map[string]*userSet
	cap   int64
	idle  time.Duration
	now   func() time.Time
}

var g = &globalState{users: map[string]*userSet{}, cap: defaultCap, idle: defaultIdle, now: time.Now}

func init() {
	store.OnWrite = onWrite
	store.OnExec = onExec
}

// onExec is the store.OnExec catch-all: every committed write statement that
// names a content table invalidates the matching slices of the user it
// belongs to, found by looking for a cached user id among the arguments (all
// content writes are scoped by user_id). When no argument names a cached user
// the slices of every cached user are dropped, which is safe and rare.
// Recall's access bookkeeping is exempt: it is mirrored by RecordAccess and
// must not force a memories reload.
func onExec(q string, args []any) {
	lq := strings.ToLower(q)
	if strings.Contains(lq, "set access_count = access_count + 1") {
		return
	}
	var kinds []Kind
	add := func(k Kind) { kinds = append(kinds, k) }
	if strings.Contains(lq, "entities") || strings.Contains(lq, "entity_aliases") {
		add(KindEntities)
	}
	if strings.Contains(lq, "memories") || strings.Contains(lq, "memory_entities") {
		add(KindMemories)
	}
	if strings.Contains(lq, "chunks") {
		add(KindChunks)
	}
	if strings.Contains(lq, "relationships") {
		add(KindRels)
	}
	if strings.Contains(lq, "embeddings") {
		add(KindVectors)
	}
	if len(kinds) == 0 {
		return
	}
	g.mu.Lock()
	ids := make([]string, 0, len(g.users))
	for id := range g.users {
		ids = append(ids, id)
	}
	g.mu.Unlock()
	if len(ids) == 0 {
		return
	}
	named := map[string]bool{}
	for _, a := range args {
		if s, ok := a.(string); ok {
			named[s] = true
		}
	}
	hit := false
	for _, id := range ids {
		if named[id] {
			hit = true
			Invalidate(id, kinds...)
		}
	}
	if !hit {
		for _, id := range ids {
			Invalidate(id, kinds...)
		}
	}
}

// onWrite is the store.OnWrite hook: a content write invalidates the slice
// it touched. Kinds that are not part of the set are ignored.
func onWrite(userID, kind string) {
	switch kind {
	case "entity", "alias":
		Invalidate(userID, KindEntities)
	case "relationship":
		Invalidate(userID, KindRels)
	case "memory":
		Invalidate(userID, KindMemories)
	case "chunk":
		Invalidate(userID, KindChunks)
	case "embedding":
		Invalidate(userID, KindVectors)
	}
}

// userSet is the shared, mutable cache entry of one user. Lock order: g.mu is
// never held while waiting for a userSet lock, and no userSet lock is held
// while taking g.mu.
type userSet struct {
	userID string
	probe  []byte

	mu     sync.RWMutex
	loaded [numKinds]bool
	ver    [numKinds]uint64

	ents    []model.Entity
	entByID map[string]int
	aliases []model.Alias
	rels    []model.Relationship
	mems    []*memRow
	memByID map[string]*memRow
	chunks  []ChunkHeader
	chunkID map[string]struct{}
	vecs    []vecRow
	memIdx  *textsearch.MemIndex
	bm25    *textsearch.BM25Index

	kindBytes [numKinds]atomic.Int64
	bm25Bytes atomic.Int64
	lastUsed  atomic.Int64 // unix nano

	loads           int64
	contentDecrypts int64
	bm25Builds      int64
}

func (u *userSet) bytes() int64 {
	var n int64
	for i := range u.kindBytes {
		n += u.kindBytes[i].Load()
	}
	return n + u.bm25Bytes.Load()
}

func (u *userSet) bm25Cached() bool {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.bm25 != nil
}

// lookup returns the user's entry (creating it), or nil when the cached entry
// was created under a different key.
func lookup(userID string, probe []byte) *userSet {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.now()
	sweepLocked(now)
	u := g.users[userID]
	if u == nil {
		u = &userSet{userID: userID, probe: append([]byte(nil), probe...)}
		g.users[userID] = u
	} else if !probeEqual(u.probe, probe) {
		return nil
	}
	u.lastUsed.Store(now.UnixNano())
	return u
}

func sweepLocked(now time.Time) {
	for id, u := range g.users {
		if now.Sub(time.Unix(0, u.lastUsed.Load())) > g.idle {
			delete(g.users, id)
			notifyEvict(id)
		}
	}
}

var (
	evictMu    sync.Mutex
	evictHooks []func(userID string)
)

// OnEvict registers a callback run whenever a user's set is evicted (idle
// sweep or Drop), so other plaintext caches can go with it. Callbacks must
// not call back into this package.
func OnEvict(fn func(userID string)) {
	evictMu.Lock()
	evictHooks = append(evictHooks, fn)
	evictMu.Unlock()
}

func notifyEvict(userID string) {
	evictMu.Lock()
	hs := append([]func(string){}, evictHooks...)
	evictMu.Unlock()
	for _, h := range hs {
		h(userID)
	}
}

// Sweep evicts every set idle for longer than the idle limit.
func Sweep() {
	g.mu.Lock()
	defer g.mu.Unlock()
	sweepLocked(g.now())
}

// StartJanitor sweeps idle sets every minute until ctx ends, so an idle
// server releases decrypted data without waiting for the next request.
func StartJanitor(ctx context.Context) {
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				Sweep()
			}
		}
	}()
}

// Touch marks the user's set as used (a key-bearing request is active).
func Touch(userID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if u := g.users[userID]; u != nil {
		u.lastUsed.Store(g.now().UnixNano())
	}
}

// Invalidate drops the named slices of a user's set; the next For reloads
// only those. With no kinds it drops everything but stays cached.
func Invalidate(userID string, kinds ...Kind) {
	g.mu.Lock()
	u := g.users[userID]
	g.mu.Unlock()
	if u == nil {
		return
	}
	if len(kinds) == 0 {
		kinds = []Kind{KindEntities, KindMemories, KindChunks, KindRels, KindVectors}
	}
	u.mu.Lock()
	for _, k := range kinds {
		u.drop(k)
	}
	u.mu.Unlock()
}

// drop clears one slice. Caller holds u.mu.
func (u *userSet) drop(k Kind) {
	u.loaded[k] = false
	u.ver[k]++
	u.kindBytes[k].Store(0)
	switch k {
	case KindEntities:
		u.ents, u.entByID, u.aliases = nil, nil, nil
	case KindMemories:
		u.mems, u.memByID, u.memIdx = nil, nil, nil
	case KindChunks:
		u.chunks, u.chunkID = nil, nil
		u.bm25 = nil
		u.bm25Bytes.Store(0)
	case KindRels:
		u.rels = nil
	case KindVectors:
		u.vecs = nil
	}
}

// Drop evicts the user's set from the cache (logout, account deletion). It
// touches nothing else: a user who only uses MCP simply reloads.
func Drop(userID string) {
	g.mu.Lock()
	delete(g.users, userID)
	g.mu.Unlock()
	notifyEvict(userID)
}

// Cached reports whether the user currently has a cached set.
func Cached(userID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.users[userID] != nil
}

// StatsFor returns the work counters of the user's cached set.
func StatsFor(userID string) (Stats, bool) {
	g.mu.Lock()
	u := g.users[userID]
	g.mu.Unlock()
	if u == nil {
		return Stats{}, false
	}
	u.mu.RLock()
	defer u.mu.RUnlock()
	return Stats{
		Loads:           int(atomic.LoadInt64(&u.loads)),
		ContentDecrypts: int(atomic.LoadInt64(&u.contentDecrypts)),
		BM25Builds:      int(atomic.LoadInt64(&u.bm25Builds)),
	}, true
}

// EvictBM25 drops only the chunk keyword index of a user.
func EvictBM25(userID string) {
	g.mu.Lock()
	u := g.users[userID]
	g.mu.Unlock()
	if u != nil {
		u.dropBM25()
	}
}

func (u *userSet) dropBM25() {
	u.mu.Lock()
	u.bm25 = nil
	u.bm25Bytes.Store(0)
	u.mu.Unlock()
}

// RecordAccess updates the cached recall access counters in place. It never
// invalidates the memories slice: access bookkeeping must not force a reload.
func RecordAccess(userID string, ids []string, at time.Time) {
	g.mu.Lock()
	u := g.users[userID]
	g.mu.Unlock()
	if u == nil {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, id := range ids {
		if r := u.memByID[id]; r != nil {
			r.access++
			t := at
			r.lastAccess = &t
		}
	}
}

// SetClock replaces the time source (tests).
func SetClock(now func() time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if now == nil {
		now = time.Now
	}
	g.now = now
}

// SetCap sets the global byte cap (tests); n <= 0 restores the default.
func SetCap(n int64) {
	g.mu.Lock()
	if n <= 0 {
		n = defaultCap
	}
	g.cap = n
	g.mu.Unlock()
}

// Reset empties the cache and restores defaults (tests).
func Reset() {
	g.mu.Lock()
	g.users = map[string]*userSet{}
	g.cap, g.idle, g.now = defaultCap, defaultIdle, time.Now
	g.mu.Unlock()
}

// enforceCap brings the total under the cap by walking users least recently
// used first: a user's BM25 index goes first (it rebuilds on demand), then
// the whole set. The user that just loaded is never evicted.
func enforceCap(current *userSet) {
	g.mu.Lock()
	var total int64
	list := make([]*userSet, 0, len(g.users))
	for _, u := range g.users {
		total += u.bytes()
		list = append(list, u)
	}
	if total <= g.cap {
		g.mu.Unlock()
		return
	}
	sort.Slice(list, func(i, j int) bool { return list[i].lastUsed.Load() < list[j].lastUsed.Load() })
	var dropBM []*userSet
	for _, u := range list {
		if total <= g.cap {
			break
		}
		if u == current {
			continue
		}
		if b := u.bm25Bytes.Load(); b > 0 {
			dropBM = append(dropBM, u)
			total -= b
			if total <= g.cap {
				break
			}
		}
		total -= u.bytes() - u.bm25Bytes.Load()
		delete(g.users, u.userID)
	}
	g.mu.Unlock()
	for _, u := range dropBM {
		u.dropBM25()
	}
}

// ensure loads every missing eager slice (entities, memories, chunk headers,
// relationships). Vectors and BM25 are lazy.
func (u *userSet) ensure(ctx context.Context, h *store.Handle) error {
	u.mu.RLock()
	ok := u.loaded[KindEntities] && u.loaded[KindMemories] && u.loaded[KindChunks] && u.loaded[KindRels]
	u.mu.RUnlock()
	if ok {
		return nil
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	for _, k := range []Kind{KindEntities, KindMemories, KindChunks, KindRels} {
		if u.loaded[k] {
			continue
		}
		if err := u.loadKind(ctx, h, k); err != nil {
			return err
		}
	}
	return nil
}

func (u *userSet) snapshot(h *store.Handle) *Set {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return &Set{
		u: u, h: h,
		ents: u.ents, entByID: u.entByID, aliases: u.aliases, rels: u.rels,
		mems: u.mems, memByID: u.memByID, chunks: u.chunks, chunkID: u.chunkID,
		ver: u.ver,
	}
}

func (u *userSet) vectors(ctx context.Context, h *store.Handle) ([]vecRow, error) {
	u.mu.RLock()
	if u.loaded[KindVectors] {
		v := u.vecs
		u.mu.RUnlock()
		return v, nil
	}
	u.mu.RUnlock()
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.loaded[KindVectors] {
		if err := u.loadKind(ctx, h, KindVectors); err != nil {
			return nil, err
		}
	}
	return u.vecs, nil
}

func (u *userSet) bm25For(ctx context.Context, s *Set) (*textsearch.BM25Index, error) {
	u.mu.RLock()
	if u.bm25 != nil && u.ver[KindChunks] == s.ver[KindChunks] {
		ix := u.bm25
		u.mu.RUnlock()
		return ix, nil
	}
	u.mu.RUnlock()
	// Build outside the lock: bodies are decrypted one by one.
	docs := make([]textsearch.Doc, 0, len(s.chunks))
	var size int64
	for _, c := range s.chunks {
		body, err := loadChunkBody(ctx, s.h, c.ID)
		if err != nil {
			return nil, err
		}
		atomic.AddInt64(&u.contentDecrypts, 1)
		docs = append(docs, textsearch.Doc{ID: c.ID, Text: body})
		size += int64(len(body))/2 + 64
	}
	ix := textsearch.BuildBM25(docs)
	atomic.AddInt64(&u.bm25Builds, 1)
	u.mu.Lock()
	if u.ver[KindChunks] == s.ver[KindChunks] {
		u.bm25 = ix
		u.bm25Bytes.Store(size)
	}
	u.mu.Unlock()
	enforceCap(u)
	return ix, nil
}
