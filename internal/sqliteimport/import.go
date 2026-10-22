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

// Import copies the SQLite brain at sqlitePath into cx, then embeds anything
// that lacks an embedding.
func Import(ctx context.Context, sqlitePath string, cx *cortex.Cortex, opts Options) (Report, error) {
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
		if err := copyAll(ctx, src, dst, opts.Force); err != nil {
			return rep, err
		}
	}
	for _, t := range tables {
		var s, d int
		if err := src.QueryRowContext(ctx, "SELECT count(*) FROM "+t).Scan(&s); err != nil {
			return rep, fmt.Errorf("count source %s: %w", t, err)
		}
		if err := dst.QueryRowContext(ctx, "SELECT count(*) FROM "+t).Scan(&d); err != nil {
			return rep, fmt.Errorf("count target %s: %w", t, err)
		}
		rep.Counts[t] = [2]int{s, d}
	}

	n, err := embedMissing(ctx, dst, cx.Embedder(), opts.BatchSize)
	rep.Embedded = n
	return rep, err
}

func copyAll(ctx context.Context, src, dst *sql.DB, force bool) error {
	if !force {
		var n int
		if err := dst.QueryRowContext(ctx, "SELECT count(*) FROM entities").Scan(&n); err != nil {
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
		if err := copyTable(ctx, src, tx, ts); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func copyTable(ctx context.Context, src *sql.DB, tx *sql.Tx, ts tableSpec) error {
	names := make([]string, len(ts.cols))
	ph := make([]string, len(ts.cols))
	for i, col := range ts.cols {
		names[i] = col.name
		ph[i] = "$" + strconv.Itoa(i+1)
	}
	ins := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s) ON CONFLICT DO NOTHING",
		ts.name, strings.Join(names, ", "), strings.Join(ph, ", "))
	rows, err := src.QueryContext(ctx, fmt.Sprintf("SELECT %s FROM %s", strings.Join(names, ", "), ts.name))
	if err != nil {
		return fmt.Errorf("read %s: %w", ts.name, err)
	}
	defer rows.Close()
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
		args := make([]any, len(raw))
		for i, v := range raw {
			a, err := conv(ts.name, rowID, ts.cols[i].kind, v)
			if err != nil {
				return err
			}
			args[i] = a
		}
		if _, err := tx.ExecContext(ctx, ins, args...); err != nil {
			return fmt.Errorf("table %s row %s: %w", ts.name, rowID, err)
		}
	}
	return rows.Err()
}

func asString(v any) string {
	if b, ok := v.([]byte); ok {
		return string(b)
	}
	return fmt.Sprint(v)
}

type pending struct{ id, text string }

// embedMissing embeds every memory, chunk and entity lacking an embedding.
func embedMissing(ctx context.Context, db *sql.DB, emb cortex.Embedder, batch int) (int, error) {
	var count int
	var dims int
	var ds string
	if err := db.QueryRowContext(ctx, `SELECT value FROM cortex_meta WHERE key='embedding_dims'`).Scan(&ds); err != nil {
		return 0, fmt.Errorf("read embedding_dims: %w", err)
	}
	dims, _ = strconv.Atoi(ds)

	kinds := []struct{ refType, query string }{
		{"memory", `SELECT m.id, m.content FROM memories m
			WHERE NOT EXISTS (SELECT 1 FROM embeddings e WHERE e.ref_id = m.id AND e.ref_type = 'memory')
			ORDER BY m.id LIMIT $1`},
		{"chunk", `SELECT ch.id, ch.content FROM chunks ch
			WHERE NOT EXISTS (SELECT 1 FROM embeddings e WHERE e.ref_id = ch.id AND e.ref_type = 'chunk')
			ORDER BY ch.id LIMIT $1`},
		{"entity", `SELECT en.id, en.name FROM entities en
			WHERE NOT EXISTS (SELECT 1 FROM embeddings e WHERE e.ref_id = en.id AND e.ref_type = 'entity')
			ORDER BY en.id LIMIT $1`},
	}
	if emb == nil {
		for _, k := range kinds {
			var n int
			q := strings.Replace(k.query, "SELECT", "SELECT count(*) FROM (SELECT", 1)
			q = strings.Replace(q, "LIMIT $1", "LIMIT 1) x", 1)
			if err := db.QueryRowContext(ctx, q).Scan(&n); err == nil && n > 0 {
				return 0, fmt.Errorf("rows need embeddings but no embedder is configured")
			}
		}
		return 0, nil
	}
	for _, k := range kinds {
		for {
			items, err := loadPending(ctx, db, k.query, batch)
			if err != nil {
				return count, err
			}
			if len(items) == 0 {
				break
			}
			texts := make([]string, len(items))
			for i, it := range items {
				texts[i] = it.text
				if k.refType == "memory" {
					names, err := entityNames(ctx, db, it.id)
					if err != nil {
						return count, err
					}
					texts[i] = cortex.AugmentForEmbedding(it.text, names)
				}
			}
			vecs, err := emb.Embed(ctx, texts)
			if err != nil {
				return count, fmt.Errorf("embed %s batch: %w", k.refType, err)
			}
			if len(vecs) != len(items) {
				return count, fmt.Errorf("embedder returned %d vectors for %d texts", len(vecs), len(items))
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return count, err
			}
			for i, it := range items {
				if len(vecs[i]) != dims {
					tx.Rollback()
					return count, fmt.Errorf("embedding has %d dims, want %d", len(vecs[i]), dims)
				}
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO embeddings (id, ref_id, ref_type, embedding, created_at)
					 VALUES ($1, $2, $3, $4::halfvec, now())`,
					newID(), it.id, k.refType, vectorLiteral(vecs[i])); err != nil {
					tx.Rollback()
					return count, fmt.Errorf("insert embedding: %w", err)
				}
			}
			if err := tx.Commit(); err != nil {
				return count, err
			}
			count += len(items)
		}
	}
	return count, nil
}

func loadPending(ctx context.Context, db *sql.DB, q string, n int) ([]pending, error) {
	rows, err := db.QueryContext(ctx, q, n)
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

func entityNames(ctx context.Context, db *sql.DB, memID string) ([]string, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT e.name FROM memory_entities me JOIN entities e ON e.id = me.entity_id WHERE me.memory_id = $1`, memID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
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
