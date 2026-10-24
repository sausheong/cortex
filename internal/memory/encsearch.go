package memory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
	"github.com/sausheong/cortex/internal/textsearch"
	"github.com/sausheong/cortex/internal/vecmath"
	"github.com/sausheong/cortex/internal/workset"
)

// This file holds the in-memory search paths of encrypted (and migrating)
// users. Postgres cannot rank, filter or join on ciphertext, so these run over
// the decrypted working set. A keyless handle gets model.ErrLocked, never an
// empty result.

// searchMemoriesEnc is SearchMemoriesMode over the working set: the same
// IDF-weighted score and id tie-break as the SQL path, filtered by mode.
func (c Ops) searchMemoriesEnc(ctx context.Context, query string, limit int, mode model.TemporalMode) ([]model.Memory, error) {
	ws, err := workset.For(ctx, c.Handle)
	if err != nil {
		return nil, err
	}
	admitted := map[string]struct{}{}
	for _, m := range ws.MemoriesMode(mode) {
		admitted[m.ID] = struct{}{}
	}
	hits := ws.MemIndex().Search(query, limit, func(id string) bool {
		_, ok := admitted[id]
		return ok
	})
	out := make([]model.Memory, 0, len(hits))
	for _, h := range hits {
		if m, ok := ws.MemoryByID(h.ID); ok {
			out = append(out, m)
		}
	}
	return out, nil
}

// searchKeywordEnc is SearchKeyword over the working set, ranked by BM25.
func (c Ops) searchKeywordEnc(ctx context.Context, query string, limit int) ([]model.Chunk, error) {
	ws, err := workset.For(ctx, c.Handle)
	if err != nil {
		return nil, err
	}
	ix, err := ws.BM25(ctx)
	if err != nil {
		return nil, err
	}
	return c.chunksByHits(ctx, ix.Search(query, limit, nil))
}

func (c Ops) chunksByHits(ctx context.Context, hits []textsearch.Hit) ([]model.Chunk, error) {
	out := make([]model.Chunk, 0, len(hits))
	for _, h := range hits {
		ch, err := c.GetChunk(ctx, h.ID)
		if err != nil {
			if errors.Is(err, model.ErrNotFound) {
				continue
			}
			return nil, err
		}
		out = append(out, *ch)
	}
	return out, nil
}

// searchVectorRawEnc is SearchVectorRaw by brute-force cosine over the
// decoded vectors of the active generation.
func (c Ops) searchVectorRawEnc(ctx context.Context, queryVec []float32, refType string, limit int) ([]VectorResult, error) {
	dims := len(queryVec)
	if dims < 1 || dims > 4000 {
		return nil, fmt.Errorf("cortex: query vector has %d dims, want 1..4000", dims)
	}
	ws, err := workset.For(ctx, c.Handle)
	if err != nil {
		return nil, err
	}
	gen, err := c.ActiveGeneration(ctx)
	if err != nil {
		return nil, err
	}
	hits, err := ws.SearchVectors(ctx, queryVec, refType, gen, limit)
	if err != nil {
		return nil, err
	}
	out := make([]VectorResult, len(hits))
	for i, h := range hits {
		out[i] = VectorResult{RefID: h.RefID, Similarity: h.Similarity}
	}
	return out, nil
}

// memoriesByVectorHits resolves memory vector hits against the working set,
// dropping those mode does not admit.
func (c Ops) memoriesByVectorHits(ctx context.Context, results []VectorResult, mode model.TemporalMode) ([]model.Memory, error) {
	ws, err := workset.For(ctx, c.Handle)
	if err != nil {
		return nil, err
	}
	admitted := map[string]struct{}{}
	for _, m := range ws.MemoriesMode(mode) {
		admitted[m.ID] = struct{}{}
	}
	out := make([]model.Memory, 0, len(results))
	for _, r := range results {
		if _, ok := admitted[r.RefID]; !ok {
			continue
		}
		if m, ok := ws.MemoryByID(r.RefID); ok {
			out = append(out, m)
		}
	}
	return out, nil
}

