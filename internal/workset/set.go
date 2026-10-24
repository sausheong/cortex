// Package workset holds the per-user decrypted working set that replaces SQL
// search over ciphertext. For an encrypted user the server cannot filter,
// rank or join on content in Postgres, so the rows a request needs are
// decrypted once into memory and searched there (textsearch for keywords,
// brute-force cosine for vectors).
//
// Single-process assumption: the cache is not coherent across processes. A
// write made by another process (the CLI, a second instance) is not seen by
// this process until the user's set is evicted (10 minutes idle, the global
// cap, Drop, or a restart). Writes made through this process reach the cache
// by store.OnWrite, which invalidates exactly the affected slice.
//
// Lifetime and keys: a *Set is a snapshot taken under the handle it was
// obtained with. Lazy parts (chunk bodies, vectors, the BM25 index) decrypt
// with that handle's key at call time, so a Set must not be used after its
// handle is released. A cached set is only ever served to a handle holding
// the same user key (a probe, Blind("probe"), is recorded at load); any other
// key gets model.ErrLocked and nothing is reloaded.
package workset

import (
	"context"
	"crypto/subtle"
	"sort"
	"sync/atomic"
	"time"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
	"github.com/sausheong/cortex/internal/textsearch"
)

// Kind names one invalidatable slice of a user's working set.
type Kind int

const (
	// KindEntities covers entities and their aliases.
	KindEntities Kind = iota
	KindMemories
	KindChunks
	KindRels
	KindVectors
	numKinds
)

// ChunkHeader is a chunk without its body. Bodies decrypt lazily per hit.
type ChunkHeader struct {
	ID        string
	EntityID  string
	Source    string
	CreatedAt time.Time
}

// VectorHit is one brute-force nearest-neighbour result.
type VectorHit struct {
	RefID      string
	Similarity float32
}

// Stats counts the work done for one user's set, for tests and diagnostics.
type Stats struct {
	Loads           int // slice loads performed (entities, memories, ...)
	ContentDecrypts int // chunk bodies decrypted (headers never count)
	BM25Builds      int
}

type memRow struct {
	m          model.Memory
	access     int
	lastAccess *time.Time
}

// Set is a read-only snapshot of one user's working set.
type Set struct {
	u *userSet
	h *store.Handle

	ents    []model.Entity
	entByID map[string]int
	aliases []model.Alias
	rels    []model.Relationship
	mems    []*memRow
	memByID map[string]*memRow
	chunks  []ChunkHeader
	chunkID map[string]struct{}
	ver     [numKinds]uint64
}

// For returns the user's working set, loading what is missing. It needs an
// enrolled user and a key: model.ErrEnrollRequired for an unenrolled user,
// model.ErrLocked for a handle without a key.
func For(ctx context.Context, h *store.Handle) (*Set, error) {
	if err := h.RequireKey(); err != nil {
		return nil, err
	}
	probe := h.Key.Blind("probe")
	if len(probe) == 0 {
		return nil, model.ErrLocked
	}
	u := lookup(h.UserID, probe)
	if u == nil {
		return nil, model.ErrLocked
	}
	if err := u.ensure(ctx, h); err != nil {
		return nil, err
	}
	s := u.snapshot(h)
	enforceCap(u)
	return s, nil
}

func probeEqual(a, b []byte) bool { return subtle.ConstantTimeCompare(a, b) == 1 }

// Entities returns the live entities.
func (s *Set) Entities() []model.Entity { return append([]model.Entity(nil), s.ents...) }

// EntityByID returns one live entity.
func (s *Set) EntityByID(id string) (model.Entity, bool) {
	i, ok := s.entByID[id]
	if !ok {
		return model.Entity{}, false
	}
	return s.ents[i], true
}

// Aliases returns the aliases of live entities.
func (s *Set) Aliases() []model.Alias { return append([]model.Alias(nil), s.aliases...) }

// Relationships returns every non-deleted relationship, closed ones included.
func (s *Set) Relationships() []model.Relationship {
	return append([]model.Relationship(nil), s.rels...)
}

// RelationshipsFiltered applies the edge validity rules of the SQL readers:
// includeInvalid returns all; asOf the edges that held at that time; else the
// edges that hold at now.
func (s *Set) RelationshipsFiltered(includeInvalid bool, asOf *time.Time, now time.Time) []model.Relationship {
	var out []model.Relationship
	for _, r := range s.rels {
		switch {
		case includeInvalid:
		case asOf != nil:
			t := *asOf
			if r.CreatedAt.After(t) || (r.ValidAt != nil && r.ValidAt.After(t)) || (r.InvalidAt != nil && !r.InvalidAt.After(t)) {
				continue
			}
		default:
			if r.InvalidAt != nil && !r.InvalidAt.After(now) {
				continue
			}
		}
		out = append(out, r)
	}
	return out
}

