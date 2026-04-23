package cortex

import (
	"strings"
	"testing"
)

func TestSplitContent_NoSplitWhenSmall(t *testing.T) {
	got := splitContent("hello world", 100)
	if len(got) != 1 || got[0] != "hello world" {
		t.Fatalf("got %v, want single-element slice with original", got)
	}
}

func TestSplitContent_NoSplitWhenMaxZero(t *testing.T) {
	long := strings.Repeat("x", 10_000)
	got := splitContent(long, 0)
	if len(got) != 1 || got[0] != long {
		t.Fatalf("expected no split when maxChars=0, got %d parts", len(got))
	}
}

func TestSplitContent_ParagraphBoundaries(t *testing.T) {
	a := strings.Repeat("a", 90)
	b := strings.Repeat("b", 90)
	c := strings.Repeat("c", 90)
	in := a + "\n\n" + b + "\n\n" + c
	parts := splitContent(in, 100)
	if len(parts) != 3 {
		t.Fatalf("want 3 parts (one per paragraph), got %d: %v", len(parts), partsLens(parts))
	}
	for i, p := range parts {
		if len(p) > 100 {
			t.Errorf("part %d exceeds maxChars: %d", i, len(p))
		}
	}
}

func TestSplitContent_HardCutWhenSingleLineExceeds(t *testing.T) {
	in := strings.Repeat("z", 10_000)
	parts := splitContent(in, 1000)
	if len(parts) < 10 {
		t.Fatalf("expected >=10 parts for 10000-char single line, got %d", len(parts))
	}
	for i, p := range parts {
		if len(p) > 1000 {
			t.Errorf("part %d exceeds maxChars: %d", i, len(p))
		}
	}
	if got := strings.Join(parts, ""); got != in {
		t.Errorf("hard-cut should be lossless")
	}
}

func TestSplitContent_GreedyFillsChunks(t *testing.T) {
	// 10 short paragraphs of ~50 chars each should pack into ~3 chunks
	// of ~150 chars when max is 200.
	p := strings.Repeat("a", 50)
	parts := splitContent(strings.Repeat(p+"\n\n", 10), 200)
	for _, x := range parts {
		if len(x) > 200 {
			t.Errorf("chunk exceeds 200: %d", len(x))
		}
	}
	if len(parts) > 5 {
		t.Errorf("greedy packing failed: got %d parts (expected <=5)", len(parts))
	}
}

func partsLens(parts []string) []int {
	out := make([]int, len(parts))
	for i, p := range parts {
		out[i] = len(p)
	}
	return out
}
