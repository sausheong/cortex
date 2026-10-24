package cortex

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// UserID returns the ID of the user this handle is bound to.
func (c *Cortex) UserID() string { return c.userID }

// ForUser returns a handle bound to another user. It shares the database
// connection but has its own configuration; opts override the LLM, embedder
// and extractor.
func (c *Cortex) ForUser(id string, opts ...Option) *Cortex {
	n := *c
	n.userID = id
	for _, o := range opts {
		o(&n.cfg)
	}
	return &n
}

const userCols = `id, COALESCE(google_sub, ''), email, name, email_verified, created_at, deleted_at`

func scanUser(sc interface{ Scan(...any) error }) (*User, error) {
	var u User
	var del sql.NullTime
	if err := sc.Scan(&u.ID, &u.GoogleSub, &u.Email, &u.Name, &u.EmailVerified, &u.CreatedAt, &del); err != nil {
		return nil, err
	}
	u.DeletedAt = nullTimePtr(del)
	return &u, nil
}

// CreateUser inserts a user and its person entity (Source "owner"), whose ID
// is stored in users.owner_entity_id.
func (c *Cortex) CreateUser(ctx context.Context, googleSub, email, name string, emailVerified bool) (*User, error) {
	id := "u_" + newID()
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("cortex: begin create user: %w", err)
	}
	defer tx.Rollback()
	var sub any
	if googleSub != "" {
		sub = googleSub
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO users (id, google_sub, email, email_verified, name) VALUES (?, ?, ?, ?, ?)`,
		id, sub, email, emailVerified, name); err != nil {
		return nil, fmt.Errorf("cortex: create user: %w", err)
	}
	if err := createOwnerEntity(ctx, tx, id, name, email); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("cortex: commit create user: %w", err)
	}
	return c.userByID(ctx, id)
}

// createOwnerEntity creates the user's person entity and records it.
func createOwnerEntity(ctx context.Context, tx *dbTx, userID, name, email string) error {
	display := name
	if display == "" {
		display = email
	}
	if display == "" {
		display = userID
	}
	entID := newID()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO entities (id, user_id, type, name, source) VALUES (?, ?, 'person', ?, 'owner')`,
		entID, userID, display); err != nil {
		return fmt.Errorf("cortex: create owner entity: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET owner_entity_id = ? WHERE id = ?`, entID, userID); err != nil {
		return fmt.Errorf("cortex: record owner entity: %w", err)
	}
	return nil
}

func (c *Cortex) userByID(ctx context.Context, id string) (*User, error) {
	u, err := scanUser(c.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("cortex: get user: %w", err)
	}
	return u, nil
}

// UserByGoogleSub returns the user with the given Google subject, or ErrNotFound.
func (c *Cortex) UserByGoogleSub(ctx context.Context, sub string) (*User, error) {
	u, err := scanUser(c.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE google_sub = ?`, sub))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("cortex: get user by sub: %w", err)
	}
	return u, nil
}

// ListUsers returns every user, oldest first.
func (c *Cortex) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := c.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("cortex: list users: %w", err)
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("cortex: scan user: %w", err)
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// BindBootstrap attaches a Google identity to the bootstrap user, keeping
// its existing data. It fails if the bootstrap user is already bound.
func (c *Cortex) BindBootstrap(ctx context.Context, googleSub, email, name string) (*User, error) {
	res, err := c.db.ExecContext(ctx,
		`UPDATE users SET google_sub = ?, email = ?, email_verified = TRUE, name = ?
		 WHERE id = ? AND google_sub IS NULL`,
		googleSub, email, name, BootstrapUserID)
	if err != nil {
		return nil, fmt.Errorf("cortex: bind bootstrap: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("cortex: bootstrap user is already bound")
	}
	return c.userByID(ctx, BootstrapUserID)
}

// ResolveUserRef resolves an admin reference: a user ID, else a verified
// email. Several matches is an error listing the IDs; none is ErrNotFound.
func (c *Cortex) ResolveUserRef(ctx context.Context, ref string) (*User, error) {
	if u, err := c.userByID(ctx, ref); err == nil {
		return u, nil
	} else if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	rows, err := c.db.QueryContext(ctx,
		`SELECT `+userCols+` FROM users WHERE email = ? AND email_verified AND email <> '' ORDER BY created_at, id`, ref)
	if err != nil {
		return nil, fmt.Errorf("cortex: resolve user: %w", err)
	}
	defer rows.Close()
	var matches []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		matches = append(matches, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	switch len(matches) {
	case 0:
		var ids []string
		urows, err := c.db.QueryContext(ctx, `SELECT id FROM users WHERE email = ? ORDER BY created_at, id`, ref)
		if err != nil {
			return nil, fmt.Errorf("cortex: resolve user: %w", err)
		}
		defer urows.Close()
		for urows.Next() {
			var id string
			if err := urows.Scan(&id); err != nil {
				return nil, err
			}
			ids = append(ids, id)
		}
		if len(ids) > 0 {
			return nil, fmt.Errorf("cortex: %q matches no verified account; use one of the user IDs: %s", ref, strings.Join(ids, ", "))
		}
		return nil, ErrNotFound
	case 1:
		return matches[0], nil
	}
	ids := make([]string, len(matches))
	for i, u := range matches {
		ids[i] = u.ID
	}
	return nil, fmt.Errorf("cortex: %q is ambiguous; use one of the user IDs: %s", ref, strings.Join(ids, ", "))
}

// OwnerEntityID returns the ID of the handle's user's owner (person) entity,
// or ErrNotFound when the user has none.
func (c *Cortex) OwnerEntityID(ctx context.Context) (string, error) {
	var id sql.NullString
	err := c.db.QueryRowContext(ctx, `SELECT owner_entity_id FROM users WHERE id = ?`, c.userID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !id.Valid) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("cortex: owner entity: %w", err)
	}
	return id.String, nil
}
