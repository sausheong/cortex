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

	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/model"
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
// and sealed under the user key at rest.
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
	_, err := s.ValidateField(allowPrivate)
	return err
}

// ValidateField is Validate that also names the form field at fault
// ("provider", "base_url", "embed_dims", "embed_base_url" or "embed_api_key").
func (s Settings) ValidateField(allowPrivate bool) (string, error) {
	s = s.WithDefaults()
	if s.Provider != ProviderOpenAI && s.Provider != ProviderAnthropic {
		return "provider", errors.New("provider must be openai-compatible or anthropic")
	}
	if s.BaseURL != "" {
		if err := checkURL(s.BaseURL, allowPrivate); err != nil {
			return "base_url", err
		}
	}
	if s.EmbedBaseURL != "" {
		if err := checkURL(s.EmbedBaseURL, allowPrivate); err != nil {
			return "embed_base_url", err
		}
	}
	if !allowedDims[s.EmbedDims] {
		return "embed_dims", fmt.Errorf("embedding dimensions %d not allowed", s.EmbedDims)
	}
	if s.Provider == ProviderAnthropic && s.EmbedAPIKey == "" {
		return "embed_api_key", errors.New("the anthropic provider needs a separate embedding API key")
	}
	return "", nil
}

// Store is the accounts data layer.
type Store struct {
	DB           *sql.DB
	AllowPrivate bool
	Now          func() time.Time // nil = time.Now

	// Passkey configuration; set once at startup by ConfigurePasskeys.
	wa       *webauthn.WebAuthn
	enrollOn bool

	// staticHash is HashToken(CORTEX_AUTH_TOKEN) when one is configured; it
	// lets ResolveBearer say why a retired static token no longer works.
	staticHash string
}

