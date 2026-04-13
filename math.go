package cortex

import (
	"encoding/binary"
	"math"
	"sort"
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
