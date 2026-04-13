package cortex

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// PutEntity upserts an entity by (name, type). If an entity with the same
// name and type already exists, its attributes, source, and updated_at are
// updated and the existing ID is set on the passed entity. Otherwise a new
// entity is inserted with a generated ULID.
func (c *Cortex) PutEntity(ctx context.Context, e *Entity) error {
	// Check for existing entity with same name + type.
	var existingID string
	err := c.db.QueryRowContext(ctx,
		"SELECT id FROM entities WHERE name = ? AND type = ?",
		e.Name, e.Type,
	).Scan(&existingID)

	attrsJSON, err2 := json.Marshal(e.Attributes)
	if err2 != nil {
		return fmt.Errorf("cortex: marshal attributes: %w", err2)
	}

	now := time.Now().UTC()

	if err == nil {
		// Entity exists — update.
		_, err = c.db.ExecContext(ctx,
			`UPDATE entities SET attributes = ?, source = ?, updated_at = ? WHERE id = ?`,
			string(attrsJSON), e.Source, now, existingID,
		)
		if err != nil {
			return fmt.Errorf("cortex: update entity: %w", err)
		}
		e.ID = existingID
		e.UpdatedAt = now
		return nil
	}
	if err != sql.ErrNoRows {
		return fmt.Errorf("cortex: check existing entity: %w", err)
	}

	// New entity — insert.
	e.ID = newID()
	e.CreatedAt = now
	e.UpdatedAt = now

	_, err = c.db.ExecContext(ctx,
		`INSERT INTO entities (id, type, name, attributes, source, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.Type, e.Name, string(attrsJSON), e.Source, e.CreatedAt, e.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("cortex: insert entity: %w", err)
	}
	return nil
}

// GetEntity retrieves an entity by ID. Returns an error if not found.
func (c *Cortex) GetEntity(ctx context.Context, id string) (*Entity, error) {
	var e Entity
	var attrsJSON sql.NullString
	err := c.db.QueryRowContext(ctx,
		`SELECT id, type, name, attributes, source, created_at, updated_at
		 FROM entities WHERE id = ?`, id,
	).Scan(&e.ID, &e.Type, &e.Name, &attrsJSON, &e.Source, &e.CreatedAt, &e.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("cortex: entity %q not found", id)
	}
	if err != nil {
		return nil, fmt.Errorf("cortex: get entity: %w", err)
	}

	if attrsJSON.Valid && attrsJSON.String != "" {
		if err := json.Unmarshal([]byte(attrsJSON.String), &e.Attributes); err != nil {
			return nil, fmt.Errorf("cortex: unmarshal attributes: %w", err)
		}
	}
	return &e, nil
}

// FindEntities returns entities matching the given filter. All filter fields
// are optional — an empty filter returns all entities.
func (c *Cortex) FindEntities(ctx context.Context, f EntityFilter) ([]Entity, error) {
	query := `SELECT id, type, name, attributes, source, created_at, updated_at FROM entities`
	var conditions []string
	var args []any

	if f.Type != "" {
		conditions = append(conditions, "type = ?")
		args = append(args, f.Type)
	}
	if f.NameLike != "" {
		conditions = append(conditions, "name LIKE ?")
		args = append(args, f.NameLike)
	}
	if f.Source != "" {
		conditions = append(conditions, "source = ?")
		args = append(args, f.Source)
	}

	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}

	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("cortex: find entities: %w", err)
	}
	defer rows.Close()

	var entities []Entity
	for rows.Next() {
		var e Entity
		var attrsJSON sql.NullString
		if err := rows.Scan(&e.ID, &e.Type, &e.Name, &attrsJSON, &e.Source, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, fmt.Errorf("cortex: scan entity: %w", err)
		}
		if attrsJSON.Valid && attrsJSON.String != "" {
			if err := json.Unmarshal([]byte(attrsJSON.String), &e.Attributes); err != nil {
				return nil, fmt.Errorf("cortex: unmarshal attributes: %w", err)
			}
		}
		entities = append(entities, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("cortex: iterate entities: %w", err)
	}
	return entities, nil
}
