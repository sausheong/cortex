package vecmath

import (
	"math"
	"testing"
)

func set(ws ...string) map[string]struct{} {
	m := map[string]struct{}{}
	for _, w := range ws {
		m[w] = struct{}{}
	}
	return m
}

func TestJaccard(t *testing.T) {
	cases := []struct {
		a, b map[string]struct{}
		want float64
	}{
		{set("a", "b"), set("a", "b"), 1},
		{set("a", "b"), set("c"), 0},
		{set("a", "b", "c"), set("b", "c", "d"), 0.5},
		{nil, set("a"), 0},
		{set("a"), nil, 0},
	}
	for _, c := range cases {
		if got := Jaccard(c.a, c.b); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("Jaccard(%v,%v) = %v want %v", c.a, c.b, got, c.want)
		}
	}
}
