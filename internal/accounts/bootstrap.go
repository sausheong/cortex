package accounts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/oklog/ulid/v2"

	"github.com/sausheong/cortex"
)

const (
	bootstrapMarker  = "bootstrap_migrated"
	tunnelTokenName  = "claude-code (tunnel)"
	defaultChatModel = "gpt-5.4-mini"
)

// MigrateBootstrap seeds the bootstrap user from the legacy environment.
// It is idempotent and one-shot: once the marker is set, later runs return
// at once, so a revoked or deleted tunnel token is never recreated.
func MigrateBootstrap(ctx context.Context, st *Store, env map[string]string) error {
	apiKey := env["OPENAI_API_KEY"]
	authToken := env["CORTEX_AUTH_TOKEN"]
	tx, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(4243)`); err != nil {
		return err
	}
	var marker string
	err = tx.QueryRowContext(ctx, `SELECT value FROM cortex_meta WHERE key = $1`, bootstrapMarker).Scan(&marker)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}

	if authToken != "" && apiKey == "" {
		return errors.New("accounts: CORTEX_AUTH_TOKEN is set but OPENAI_API_KEY is not")
	}
	if err := ensureBootstrapOwner(ctx, tx); err != nil {
		return err
	}
	if apiKey == "" {
		// Nothing to migrate yet; leave the marker unset so a later start can.
		return tx.Commit()
	}

	s := Settings{Provider: ProviderOpenAI, BaseURL: env["OPENAI_BASE_URL"], APIKey: apiKey, ChatModel: env["LLM_MODEL"],
		EmbedModel: env["EMBEDDING_MODEL"], EmbedDims: DefaultEmbedDims,
		EmbedBaseURL: env["EMBEDDING_BASE_URL"], EmbedAPIKey: env["EMBEDDING_API_KEY"]}
	if d, err := strconv.Atoi(strings.TrimSpace(env["EMBEDDING_DIMS"])); err == nil && allowedDims[d] {
		s.EmbedDims = d
	}
	s = s.WithDefaults()
	if s.ChatModel == "" {
		s.ChatModel = defaultChatModel
	}
	keySealed, err := st.seal(s.APIKey)
	if err != nil {
		return err
	}
	embKeySealed, err := st.seal(s.EmbedAPIKey)
	if err != nil {
		return err
	}
	activeSealed, err := st.sealActive(s)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO user_settings (user_id, provider, base_url, api_key_sealed, chat_model,
		embed_model, embed_dims, embed_base_url, embed_key_sealed, active_embed_sealed, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (user_id) DO NOTHING`,
		cortex.BootstrapUserID, s.Provider, s.BaseURL, keySealed, s.ChatModel, s.EmbedModel, s.EmbedDims,
		s.EmbedBaseURL, embKeySealed, activeSealed, st.now()); err != nil {
		return fmt.Errorf("accounts: bootstrap settings: %w", err)
	}
	if authToken != "" {
		conn, err := st.createConnection(ctx, tx, cortex.BootstrapUserID, "agent", tunnelTokenName, "")
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO agent_tokens (id, connection_id, token_hash) VALUES ($1,$2,$3)
			ON CONFLICT (token_hash) DO NOTHING`, "t_"+randHex(12), conn.ID, HashToken(authToken)); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO cortex_meta (key, value) VALUES ($1, 'true') ON CONFLICT (key) DO NOTHING`,
		bootstrapMarker); err != nil {
		return err
	}
	return tx.Commit()
}

// ensureBootstrapOwner sets users.owner_entity_id for the bootstrap user to
// its owner entity, creating one if the legacy database has none.
func ensureBootstrapOwner(ctx context.Context, tx *sql.Tx) error {
	var cur sql.NullString
	var name, email string
	if err := tx.QueryRowContext(ctx, `SELECT owner_entity_id, name, email FROM users WHERE id = $1 FOR UPDATE`,
		cortex.BootstrapUserID).Scan(&cur, &name, &email); err != nil {
		return err
	}
	if cur.Valid && cur.String != "" {
		return nil
	}
	var id string
	err := tx.QueryRowContext(ctx, `SELECT id FROM entities WHERE source = 'owner' AND user_id = $1 AND deleted_at IS NULL
		ORDER BY created_at, id LIMIT 1`, cortex.BootstrapUserID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		display := name
		if display == "" {
			display = email
		}
		if display == "" {
			display = "Owner"
		}
		id = ulid.Make().String()
		if _, err := tx.ExecContext(ctx, `INSERT INTO entities (id, user_id, type, name, source) VALUES ($1,$2,'person',$3,'owner')`,
			id, cortex.BootstrapUserID, display); err != nil {
			return fmt.Errorf("accounts: bootstrap owner entity: %w", err)
		}
	} else if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE users SET owner_entity_id = $2 WHERE id = $1`, cortex.BootstrapUserID, id)
	return err
}
