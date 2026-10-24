package tenancy

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/store"
)

const userCols = `id, COALESCE(google_sub, ''), email, name, email_verified, created_at, deleted_at`

func scanUser(sc interface{ Scan(...any) error }) (*model.User, error) {
	var u model.User
	var del sql.NullTime
	if err := sc.Scan(&u.ID, &u.GoogleSub, &u.Email, &u.Name, &u.EmailVerified, &u.CreatedAt, &del); err != nil {
		return nil, err
	}
	u.DeletedAt = store.NullTimePtr(del)
	return &u, nil
}

// CreateUser inserts a user and its person entity (Source "owner"), whose ID
// is stored in users.owner_entity_id.
func (c Ops) CreateUser(ctx context.Context, googleSub, email, name string, emailVerified bool) (*model.User, error) {
	id := "u_" + store.NewID()
	tx, err := c.DB.BeginTx(ctx, nil)
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
func createOwnerEntity(ctx context.Context, tx *store.Tx, userID, name, email string) error {
	display := name
	if display == "" {
		display = email
	}
	if display == "" {
		display = userID
	}
	entID := store.NewID()
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO entities (id, user_id, type, name, source, is_owner) VALUES (?, ?, 'person', ?, 'owner', TRUE)`,
		entID, userID, display); err != nil {
		return fmt.Errorf("cortex: create owner entity: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE users SET owner_entity_id = ? WHERE id = ?`, entID, userID); err != nil {
		return fmt.Errorf("cortex: record owner entity: %w", err)
	}
	return nil
}

func (c Ops) userByID(ctx context.Context, id string) (*model.User, error) {
	u, err := scanUser(c.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("cortex: get user: %w", err)
	}
	return u, nil
}

// UserByGoogleSub returns the user with the given Google subject, or model.ErrNotFound.
func (c Ops) UserByGoogleSub(ctx context.Context, sub string) (*model.User, error) {
	u, err := scanUser(c.DB.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE google_sub = ?`, sub))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, model.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("cortex: get user by sub: %w", err)
	}
	return u, nil
}

// ListUsers returns every user, oldest first.
func (c Ops) ListUsers(ctx context.Context) ([]model.User, error) {
	rows, err := c.DB.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("cortex: list users: %w", err)
	}
	defer rows.Close()
	var out []model.User
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
func (c Ops) BindBootstrap(ctx context.Context, googleSub, email, name string) (*model.User, error) {
	res, err := c.DB.ExecContext(ctx,
		`UPDATE users SET google_sub = ?, email = ?, email_verified = TRUE, name = ?
		 WHERE id = ? AND google_sub IS NULL`,
		googleSub, email, name, model.BootstrapUserID)
	if err != nil {
		return nil, fmt.Errorf("cortex: bind bootstrap: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("cortex: bootstrap user is already bound")
	}
	return c.userByID(ctx, model.BootstrapUserID)
}

// ResolveUserRef resolves an admin reference: a user ID, else a verified
// email. Several matches is an error listing the IDs; none is model.ErrNotFound.
func (c Ops) ResolveUserRef(ctx context.Context, ref string) (*model.User, error) {
	if u, err := c.userByID(ctx, ref); err == nil {
		return u, nil
	} else if !errors.Is(err, model.ErrNotFound) {
		return nil, err
	}
	rows, err := c.DB.QueryContext(ctx,
		`SELECT `+userCols+` FROM users WHERE email = ? AND email_verified AND email <> '' ORDER BY created_at, id`, ref)
	if err != nil {
		return nil, fmt.Errorf("cortex: resolve user: %w", err)
	}
	defer rows.Close()
	var matches []*model.User
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
		urows, err := c.DB.QueryContext(ctx, `SELECT id FROM users WHERE email = ? ORDER BY created_at, id`, ref)
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
		return nil, model.ErrNotFound
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
// or model.ErrNotFound when the user has none.
func (c Ops) OwnerEntityID(ctx context.Context) (string, error) {
	var id sql.NullString
	err := c.DB.QueryRowContext(ctx, `SELECT owner_entity_id FROM users WHERE id = ?`, c.UserID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !id.Valid) {
		return "", model.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("cortex: owner entity: %w", err)
	}
	return id.String, nil
}
