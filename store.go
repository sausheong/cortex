package cortex

import (
	"context"
	"database/sql"
	"errors"
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

// Cortex is the main knowledge graph store backed by Postgres.
type Cortex struct {
	db     dbConn
	cfg    model.Config
	userID string // the user this handle is bound to
	// pinnedGen, when > 0, pins embedding writes to that generation.
	pinnedGen int
	// connID, when set, is stamped on every row this handle writes.
	connID string
}

// WithConnection returns a copy of the handle whose writes record connID as
// written_by_connection_id.
func (c *Cortex) WithConnection(connID string) *Cortex {
	n := *c
	n.connID = connID
	return &n
}

// ConnectionID is the connection this handle stamps on its writes, "" when none.
func (c *Cortex) ConnectionID() string { return c.connID }

// nullConn returns the connection ID as a SQL value, NULL when unset.
func (c *Cortex) nullConn() any {
	if c.connID == "" {
		return nil
	}
	return c.connID
}

// Open connects to the Postgres database at dsn and applies pending migrations.
// Options are applied before schema setup.
func Open(dsn string, opts ...Option) (*Cortex, error) {
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

	c := &Cortex{db: dbConn{sdb}, userID: BootstrapUserID}
	for _, o := range opts {
		o(&c.cfg)
	}
	mt := c.cfg.MigrationTimeout
	if mt <= 0 {
		mt = 60 * time.Second
	}
	mctx, mcancel := context.WithTimeout(context.Background(), mt)
	defer mcancel()
	if err := c.migrate(mctx); err != nil {
		sdb.Close()
		return nil, err
	}
	if err := c.ensureVectorSchema(mctx); err != nil {
		sdb.Close()
		return nil, err
	}
	return c, nil
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

// Close closes the underlying database connection.
func (c *Cortex) Close() error {
	if c.db.DB != nil {
		return c.db.Close()
	}
	return nil
}

// SetExtractor sets the extractor on the Cortex instance.
func (c *Cortex) SetExtractor(e Extractor) { c.cfg.Extractor = e }

// SetLLM sets the LLM on the Cortex instance.
func (c *Cortex) SetLLM(l LLM) { c.cfg.LLM = l }

// SetEmbedder sets the embedder on the Cortex instance.
func (c *Cortex) SetEmbedder(e Embedder) { c.cfg.Embedder = e }

// GetSyncState returns the last-known sync state for a connector.
func (c *Cortex) GetSyncState(ctx context.Context, connector string) (string, error) {
	var state string
	err := c.db.QueryRowContext(ctx,
		"SELECT state FROM sync_state WHERE user_id = ? AND connector = ?", c.userID, connector,
	).Scan(&state)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("cortex: get sync state: %w", err)
	}
	return state, nil
}

// SetSyncState upserts the sync state for a connector.
func (c *Cortex) SetSyncState(ctx context.Context, connector, state string) error {
	_, err := c.db.ExecContext(ctx,
		`INSERT INTO sync_state (user_id, connector, state, updated_at)
		 VALUES (?, ?, ?, now())
		 ON CONFLICT(user_id, connector) DO UPDATE SET state = excluded.state, updated_at = excluded.updated_at`,
		c.userID, connector, state,
	)
	if err != nil {
		return fmt.Errorf("cortex: set sync state: %w", err)
	}
	return nil
}

func isNoRows(err error) bool { return errors.Is(err, sql.ErrNoRows) }
