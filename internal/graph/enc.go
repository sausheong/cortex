package graph

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/ontology"
	"github.com/sausheong/cortex/internal/store"
)

type scanner interface{ Scan(dest ...any) error }

// queryRower is satisfied by store.DB and *store.Tx.
type queryRower interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
	QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
}

// legacyOK reports whether legacy plaintext rows may be consulted by an
// equality lookup: in states "none" and "migrating", never "encrypted".
func (c Ops) legacyOK() bool { return c.EncState != "encrypted" }

// entCols is the select list of scanEntity.
const entCols = `id, enc, type, name, attributes, source, type_ct, name_ct, source_ct, attributes_ct,
	confidence, created_at, updated_at, COALESCE(written_by_connection_id, '')`

// entRow is a decoded entities row with the plain flags the writers need.
type entRow struct {
	model.Entity
	enc     bool
	isOwner bool
}

// scanEntityRow reads entCols (+ is_owner when withOwner) from sc, decoding
// either form behind the row-form guard.
func (c Ops) scanEntityRow(sc scanner, withOwner bool) (*entRow, error) {
	var r entRow
	var typ, name, attrs, source sql.NullString
	var typeCT, nameCT, sourceCT, attrsCT []byte
	dest := []any{&r.ID, &r.enc, &typ, &name, &attrs, &source, &typeCT, &nameCT, &sourceCT, &attrsCT,
		&r.Confidence, &r.CreatedAt, &r.UpdatedAt, &r.WrittenBy}
	if withOwner {
		dest = append(dest, &r.isOwner)
	}
	if err := sc.Scan(dest...); err != nil {
		return nil, err
	}
	if err := c.OpenEntityFields(&r.Entity, r.enc, name, typ, source, attrs, nameCT, typeCT, sourceCT, attrsCT); err != nil {
		return nil, err
	}
	return &r, nil
}

func (c Ops) scanEntity(sc scanner) (*model.Entity, error) {
	r, err := c.scanEntityRow(sc, false)
	if err != nil {
		return nil, err
	}
	return &r.Entity, nil
}

// loadEntityRowForUpdate loads and locks one live entity matching where
// (which must start from user_id-free conditions; user_id and deleted_at are
// added here). Encrypted rows are preferred over legacy ones.
func (c Ops) loadEntityRowForUpdate(ctx context.Context, q queryRower, where string, args ...any) (*entRow, error) {
	all := append([]any{c.UserID}, args...)
	return c.scanEntityRow(q.QueryRowContext(ctx,
		`SELECT `+entCols+`, is_owner FROM entities
		 WHERE user_id = ? AND deleted_at IS NULL AND (`+where+`)
		 ORDER BY enc DESC, created_at, id LIMIT 1 FOR UPDATE`, all...), true)
}

// entWrite carries the plain values of an encrypted entity write.
type entWrite struct {
	id, name, typ, source string
	attrs                 map[string]any
	conf                  float64
	updated               time.Time
	conn                  any // written_by_connection_id, nil to keep
}

