package cortex

import (
	"math"
	"testing"
)

func TestCosineSimilarity(t *testing.T) {
	// Identical vectors should have similarity 1.0.
	a := []float32{1, 2, 3, 4}
	b := []float32{1, 2, 3, 4}
	sim := cosineSimilarity(a, b)
	if math.Abs(float64(sim)-1.0) > 1e-6 {
		t.Errorf("identical vectors: got %f, want 1.0", sim)
	}

	// Orthogonal vectors should have similarity 0.0.
	c := []float32{1, 0, 0, 0}
	d := []float32{0, 1, 0, 0}
	sim = cosineSimilarity(c, d)
	if math.Abs(float64(sim)) > 1e-6 {
		t.Errorf("orthogonal vectors: got %f, want 0.0", sim)
	}

	// Anti-parallel vectors should have similarity -1.0.
	e := []float32{1, 0, 0}
	f := []float32{-1, 0, 0}
	sim = cosineSimilarity(e, f)
	if math.Abs(float64(sim)+1.0) > 1e-6 {
		t.Errorf("anti-parallel vectors: got %f, want -1.0", sim)
	}
}

func TestCosineSimilarityZeroVector(t *testing.T) {
	a := []float32{0, 0, 0}
	b := []float32{1, 2, 3}
	sim := cosineSimilarity(a, b)
	if sim != 0.0 {
		t.Errorf("zero vector: got %f, want 0.0", sim)
	}

	// Both zero vectors.
	sim = cosineSimilarity(a, a)
	if sim != 0.0 {
		t.Errorf("both zero vectors: got %f, want 0.0", sim)
	}
}

func TestEncodeDecodeFloat32s(t *testing.T) {
	original := []float32{1.5, -2.3, 0.0, 3.14159, -0.001}
	encoded := encodeFloat32s(original)

	// Should be 4 bytes per float32.
	if len(encoded) != len(original)*4 {
		t.Fatalf("encoded length = %d, want %d", len(encoded), len(original)*4)
	}

	decoded := decodeFloat32s(encoded)
	if len(decoded) != len(original) {
		t.Fatalf("decoded length = %d, want %d", len(decoded), len(original))
	}

	for i, v := range decoded {
		if v != original[i] {
			t.Errorf("decoded[%d] = %f, want %f", i, v, original[i])
		}
	}
}

func TestRRFMerge(t *testing.T) {
	// List 1: A is rank 0, B is rank 1
	// List 2: B is rank 0, C is rank 1
	// B appears in both lists, so it should get the highest combined score.
	list1 := []rankedItem{
		{id: "A", rank: 0},
		{id: "B", rank: 1},
	}
	list2 := []rankedItem{
		{id: "B", rank: 0},
		{id: "C", rank: 1},
	}

	k := 60
	merged := rrfMerge([][]rankedItem{list1, list2}, k)

	if len(merged) != 3 {
		t.Fatalf("expected 3 merged items, got %d", len(merged))
	}

	// B should be first since it appears in both lists.
	if merged[0].id != "B" {
		t.Errorf("expected B to be ranked first, got %q", merged[0].id)
	}

	// B's score should be 1/(60+0+1) + 1/(60+1+1) = 1/61 + 1/62
	expectedBScore := 1.0/61.0 + 1.0/62.0
	if math.Abs(merged[0].score-expectedBScore) > 1e-10 {
		t.Errorf("B score = %f, want %f", merged[0].score, expectedBScore)
	}

	// A and C should follow (they appear in only one list each).
	// A: 1/(60+0+1) = 1/61
	// C: 1/(60+1+1) = 1/62
	// So A should be second.
	if merged[1].id != "A" {
		t.Errorf("expected A to be ranked second, got %q", merged[1].id)
	}
	if merged[2].id != "C" {
		t.Errorf("expected C to be ranked third, got %q", merged[2].id)
	}
}
