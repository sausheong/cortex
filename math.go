package cortex

import (
	"encoding/binary"
	"math"
	"sort"
	"time"
)

// rankedItem represents an item with its rank and computed score for
// reciprocal rank fusion merging.
type rankedItem struct {
	id    string
	rank  int
	score float64
}

// cosineSimilarity computes the cosine similarity between two float32 vectors.
// Returns 0.0 if either vector has zero magnitude.
func cosineSimilarity(a, b []float32) float32 {
	if len(a) != len(b) {
		return 0
	}

	var dot, normA, normB float64
	for i := range a {
		ai, bi := float64(a[i]), float64(b[i])
		dot += ai * bi
		normA += ai * ai
		normB += bi * bi
	}

	if normA == 0 || normB == 0 {
		return 0
	}
	return float32(dot / (math.Sqrt(normA) * math.Sqrt(normB)))
}

// encodeFloat32s encodes a slice of float32 values to little-endian bytes
// suitable for BLOB storage.
func encodeFloat32s(v []float32) []byte {
	buf := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(buf[i*4:], math.Float32bits(f))
	}
	return buf
}

// decodeFloat32s decodes little-endian bytes back to a slice of float32 values.
func decodeFloat32s(b []byte) []float32 {
	n := len(b) / 4
	v := make([]float32, n)
	for i := 0; i < n; i++ {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}

// rrfMerge performs reciprocal rank fusion across multiple ranked lists.
// The score for each item is sum(1/(k+rank+1)) across all lists where it appears.
// Returns items sorted by score descending.
func rrfMerge(lists [][]rankedItem, k int) []rankedItem {
	scores := make(map[string]float64)

	for _, list := range lists {
		for _, item := range list {
			scores[item.id] += 1.0 / float64(k+item.rank+1)
		}
	}

	merged := make([]rankedItem, 0, len(scores))
	for id, score := range scores {
		merged = append(merged, rankedItem{id: id, score: score})
	}

	sort.Slice(merged, func(i, j int) bool {
		return merged[i].score > merged[j].score
	})

	// Assign final ranks.
	for i := range merged {
		merged[i].rank = i
	}

	return merged
}

// mmrCandidate is one item to be reranked: an id, its relevance score
// (already confidence-weighted), and its embedding vector (may be nil).
type mmrCandidate struct {
	id    string
	score float64
	vec   []float32
}

// mmrRerank reorders candidates by Maximal Marginal Relevance: it greedily
// selects, at each step, the unpicked candidate maximizing
//
//	lambda*relevance - (1-lambda)*maxSimilarityToAlreadyPicked
//
// balancing relevance (lambda=1 → pure relevance) against diversity
// (lambda=0 → pure novelty). Returns the selected ids in pick order, up to
// limit. Deterministic: candidates are pre-sorted by score (then id) so ties
// resolve stably, and a candidate with a nil/empty vector contributes zero
// similarity (ranked on relevance alone — graceful when embeddings are
// absent).
func mmrRerank(items []mmrCandidate, lambda float64, limit int) []string {
	n := len(items)
	if n == 0 {
		return nil
	}
	// Stable base order: relevance desc, then id asc.
	order := make([]mmrCandidate, n)
	copy(order, items)
	sort.SliceStable(order, func(i, j int) bool {
		if order[i].score != order[j].score {
			return order[i].score > order[j].score
		}
		return order[i].id < order[j].id
	})

	if limit > n {
		limit = n
	}
	picked := make([]mmrCandidate, 0, limit)
	used := make([]bool, n)
	result := make([]string, 0, limit)

	for len(result) < limit {
		bestIdx := -1
		var bestScore float64
		for i := range order {
			if used[i] {
				continue
			}
			// Max similarity to already-picked.
			var maxSim float64
			for _, p := range picked {
				s := float64(cosineSimilarity(order[i].vec, p.vec))
				if s > maxSim {
					maxSim = s
				}
			}
			mmr := lambda*order[i].score - (1-lambda)*maxSim
			if bestIdx == -1 || mmr > bestScore {
				bestIdx = i
				bestScore = mmr
			}
		}
		if bestIdx == -1 {
			break
		}
		used[bestIdx] = true
		picked = append(picked, order[bestIdx])
		result = append(result, order[bestIdx].id)
	}
	return result
}

// decayedConfidence applies exponential half-life decay to a confidence
// value: it returns current · 0.5^(elapsed/halfLife), clamped to [0, 1].
// Decay only ever decreases confidence. A non-positive halfLife or elapsed
// returns current unchanged (no decay). Exponential decay composes —
// decaying by t1 then t2 equals decaying by t1+t2 once — which is what lets
// the decay pass run on any cadence and converge to the same value for a
// given age.
func decayedConfidence(current float64, elapsed, halfLife time.Duration) float64 {
	if halfLife <= 0 || elapsed <= 0 {
		return current
	}
	factor := math.Pow(0.5, float64(elapsed)/float64(halfLife))
	out := current * factor
	if out < 0 {
		return 0
	}
	if out > current {
		return current // decay never raises
	}
	return out
}
