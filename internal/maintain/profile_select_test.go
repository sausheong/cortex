package maintain

import (
	"testing"

	"github.com/sausheong/cortex/internal/model"
)

func TestSelectStaticCandidates_PerEntityCap(t *testing.T) {
	var pool []model.Memory
	words := []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet"}
	for _, w := range words {
		pool = append(pool, model.Memory{Content: "met " + w + " zebra " + w + " unique " + w + w, EntityIDs: []string{"self", "acq"}, Confidence: 1})
	}
	got := selectStaticCandidates(pool, "self", 60)
	if len(got) != staticPerEntityCap {
		t.Fatalf("got %d, want cap %d", len(got), staticPerEntityCap)
	}
}
