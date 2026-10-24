package workset

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync/atomic"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
)

// loadKind loads one slice with the caller's handle. Caller holds u.mu.
// Deleted rows are excluded; legacy plaintext rows (enc IS NOT TRUE) are read
// into the same structures as encrypted ones, which the row-form guard of the
// handle limits to unencrypted and migrating users.
func (u *userSet) loadKind(ctx context.Context, h *store.Handle, k Kind) error {
	var err error
	switch k {
	case KindEntities:
		err = u.loadEntities(ctx, h)
	case KindMemories:
		err = u.loadMemories(ctx, h)
	case KindChunks:
		err = u.loadChunkHeaders(ctx, h)
	case KindRels:
		err = u.loadRels(ctx, h)
	case KindVectors:
		err = u.loadVectors(ctx, h)
	}
	if err != nil {
		return err
	}
	u.loaded[k] = true
	atomic.AddInt64(&u.loads, 1)
	return nil
}

func lenStr(s ...string) int64 {
	var n int64
	for _, x := range s {
		n += int64(len(x))
	}
	return n
}

func (u *userSet) loadEntities(ctx context.Context, h *store.Handle) error {
	rows, err := h.DB.QueryContext(ctx,
		`SELECT id, enc, type, name, attributes, source, type_ct, name_ct, source_ct, attributes_ct,
		        confidence, created_at, updated_at, COALESCE(written_by_connection_id, '')
		 FROM entities WHERE user_id = ? AND deleted_at IS NULL ORDER BY created_at, id`, h.UserID)
	if err != nil {
		return fmt.Errorf("cortex: load entities: %w", err)
	}
	defer rows.Close()
	var ents []model.Entity
	var size int64
	for rows.Next() {
		var e model.Entity
		var enc bool
		var typ, name, attrs, source sql.NullString
		var typeCT, nameCT, sourceCT, attrsCT []byte
		if err := rows.Scan(&e.ID, &enc, &typ, &name, &attrs, &source, &typeCT, &nameCT, &sourceCT, &attrsCT,
			&e.Confidence, &e.CreatedAt, &e.UpdatedAt, &e.WrittenBy); err != nil {
			return fmt.Errorf("cortex: scan entity: %w", err)
		}
		if err := h.OpenEntityFields(&e, enc, name, typ, source, attrs, nameCT, typeCT, sourceCT, attrsCT); err != nil {
			return err
		}
		size += 192 + lenStr(e.Name, e.Type, e.Source) + int64(len(attrsCT)+len(attrs.String))
		ents = append(ents, e)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("cortex: load entities: %w", err)
	}
	rows.Close()

	arows, err := h.DB.QueryContext(ctx,
		`SELECT id, entity_id, kind, enc, value, source, value_ct, source_ct, created_at
		 FROM entity_aliases
		 WHERE user_id = ? AND entity_id IN (SELECT id FROM entities WHERE user_id = ? AND deleted_at IS NULL)
		 ORDER BY created_at, id`, h.UserID, h.UserID)
	if err != nil {
		return fmt.Errorf("cortex: load aliases: %w", err)
	}
	defer arows.Close()
	var aliases []model.Alias
	for arows.Next() {
		var id string
		var a model.Alias
		var enc bool
		var value, source sql.NullString
		var valueCT, sourceCT []byte
		if err := arows.Scan(&id, &a.EntityID, &a.Kind, &enc, &value, &source, &valueCT, &sourceCT, &a.CreatedAt); err != nil {
			return fmt.Errorf("cortex: scan alias: %w", err)
		}
		if err := h.CheckRowForm(enc); err != nil {
			return err
		}
		if enc {
			if a.Value, err = h.OpenStr(store.TblAliases, id, "value", valueCT); err != nil {
				return err
			}
			if a.Source, err = h.OpenOptStr(store.TblAliases, id, "source", sourceCT); err != nil {
				return err
			}
		} else {
			a.Value, a.Source = value.String, source.String
		}
		size += 112 + lenStr(a.Kind, a.Value, a.Source)
		aliases = append(aliases, a)
	}
	if err := arows.Err(); err != nil {
		return fmt.Errorf("cortex: load aliases: %w", err)
	}
	u.ents, u.aliases = ents, aliases
	u.entByID = make(map[string]int, len(ents))
	for i, e := range ents {
		u.entByID[e.ID] = i
	}
	u.kindBytes[KindEntities].Store(size)
	return nil
}

