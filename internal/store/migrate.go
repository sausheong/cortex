package store

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migration is one embedded schema migration.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

func LoadMigrations() ([]Migration, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	var out []Migration
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".sql") {
			continue
		}
		prefix, _, _ := strings.Cut(name, "_")
		v, err := strconv.Atoi(prefix)
		if err != nil {
			return nil, fmt.Errorf("cortex: bad migration name %q", name)
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		out = append(out, Migration{Version: v, Name: name, SQL: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}

// Migrate applies unapplied migrations in order. A transaction-scoped
// advisory lock serialises concurrent Opens.
func (h *Handle) Migrate(ctx context.Context) error {
	return h.MigrateUpTo(ctx, 0)
}

// MigrateUpTo applies unapplied migrations with version <= max (all of them
// when max <= 0). It is a test hook for upgrade-path tests.
func (h *Handle) MigrateUpTo(ctx context.Context, max int) error {
	migs, err := LoadMigrations()
	if err != nil {
		return err
	}
	tx, err := h.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("cortex: begin migration: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(4242)`); err != nil {
		return fmt.Errorf("cortex: migration lock: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("cortex: create schema_migrations: %w", err)
	}
	applied := map[int]bool{}
	rows, err := tx.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("cortex: read schema_migrations: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	for _, m := range migs {
		if applied[m.Version] || (max > 0 && m.Version > max) {
			continue
		}
		if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
			return fmt.Errorf("cortex: migration %s: %w", m.Name, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES (?)`, m.Version); err != nil {
			return fmt.Errorf("cortex: record migration %s: %w", m.Name, err)
		}
	}
	return tx.Commit()
}

// AppliedMigrations returns the versions recorded in schema_migrations, ascending.
func (h *Handle) AppliedMigrations(ctx context.Context) ([]int, error) {
	rows, err := h.DB.QueryContext(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("cortex: read schema_migrations: %w", err)
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
