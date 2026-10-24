package graph

import (
	"testing"

	"github.com/sausheong/cortex/internal/model"
)

func TestNameSimilarity(t *testing.T) {
	cases := []struct {
		a, b     string
		min, max float64
	}{
		{"Sau Sheong", "sau sheong", 1, 1},
		{"Sau Sheong", "Chang Sau Sheong", 0.88, 1},
		{"Apollo Platform", "Apolo Platform", 0.6, 0.99},
		{"Platform", "Mobile Platform", 0.6, 0.87}, // one word: ask, don't auto-link
		{"Carol Seah", "Collin Ng", 0, 0.2},
		{"", "x", 0, 0},
	}
	for _, c := range cases {
		if got := NameSimilarity(c.a, c.b); got < c.min || got > c.max {
			t.Errorf("NameSimilarity(%q, %q) = %.3f, want in [%.2f, %.2f]", c.a, c.b, got, c.min, c.max)
		}
	}
}

func TestDetectAliasKindAndNormalize(t *testing.T) {
	cases := []struct{ in, kind, norm string }{
		{"SauSheong@Gmail.com ", model.AliasEmail, "sausheong@gmail.com"},
		{"+65 9123-4567", model.AliasPhone, "+6591234567"},
		{"@sausheong", model.AliasHandle, "sausheong"},
		{"Chang  Sau Sheong", model.AliasName, "chang sau sheong"},
		{"Room 101", model.AliasName, "room 101"},
	}
	for _, c := range cases {
		if k := DetectAliasKind(c.in); k != c.kind {
			t.Errorf("DetectAliasKind(%q) = %q, want %q", c.in, k, c.kind)
		}
		if n := normalizeAlias(c.kind, c.in); n != c.norm {
			t.Errorf("normalizeAlias(%q, %q) = %q, want %q", c.kind, c.in, n, c.norm)
		}
	}
}
