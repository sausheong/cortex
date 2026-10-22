package accounts

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"
)

// Errors returned by the store.
var (
	ErrNoSettings   = errors.New("accounts: no provider settings")
	ErrNoProvider   = errors.New("set up your AI provider at https://cortex.sausheong.dev/settings")
	ErrNotFound     = errors.New("accounts: not found")
	ErrInvalidToken = errors.New("accounts: invalid token")
)

const (
	ProviderOpenAI    = "openai-compatible"
	ProviderAnthropic = "anthropic"

	DefaultBaseURL    = "https://api.openai.com/v1"
	DefaultEmbedModel = "text-embedding-3-small"
	DefaultEmbedDims  = 512
)

var allowedDims = map[int]bool{256: true, 384: true, 512: true, 768: true, 1024: true, 1536: true, 2048: true, 3072: true}

// Settings are a user's AI provider settings. Keys are plaintext in memory
// and sealed at rest.
type Settings struct {
	Provider, BaseURL, APIKey, ChatModel, EmbedModel string
	EmbedDims                                        int
	EmbedBaseURL, EmbedAPIKey                        string
	UpdatedAt                                        time.Time
}

func maskKey(k string) string {
	if k == "" {
		return ""
	}
	if len(k) <= 4 {
		return "••••"
	}
	return "••••" + k[len(k)-4:]
}

// Masked returns the settings with keys shown as "••••" + last 4 characters.
func (s Settings) Masked() Settings {
	s.APIKey = maskKey(s.APIKey)
	s.EmbedAPIKey = maskKey(s.EmbedAPIKey)
	return s
}

// WithDefaults fills empty fields with the documented defaults.
func (s Settings) WithDefaults() Settings {
	if s.Provider == "" {
		s.Provider = ProviderOpenAI
	}
	if s.BaseURL == "" && s.Provider == ProviderOpenAI {
		s.BaseURL = DefaultBaseURL
	}
	if s.EmbedModel == "" {
		s.EmbedModel = DefaultEmbedModel
	}
	if s.EmbedDims == 0 {
		s.EmbedDims = DefaultEmbedDims
	}
	return s
}

// EffectiveEmbedURL is the embedding base URL, falling back to the chat one.
func (s Settings) EffectiveEmbedURL() string {
	if s.EmbedBaseURL != "" {
		return s.EmbedBaseURL
	}
	if s.Provider == ProviderAnthropic {
		return DefaultBaseURL
	}
	return s.BaseURL
}

func checkURL(raw string, allowPrivate bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("base URL is not valid")
	}
	if allowPrivate {
		if u.Scheme != "http" && u.Scheme != "https" {
			return errors.New("base URL must be http or https")
		}
		return nil
	}
	if u.Scheme != "https" {
		return errors.New("base URL must use https")
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && blockedIP(ip) {
		return errors.New("base URL must be a public address")
	}
	return nil
}

// Validate enforces the provider URL policy and the dims allowlist. Empty
// fields are checked as their defaults.
func (s Settings) Validate(allowPrivate bool) error {
	s = s.WithDefaults()
	if s.Provider != ProviderOpenAI && s.Provider != ProviderAnthropic {
		return errors.New("provider must be openai-compatible or anthropic")
	}
	if s.BaseURL != "" {
		if err := checkURL(s.BaseURL, allowPrivate); err != nil {
			return err
		}
	}
	if s.EmbedBaseURL != "" {
		if err := checkURL(s.EmbedBaseURL, allowPrivate); err != nil {
			return err
		}
	}
	if !allowedDims[s.EmbedDims] {
		return fmt.Errorf("embedding dimensions %d not allowed", s.EmbedDims)
	}
	if s.Provider == ProviderAnthropic && s.EmbedAPIKey == "" {
		return errors.New("the anthropic provider needs a separate embedding API key")
	}
	return nil
}

// Store is the accounts data layer.
type Store struct {
	DB           *sql.DB
	Sealer       *Sealer
	AllowPrivate bool
	Now          func() time.Time // nil = time.Now
}

func (st *Store) now() time.Time {
	if st.Now != nil {
		return st.Now().UTC()
	}
	return time.Now().UTC()
}

