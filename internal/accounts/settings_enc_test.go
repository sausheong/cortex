package accounts

import (
	"errors"
	"strings"
	"testing"

	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/model"
)

func encUser(t *testing.T, e *env, sub, state string) (string, *keys.UK) {
	t.Helper()
	uid := e.user(t, sub)
	if _, err := e.st.DB.ExecContext(t.Context(), `UPDATE users SET encryption_state = $2 WHERE id = $1`, uid, state); err != nil {
		t.Fatal(err)
	}
	uk, err := keys.NewUK()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(uk.Zero)
	return uid, uk
}

func encSettings() Settings {
	return Settings{Provider: ProviderOpenAI, BaseURL: "https://llm.secret-host.example/v1", APIKey: "sk-SECRETKEY-1234",
		ChatModel: "secret-chat-model", EmbedModel: "secret-embed-model", EmbedDims: 512,
		EmbedBaseURL: "https://emb.secret-host.example/v1", EmbedAPIKey: "sk-EMBEDSECRET-5678"}
}

func TestSettingsEncryptedAtRest(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	uid, uk := encUser(t, e, "enc1", "encrypted")
	if _, err := e.st.PutSettings(ctx, uid, encSettings(), uk); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := e.st.DB.QueryRowContext(ctx, `SELECT to_jsonb(t)::text FROM user_settings t WHERE user_id = $1`, uid).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"secret-host", "SECRETKEY", "EMBEDSECRET", "secret-chat", "secret-embed", "openai-compatible"} {
		if strings.Contains(raw, leak) {
			t.Fatalf("row leaks %q: %s", leak, raw)
		}
	}
	var ct []byte
	var enc bool
	e.st.DB.QueryRowContext(ctx, `SELECT settings_ct, enc FROM user_settings WHERE user_id = $1`, uid).Scan(&ct, &enc)
	if !enc || len(ct) == 0 {
		t.Fatal("not stored encrypted")
	}
	if _, err := uk.Open(uid, "user_settings", uid, "settings", ct); err != nil {
		t.Fatalf("ct does not open with the UK: %v", err)
	}
	got, err := e.st.GetSettings(ctx, uid, uk)
	want := encSettings()
	if err != nil || got.APIKey != want.APIKey || got.BaseURL != want.BaseURL || got.EmbedAPIKey != want.EmbedAPIKey ||
		got.EmbedBaseURL != want.EmbedBaseURL || got.ChatModel != want.ChatModel || got.EmbedDims != 512 {
		t.Fatalf("round trip: %+v %v", got, err)
	}
	act, err := e.st.GetActiveEmbed(ctx, uid, uk)
	if err != nil || act.EmbedModel != "secret-embed-model" || act.EmbedAPIKey != want.EmbedAPIKey || act.BaseURL != want.EmbedBaseURL {
		t.Fatalf("active: %+v %v", act, err)
	}
	// A blank key on update keeps the stored one.
	s := want
	s.APIKey, s.EmbedAPIKey = "", ""
	if _, err := e.st.PutSettings(ctx, uid, s, uk); err != nil {
		t.Fatal(err)
	}
	if got, _ := e.st.GetSettings(ctx, uid, uk); got.APIKey != want.APIKey {
		t.Fatalf("blank key not kept: %+v", got)
	}
}