// writeEntityEnc rewrites the row id as encrypted (plain columns cleared) and
// returns its created_at. It is the single path by which an entity row is
// updated under a key, so a legacy row touched through it becomes encrypted.
func (c Ops) writeEntityEnc(ctx context.Context, q queryRower, w entWrite) (time.Time, error) {
	nameCT, err := c.SealStr(store.TblEntities, w.id, "name", w.name)
	if err != nil {
		return time.Time{}, err
	}
	typeCT, err := c.SealStr(store.TblEntities, w.id, "type", w.typ)
	if err != nil {
		return time.Time{}, err
	}
	sourceCT, err := c.SealStr(store.TblEntities, w.id, "source", w.source)
	if err != nil {
		return time.Time{}, err
	}
	attrsCT, err := c.SealJSON(store.TblEntities, w.id, "attributes", w.attrs)
	if err != nil {
		return time.Time{}, err
	}
	var created time.Time
	err = q.QueryRowContext(ctx,
		`UPDATE entities SET enc = TRUE, name = NULL, type = NULL, source = NULL, attributes = NULL,
		   name_ct = ?, type_ct = ?, source_ct = ?, attributes_ct = ?, name_blind = ?, type_blind = ?,
		   confidence = ?, updated_at = ?,
		   written_by_connection_id = COALESCE(?, written_by_connection_id)
		 WHERE id = ? AND user_id = ?
		 RETURNING created_at`,
		nameCT, typeCT, sourceCT, attrsCT, c.BlindNorm(w.name), c.BlindNorm(w.typ),
		w.conf, w.updated, w.conn, w.id, c.UserID).Scan(&created)
	if err != nil {
		return time.Time{}, fmt.Errorf("cortex: update entity: %w", err)
	}
	return created, nil
}

// rewriteEntityLegacy rewrites an entity just read in legacy form as
// encrypted, unchanged. Only while migrating.
func (c Ops) rewriteEntityLegacy(ctx context.Context, e *model.Entity) {
	if !c.Migrating() || c.Key == nil {
		return
	}
	if _, err := c.writeEntityEnc(ctx, c.DB, entWrite{id: e.ID, name: e.Name, typ: e.Type, source: e.Source,
		attrs: e.Attributes, conf: e.Confidence, updated: e.UpdatedAt}); err != nil {
		c.Logf("cortex: rewrite legacy entity: %v", err)
		return
	}
	c.NotifyWrite("entity")
}