func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func (st *Store) seal(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	return st.Sealer.Seal(v)
}

func (st *Store) open(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	return st.Sealer.Open(v)
}

type settingsRow struct {
	provider, baseURL, keySealed, chatModel, embedModel string
	dims                                                int
	embedURL, embedKeySealed, activeSealed              string
	updatedAt                                           time.Time
}

type queryer interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

func loadRow(ctx context.Context, q queryer, userID string, forUpdate bool) (*settingsRow, error) {
	query := `SELECT provider, base_url, api_key_sealed, chat_model, embed_model, embed_dims,
		embed_base_url, embed_key_sealed, active_embed_sealed, updated_at FROM user_settings WHERE user_id = $1`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var r settingsRow
	err := q.QueryRowContext(ctx, query, userID).Scan(&r.provider, &r.baseURL, &r.keySealed, &r.chatModel,
		&r.embedModel, &r.dims, &r.embedURL, &r.embedKeySealed, &r.activeSealed, &r.updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSettings
	}
	if err != nil {
		return nil, fmt.Errorf("accounts: load settings: %w", err)
	}
	return &r, nil
}

func (st *Store) rowSettings(r *settingsRow) (*Settings, error) {
	key, err := st.open(r.keySealed)
	if err != nil {
		return nil, err
	}
	ekey, err := st.open(r.embedKeySealed)
	if err != nil {
		return nil, err
	}
	return &Settings{Provider: r.provider, BaseURL: r.baseURL, APIKey: key, ChatModel: r.chatModel,
		EmbedModel: r.embedModel, EmbedDims: r.dims, EmbedBaseURL: r.embedURL, EmbedAPIKey: ekey,
		UpdatedAt: r.updatedAt}, nil
}

// GetSettings returns the user's settings with keys decrypted, or ErrNoSettings.
func (st *Store) GetSettings(ctx context.Context, userID string) (*Settings, error) {
	r, err := loadRow(ctx, st.DB, userID, false)
	if err != nil {
		return nil, err
	}
	return st.rowSettings(r)
}

// activeEmbed is what is sealed in active_embed_sealed.
type activeEmbed struct {
	URL   string `json:"url"`
	Key   string `json:"key"`
	Model string `json:"model"`
	Dims  int    `json:"dims"`
}

func (st *Store) sealActive(s Settings) (string, error) {
	key := s.EmbedAPIKey
	if key == "" && s.Provider != ProviderAnthropic {
		key = s.APIKey
	}
	b, _ := json.Marshal(activeEmbed{URL: s.EffectiveEmbedURL(), Key: key, Model: s.EmbedModel, Dims: s.EmbedDims})
	return st.Sealer.Seal(string(b))
}

func (st *Store) openActive(sealed string) (*activeEmbed, error) {
	plain, err := st.Sealer.Open(sealed)
	if err != nil {
		return nil, err
	}
	var a activeEmbed
	if err := json.Unmarshal([]byte(plain), &a); err != nil {
		return nil, errors.New("accounts: bad active embedder")
	}
	return &a, nil
}

