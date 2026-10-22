// Package sqliteimport copies a v0.4 (SQLite) cortex brain into Postgres.
// It is the only package allowed to import modernc.org/sqlite.
package sqliteimport

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/oklog/ulid/v2"
	_ "modernc.org/sqlite"

	"github.com/sausheong/cortex"
)

// Options controls Import.
type Options struct {
	Force     bool // copy even if the target already has entities
	EmbedOnly bool // skip the copy phase; only embed rows lacking embeddings
	BatchSize int  // embedding batch size (default 64)
}

// Report summarises an import. Counts maps table name to {source, target}.
type Report struct {
	Counts   map[string][2]int
	Embedded int
}

var tables = []string{"entities", "relationships", "memories", "memory_entities", "memory_edges", "chunks", "sync_state"}

var timeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05Z",
}

func parseTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, l := range timeLayouts {
		if t, err := time.ParseInLocation(l, s, time.UTC); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("unparseable")
}

// conv converts a value scanned from SQLite into a Postgres parameter.
// kind is "t" (timestamp), "j" (json), "b" (bool) or "" (as-is).
func conv(table, row, kind string, v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	if b, ok := v.([]byte); ok {
		v = string(b)
	}
	switch kind {
	case "t":
		switch x := v.(type) {
		case time.Time:
			return x, nil
		case string:
			t, err := parseTime(x)
			if err != nil {
				return nil, fmt.Errorf("table %s row %s: bad timestamp %q", table, row, x)
			}
			return t, nil
		}
		return nil, fmt.Errorf("table %s row %s: bad timestamp %v", table, row, v)
	case "j":
		if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
			return nil, nil
		}
	case "b":
		switch x := v.(type) {
		case int64:
			return x != 0, nil
		case bool:
			return x, nil
		case string:
			n, _ := strconv.Atoi(x)
			return n != 0, nil
		}
	}
	return v, nil
}

type colSpec struct{ name, kind string }

type tableSpec struct {
	name   string
	cols   []colSpec
	idCols []int // indexes of columns identifying a row (for error messages)
}

func c(name, kind string) colSpec { return colSpec{name, kind} }

var specs = []tableSpec{
	{"entities", []colSpec{c("id", ""), c("type", ""), c("name", ""), c("attributes", "j"), c("source", ""), c("confidence", ""), c("created_at", "t"), c("updated_at", "t")}, []int{0}},
	{"relationships", []colSpec{c("id", ""), c("source_id", ""), c("target_id", ""), c("type", ""), c("attributes", "j"), c("source", ""), c("confidence", ""), c("created_at", "t")}, []int{0}},
	{"memories", []colSpec{c("id", ""), c("content", ""), c("source", ""), c("confidence", ""), c("valid_at", "t"), c("invalid_at", "t"), c("expired_at", "t"), c("speaker", ""), c("static", "b"), c("last_decay_at", "t"), c("forget_after", "t"), c("created_at", "t"), c("updated_at", "t")}, []int{0}},
	{"memory_entities", []colSpec{c("memory_id", ""), c("entity_id", "")}, []int{0, 1}},
	{"memory_edges", []colSpec{c("id", ""), c("source_id", ""), c("target_id", ""), c("type", ""), c("source", ""), c("created_at", "t")}, []int{0}},
	{"chunks", []colSpec{c("id", ""), c("entity_id", ""), c("content", ""), c("metadata", "j"), c("created_at", "t")}, []int{0}},
	{"sync_state", []colSpec{c("connector", ""), c("state", ""), c("updated_at", "t")}, []int{0}},
}

