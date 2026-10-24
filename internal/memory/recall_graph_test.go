package memory

import "testing"

func TestQueryNgrams(t *testing.T) {
	got := queryNgrams("What does Carol Seah's team do?")
	want := map[string]bool{"what": true, "carol": true, "seah": true, "carol seah": true, "does carol seah": true}
	have := map[string]bool{}
	for _, g := range got {
		have[g] = true
	}
	for w := range want {
		if !have[w] {
			t.Errorf("missing n-gram %q in %v", w, got)
		}
	}
	if have["do"] {
		t.Error("two-letter word kept as a unigram")
	}
}

func TestPopularityIsCapped(t *testing.T) {
	if popularity(0) != 1 {
		t.Error("no access should not lift")
	}
	if p := popularity(1_000_000); p > 1+popularityCap+1e-9 {
		t.Errorf("popularity(1e6) = %v exceeds the cap", p)
	}
	if popularity(3) <= popularity(1) {
		t.Error("more access should lift more (below the cap)")
	}
}
