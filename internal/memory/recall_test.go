package memory

import "testing"

func TestAugmentForEmbedding(t *testing.T) {
	// No entities → unchanged.
	if got := AugmentForEmbedding("plain content", nil); got != "plain content" {
		t.Fatalf("no-entity case: got %q", got)
	}
	// Entities → sorted prefix, content preserved.
	got := AugmentForEmbedding("ships in April", []string{"Stripe", "Alice"})
	want := "Entities: Alice, Stripe. ships in April"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	// Empty-string entities are ignored (no blank tokens).
	got = AugmentForEmbedding("x", []string{"", "Bob", ""})
	want = "Entities: Bob. x"
	if got != want {
		t.Fatalf("blank-token case: got %q want %q", got, want)
	}
}