// Import copies the SQLite brain at sqlitePath into cx for userID, then
// embeds anything of that user's that lacks an embedding.
func Import(ctx context.Context, sqlitePath string, cx *cortex.Cortex, userID string, opts Options) (Report, error) {
	if userID == "" {
		return Report{}, fmt.Errorf("import: user ID is required")
	}
	rep := Report{Counts: map[string][2]int{}}
	if opts.BatchSize <= 0 {
		opts.BatchSize = 64
	}
	src, err := sql.Open("sqlite", "file:"+sqlitePath+"?mode=ro")
	if err != nil {
		return rep, fmt.Errorf("open sqlite: %w", err)
	}
	defer src.Close()
	if err := src.PingContext(ctx); err != nil {
		return rep, fmt.Errorf("open sqlite %s: %w", sqlitePath, err)
	}
	dst := cx.DB()

	if !opts.EmbedOnly {
		if err := copyAll(ctx, src, dst, userID, opts.Force); err != nil {
			return rep, err
		}
	}
	for _, t := range tables {
		var s, d int
		have, err := sourceColumns(ctx, src, t)
		if err != nil {
			return rep, err
		}
		if len(have) > 0 { // a missing table (older brain) counts as 0 rows
			if err := src.QueryRowContext(ctx, "SELECT count(*) FROM "+t).Scan(&s); err != nil {
				return rep, fmt.Errorf("count source %s: %w", t, err)
			}
		}
		if err := dst.QueryRowContext(ctx, "SELECT count(*) FROM "+t+" WHERE user_id = $1", userID).Scan(&d); err != nil {
			return rep, fmt.Errorf("count target %s: %w", t, err)
		}
		rep.Counts[t] = [2]int{s, d}
	}

	srcDims, err := sourceDims(ctx, src)
	if err != nil {
		return rep, err
	}
	n, err := embedMissing(ctx, dst, cx.Embedder(), userID, srcDims, opts.BatchSize)
	rep.Embedded = n
	return rep, err
}

func copyAll(ctx context.Context, src, dst *sql.DB, userID string, force bool) error {
	if !force {
		var n int
		if err := dst.QueryRowContext(ctx, "SELECT count(*) FROM entities WHERE user_id = $1", userID).Scan(&n); err != nil {
			return fmt.Errorf("check target: %w", err)
		}
		if n > 0 {
			return fmt.Errorf("target database already has %d entities; use --force to import anyway", n)
		}
	}
	tx, err := dst.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, ts := range specs {
		if err := copyTable(ctx, src, tx, userID, ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// sourceColumns returns the columns the SQLite table actually has. Brains
// written by older cortex versions lack later columns (static, forget_after,
// ...) and tables (memory_edges); an empty result means the table is absent.
func sourceColumns(ctx context.Context, src *sql.DB, table string) (map[string]bool, error) {
	rows, err := src.QueryContext(ctx, "SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", table, err)
	}
	defer rows.Close()
	have := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("inspect %s: %w", table, err)
		}
		have[n] = true
	}
	return have, rows.Err()
}