func TestSettingsPlainColumnsBlankedIncludingEmbedBaseURL(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	// A migrating user with a plaintext row, then a save under the UK.
	uid, uk := encUser(t, e, "enc2", "migrating")
	if _, err := e.st.DB.ExecContext(ctx, `UPDATE users SET encryption_state = 'none' WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.PutSettings(ctx, uid, encSettings(), nil); err != nil {
		t.Fatal(err)
	}
	e.st.DB.ExecContext(ctx, `UPDATE users SET encryption_state = 'migrating' WHERE id = $1`, uid)
	// Dual read: plaintext row readable with or without the key.
	if got, err := e.st.GetSettings(ctx, uid, nil); err != nil || got.APIKey != "sk-SECRETKEY-1234" {
		t.Fatalf("migrating plaintext read: %+v %v", got, err)
	}
	s := encSettings()
	s.EmbedDims = 768
	if _, err := e.st.PutSettings(ctx, uid, s, uk); err != nil {
		t.Fatal(err)
	}
	var p, b, k, cm, em, eb, ek, ae string
	var dims int
	err := e.st.DB.QueryRowContext(ctx, `SELECT provider, base_url, api_key_sealed, chat_model, embed_model, embed_base_url,
		embed_key_sealed, active_embed_sealed, embed_dims FROM user_settings WHERE user_id = $1`, uid).Scan(&p, &b, &k, &cm, &em, &eb, &ek, &ae, &dims)
	if err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]string{"provider": p, "base_url": b, "api_key_sealed": k, "chat_model": cm,
		"embed_model": em, "embed_base_url": eb, "embed_key_sealed": ek, "active_embed_sealed": ae} {
		if v != "" {
			t.Fatalf("column %s not blanked: %q", name, v)
		}
	}
	if dims != 768 {
		t.Fatalf("embed_dims = %d, want plain 768", dims)
	}
	if got, err := e.st.GetSettings(ctx, uid, uk); err != nil || got.EmbedBaseURL != "https://emb.secret-host.example/v1" || got.EmbedDims != 768 {
		t.Fatalf("read back: %+v %v", got, err)
	}
	// The old plaintext active embedder carried across the conversion.
	if act, err := e.st.GetActiveEmbed(ctx, uid, uk); err != nil || act.EmbedModel == "" {
		t.Fatalf("active lost: %+v %v", act, err)
	}
}

func TestSettingsWrongKeyFails(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	uid, uk := encUser(t, e, "enc3", "encrypted")
	if _, err := e.st.PutSettings(ctx, uid, encSettings(), uk); err != nil {
		t.Fatal(err)
	}
	other, _ := keys.NewUK()
	defer other.Zero()
	if _, err := e.st.GetSettings(ctx, uid, other); !errors.Is(err, keys.ErrDecrypt) {
		t.Fatalf("wrong key GetSettings = %v", err)
	}
	if _, err := e.st.GetActiveEmbed(ctx, uid, other); err == nil {
		t.Fatal("wrong key GetActiveEmbed succeeded")
	}
	if _, err := e.st.PutSettings(ctx, uid, encSettings(), other); err == nil {
		t.Fatal("wrong key PutSettings succeeded")
	}
}

func TestSettingsLockedErrors(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	uid, uk := encUser(t, e, "enc4", "encrypted")
	if _, err := e.st.PutSettings(ctx, uid, encSettings(), nil); !errors.Is(err, model.ErrLocked) {
		t.Fatalf("keyless put = %v", err)
	}
	if _, err := e.st.PutSettings(ctx, uid, encSettings(), uk); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.GetSettings(ctx, uid, nil); !errors.Is(err, model.ErrLocked) {
		t.Fatalf("keyless get = %v", err)
	}
	if _, err := e.st.GetActiveEmbed(ctx, uid, nil); !errors.Is(err, model.ErrLocked) {
		t.Fatalf("keyless active = %v", err)
	}
	if ok, err := e.st.HasSettings(ctx, uid); err != nil || !ok {
		t.Fatalf("HasSettings = %v %v", ok, err)
	}
}

func TestPutSettingsDetectsEmbedChangeUnderUK(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	uid, uk := encUser(t, e, "enc5", "encrypted")
	if need, err := e.st.PutSettings(ctx, uid, encSettings(), uk); err != nil || need {
		t.Fatalf("first: %v %v", need, err)
	}
	// Stored embeddings make an embedder change need a re-embed.
	insertEmbedding(t, e, uid)
	s := encSettings()
	s.EmbedModel = "another-model"
	need, err := e.st.PutSettings(ctx, uid, s, uk)
	if err != nil || !need {
		t.Fatalf("change with embeddings: need=%v err=%v", need, err)
	}
	act, _ := e.st.GetActiveEmbed(ctx, uid, uk)
	if act.EmbedModel != "secret-embed-model" {
		t.Fatalf("active moved before re-embed: %+v", act)
	}
	// Promote exactly the new settings, as the reembed job does.
	tx, _ := e.st.DB.BeginTx(ctx, nil)
	if err := e.st.PromoteActiveEmbedSettingsTx(ctx, tx, uid, s, uk); err != nil {
		t.Fatal(err)
	}
	tx.Commit()
	if act, _ = e.st.GetActiveEmbed(ctx, uid, uk); act.EmbedModel != "another-model" {
		t.Fatalf("not promoted: %+v", act)
	}
	if need, err := e.st.PutSettings(ctx, uid, s, uk); err != nil || need {
		t.Fatalf("same settings after flip: %v %v", need, err)
	}
}
