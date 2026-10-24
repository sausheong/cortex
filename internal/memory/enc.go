package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
)

// memCols is the select list scanMemory reads, qualified with alias m.
const memCols = `m.id, m.enc, m.content, m.source, m.speaker, m.content_ct, m.source_ct, m.speaker_ct,
	m.confidence, m.created_at, m.updated_at, m.valid_at, m.invalid_at, m.expired_at, m.static, m.forget_after,
	COALESCE(m.written_by_connection_id, '')`

type scanner interface{ Scan(dest ...any) error }

// scanMemory reads one memCols row of either form, decrypting enc rows and
// applying the row-form guard. Callers that need extra leading columns use
// scanMemoryLead.
func (c Ops) scanMemory(sc scanner) (model.Memory, error) {
	return c.scanMemoryLead(sc)
}

func (c Ops) scanMemoryLead(sc scanner, lead ...any) (model.Memory, error) {
	var m model.Memory
	var enc bool
	var content, source, speaker sql.NullString
	var contentCT, sourceCT, speakerCT []byte
	var vat, iat, eat, fa sql.NullTime
	dest := append(append([]any{}, lead...), &m.ID, &enc, &content, &source, &speaker, &contentCT, &sourceCT, &speakerCT,
		&m.Confidence, &m.CreatedAt, &m.UpdatedAt, &vat, &iat, &eat, &m.Static, &fa, &m.WrittenBy)
	if err := sc.Scan(dest...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return m, err
		}
		return m, fmt.Errorf("cortex: scan memory: %w", err)
	}
	if err := c.OpenMemoryFields(&m, enc, content, source, speaker, contentCT, sourceCT, speakerCT); err != nil {
		return m, err
	}
	m.ValidAt = store.NullTimePtr(vat)
	m.InvalidAt = store.NullTimePtr(iat)
	m.ExpiredAt = store.NullTimePtr(eat)
	m.ForgetAfter = store.NullTimePtr(fa)
	return m, nil
}