func copyTable(ctx context.Context, src *sql.DB, tx *sql.Tx, userID string, ts tableSpec) error {
	have, err := sourceColumns(ctx, src, ts.name)
	if err != nil {
		return err
	}
	if len(have) == 0 {
		return nil
	}
	// Copy only columns the source has; absent ones take Postgres defaults.
	// Key columns lead every spec and exist in every version, so idCols stay
	// valid after filtering.
	var cols []colSpec
	for _, col := range ts.cols {
		if have[col.name] {
			cols = append(cols, col)
		}
	}
	for _, i := range ts.idCols {
		if !have[ts.cols[i].name] {
			return fmt.Errorf("table %s: missing key column %s", ts.name, ts.cols[i].name)
		}
	}
	ts = tableSpec{name: ts.name, cols: cols, idCols: ts.idCols}
	names := make([]string, len(ts.cols))
	for i, col := range ts.cols {
		names[i] = col.name
	}
	rows, err := src.QueryContext(ctx, fmt.Sprintf("SELECT %s FROM %s", strings.Join(names, ", "), ts.name))
	if err != nil {
		return fmt.Errorf("read %s: %w", ts.name, err)
	}
	defer rows.Close()

	// Rows are sent copyBatchRows at a time in one multi-row INSERT: the
	// target is usually across a network, and one round trip per row made a
	// 30K-row brain take most of an hour over the internet.
	var args []any
	var ids []string
	flush := func() error {
		if len(ids) == 0 {
			return nil
		}
		if _, err := tx.ExecContext(ctx, batchInsert(ts.name, append([]string{"user_id"}, names...), len(ids)), args...); err != nil {
			return fmt.Errorf("table %s rows %s..%s: %w", ts.name, ids[0], ids[len(ids)-1], err)
		}
		args, ids = args[:0], ids[:0]
		return nil
	}
	for rows.Next() {
		raw := make([]any, len(ts.cols))
		ptrs := make([]any, len(raw))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return fmt.Errorf("scan %s: %w", ts.name, err)
		}
		var idParts []string
		for _, i := range ts.idCols {
			idParts = append(idParts, fmt.Sprint(asString(raw[i])))
		}
		rowID := strings.Join(idParts, "/")
		args = append(args, userID)
		for i, v := range raw {
			a, err := conv(ts.name, rowID, ts.cols[i].kind, v)
			if err != nil {
				return err
			}
			args = append(args, a)
		}
		ids = append(ids, rowID)
		if len(ids) == copyBatchRows {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return flush()
}

// copyBatchRows is how many rows each INSERT carries. 500 rows of the widest
// table (14 columns with user_id) stays well under Postgres's 65535-parameter limit.
var copyBatchRows = 500

// batchInsert builds "INSERT INTO t (cols) VALUES ($1,..),(..) ON CONFLICT DO NOTHING"
// for n rows.
func batchInsert(table string, cols []string, n int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "INSERT INTO %s (%s) VALUES ", table, strings.Join(cols, ", "))
	p := 1
	for r := 0; r < n; r++ {
		if r > 0 {
			b.WriteString(", ")
		}
		b.WriteByte('(')
		for c := range cols {
			if c > 0 {
				b.WriteString(", ")
			}
			b.WriteString("$" + strconv.Itoa(p))
			p++
		}
		b.WriteByte(')')
	}
	b.WriteString(" ON CONFLICT DO NOTHING")
	return b.String()
}

func asString(v any) string {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return fmt.Sprint(v)
}

type pending struct{ id, text string }

// embedMissing embeds every memory, chunk and entity lacking an embedding.
// dims is the source brain's embedding dimension (0 = unknown: the first
// vector decides); it is written per row.
func embedMissing(ctx context.Context, db *sql.DB, emb cortex.Embedder, userID string, dims, batch int) (int, error) {
	var count int

	kinds := []struct{ refType, query string }{
		{"memory", `SELECT m.id, m.content FROM memories m
			WHERE m.user_id = $2 AND m.deleted_at IS NULL
			AND NOT EXISTS (SELECT 1 FROM embeddings e WHERE e.user_id = m.user_id AND e.ref_id = m.id AND e.ref_type = 'memory')
			ORDER BY m.id LIMIT $1`},
		{"chunk", `SELECT ch.id, ch.content FROM chunks ch
			WHERE ch.user_id = $2 AND ch.deleted_at IS NULL
			AND NOT EXISTS (SELECT 1 FROM embeddings e WHERE e.user_id = ch.user_id AND e.ref_id = ch.id AND e.ref_type = 'chunk')
			ORDER BY ch.id LIMIT $1`},
		{"entity", `SELECT en.id, en.name FROM entities en
			WHERE en.user_id = $2 AND en.deleted_at IS NULL
			AND NOT EXISTS (SELECT 1 FROM embeddings e WHERE e.user_id = en.user_id AND e.ref_id = en.id AND e.ref_type = 'entity')
			ORDER BY en.id LIMIT $1`},
	}
	if emb == nil {
		for _, k := range kinds {
			var n int
			q := strings.Replace(k.query, "SELECT", "SELECT count(*) FROM (SELECT", 1)
			q = strings.Replace(q, "LIMIT $1", "LIMIT 1) x", 1)
			q = strings.Replace(q, "$2", "$1", 1)
			if err := db.QueryRowContext(ctx, q, userID).Scan(&n); err == nil && n > 0 {
				return 0, fmt.Errorf("rows need embeddings but no embedder is configured")
			}
		}
		return 0, nil
	}
	for _, k := range kinds {
		for {
			items, err := loadPending(ctx, db, k.query, batch, userID)
			if err != nil {
				return count, err
			}
			if len(items) == 0 {
				break
			}
			texts := make([]string, len(items))
			var names map[string][]string
			if k.refType == "memory" {
				if names, err = batchEntityNames(ctx, db, userID, items); err != nil {
					return count, err
				}
			}
			for i, it := range items {
				texts[i] = it.text
				if k.refType == "memory" {
					texts[i] = cortex.AugmentForEmbedding(it.text, names[it.id])
				}
				texts[i] = truncateRunes(texts[i], maxEmbedChars)
			}
			vecs, err := emb.Embed(ctx, texts)
			if err != nil {
				return count, fmt.Errorf("embed %s batch: %w", k.refType, err)
			}
			if len(vecs) != len(items) {
				return count, fmt.Errorf("embedder returned %d vectors for %d texts", len(vecs), len(items))
			}
			// One multi-row INSERT per batch (one round trip), committed
			// atomically so a rerun resumes cleanly.
			var q strings.Builder
			q.WriteString("INSERT INTO embeddings (id, user_id, ref_id, ref_type, embedding, dims, generation, created_at) VALUES ")
			args := make([]any, 0, 6*len(items))
			for i, it := range items {
				if dims == 0 {
					dims = len(vecs[i])
				}
				if len(vecs[i]) != dims {
					return count, fmt.Errorf("embedding has %d dims, want %d", len(vecs[i]), dims)
				}
				if i > 0 {
					q.WriteString(", ")
				}
				n := 6 * i
				fmt.Fprintf(&q, "($%d, $%d, $%d, $%d, $%d::halfvec, $%d, 1, now())", n+1, n+2, n+3, n+4, n+5, n+6)
				args = append(args, newID(), userID, it.id, k.refType, vectorLiteral(vecs[i]), dims)
			}
			q.WriteString(" ON CONFLICT (user_id, ref_type, ref_id, generation) DO NOTHING")
			if _, err := db.ExecContext(ctx, q.String(), args...); err != nil {
				return count, fmt.Errorf("insert embeddings: %w", err)
			}
			count += len(items)
		}
	}
	return count, nil
}

