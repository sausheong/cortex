package cortex

import (
	"strings"
	"testing"
)

func TestRenderDecayMarkdown(t *testing.T) {
	r := DecayReport{
		Scanned: 3, Decayed: 2, Pruned: 1, DryRun: true,
		Changes: []DecayChange{
			{ID: "a", Content: "fading", OldConfidence: 0.9, NewConfidence: 0.45, Pruned: false},
			{ID: "b", Content: "dead", OldConfidence: 0.5, NewConfidence: 0.01, Pruned: true},
		},
	}
	out := RenderDecayMarkdown(r)
	for _, want := range []string{"Decay", "fading", "dead", "PRUNED", "0.45", "Dry"} {
		if !strings.Contains(out, want) {
			t.Fatalf("expected output to contain %q, got:\n%s", want, out)
		}
	}
}