func (u *userSet) loadRels(ctx context.Context, h *store.Handle) error {
	rows, err := h.DB.QueryContext(ctx,
		`SELECT id, source_id, target_id, enc, type, attributes, source, type_ct, attributes_ct, source_ct,
		        confidence, created_at, evidence_count, last_seen_at, valid_at, invalid_at
		 FROM relationships WHERE user_id = ? AND deleted_at IS NULL ORDER BY created_at, id`, h.UserID)
	if err != nil {
		return fmt.Errorf("cortex: load relationships: %w", err)
	}
	defer rows.Close()
	var out []model.Relationship
	var size int64
	for rows.Next() {
		var r model.Relationship
		var enc bool
		var typ, attrs, source sql.NullString
		var typeCT, attrsCT, sourceCT []byte
		var vat, iat sql.NullTime
		if err := rows.Scan(&r.ID, &r.SourceID, &r.TargetID, &enc, &typ, &attrs, &source, &typeCT, &attrsCT, &sourceCT,
			&r.Confidence, &r.CreatedAt, &r.EvidenceCount, &r.LastSeenAt, &vat, &iat); err != nil {
			return fmt.Errorf("cortex: scan relationship: %w", err)
		}
		if err := h.OpenRelFields(&r, enc, typ, source, attrs, typeCT, sourceCT, attrsCT); err != nil {
			return err
		}
		r.ValidAt, r.InvalidAt = store.NullTimePtr(vat), store.NullTimePtr(iat)
		size += 224 + lenStr(r.Type, r.Source) + int64(len(attrsCT)+len(attrs.String))
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("cortex: load relationships: %w", err)
	}
	u.rels = out
	u.kindBytes[KindRels].Store(size)
	return nil
}

func (u *userSet) loadMemories(ctx context.Context, h *store.Handle) error {
	links := map[string][]string{}
	lrows, err := h.DB.QueryContext(ctx,
		`SELECT memory_id, entity_id FROM memory_entities WHERE user_id = ? ORDER BY memory_id, entity_id`, h.UserID)
	if err != nil {
		return fmt.Errorf("cortex: load memory links: %w", err)
	}
	for lrows.Next() {
		var mid, eid string
		if err := lrows.Scan(&mid, &eid); err != nil {
			lrows.Close()
			return fmt.Errorf("cortex: scan memory link: %w", err)
		}
		links[mid] = append(links[mid], eid)
	}
	lrows.Close()
	if err := lrows.Err(); err != nil {
		return fmt.Errorf("cortex: load memory links: %w", err)
	}

	rows, err := h.DB.QueryContext(ctx,
		`SELECT id, enc, content, source, speaker, content_ct, source_ct, speaker_ct,
		        confidence, created_at, updated_at, valid_at, invalid_at, expired_at, static, forget_after,
		        COALESCE(written_by_connection_id, ''), access_count, last_accessed_at
		 FROM memories WHERE user_id = ? AND deleted_at IS NULL ORDER BY created_at, id`, h.UserID)
	if err != nil {
		return fmt.Errorf("cortex: load memories: %w", err)
	}
	defer rows.Close()
	var out []*memRow
	var size int64
	byID := map[string]*memRow{}
	for rows.Next() {
		var m model.Memory
		var enc bool
		var content, source, speaker sql.NullString
		var contentCT, sourceCT, speakerCT []byte
		var vat, iat, eat, fa, la sql.NullTime
		var access int
		if err := rows.Scan(&m.ID, &enc, &content, &source, &speaker, &contentCT, &sourceCT, &speakerCT,
			&m.Confidence, &m.CreatedAt, &m.UpdatedAt, &vat, &iat, &eat, &m.Static, &fa, &m.WrittenBy, &access, &la); err != nil {
			return fmt.Errorf("cortex: scan memory: %w", err)
		}
		if err := h.OpenMemoryFields(&m, enc, content, source, speaker, contentCT, sourceCT, speakerCT); err != nil {
			return err
		}
		m.ValidAt, m.InvalidAt, m.ExpiredAt, m.ForgetAfter = store.NullTimePtr(vat), store.NullTimePtr(iat), store.NullTimePtr(eat), store.NullTimePtr(fa)
		m.EntityIDs = links[m.ID]
		r := &memRow{m: m, access: access, lastAccess: store.NullTimePtr(la)}
		size += 256 + lenStr(m.Content, m.Source, m.Speaker) + int64(len(m.EntityIDs))*40
		out = append(out, r)
		byID[m.ID] = r
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("cortex: load memories: %w", err)
	}
	u.mems, u.memByID, u.memIdx = out, byID, nil
	u.kindBytes[KindMemories].Store(size)
	return nil
}