func loadPending(ctx context.Context, db *sql.DB, q string, n int, userID string) ([]pending, error) {
	rows, err := db.QueryContext(ctx, q, n, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.text); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// maxEmbedChars caps embedding input, in bytes. Old brains hold chunks of
// 300K+ chars, far past text-embedding-3's 8192-token limit (which the old
// store never embedded at all); a prefix is embedded instead and the stored
// chunk keeps its full text. Every BPE token covers at least one byte, so
// 8000 bytes can never exceed 8192 tokens, however dense the text.
const maxEmbedChars = 8000

// truncateRunes returns s cut to at most n bytes on a UTF-8 boundary.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// batchEntityNames returns linked entity names keyed by memory ID for every
// memory in items, in one query.
func batchEntityNames(ctx context.Context, db *sql.DB, userID string, items []pending) (map[string][]string, error) {
	ids := make([]string, len(items))
	for i, it := range items {
		ids[i] = it.id
	}
	rows, err := db.QueryContext(ctx,
		`SELECT me.memory_id, e.name FROM memory_entities me JOIN entities e ON e.id = me.entity_id AND e.user_id = me.user_id
		 WHERE me.user_id = $2 AND me.memory_id = ANY($1)`, ids, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = append(out[id], name)
	}
	return out, rows.Err()
}

func vectorLiteral(v []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, x := range v {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(x), 'g', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

func newID() string { return ulid.MustNew(ulid.Timestamp(time.Now()), rand.Reader).String() }

// sourceDims returns the embedding dimension recorded in the SQLite brain's
// own cortex_meta, or 0 when it has none.
func sourceDims(ctx context.Context, src *sql.DB) (int, error) {
	have, err := sourceColumns(ctx, src, "cortex_meta")
	if err != nil {
		return 0, err
	}
	if !have["key"] || !have["value"] {
		return 0, nil
	}
	var v string
	err = src.QueryRowContext(ctx, `SELECT value FROM cortex_meta WHERE key = 'embedding_dims'`).Scan(&v)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read source embedding_dims: %w", err)
	}
	n, _ := strconv.Atoi(v)
	return n, nil
}
