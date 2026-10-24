package memory

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
)

// memCols is the select list scanMemory reads, qualified with alias m.
const memCols = `m.id, m.content_ct, m.source_ct, m.speaker_ct,
	m.confidence, m.created_at, m.updated_at, m.valid_at, m.invalid_at, m.expired_at, m.static, m.forget_after,
	COALESCE(m.written_by_connection_id, '')`

type scanner interface{ Scan(dest ...any) error }

// scanMemory reads one memCols row and decrypts it. Callers that need extra leading columns use
// scanMemoryLead.
func (c Ops) scanMemory(sc scanner) (model.Memory, error) {
	return c.scanMemoryLead(sc)
}

func (c Ops) scanMemoryLead(sc scanner, lead ...any) (model.Memory, error) {
	var m model.Memory
	var contentCT, sourceCT, speakerCT []byte
	var vat, iat, eat, fa sql.NullTime
	dest := append(append([]any{}, lead...), &m.ID, &contentCT, &sourceCT, &speakerCT,
		&m.Confidence, &m.CreatedAt, &m.UpdatedAt, &vat, &iat, &eat, &m.Static, &fa, &m.WrittenBy)
	if err := sc.Scan(dest...); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return m, err
		}
		return m, fmt.Errorf("cortex: scan memory: %w", err)
	}
	if err := c.OpenMemoryFields(&m, contentCT, sourceCT, speakerCT); err != nil {
		return m, err
	}
	m.ValidAt = store.NullTimePtr(vat)
	m.InvalidAt = store.NullTimePtr(iat)
	m.ExpiredAt = store.NullTimePtr(eat)
	m.ForgetAfter = store.NullTimePtr(fa)
	return m, nil
}

// insertMemoryEnc writes m (ID, timestamps set) as a sealed row.
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
		`INSERT INTO memories (id, user_id, content_ct, source_ct, speaker_ct, confidence, created_at, updated_at, valid_at, invalid_at, static, forget_after, written_by_connection_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
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

// GetChunk loads one chunk by id.
func (c Ops) GetChunk(ctx context.Context, id string) (*model.Chunk, error) {
	var ch model.Chunk
	var entityID sql.NullString
	var contentCT, metaCT []byte
	err := c.DB.QueryRowContext(ctx,
		`SELECT id, entity_id, content_ct, metadata_ct, created_at, COALESCE(written_by_connection_id, '')
		 FROM chunks WHERE id = ? AND user_id = ? AND deleted_at IS NULL`, id, c.UserID,
	).Scan(&ch.ID, &entityID, &contentCT, &metaCT, &ch.CreatedAt, &ch.WrittenBy)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.NotFound("cortex: chunk not found")
	}
	if err != nil {
		return nil, fmt.Errorf("cortex: get chunk: %w", err)
	}
	if err := c.openChunk(&ch, contentCT, metaCT); err != nil {
		return nil, err
	}
	ch.EntityID = entityID.String
	return &ch, nil
}

func (c Ops) openChunk(ch *model.Chunk, contentCT, metaCT []byte) error {
	var err error
	if ch.Content, err = c.OpenStr(store.TblChunks, ch.ID, "content", contentCT); err != nil {
		return err
	}
	return c.OpenJSON(store.TblChunks, ch.ID, "metadata", metaCT, &ch.Metadata)
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
	var ct []byte
	err := c.DB.QueryRowContext(ctx,
		`SELECT id, embedding_ct FROM embeddings
		 WHERE user_id = ? AND ref_id = ? AND ref_type = ? AND generation = ?`,
		c.UserID, refID, refType, gen).Scan(&id, &ct)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.NotFound("cortex: embedding not found")
	}
	if err != nil {
		return nil, fmt.Errorf("cortex: get embedding: %w", err)
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

// writeEmbedding upserts one embedding for gen. It seals the
// little-endian float32 bytes under the row id; an upsert that lands on an
// existing row (different id) re-seals under that row's id so the AAD holds.
func (c Ops) writeEmbedding(ctx context.Context, q embWriter, refID, refType string, gen int, v []float32) error {
	id := store.NewID()
	raw := store.EncodeVec(v)
	ct, err := c.Seal(store.TblEmbeddings, id, "embedding", raw)
	if err != nil {
		return err
	}
	var rid string
	if err := q.QueryRowContext(ctx,
		`INSERT INTO embeddings (id, user_id, ref_id, ref_type, embedding_ct, dims, generation, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, now())
		 ON CONFLICT (user_id, ref_type, ref_id, generation) DO UPDATE SET
		   embedding_ct = EXCLUDED.embedding_ct, dims = EXCLUDED.dims
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