// loadChunkHeaders reads chunk headers. Metadata (which holds the source) is
// decrypted; the body (content_ct) is never touched here.
func (u *userSet) loadChunkHeaders(ctx context.Context, h *store.Handle) error {
	rows, err := h.DB.QueryContext(ctx,
		`SELECT id, enc, entity_id, metadata::text, metadata_ct, created_at
		 FROM chunks WHERE user_id = ? AND deleted_at IS NULL ORDER BY created_at, id`, h.UserID)
	if err != nil {
		return fmt.Errorf("cortex: load chunks: %w", err)
	}
	defer rows.Close()
	var out []ChunkHeader
	ids := map[string]struct{}{}
	var size int64
	for rows.Next() {
		var c ChunkHeader
		var enc bool
		var entityID, meta sql.NullString
		var metaCT []byte
		if err := rows.Scan(&c.ID, &enc, &entityID, &meta, &metaCT, &c.CreatedAt); err != nil {
			return fmt.Errorf("cortex: scan chunk: %w", err)
		}
		if err := h.CheckRowForm(enc); err != nil {
			return err
		}
		var md map[string]any
		if enc {
			if err := h.OpenJSON(store.TblChunks, c.ID, "metadata", metaCT, &md); err != nil {
				return err
			}
		} else if meta.Valid && meta.String != "" {
			if err := json.Unmarshal([]byte(meta.String), &md); err != nil {
				return fmt.Errorf("cortex: unmarshal chunk metadata: %w", err)
			}
		}
		c.Source, _ = md["source"].(string)
		c.EntityID = entityID.String
		size += 128 + lenStr(c.Source, c.EntityID)
		out = append(out, c)
		ids[c.ID] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("cortex: load chunks: %w", err)
	}
	u.chunks, u.chunkID = out, ids
	u.bm25 = nil
	u.bm25Bytes.Store(0)
	u.kindBytes[KindChunks].Store(size)
	return nil
}

// loadChunkBody decrypts (or reads) one chunk body.
func loadChunkBody(ctx context.Context, h *store.Handle, id string) (string, error) {
	var enc bool
	var content sql.NullString
	var ct []byte
	err := h.DB.QueryRowContext(ctx,
		`SELECT enc, content, content_ct FROM chunks WHERE id = ? AND user_id = ? AND deleted_at IS NULL`,
		id, h.UserID).Scan(&enc, &content, &ct)
	if err == sql.ErrNoRows {
		return "", model.NotFound("cortex: chunk not found")
	}
	if err != nil {
		return "", fmt.Errorf("cortex: load chunk body: %w", err)
	}
	if err := h.CheckRowForm(enc); err != nil {
		return "", err
	}
	if !enc {
		return content.String, nil
	}
	return h.OpenStr(store.TblChunks, id, "content", ct)
}
