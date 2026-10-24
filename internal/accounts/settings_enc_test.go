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
	e.st.DB.QueryRowContext(ctx, `SELECT settings_ct FROM user_settings WHERE user_id = $1`, uid).Scan(&ct)
	if len(ct) == 0 {
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

// The only plain column left on user_settings besides bookkeeping is embed_dims.
func TestSettingsOnlyEmbedDimsStaysPlain(t *testing.T) {
	e := newEnv(t)
	ctx := t.Context()
	uid, uk := encUser(t, e, "enc2", "encrypted")
	s := encSettings()
	s.EmbedDims = 768
	if _, err := e.st.PutSettings(ctx, uid, s, uk); err != nil {
		t.Fatal(err)
	}
	rows, err := e.st.DB.QueryContext(ctx, `SELECT column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'user_settings' ORDER BY column_name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		rows.Scan(&c)
		cols = append(cols, c)
	}
	if got, want := strings.Join(cols, ","), "embed_dims,settings_ct,updated_at,user_id"; got != want {
		t.Fatalf("user_settings columns = %s, want %s", got, want)
	}
	var dims int
	e.st.DB.QueryRowContext(ctx, `SELECT embed_dims FROM user_settings WHERE user_id = $1`, uid).Scan(&dims)
	if dims != 768 {
		t.Fatalf("embed_dims = %d, want 768", dims)
	}
	if _, err := e.st.PutSettings(ctx, e.user(t, "nokey"), s, nil); !errors.Is(err, model.ErrEnrollRequired) {
		t.Fatalf("unenrolled put = %v, want ErrEnrollRequired", err)
	}
}