// putEntityEnc is PutEntity for a handle in state migrating or encrypted. The
// ciphertext is bound to the row id, so the upsert is a lookup (blind index,
// plus the legacy columns while migrating) followed by an update of the found
// row or an insert that retries on a lost race.
func (c Ops) putEntityEnc(ctx context.Context, e *model.Entity) error {
	if err := c.RequireKey(); err != nil {
		return err
	}
	now := time.Now().UTC()
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cortex: begin entity write: %w", err)
	}
	defer tx.Rollback()

	if e.ID != "" {
		row, err := c.loadEntityRowForUpdate(ctx, tx, `id = ?`, e.ID)
		switch {
		case err == nil:
			if ontology.NormalizeType(row.Type) == e.Type {
				e.Type = row.Type
			}
			source := e.Source
			if row.isOwner {
				source = row.Source
			}
			created, err := c.writeEntityEnc(ctx, tx, entWrite{id: e.ID, name: e.Name, typ: e.Type, source: source,
				attrs: e.Attributes, conf: e.Confidence, updated: now, conn: c.NullConn()})
			if err != nil {
				return err
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("cortex: commit entity: %w", err)
			}
			e.CreatedAt, e.UpdatedAt, e.WrittenBy = created, now, c.ConnID
			c.NotifyWrite("entity")
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
	}

	nameKey, typeKey := store.NormName(e.Name), store.NormName(e.Type)
	where := `(enc AND name_blind = ? AND type_blind = ?)`
	args := []any{c.BlindNorm(e.Name), c.BlindNorm(e.Type)}
	if c.legacyOK() {
		where += ` OR (enc IS NOT TRUE AND lower(name) = ? AND lower(type) = ?)`
		args = append(args, nameKey, typeKey)
	}
	for attempt := 0; attempt < 3; attempt++ {
		row, err := c.loadEntityRowForUpdate(ctx, tx, where, args...)
		switch {
		case err == nil:
			source := e.Source
			if row.isOwner {
				source = row.Source
			}
			created, err := c.writeEntityEnc(ctx, tx, entWrite{id: row.ID, name: row.Name, typ: row.Type, source: source,
				attrs: e.Attributes, conf: e.Confidence, updated: now, conn: c.NullConn()})
			if err != nil {
				return err
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("cortex: commit entity: %w", err)
			}
			e.ID, e.CreatedAt, e.UpdatedAt, e.WrittenBy = row.ID, created, now, c.ConnID
			c.NotifyWrite("entity")
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		id := store.NewID()
		nameCT, err := c.SealStr(store.TblEntities, id, "name", e.Name)
		if err != nil {
			return err
		}
		typeCT, err := c.SealStr(store.TblEntities, id, "type", e.Type)
		if err != nil {
			return err
		}
		sourceCT, err := c.SealStr(store.TblEntities, id, "source", e.Source)
		if err != nil {
			return err
		}
		attrsCT, err := c.SealJSON(store.TblEntities, id, "attributes", e.Attributes)
		if err != nil {
			return err
		}
		var created time.Time
		err = tx.QueryRowContext(ctx,
			`INSERT INTO entities (id, user_id, enc, name_ct, type_ct, source_ct, attributes_ct, name_blind, type_blind,
			                       confidence, created_at, updated_at, written_by_connection_id)
			 VALUES (?, ?, TRUE, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT (user_id, name_blind, type_blind) WHERE deleted_at IS NULL AND enc DO NOTHING
			 RETURNING created_at`,
			id, c.UserID, nameCT, typeCT, sourceCT, attrsCT, c.BlindNorm(e.Name), c.BlindNorm(e.Type),
			e.Confidence, now, now, c.NullConn()).Scan(&created)
		if errors.Is(err, sql.ErrNoRows) {
			continue // lost a race to a concurrent insert; look it up again
		}
		if err != nil {
			return fmt.Errorf("cortex: upsert entity: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("cortex: commit entity: %w", err)
		}
		e.ID, e.CreatedAt, e.UpdatedAt, e.WrittenBy = id, created, now, c.ConnID
		c.NotifyWrite("entity")
		return nil
	}
	return fmt.Errorf("cortex: upsert entity: concurrent writers did not converge")
}

// openEntityRow decodes one entity row read with a custom column list:
// callers pass the already scanned parts.
func (c Ops) entityType(id string, enc bool, typ sql.NullString, typeCT []byte) (string, error) {
	if err := c.CheckRowForm(enc); err != nil {
		return "", err
	}
	if !enc {
		return typ.String, nil
	}
	return c.OpenStr(store.TblEntities, id, "type", typeCT)
}

// ilikeMatch implements the ILIKE pattern semantics FindEntities exposes
// (% and _ wildcards, case-insensitive) over a decrypted value.
func ilikeMatch(pattern, s string) bool {
	p, v := []rune(strings.ToLower(pattern)), []rune(strings.ToLower(s))
	var match func(i, j int) bool
	match = func(i, j int) bool {
		for i < len(p) {
			switch p[i] {
			case '%':
				for k := j; k <= len(v); k++ {
					if match(i+1, k) {
						return true
					}
				}
				return false
			case '_':
				if j >= len(v) {
					return false
				}
			default:
				if j >= len(v) || v[j] != p[i] {
					return false
				}
			}
			i++
			j++
		}
		return j == len(v)
	}
	return match(0, 0)
}

// ---- relationships ----

// relCols is the select list of the relationship readers.
const relCols = `id, source_id, target_id, enc, type, attributes, source, type_ct, attributes_ct, source_ct,
	confidence, created_at, evidence_count, last_seen_at, valid_at, invalid_at`

func (c Ops) scanRelationship(sc scanner) (model.Relationship, error) {
	var r model.Relationship
	var enc bool
	var typ, attrs, source sql.NullString
	var typeCT, attrsCT, sourceCT []byte
	var validAt, invalidAt sql.NullTime
	if err := sc.Scan(&r.ID, &r.SourceID, &r.TargetID, &enc, &typ, &attrs, &source, &typeCT, &attrsCT, &sourceCT,
		&r.Confidence, &r.CreatedAt, &r.EvidenceCount, &r.LastSeenAt, &validAt, &invalidAt); err != nil {
		return r, err
	}
	if err := c.OpenRelFields(&r, enc, typ, source, attrs, typeCT, sourceCT, attrsCT); err != nil {
		return r, err
	}
	r.ValidAt, r.InvalidAt = store.NullTimePtr(validAt), store.NullTimePtr(invalidAt)
	return r, nil
}

// putRelationshipEnc is the encrypted upsert of PutRelationship. Evidence is
// merged in Go (attributes are ciphertext), the row is found by type_blind
// (or the legacy type while migrating) and rewritten as encrypted.
func (c Ops) putRelationshipEnc(ctx context.Context, r *model.Relationship, attrs map[string]any, now time.Time) error {
	tx, err := c.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cortex: begin relationship: %w", err)
	}
	defer tx.Rollback()

	requestedStart := r.ValidAt
	blind := c.BlindNorm(r.Type)
	done := false
	for attempt := 0; attempt < 3 && !done; attempt++ {
		old, err := c.scanRelationship(tx.QueryRowContext(ctx,
			`SELECT `+relCols+` FROM relationships
			 WHERE user_id = ? AND source_id = ? AND target_id = ? AND deleted_at IS NULL
			   AND ((enc AND type_blind = ?) OR (?::boolean AND enc IS NOT TRUE AND type = ?))
			 ORDER BY enc DESC LIMIT 1 FOR UPDATE`,
			c.UserID, r.SourceID, r.TargetID, blind, c.legacyOK(), r.Type))
		switch {
		case err == nil:
			merged := attrs
			if old.Attributes != nil {
				merged = map[string]any{}
				for k, v := range old.Attributes {
					merged[k] = v
				}
				for k, v := range attrs {
					merged[k] = v
				}
			}
			conf := old.Confidence
			if r.Confidence > conf {
				conf = r.Confidence
			}
			var newValid *time.Time
			if old.InvalidAt != nil && r.InvalidAt == nil {
				newValid = r.ValidAt
				if newValid == nil {
					n := now
					newValid = &n
				}
			} else {
				newValid = old.ValidAt
				if newValid == nil {
					newValid = r.ValidAt
				}
			}
			typeCT, err := c.SealStr(store.TblRelationships, old.ID, "type", old.Type)
			if err != nil {
				return err
			}
			sourceCT, err := c.SealStr(store.TblRelationships, old.ID, "source", old.Source)
			if err != nil {
				return err
			}
			attrsCT, err := c.SealJSON(store.TblRelationships, old.ID, "attributes", merged)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx,
				`UPDATE relationships SET enc = TRUE, type = NULL, source = NULL, attributes = NULL,
				   type_ct = ?, source_ct = ?, attributes_ct = ?, type_blind = ?,
				   evidence_count = ?, last_seen_at = ?, confidence = ?, valid_at = ?, invalid_at = ?
				 WHERE id = ? AND user_id = ?`,
				typeCT, sourceCT, attrsCT, c.BlindNorm(old.Type),
				old.EvidenceCount+1, now, conf, newValid, r.InvalidAt, old.ID, c.UserID); err != nil {
				return fmt.Errorf("cortex: upsert relationship: %w", err)
			}
			r.ID, r.CreatedAt, r.EvidenceCount, r.Confidence = old.ID, old.CreatedAt, old.EvidenceCount+1, conf
			r.ValidAt = newValid
			done = true
		case errors.Is(err, sql.ErrNoRows):
			id := store.NewID()
			typeCT, err := c.SealStr(store.TblRelationships, id, "type", r.Type)
			if err != nil {
				return err
			}
			sourceCT, err := c.SealStr(store.TblRelationships, id, "source", r.Source)
			if err != nil {
				return err
			}
			attrsCT, err := c.SealJSON(store.TblRelationships, id, "attributes", attrs)
			if err != nil {
				return err
			}
			var created time.Time
			err = tx.QueryRowContext(ctx,
				`INSERT INTO relationships (id, user_id, source_id, target_id, enc, type_ct, source_ct, attributes_ct, type_blind,
				                            confidence, created_at, last_seen_at, valid_at, invalid_at, written_by_connection_id)
				 VALUES (?, ?, ?, ?, TRUE, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
				 ON CONFLICT (user_id, source_id, target_id, type_blind) WHERE deleted_at IS NULL AND enc DO NOTHING
				 RETURNING created_at`,
				id, c.UserID, r.SourceID, r.TargetID, typeCT, sourceCT, attrsCT, blind,
				r.Confidence, now, now, r.ValidAt, r.InvalidAt, c.NullConn()).Scan(&created)
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			if err != nil {
				return fmt.Errorf("cortex: upsert relationship: %w", err)
			}
			r.ID, r.CreatedAt, r.EvidenceCount = id, created, 1
			done = true
		default:
			return err
		}
	}
	if !done {
		return fmt.Errorf("cortex: upsert relationship: concurrent writers did not converge")
	}

	if r.InvalidAt == nil && contains(ontology.SingleValuedVerbs(), r.Type) {
		var closeAt any
		if requestedStart != nil {
			closeAt = *requestedStart
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE relationships SET invalid_at = COALESCE(?::timestamptz, now())
			 WHERE user_id = ? AND deleted_at IS NULL AND invalid_at IS NULL
			   AND source_id = ? AND ((enc AND type_blind = ?) OR (enc IS NOT TRUE AND type = ?)) AND target_id <> ?`,
			closeAt, c.UserID, r.SourceID, blind, r.Type, r.TargetID); err != nil {
			return fmt.Errorf("cortex: close superseded relationships: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cortex: commit relationship: %w", err)
	}
	r.LastSeenAt = now
	c.NotifyWrite("relationship")
	return nil
}

// ---- aliases ----

// aliasRow is a decoded entity_aliases row.
type aliasRow struct {
	id, entityID string
	enc          bool
	kind         string
	value        string
	source       string
	created      time.Time
}

func (c Ops) scanAlias(sc scanner) (*aliasRow, error) {
	var a aliasRow
	var value, source sql.NullString
	var valueCT, sourceCT []byte
	if err := sc.Scan(&a.id, &a.entityID, &a.kind, &a.enc, &value, &source, &valueCT, &sourceCT, &a.created); err != nil {
		return nil, err
	}
	if err := c.CheckRowForm(a.enc); err != nil {
		return nil, err
	}
	if !a.enc {
		a.value, a.source = value.String, source.String
		return &a, nil
	}
	var err error
	if a.value, err = c.OpenStr(store.TblAliases, a.id, "value", valueCT); err != nil {
		return nil, err
	}
	if a.source, err = c.OpenOptStr(store.TblAliases, a.id, "source", sourceCT); err != nil {
		return nil, err
	}
	return &a, nil
}

const aliasCols = `id, entity_id, kind, enc, value, source, value_ct, source_ct, created_at`

// lookupAlias finds the alias (kind, norm): the blind index for encrypted
// rows, plus value_norm for legacy rows unless the user is fully encrypted.
func (c Ops) lookupAlias(ctx context.Context, q queryRower, kind, norm string, lock bool) (*aliasRow, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	return c.scanAlias(q.QueryRowContext(ctx,
		`SELECT `+aliasCols+` FROM entity_aliases
		 WHERE user_id = ? AND kind = ?
		   AND ((enc AND value_blind = ?) OR (?::boolean AND enc IS NOT TRUE AND value_norm = ?))
		 ORDER BY enc DESC LIMIT 1`+suffix, c.UserID, kind, c.Blind(norm), c.legacyOK(), norm))
}

// writeAliasEnc rewrites alias row id as encrypted under entityID.
func (c Ops) writeAliasEnc(ctx context.Context, q queryRower, id, entityID, kind, value, source string) error {
	valueCT, err := c.SealStr(store.TblAliases, id, "value", value)
	if err != nil {
		return err
	}
	var sourceCT []byte
	if source != "" {
		if sourceCT, err = c.SealStr(store.TblAliases, id, "source", source); err != nil {
			return err
		}
	}
	if _, err := q.ExecContext(ctx,
		`UPDATE entity_aliases SET enc = TRUE, entity_id = ?, value = NULL, value_norm = NULL, source = NULL,
		   value_ct = ?, value_blind = ?, source_ct = ?
		 WHERE id = ? AND user_id = ?`,
		entityID, valueCT, c.Blind(normalizeAlias(kind, value)), sourceCT, id, c.UserID); err != nil {
		return fmt.Errorf("cortex: update alias: %w", err)
	}
	return nil
}

// insertAliasEnc inserts a new encrypted alias; false when the unique index
// already holds one for (user, kind, blind).
func (c Ops) insertAliasEnc(ctx context.Context, q queryRower, entityID, kind, value, source string) (bool, error) {
	id := store.NewID()
	value = strings.TrimSpace(value)
	valueCT, err := c.SealStr(store.TblAliases, id, "value", value)
	if err != nil {
		return false, err
	}
	var sourceCT []byte
	if source != "" {
		if sourceCT, err = c.SealStr(store.TblAliases, id, "source", source); err != nil {
			return false, err
		}
	}
	var got string
	err = q.QueryRowContext(ctx,
		`INSERT INTO entity_aliases (id, user_id, entity_id, kind, enc, value_ct, value_blind, source_ct)
		 VALUES (?, ?, ?, ?, TRUE, ?, ?, ?)
		 ON CONFLICT DO NOTHING RETURNING id`,
		id, c.UserID, entityID, kind, valueCT, c.Blind(normalizeAlias(kind, value)), sourceCT).Scan(&got)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("cortex: add alias: %w", err)
	}
	return true, nil
}

// addAliasEnc is AddAlias for migrating and encrypted handles.
func (c Ops) addAliasEnc(ctx context.Context, entityID, kind, value, norm string) error {
	for attempt := 0; attempt < 3; attempt++ {
		tx, err := c.DB.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("cortex: add alias: %w", err)
		}
		row, err := c.lookupAlias(ctx, tx, kind, norm, true)
		switch {
		case err == nil:
			target := row.entityID
			var live bool
			if err := tx.QueryRowContext(ctx,
				`SELECT EXISTS (SELECT 1 FROM entities WHERE id = ? AND user_id = ? AND deleted_at IS NULL)`,
				row.entityID, c.UserID).Scan(&live); err != nil {
				tx.Rollback()
				return fmt.Errorf("cortex: add alias: %w", err)
			}
			if !live {
				target = entityID
			}
			if target != entityID {
				tx.Rollback()
				return model.ErrAliasTaken
			}
			if !row.enc || target != row.entityID {
				if err := c.writeAliasEnc(ctx, tx, row.id, target, row.kind, row.value, row.source); err != nil {
					tx.Rollback()
					return err
				}
			}
			if err := tx.Commit(); err != nil {
				return fmt.Errorf("cortex: add alias: %w", err)
			}
			c.NotifyWrite("alias")
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			tx.Rollback()
			return err
		}
		ok, err := c.insertAliasEnc(ctx, tx, entityID, kind, value, "user")
		if err != nil {
			tx.Rollback()
			return err
		}
		if !ok {
			tx.Rollback()
			continue
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("cortex: add alias: %w", err)
		}
		c.NotifyWrite("alias")
		return nil
	}
	return fmt.Errorf("cortex: add alias: concurrent writers did not converge")
}

// addAliasQuietEnc is AddAliasQuiet for migrating and encrypted handles.
func (c Ops) addAliasQuietEnc(ctx context.Context, entityID, kind, value, source, norm string) error {
	for attempt := 0; attempt < 3; attempt++ {
		row, err := c.lookupAlias(ctx, c.DB, kind, norm, false)
		switch {
		case err == nil:
			if !row.enc && c.Migrating() {
				if err := c.writeAliasEnc(ctx, c.DB, row.id, row.entityID, row.kind, row.value, row.source); err != nil {
					return err
				}
				c.NotifyWrite("alias")
			}
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		ok, err := c.insertAliasEnc(ctx, c.DB, entityID, kind, value, source)
		if err != nil {
			return err
		}
		if ok {
			c.NotifyWrite("alias")
			return nil
		}
	}
	return nil
}

func sortAliases(rows []aliasRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].kind != rows[j].kind {
			return rows[i].kind < rows[j].kind
		}
		return normalizeAlias(rows[i].kind, rows[i].value) < normalizeAlias(rows[j].kind, rows[j].value)
	})
}

// sameRelType is the SQL predicate that two relationship rows (aliases a and
// b) have the same type: by type_blind when both are encrypted, by plain type
// when both are legacy. A legacy/encrypted pair compares as different; they
// cannot violate either partial unique index.
func sameRelType(a, b string) string {
	return fmt.Sprintf(`(CASE WHEN %[1]s.enc AND %[2]s.enc THEN %[1]s.type_blind = %[2]s.type_blind
	 WHEN NOT %[1]s.enc AND NOT %[2]s.enc THEN %[1]s.type = %[2]s.type ELSE FALSE END)`, a, b)
}

// openNameType fills only name and type of e (ID set) from a partial row.
func (c Ops) openNameType(e *model.Entity, enc bool, name, typ sql.NullString, nameCT, typeCT []byte) error {
	if err := c.CheckRowForm(enc); err != nil {
		return err
	}
	if !enc {
		e.Name, e.Type = name.String, typ.String
		return nil
	}
	var err error
	if e.Name, err = c.OpenStr(store.TblEntities, e.ID, "name", nameCT); err != nil {
		return err
	}
	e.Type, err = c.OpenStr(store.TblEntities, e.ID, "type", typeCT)
	return err
}

// renameEntityEnc is RenameEntity's body for migrating and encrypted
// handles: uniqueness is checked through the blind indexes (and legacy
// columns while migrating), and the row is rewritten encrypted.
func (c Ops) renameEntityEnc(ctx context.Context, tx *store.Tx, id, name string) error {
	row, err := c.loadEntityRowForUpdate(ctx, tx, `id = ?`, id)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrEntityNotFound()
	}
	if err != nil {
		return err
	}
	where := `(enc AND name_blind = ? AND type_blind = ?)`
	args := []any{c.UserID, id, c.BlindNorm(name), c.BlindNorm(row.Type)}
	if c.legacyOK() {
		where += ` OR (enc IS NOT TRUE AND lower(name) = ? AND lower(type) = ?)`
		args = append(args, store.NormName(name), store.NormName(row.Type))
	}
	var other string
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM entities WHERE user_id = ? AND deleted_at IS NULL AND id <> ? AND (`+where+`) LIMIT 1`, args...).Scan(&other)
	if err == nil {
		return &model.ErrEntityNameTaken{ConflictID: other}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("cortex: rename entity: %w", err)
	}
	if _, err := c.writeEntityEnc(ctx, tx, entWrite{id: id, name: name, typ: row.Type, source: row.Source,
		attrs: row.Attributes, conf: row.Confidence, updated: time.Now().UTC()}); err != nil {
		return fmt.Errorf("cortex: rename entity: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM embeddings WHERE user_id = ? AND ref_type = 'entity' AND ref_id = ?`, c.UserID, id); err != nil {
		return fmt.Errorf("cortex: drop stale entity embedding: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("cortex: commit rename: %w", err)
	}
	c.NotifyWrite("entity")
	return nil
}