// insertMemoryEnc writes m (ID, timestamps set) as an encrypted row.
func (c Ops) insertMemoryEnc(ctx context.Context, tx *store.Tx, m *model.Memory) error {
	contentCT, err := c.SealStr(store.TblMemories, m.ID, "content", m.Content)
	if err != nil {
		return err
	}
	sourceCT, err := c.SealStr(store.TblMemories, m.ID, "source", m.Source)
	if err != nil {
		return err
	}
	var speakerCT []byte
	if m.Speaker != "" {
		if speakerCT, err = c.SealStr(store.TblMemories, m.ID, "speaker", m.Speaker); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO memories (id, user_id, enc, content_ct, source_ct, speaker_ct, confidence, created_at, updated_at, valid_at, invalid_at, static, forget_after, written_by_connection_id)
		 VALUES (?, ?, TRUE, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, c.UserID, contentCT, sourceCT, speakerCT, m.Confidence, m.CreatedAt, m.UpdatedAt, m.ValidAt, m.InvalidAt, m.Static, m.ForgetAfter, c.NullConn()); err != nil {
		return fmt.Errorf("cortex: insert memory: %w", err)
	}
	return nil
}

// chunkSource returns metadata["source"] when it is a non-empty string.
func chunkSource(meta map[string]any) string {
	s, _ := meta["source"].(string)
	return s
}

// GetChunk loads one chunk by id (either form).
func (c Ops) GetChunk(ctx context.Context, id string) (*model.Chunk, error) {
	var ch model.Chunk
	var enc bool
	var entityID, content, meta sql.NullString
	var contentCT, metaCT []byte
	err := c.DB.QueryRowContext(ctx,
		`SELECT id, enc, entity_id, content, metadata::text, content_ct, metadata_ct, created_at, COALESCE(written_by_connection_id, '')
		 FROM chunks WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, id, c.UserID,
	).Scan(&ch.ID, &enc, &entityID, &content, &meta, &contentCT, &metaCT, &ch.CreatedAt, &ch.WrittenBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.NotFound("cortex: chunk not found")
	}
	if err != nil {
		return nil, fmt.Errorf("cortex: get chunk: %w", err)
	}
	if err := c.openChunk(&ch, enc, content, meta, contentCT, metaCT); err != nil {
		return nil, err
	}
	ch.EntityID = entityID.String
	return &ch, nil
}

func (c Ops) openChunk(ch *model.Chunk, enc bool, content, meta sql.NullString, contentCT, metaCT []byte) error {
	if err := c.CheckRowForm(enc); err != nil {
		return err
	}
	if !enc {
		ch.Content = content.String
		if meta.Valid && meta.String != "" {
			if err := json.Unmarshal([]byte(meta.String), &ch.Metadata); err != nil {
				return fmt.Errorf("cortex: unmarshal chunk metadata: %w", err)
			}
		}
		return nil
	}
	var err error
	if ch.Content, err = c.OpenStr(store.TblChunks, ch.ID, "content", contentCT); err != nil {
		return err
	}
	return c.OpenJSON(store.TblChunks, ch.ID, "metadata", metaCT, &ch.Metadata)
}

// rewriteChunkEnc rewrites a legacy chunk as encrypted: ciphertext in, plain
// content and metadata cleared, source blind index filled.
func (c Ops) rewriteChunkEnc(ctx context.Context, id, content string, meta map[string]any) error {
	contentCT, err := c.SealStr(store.TblChunks, id, "content", content)
	if err != nil {
		return err
	}
	metaCT, err := c.SealJSON(store.TblChunks, id, "metadata", meta)
	if err != nil {
		return err
	}
	var blind []byte
	if src := chunkSource(meta); src != "" {
		blind = c.BlindNorm(src)
	}
	if _, err := c.DB.ExecContext(ctx,
		`UPDATE chunks SET enc = TRUE, content = NULL, metadata = NULL, content_ct = ?, metadata_ct = ?, source_blind = ?
		 WHERE id = ? AND user_id = ? AND enc IS NOT TRUE`, contentCT, metaCT, blind, id, c.UserID); err != nil {
		return fmt.Errorf("cortex: rewrite chunk: %w", err)
	}
	c.NotifyWrite("chunk")
	return nil
}

// GetEmbedding returns the vector stored for (refID, refType) in the handle's
// generation (pinned, or the active one).
func (c Ops) GetEmbedding(ctx context.Context, refID, refType string) ([]float32, error) {
	gen := c.PinnedGen
	if gen <= 0 {
		g, err := c.ActiveGeneration(ctx)
		if err != nil {
			return nil, err
		}
		gen = g
	}
	var id string
	var enc bool
	var lit sql.NullString
	var ct []byte
	err := c.DB.QueryRowContext(ctx,
		`SELECT id, enc, embedding::text, embedding_ct FROM embeddings
		 WHERE user_id = ? AND ref_id = ? AND ref_type = ? AND generation = ?`,
		c.UserID, refID, refType, gen).Scan(&id, &enc, &lit, &ct)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.NotFound("cortex: embedding not found")
	}
	if err != nil {
		return nil, fmt.Errorf("cortex: get embedding: %w", err)
	}
	if err := c.CheckRowForm(enc); err != nil {
		return nil, err
	}
	if !enc {
		return ParseVectorLiteral(lit.String)
	}
	if len(ct) == 0 {
		return nil, store.ErrCiphertextMissing
	}
	b, err := c.Open(store.TblEmbeddings, id, "embedding", ct)
	if err != nil {
		return nil, err
	}
	return store.DecodeVec(b)
}

// embWriter is satisfied by *store.Tx and store.DB.
type embWriter interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

// writeEmbedding upserts one embedding for gen. Encrypted handles seal the
// little-endian float32 bytes under the row id; an upsert that lands on an
// existing row (different id) re-seals under that row's id so the AAD holds.
func (c Ops) writeEmbedding(ctx context.Context, q embWriter, refID, refType string, gen int, v []float32) error {
	id := store.NewID()
	if !c.Encrypted() {
		if _, err := q.ExecContext(ctx, upsertEmbeddingSQL(), id, c.UserID, refID, refType, VectorLiteral(v), len(v), gen); err != nil {
			return fmt.Errorf("cortex: insert embedding: %w", err)
		}
		return nil
	}
	raw := store.EncodeVec(v)
	ct, err := c.Seal(store.TblEmbeddings, id, "embedding", raw)
	if err != nil {
		return err
	}
	var rid string
	if err := q.QueryRowContext(ctx,
		`INSERT INTO embeddings (id, user_id, ref_id, ref_type, enc, embedding, embedding_ct, dims, generation, created_at)
		 VALUES (?, ?, ?, ?, TRUE, NULL, ?, ?, ?, now())
		 ON CONFLICT (user_id, ref_type, ref_id, generation) DO UPDATE SET
		   enc = TRUE, embedding = NULL, embedding_ct = EXCLUDED.embedding_ct, dims = EXCLUDED.dims
		 RETURNING id`,
		id, c.UserID, refID, refType, ct, len(v), gen).Scan(&rid); err != nil {
		return fmt.Errorf("cortex: insert embedding: %w", err)
	}
	if rid != id {
		ct, err = c.Seal(store.TblEmbeddings, rid, "embedding", raw)
		if err != nil {
			return err
		}
		if _, err := q.ExecContext(ctx, `UPDATE embeddings SET embedding_ct = ? WHERE id = ? AND user_id = ?`, ct, rid, c.UserID); err != nil {
			return fmt.Errorf("cortex: reseal embedding: %w", err)
		}
	}
	return nil
}