// Memories returns every non-deleted memory (EntityIDs filled; treat the
// slices as read-only), whatever its validity.
func (s *Set) Memories() []model.Memory {
	out := make([]model.Memory, len(s.mems))
	for i, r := range s.mems {
		out[i] = r.m
	}
	return out
}

// MemoryByID returns one non-deleted memory.
func (s *Set) MemoryByID(id string) (model.Memory, bool) {
	r, ok := s.memByID[id]
	if !ok {
		return model.Memory{}, false
	}
	return r.m, true
}

// MemoriesMode returns the memories a TemporalMode admits, mirroring
// model.TemporalMode.Clause.
func (s *Set) MemoriesMode(mode model.TemporalMode) []model.Memory {
	var out []model.Memory
	for _, r := range s.mems {
		if memoryAdmits(r.m, mode) {
			out = append(out, r.m)
		}
	}
	return out
}

func memoryAdmits(m model.Memory, mode model.TemporalMode) bool {
	switch {
	case mode.IncludeInvalid:
	case mode.AsOf != nil:
		t := *mode.AsOf
		if m.CreatedAt.After(t) ||
			(m.ExpiredAt != nil && !m.ExpiredAt.After(t)) ||
			(m.ValidAt != nil && m.ValidAt.After(t)) ||
			(m.InvalidAt != nil && !m.InvalidAt.After(t)) {
			return false
		}
	default:
		if m.ExpiredAt != nil || m.InvalidAt != nil {
			return false
		}
	}
	if mode.RangeTo != nil && m.ValidAt != nil && !m.ValidAt.Before(*mode.RangeTo) {
		return false
	}
	if mode.RangeFrom != nil && m.InvalidAt != nil && !m.InvalidAt.After(*mode.RangeFrom) {
		return false
	}
	return true
}

// MemoryAccess reports the cached recall access counters of a memory.
func (s *Set) MemoryAccess(id string) (count int, last *time.Time, ok bool) {
	r, ok := s.memByID[id]
	if !ok {
		return 0, nil, false
	}
	s.u.mu.RLock()
	defer s.u.mu.RUnlock()
	return r.access, r.lastAccess, true
}

// ChunkHeaders returns the live chunk headers (no bodies).
func (s *Set) ChunkHeaders() []ChunkHeader { return append([]ChunkHeader(nil), s.chunks...) }

// MemIndex returns the memory keyword index, built lazily over every
// non-deleted memory (the corpus the SQL scorer counted).
func (s *Set) MemIndex() *textsearch.MemIndex {
	s.u.mu.Lock()
	defer s.u.mu.Unlock()
	if s.u.memIdx != nil && s.u.ver[KindMemories] == s.ver[KindMemories] {
		return s.u.memIdx
	}
	docs := make([]textsearch.Doc, len(s.mems))
	for i, r := range s.mems {
		docs[i] = textsearch.Doc{ID: r.m.ID, Text: r.m.Content}
	}
	ix := textsearch.BuildMemIndex(docs)
	if s.u.ver[KindMemories] == s.ver[KindMemories] {
		s.u.memIdx = ix
	}
	return ix
}

// ChunkBody decrypts one chunk body on demand.
func (s *Set) ChunkBody(ctx context.Context, id string) (string, error) {
	if _, ok := s.chunkID[id]; !ok {
		return "", model.NotFound("cortex: chunk not found")
	}
	body, err := loadChunkBody(ctx, s.h, id)
	if err != nil {
		return "", err
	}
	atomic.AddInt64(&s.u.contentDecrypts, 1)
	return body, nil
}

// BM25 returns the chunk keyword index, built from chunk bodies on first use
// (every body is decrypted once; the cost is paid at the first keyword
// search, not at load). It counts toward the cap and can be evicted alone.
func (s *Set) BM25(ctx context.Context) (*textsearch.BM25Index, error) {
	return s.u.bm25For(ctx, s)
}

// Bytes is the estimated size of this user's cached data.
func (s *Set) Bytes() int64 { return s.u.bytes() }

// BM25Cached reports whether the chunk index is currently cached.
func (s *Set) BM25Cached() bool { return s.u.bm25Cached() }

// sortedHits orders by similarity descending, ties by ref id.
func sortedHits(h []VectorHit, limit int) []VectorHit {
	sort.Slice(h, func(i, j int) bool {
		if h[i].Similarity != h[j].Similarity {
			return h[i].Similarity > h[j].Similarity
		}
		return h[i].RefID < h[j].RefID
	})
	if limit > 0 && len(h) > limit {
		h = h[:limit]
	}
	return h
}
