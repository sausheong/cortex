package cortex_test

import (
	"testing"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/testutil"
)

func TestStoreHandleAndLLMAccessors(t *testing.T) {
	llm := &testutil.MockLLM{}
	c, _ := testutil.OpenEncryptedWith(t, cortex.WithLLM(llm))
	if c.StoreHandle() == nil || c.StoreHandle() != testutil.H(c) {
		t.Fatal("StoreHandle does not return the underlying handle")
	}
	if c.StoreHandle().UserID != c.UserID() {
		t.Fatal("handle user id mismatch")
	}
	if c.LLM() != cortex.LLM(llm) {
		t.Fatal("LLM() did not return the configured LLM")
	}
	if _, ok := c.LLM().(cortex.ArticleWriter); !ok {
		t.Fatal("configured LLM should satisfy ArticleWriter")
	}
	c.SetLLM(nil)
	if c.LLM() != nil {
		t.Fatal("LLM() should be nil after SetLLM(nil)")
	}
}