// recordAccessEnc mirrors markAccessed into the cached counters in place.
func (c Ops) recordAccessEnc(ids []string) {
	workset.RecordAccess(c.UserID, ids, time.Now().UTC())
}

// recallSeedMemoriesEnc is recallSeedMemories over the working set: memories
// linked to more seeds first, then higher confidence, then newer, then id.
func (c Ops) recallSeedMemoriesEnc(ctx context.Context, g graphContext, limit int, mode model.TemporalMode) ([]vecmath.Ranked, map[string]model.Result) {
	ws, err := workset.For(ctx, c.Handle)
	if err != nil {
		c.Logf("cortex: recall seed memories: %v", err)
		return nil, nil
	}
	seed := make(map[string]struct{}, len(g.seeds))
	for _, id := range g.seeds {
		seed[id] = struct{}{}
	}
	type cand struct {
		m model.Memory
		n int
	}
	var cands []cand
	for _, m := range ws.MemoriesMode(mode) {
		n := 0
		for _, id := range m.EntityIDs {
			if _, ok := seed[id]; ok {
				n++
			}
		}
		if n > 0 {
			cands = append(cands, cand{m, n})
		}
	}
	sort.Slice(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		switch {
		case a.n != b.n:
			return a.n > b.n
		case a.m.Confidence != b.m.Confidence:
			return a.m.Confidence > b.m.Confidence
		case !a.m.CreatedAt.Equal(b.m.CreatedAt):
			return a.m.CreatedAt.After(b.m.CreatedAt)
		}
		return a.m.ID < b.m.ID
	})
	if limit > 0 && len(cands) > limit {
		cands = cands[:limit]
	}
	var items []vecmath.Ranked
	results := map[string]model.Result{}
	for _, cd := range cands {
		key := "mem:" + cd.m.ID
		items = append(items, vecmath.Ranked{ID: key, Rank: len(items)})
		results[key] = model.Result{ID: cd.m.ID, Type: "memory", Content: cd.m.Content, Source: cd.m.Source,
			Speaker: cd.m.Speaker, Confidence: cd.m.Confidence, EntityIDs: cd.m.EntityIDs}
	}
	return items, results
}

// InsertMemoryTx inserts m (ID, timestamps and confidence already set by the
// caller) inside tx, as an encrypted row for encrypted users. Entity links
// are the caller's business.
func (c Ops) InsertMemoryTx(ctx context.Context, tx *store.Tx, m *model.Memory) error {
	if c.Encrypted() {
		if err := c.RequireKey(); err != nil {
			return err
		}
		return c.insertMemoryEnc(ctx, tx, m)
	}
	var speaker any
	if m.Speaker != "" {
		speaker = m.Speaker
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO memories (id, user_id, content, source, speaker, confidence, created_at, updated_at, valid_at, invalid_at, static, forget_after, written_by_connection_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, c.UserID, m.Content, m.Source, speaker, m.Confidence, m.CreatedAt, m.UpdatedAt, m.ValidAt, m.InvalidAt, m.Static, m.ForgetAfter, c.NullConn()); err != nil {
		return fmt.Errorf("cortex: insert memory: %w", err)
	}
	return nil
}

// InsertMemoryEdgeTx inserts a memory edge inside tx (source sealed for
// encrypted users), ignoring a duplicate.
func (c Ops) InsertMemoryEdgeTx(ctx context.Context, tx *store.Tx, e *model.MemoryEdge) error {
	var source, sourceCT any
	enc := c.Encrypted()
	if e.Source != "" {
		if enc {
			ct, err := c.SealStr(store.TblMemoryEdges, e.ID, "source", e.Source)
			if err != nil {
				return err
			}
			sourceCT = ct
		} else {
			source = e.Source
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO memory_edges (id, user_id, source_id, target_id, type, enc, source, source_ct, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT (user_id, source_id, target_id, type) DO NOTHING`,
		e.ID, c.UserID, e.SourceID, e.TargetID, e.Type, enc, source, sourceCT, e.CreatedAt); err != nil {
		return fmt.Errorf("cortex: insert memory edge: %w", err)
	}
	return nil
}
