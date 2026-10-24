package workset

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/sausheong/cortex/internal/store"
	"github.com/sausheong/cortex/internal/vecmath"
)

type vecRow struct {
	refID, refType string
	gen, dims      int
	vec            []float32
}

func (u *userSet) loadVectors(ctx context.Context, h *store.Handle) error {
	rows, err := h.DB.QueryContext(ctx,
		`SELECT id, ref_id, ref_type, enc, embedding::text, embedding_ct, dims, generation
		 FROM embeddings WHERE user_id = ?`, h.UserID)
	if err != nil {
		return fmt.Errorf("cortex: load embeddings: %w", err)
	}
	defer rows.Close()
	var out []vecRow
	var size int64
	for rows.Next() {
		var id string
		var r vecRow
		var enc bool
		var lit sql.NullString
		var ct []byte
		if err := rows.Scan(&id, &r.refID, &r.refType, &enc, &lit, &ct, &r.dims, &r.gen); err != nil {
			return fmt.Errorf("cortex: scan embedding: %w", err)
		}
		if err := h.CheckRowForm(enc); err != nil {
			return err
		}
		if enc {
			if len(ct) == 0 {
				return store.ErrCiphertextMissing
			}
			b, err := h.Open(store.TblEmbeddings, id, "embedding", ct)
			if err != nil {
				return err
			}
			if r.vec, err = store.DecodeVec(b); err != nil {
				return err
			}
		} else if r.vec, err = parseLiteral(lit.String); err != nil {
			return err
		}
		size += int64(4*len(r.vec)) + 96 + lenStr(r.refID, r.refType)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("cortex: load embeddings: %w", err)
	}
	u.vecs = out
	u.kindBytes[KindVectors].Store(size)
	return nil
}

// parseLiteral parses a pgvector text literal such as "[0.1,0.2]".
func parseLiteral(s string) ([]float32, error) {
	s = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(s), "["), "]")
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]float32, len(parts))
	for i, p := range parts {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 32)
		if err != nil {
			return nil, err
		}
		out[i] = float32(f)
	}
	return out, nil
}

// SearchVectors is cosine brute force over the cached embeddings of refType
// in generation gen whose dims match the query, best first. Memory and chunk
// refs that are soft-deleted (absent from the loaded set) are skipped, as the
// SQL path does. Vectors load lazily on first use.
func (s *Set) SearchVectors(ctx context.Context, query []float32, refType string, gen, limit int) ([]VectorHit, error) {
	if n := len(query); n < 1 || n > 4000 {
		return nil, fmt.Errorf("cortex: query vector has %d dims, want 1..4000", n)
	}
	vecs, err := s.u.vectors(ctx, s.h)
	if err != nil {
		return nil, err
	}
	var hits []VectorHit
	for i := range vecs {
		r := &vecs[i]
		if r.refType != refType || r.gen != gen || r.dims != len(query) || len(r.vec) != len(query) {
			continue
		}
		switch refType {
		case "memory":
			if _, ok := s.memByID[r.refID]; !ok {
				continue
			}
		case "chunk":
			if _, ok := s.chunkID[r.refID]; !ok {
				continue
			}
		}
		hits = append(hits, VectorHit{RefID: r.refID, Similarity: vecmath.Cosine(query, r.vec)})
	}
	return sortedHits(hits, limit), nil
}
