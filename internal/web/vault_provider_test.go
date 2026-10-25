package web

import (
	"strings"
	"testing"

	"github.com/sausheong/cortex/internal/accounts"
)

// Production roots carry no LLM: the user's chat model comes from their own
// provider settings. Vault must resolve it, not test the root handle.
func TestVaultUsesUsersProviderNotRoot(t *testing.T) {
	e, _, id := vaultEnv(t)
	if e.root.LLM() != nil {
		t.Fatal("test root should have no LLM, like production")
	}
	s := accounts.Settings{BaseURL: "https://api.example.com/v1", APIKey: "k1", ChatModel: "m", EmbedModel: "e", EmbedDims: 512}
	if _, err := e.st.PutSettings(t.Context(), id, s, e.ensureEnc(id)); err != nil {
		t.Fatal(err)
	}
	_, body := e.get("/vault")
	if strings.Contains(body, "cannot write articles") {
		t.Fatalf("Vault says the model cannot write although the user has a provider: %s", body)
	}
}