// PutSettings validates and saves s. A blank key keeps the stored one. The
// active embedder is promoted in the same transaction when nothing needs
// re-embedding; otherwise needsReembed is true and it is left untouched.
func (st *Store) PutSettings(ctx context.Context, userID string, s Settings) (needsReembed bool, err error) {
	s = s.WithDefaults()
	tx, err := st.DB.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	old, err := loadRow(ctx, tx, userID, true)
	first := errors.Is(err, ErrNoSettings)
	if err != nil && !first {
		return false, err
	}
	var prev *Settings
	if !first {
		if prev, err = st.rowSettings(old); err != nil {
			return false, err
		}
		if s.APIKey == "" {
			s.APIKey = prev.APIKey
		}
		if s.EmbedAPIKey == "" {
			s.EmbedAPIKey = prev.EmbedAPIKey
		}
	}
	if err := s.Validate(st.AllowPrivate); err != nil {
		return false, err
	}
	keySealed, err := st.seal(s.APIKey)
	if err != nil {
		return false, err
	}
	embKeySealed, err := st.seal(s.EmbedAPIKey)
	if err != nil {
		return false, err
	}

	promote := first
	if !first {
		active, aerr := st.openActive(old.activeSealed)
		switch {
		case old.activeSealed == "" || aerr != nil:
			promote = true
		case active.Model == s.EmbedModel && active.Dims == s.EmbedDims && active.URL == s.EffectiveEmbedURL():
			promote = true
		default:
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM embeddings WHERE user_id = $1`, userID).Scan(&n); err != nil {
				return false, err
			}
			promote = n == 0
		}
	}
	now := st.now()
	if first {
		_, err = tx.ExecContext(ctx, `INSERT INTO user_settings (user_id, provider, base_url, api_key_sealed, chat_model,
			embed_model, embed_dims, embed_base_url, embed_key_sealed, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			userID, s.Provider, s.BaseURL, keySealed, s.ChatModel, s.EmbedModel, s.EmbedDims, s.EmbedBaseURL, embKeySealed, now)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE user_settings SET provider=$2, base_url=$3, api_key_sealed=$4, chat_model=$5,
			embed_model=$6, embed_dims=$7, embed_base_url=$8, embed_key_sealed=$9, updated_at=$10 WHERE user_id=$1`,
			userID, s.Provider, s.BaseURL, keySealed, s.ChatModel, s.EmbedModel, s.EmbedDims, s.EmbedBaseURL, embKeySealed, now)
	}
	if err != nil {
		return false, fmt.Errorf("accounts: save settings: %w", err)
	}
	if promote {
		if err := st.PromoteActiveEmbedTx(ctx, tx, userID); err != nil {
			return false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return !promote, nil
}

// PromoteActiveEmbedTx makes the user's desired embedder settings the
// active ones.
func (st *Store) PromoteActiveEmbedTx(ctx context.Context, tx *sql.Tx, userID string) error {
	r, err := loadRow(ctx, tx, userID, true)
	if err != nil {
		return err
	}
	s, err := st.rowSettings(r)
	if err != nil {
		return err
	}
	return st.PromoteActiveEmbedSettingsTx(ctx, tx, userID, *s)
}

// PromoteActiveEmbedSettingsTx makes exactly s the active embedder settings,
// regardless of what the user's desired settings are now.
func (st *Store) PromoteActiveEmbedSettingsTx(ctx context.Context, tx *sql.Tx, userID string, s Settings) error {
	sealed, err := st.sealActive(s)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE user_settings SET active_embed_sealed = $2 WHERE user_id = $1`, userID, sealed)
	return err
}

// GetActiveEmbed returns the embedder settings currently in use (the stored
// ones until a re-embed flips). The result is usable as the embed argument
// of Providers.
func (st *Store) GetActiveEmbed(ctx context.Context, userID string) (*Settings, error) {
	r, err := loadRow(ctx, st.DB, userID, false)
	if err != nil {
		return nil, err
	}
	if r.activeSealed == "" {
		s, err := st.rowSettings(r)
		if err != nil {
			return nil, err
		}
		key := s.EmbedAPIKey
		if key == "" && s.Provider != ProviderAnthropic {
			key = s.APIKey
		}
		return &Settings{Provider: ProviderOpenAI, BaseURL: s.EffectiveEmbedURL(), APIKey: key, EmbedModel: s.EmbedModel,
			EmbedDims: s.EmbedDims, EmbedBaseURL: s.EffectiveEmbedURL(), EmbedAPIKey: key, UpdatedAt: s.UpdatedAt}, nil
	}
	a, err := st.openActive(r.activeSealed)
	if err != nil {
		return nil, err
	}
	return &Settings{Provider: ProviderOpenAI, BaseURL: a.URL, APIKey: a.Key, EmbedModel: a.Model, EmbedDims: a.Dims,
		EmbedBaseURL: a.URL, EmbedAPIKey: a.Key, UpdatedAt: r.updatedAt}, nil
}

// ActiveGeneration returns users.active_embedding_generation.
func (st *Store) ActiveGeneration(ctx context.Context, userID string) (int, error) {
	var g int
	err := st.DB.QueryRowContext(ctx, `SELECT active_embedding_generation FROM users WHERE id = $1`, userID).Scan(&g)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return g, err
}
