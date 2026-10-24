package accounts

import (
	"context"
	"database/sql"
	"errors"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/model"
	"net/http"

	oai "github.com/sashabaranov/go-openai"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/extractor/deterministic"
	"github.com/sausheong/cortex/extractor/hybrid"
	"github.com/sausheong/cortex/extractor/llmext"
	anthropicllm "github.com/sausheong/cortex/llm/anthropic"
	oaillm "github.com/sausheong/cortex/llm/openai"
)

// allowsPrivate reports whether hc is a SafeClient built with allowPrivate.
func allowsPrivate(hc *http.Client) bool {
	if hc == nil {
		return false
	}
	ct, ok := hc.Transport.(*capTransport)
	return ok && !ct.requireHTTPS
}

// dimEmbedder fails when the provider returns vectors of the wrong length.
type dimEmbedder struct {
	inner cortex.Embedder
	dims  int
}

func (d dimEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	vecs, err := d.inner.Embed(ctx, texts)
	if err != nil {
		return nil, ProviderError(err)
	}
	if len(vecs) != len(texts) {
		return nil, ErrProviderUnexpected
	}
	for _, v := range vecs {
		if len(v) != d.dims {
			return nil, DimsMismatchError{Got: len(v), Want: d.dims}
		}
	}
	return vecs, nil
}

func (d dimEmbedder) Dimensions() int { return d.dims }

// Providers builds the chat LLM, the embedder and the hybrid extractor from
// settings. hc is the chat client; the embedder gets its own SafeClient with
// the embedding body cap (private addresses allowed only if hc allows them).
// Callers outside the resolver pass the same row for chat and embed.
func Providers(chat, embed *Settings, hc *http.Client) (cortex.LLM, cortex.Embedder, cortex.Extractor, error) {
	if chat == nil || embed == nil {
		return nil, nil, nil, ErrNoProvider
	}
	c := chat.WithDefaults()
	e := embed.WithDefaults()
	if hc == nil {
		hc = SafeClient(false, ChatBodyCap)
	}
	if c.APIKey == "" {
		return nil, nil, nil, ErrNoProvider
	}

	var llm cortex.LLM
	if c.Provider == ProviderAnthropic {
		opts := []anthropicllm.LLMOption{anthropicllm.WithHTTPClient(hc)}
		if c.BaseURL != "" {
			opts = append(opts, anthropicllm.WithBaseURL(c.BaseURL))
		}
		if c.ChatModel != "" {
			opts = append(opts, anthropicllm.WithModel(c.ChatModel))
		}
		llm = anthropicllm.NewLLM(c.APIKey, opts...)
	} else {
		opts := []oaillm.LLMOption{oaillm.WithHTTPClient(hc)}
		if c.BaseURL != "" {
			opts = append(opts, oaillm.WithBaseURL(c.BaseURL))
		}
		if c.ChatModel != "" {
			opts = append(opts, oaillm.WithModel(c.ChatModel))
		}
		llm = oaillm.NewLLM(c.APIKey, opts...)
	}

	ekey := e.EmbedAPIKey
	if ekey == "" && e.Provider != ProviderAnthropic {
		ekey = e.APIKey
	}
	if ekey == "" {
		return nil, nil, nil, ErrNoProvider
	}
	eopts := []oaillm.EmbedderOption{
		oaillm.WithEmbeddingModel(oai.EmbeddingModel(e.EmbedModel), e.EmbedDims),
		oaillm.WithEmbedderHTTPClient(SafeClient(allowsPrivate(hc), EmbedBodyCap)),
	}
	if u := e.EffectiveEmbedURL(); u != "" {
		eopts = append(eopts, oaillm.WithEmbedderBaseURL(u))
	}
	emb := dimEmbedder{inner: oaillm.NewEmbedder(ekey, eopts...), dims: e.EmbedDims}
	ext := hybrid.New(deterministic.New(), llmext.New(llm))
	return llm, emb, ext, nil
}

// TestProvider makes one chat call and one embed call and checks the
// embedding length equals EmbedDims. Errors carry only fixed text.
func TestProvider(ctx context.Context, s Settings, hc *http.Client) error {
	llm, emb, _, err := Providers(&s, &s, hc)
	if err != nil {
		return err
	}
	if _, err := llm.Summarize(ctx, []string{"ping"}); err != nil {
		return ProviderError(err)
	}
	if _, err := emb.Embed(ctx, []string{"ping"}); err != nil {
		return ProviderError(err)
	}
	return nil
}

// pinned is the optional capability of *cortex.Cortex that scopes writes to
// a connection and an embedding generation.
type connPinner interface {
	WithConnection(connID string) *cortex.Cortex
}
type genPinner interface {
	WithGeneration(g int) *cortex.Cortex
}

// ResolveUser is the one factory for a per-user handle: chat settings from
// GetSettings, the embedder from GetActiveEmbed, then
// root.ForUser(...).WithConnection(connID).WithGeneration(active).
func (st *Store) ResolveUser(ctx context.Context, root *cortex.Cortex, userID, connID string, uk *keys.UK) (*cortex.Cortex, error) {
	state, err := st.encState(ctx, userID)
	if err != nil {
		return nil, err
	}
	if state == "none" {
		return nil, model.ErrEnrollRequired
	}
	if uk == nil {
		return nil, model.ErrLocked
	}
	chat, err := st.GetSettings(ctx, userID, uk)
	if errors.Is(err, ErrNoSettings) {
		return nil, ErrNoProvider
	}
	if err != nil {
		return nil, err
	}
	embed, err := st.GetActiveEmbed(ctx, userID, uk)
	if err != nil {
		return nil, err
	}
	hc := SafeClient(st.AllowPrivate, ChatBodyCap)
	llm, emb, ext, err := Providers(chat, embed, hc)
	if err != nil {
		return nil, ErrNoProvider
	}
	active, err := st.ActiveGeneration(ctx, userID)
	if err != nil {
		return nil, err
	}
	cx := root.ForUser(userID, cortex.WithLLM(llm), cortex.WithEmbedder(emb), cortex.WithExtractor(ext))
	cp, ok1 := any(cx).(connPinner)
	if !ok1 {
		return nil, errNeedPinning
	}
	cx = cp.WithConnection(connID)
	gp, ok2 := any(cx).(genPinner)
	if !ok2 {
		return nil, errNeedPinning
	}
	cx = gp.WithGeneration(active)
	return cx.WithKey(uk, state), nil
}

// encState reads users.encryption_state; an unknown user counts as "none"
// so the existing no-settings errors still apply.
func (st *Store) encState(ctx context.Context, userID string) (string, error) {
	var state string
	err := st.DB.QueryRowContext(ctx, `SELECT encryption_state FROM users WHERE id = $1`, userID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return "none", nil
	}
	return state, err
}

var errNeedPinning = errors.New("accounts: cortex handle lacks WithConnection/WithGeneration (Tasks 3 and 5)")
