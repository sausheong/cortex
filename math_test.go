package cortex

import (
	"math"
	"testing"
	"time"
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

func TestCosineSimilarityMismatchedLength(t *testing.T) {
	a := []float32{1, 2, 3}
	b := []float32{1, 2}
	sim := cosineSimilarity(a, b)
	if sim != 0.0 {
		t.Errorf("mismatched length: got %f, want 0.0", sim)
	}
}

func TestRRFMergeEmptyInput(t *testing.T) {
	merged := rrfMerge(nil, 60)
	if len(merged) != 0 {
		t.Errorf("expected empty result from nil input, got %d items", len(merged))
	}
	merged = rrfMerge([][]rankedItem{}, 60)
	if len(merged) != 0 {
		t.Errorf("expected empty result from empty input, got %d items", len(merged))
	}
}

func TestRRFMergeSingleList(t *testing.T) {
	list := []rankedItem{
		{id: "X", rank: 0},
		{id: "Y", rank: 1},
		{id: "Z", rank: 2},
	}
	merged := rrfMerge([][]rankedItem{list}, 60)
	if len(merged) != 3 {
		t.Fatalf("expected 3 items, got %d", len(merged))
	}
	// X should be first: 1/(60+0+1) = 1/61
	if merged[0].id != "X" {
		t.Errorf("expected X first (highest rank), got %q", merged[0].id)
	}
	// Scores should be strictly decreasing.
	for i := 1; i < len(merged); i++ {
		if merged[i].score >= merged[i-1].score {
			t.Errorf("scores not decreasing: [%d]=%f >= [%d]=%f",
				i, merged[i].score, i-1, merged[i-1].score)
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

func TestMMRRerank_PrefersDiversity(t *testing.T) {
	// Three candidates: A and B are near-identical vectors (redundant),
	// C is distinct. By raw relevance order: A, B, C. MMR with moderate
	// lambda should pull C ahead of B (diversity) once A is picked.
	a := mmrCandidate{id: "A", score: 0.9, vec: []float32{1, 0, 0}}
	b := mmrCandidate{id: "B", score: 0.8, vec: []float32{0.99, 0.01, 0}}
	cc := mmrCandidate{id: "C", score: 0.7, vec: []float32{0, 1, 0}}

	got := mmrRerank([]mmrCandidate{a, b, cc}, 0.5, 3)
	if len(got) != 3 {
		t.Fatalf("expected 3, got %v", got)
	}
	if got[0] != "A" {
		t.Fatalf("expected A first (highest relevance), got %q", got[0])
	}
	if got[1] != "C" {
		t.Fatalf("expected C second (diversity over redundant B), got %v", got)
	}
}

func TestMMRRerank_NoVectorsFallsBackToRelevance(t *testing.T) {
	// No vectors → pure relevance order, stable.
	items := []mmrCandidate{
		{id: "X", score: 0.5},
		{id: "Y", score: 0.9},
		{id: "Z", score: 0.7},
	}
	got := mmrRerank(items, 0.5, 3)
	want := []string{"Y", "Z", "X"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("relevance fallback: got %v want %v", got, want)
		}
	}
}

func TestDecayedConfidence(t *testing.T) {
	const H = 30 * 24 * time.Hour // 30-day half-life

	// One half-life halves confidence.
	if got := decayedConfidence(1.0, H, H); !approxEq(got, 0.5, 1e-9) {
		t.Fatalf("one half-life: got %v want 0.5", got)
	}
	// Two half-lives → quarter.
	if got := decayedConfidence(1.0, 2*H, H); !approxEq(got, 0.25, 1e-9) {
		t.Fatalf("two half-lives: got %v want 0.25", got)
	}
	// Composition: decaying t1 then t2 == decaying (t1+t2) once.
	t1, t2 := 10*24*time.Hour, 20*24*time.Hour
	once := decayedConfidence(1.0, t1+t2, H)
	twice := decayedConfidence(decayedConfidence(1.0, t1, H), t2, H)
	if !approxEq(once, twice, 1e-9) {
		t.Fatalf("composition broken: once=%v twice=%v", once, twice)
	}
	// elapsed <= 0 → unchanged.
	if got := decayedConfidence(0.8, 0, H); got != 0.8 {
		t.Fatalf("zero elapsed: got %v want 0.8", got)
	}
	if got := decayedConfidence(0.8, -time.Hour, H); got != 0.8 {
		t.Fatalf("negative elapsed: got %v want 0.8", got)
	}
	// halfLife <= 0 → unchanged (no decay configured).
	if got := decayedConfidence(0.8, H, 0); got != 0.8 {
		t.Fatalf("zero half-life: got %v want 0.8", got)
	}
	// Result never exceeds current, never below 0.
	if got := decayedConfidence(0.3, 100*H, H); got < 0 || got > 0.3 {
		t.Fatalf("clamp: got %v out of (0, 0.3]", got)
	}
}

func approxEq(a, b, eps float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= eps
}
