// Package store holds the database plumbing shared by every cortex layer:
// the per-handle state, the Postgres connection wrapper that rewrites '?'
// placeholders, ID generation, scope checks and schema migrations.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/sausheong/cortex/internal/model"
)

// Handle is the per-handle state of a Cortex: the database, the
// configuration, the bound user, and the optional embedding generation and
// connection pins. Derive a new handle by copying the Handle value; the copy
// must apply options to its own Cfg only.
type Handle struct {
	DB     DB
	Cfg    model.Config
	UserID string // the user this handle is bound to
	// PinnedGen, when > 0, pins embedding writes to that generation.
	PinnedGen int
	// ConnID, when set, is stamped on every row this handle writes.
	ConnID string
}

// Logf logs through the configured logger, if any.
func (h *Handle) Logf(format string, args ...any) {
	if h.Cfg.Logf != nil {
		h.Cfg.Logf(format, args...)
	}
}

// NullConn returns the connection ID as a SQL value, NULL when unset.
func (h *Handle) NullConn() any {
	if h.ConnID == "" {
		return nil
	}
	return h.ConnID
}

// Embedder returns the configured embedder, or nil.
func (h *Handle) Embedder() model.Embedder { return h.Cfg.Embedder }

// Close closes the underlying database connection.
func (h *Handle) Close() error {
	if h.DB.DB != nil {
		return h.DB.Close()
	}
	return nil
}

// NullTimePtr converts a sql.NullTime to a *time.Time (nil when not valid).
func NullTimePtr(nt sql.NullTime) *time.Time {
	if !nt.Valid {
		return nil
	}
	t := nt.Time
	return &t
}

// Connect opens and pings the Postgres database at dsn and applies opts to a
// fresh handle bound to the bootstrap user. It does not migrate.
func Connect(dsn string, opts ...model.Option) (*Handle, error) {
	if dsn == "" {
		return nil, fmt.Errorf("cortex: no database URL (set CORTEX_DATABASE_URL or pass --db)")
	}
	dsn = withConnectTimeout(dsn)
	pc, err := pgconn.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("cortex: invalid database URL: %w", err)
	}
	hostPort := net.JoinHostPort(pc.Host, strconv.Itoa(int(pc.Port)))

	sdb, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("cortex: open database: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := sdb.PingContext(ctx); err != nil {
		sdb.Close()
		return nil, fmt.Errorf("cortex: can't reach Postgres at %s: %w", hostPort, err)
	}

	h := &Handle{DB: DB{sdb}, UserID: model.BootstrapUserID}
	for _, o := range opts {
		o(&h.Cfg)
	}
	return h, nil
}

// MigrationContext returns a context bounded by the configured migration
// timeout (60s by default).
func (h *Handle) MigrationContext() (context.Context, context.CancelFunc) {
	mt := h.Cfg.MigrationTimeout
	if mt <= 0 {
		mt = 60 * time.Second
	}
	return context.WithTimeout(context.Background(), mt)
}

// withConnectTimeout adds connect_timeout=5 to dsn when it has none.
func withConnectTimeout(dsn string) string {
	if strings.Contains(dsn, "connect_timeout") {
		return dsn
	}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return dsn
		}
		q := u.Query()
		q.Set("connect_timeout", "5")
		u.RawQuery = q.Encode()
		return u.String()
	}
	return dsn + " connect_timeout=5"
}