// SetStaticToken records the operator's static CORTEX_AUTH_TOKEN so that
// presenting it after its owner encrypted yields model.ErrStaticTokenDisabled
// rather than a bare invalid-token answer. Call once at startup.
func (st *Store) SetStaticToken(tok string) {
	if tok == "" {
		st.staticHash = ""
		return
	}
	st.staticHash = HashToken(tok)
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

type settingsRow struct {
	dims      int
	ct        []byte
	updatedAt time.Time
}

type queryer interface {
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

func loadRow(ctx context.Context, q queryer, userID string, forUpdate bool) (*settingsRow, error) {
	query := `SELECT embed_dims, settings_ct, updated_at FROM user_settings WHERE user_id = $1`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	var r settingsRow
	err := q.QueryRowContext(ctx, query, userID).Scan(&r.dims, &r.ct, &r.updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoSettings
	}
	if err != nil {
		return nil, fmt.Errorf("accounts: load settings: %w", err)
	}
	return &r, nil
}

// activeEmbed is the active embedder, a field of the settings blob.
type activeEmbed struct {
	URL   string `json:"url"`
	Key   string `json:"key"`
	Model string `json:"model"`
	Dims  int    `json:"dims"`
}

// settingsBlob is everything of user_settings that settings_ct seals.
type settingsBlob struct {
	Provider     string       `json:"provider"`
	BaseURL      string       `json:"base_url"`
	APIKey       string       `json:"api_key"`
	ChatModel    string       `json:"chat_model"`
	EmbedModel   string       `json:"embed_model"`
	EmbedBaseURL string       `json:"embed_base_url"`
	EmbedAPIKey  string       `json:"embed_key"`
	Active       *activeEmbed `json:"active_embed,omitempty"`
}

const settingsTable, settingsColumn = "user_settings", "settings"

// decode returns the row's settings and active embedder (nil when none is
// recorded). It needs uk (model.ErrLocked otherwise).
func (st *Store) decode(userID string, r *settingsRow, uk *keys.UK) (*Settings, *activeEmbed, error) {
	if uk == nil {
		return nil, nil, model.ErrLocked
	}
	plain, err := uk.Open(userID, settingsTable, userID, settingsColumn, r.ct)
	if err != nil {
		return nil, nil, fmt.Errorf("accounts: open settings: %w", err)
	}
	var b settingsBlob
	if err := json.Unmarshal(plain, &b); err != nil {
		return nil, nil, errors.New("accounts: bad settings")
	}
	return &Settings{Provider: b.Provider, BaseURL: b.BaseURL, APIKey: b.APIKey, ChatModel: b.ChatModel,
		EmbedModel: b.EmbedModel, EmbedDims: r.dims, EmbedBaseURL: b.EmbedBaseURL, EmbedAPIKey: b.EmbedAPIKey,
		UpdatedAt: r.updatedAt}, b.Active, nil
}

// GetSettings returns the user's settings with keys decrypted, or ErrNoSettings.
// uk is the user's key; without it the call fails with model.ErrLocked.
func (st *Store) GetSettings(ctx context.Context, userID string, uk *keys.UK) (*Settings, error) {
	r, err := loadRow(ctx, st.DB, userID, false)
	if err != nil {
		return nil, err
	}
	s, _, err := st.decode(userID, r, uk)
	if err != nil {
		return nil, err
	}
	return s, nil
}

// HasSettings reports whether the user has saved settings, without needing a
// key.
func (st *Store) HasSettings(ctx context.Context, userID string) (bool, error) {
	var n int
	err := st.DB.QueryRowContext(ctx, `SELECT count(*) FROM user_settings WHERE user_id = $1`, userID).Scan(&n)
	return n > 0, err
}

func activeFor(s Settings) activeEmbed {
	key := s.EmbedAPIKey
	if key == "" && s.Provider != ProviderAnthropic {
		key = s.APIKey
	}
	return activeEmbed{URL: s.EffectiveEmbedURL(), Key: key, Model: s.EmbedModel, Dims: s.EmbedDims}
}

// sealBlob seals s and the active embedder.
func sealBlob(userID string, uk *keys.UK, s Settings, active *activeEmbed) ([]byte, error) {
	b, err := json.Marshal(settingsBlob{Provider: s.Provider, BaseURL: s.BaseURL, APIKey: s.APIKey,
		ChatModel: s.ChatModel, EmbedModel: s.EmbedModel, EmbedBaseURL: s.EmbedBaseURL,
		EmbedAPIKey: s.EmbedAPIKey, Active: active})
	if err != nil {
		return nil, err
	}
	return uk.Seal(userID, settingsTable, userID, settingsColumn, b)
}

// writeEncrypted stores everything under uk and leaves only embed_dims
// (metadata) in the clear.
func writeEncrypted(ctx context.Context, tx *sql.Tx, userID string, uk *keys.UK, s Settings, active *activeEmbed, now time.Time) error {
	ct, err := sealBlob(userID, uk, s, active)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO user_settings (user_id, embed_dims, settings_ct, updated_at)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (user_id) DO UPDATE SET embed_dims=$2, settings_ct=$3, updated_at=$4`, userID, s.EmbedDims, ct, now)
	return err
}

// PutSettings validates and saves s. A blank key keeps the stored one. The
// active embedder is promoted in the same transaction when nothing needs
// re-embedding; otherwise needsReembed is true and it is left untouched.
// Settings are sealed under uk (model.ErrLocked without it,
// model.ErrEnrollRequired for a user who has not enrolled).
func (st *Store) PutSettings(ctx context.Context, userID string, s Settings, uk *keys.UK) (needsReembed bool, err error) {
	s = s.WithDefaults()
	state, err := st.encState(ctx, userID)
	if err != nil {
		return false, err
	}
	if state == "none" {
		return false, model.ErrEnrollRequired
	}
	if uk == nil {
		return false, model.ErrLocked
	}
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
	var oldActive *activeEmbed
	if !first {
		prev, act, derr := st.decode(userID, old, uk)
		if derr != nil {
			return false, derr
		}
		oldActive = act
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

	promote := first
	if !first {
		switch {
		case oldActive == nil:
			promote = true
		case oldActive.Model == s.EmbedModel && oldActive.Dims == s.EmbedDims && oldActive.URL == s.EffectiveEmbedURL():
			promote = true
		default:
			var n int
			if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM embeddings WHERE user_id = $1`, userID).Scan(&n); err != nil {
				return false, err
			}
			promote = n == 0
		}
	}
	active := oldActive
	if promote {
		a := activeFor(s)
		active = &a
	}
	if err := writeEncrypted(ctx, tx, userID, uk, s, active, st.now()); err != nil {
		return false, fmt.Errorf("accounts: save settings: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return !promote, nil
}

// PromoteActiveEmbedTx makes the user's desired embedder settings the
// active ones.
func (st *Store) PromoteActiveEmbedTx(ctx context.Context, tx *sql.Tx, userID string, uk *keys.UK) error {
	r, err := loadRow(ctx, tx, userID, true)
	if err != nil {
		return err
	}
	s, _, err := st.decode(userID, r, uk)
	if err != nil {
		return err
	}
	return st.PromoteActiveEmbedSettingsTx(ctx, tx, userID, *s, uk)
}

// PromoteActiveEmbedSettingsTx makes exactly s the active embedder settings,
// regardless of what the user's desired settings are now.
func (st *Store) PromoteActiveEmbedSettingsTx(ctx context.Context, tx *sql.Tx, userID string, s Settings, uk *keys.UK) error {
	r, err := loadRow(ctx, tx, userID, true)
	if err != nil {
		return err
	}
	cur, _, err := st.decode(userID, r, uk)
	if err != nil {
		return err
	}
	a := activeFor(s)
	ct, err := sealBlob(userID, uk, *cur, &a)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `UPDATE user_settings SET settings_ct = $2 WHERE user_id = $1`, userID, ct)
	return err
}

// GetActiveEmbed returns the embedder settings currently in use (the stored
// ones until a re-embed flips). The result is usable as the embed argument
// of Providers.
func (st *Store) GetActiveEmbed(ctx context.Context, userID string, uk *keys.UK) (*Settings, error) {
	r, err := loadRow(ctx, st.DB, userID, false)
	if err != nil {
		return nil, err
	}
	s, a, err := st.decode(userID, r, uk)
	if err != nil {
		return nil, err
	}
	if a == nil {
		key := s.EmbedAPIKey
		if key == "" && s.Provider != ProviderAnthropic {
			key = s.APIKey
		}
		return &Settings{Provider: ProviderOpenAI, BaseURL: s.EffectiveEmbedURL(), APIKey: key, EmbedModel: s.EmbedModel,
			EmbedDims: s.EmbedDims, EmbedBaseURL: s.EffectiveEmbedURL(), EmbedAPIKey: key, UpdatedAt: s.UpdatedAt}, nil
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
