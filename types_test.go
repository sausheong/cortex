package cortex

import (
	"math"
	"testing"
)

func TestCoerceConfidence(t *testing.T) {
	cases := []struct {
		name string
		in   float64
		want float64
	}{
		{"zero defaults to one", 0, 1.0},
		{"NaN defaults to one", math.NaN(), 1.0},
		{"negative clamped to zero", -0.5, 0},
		{"above one clamped to one", 1.5, 1},
		{"in-range passthrough", 0.42, 0.42},
		{"one passthrough", 1.0, 1.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := coerceConfidence(tc.in)
			if got != tc.want {
				t.Errorf("coerceConfidence(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
