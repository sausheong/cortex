package cortex

import "testing"

func TestRebind(t *testing.T) {
	if got := rebind("a = ? AND b IN (?, ?)"); got != "a = $1 AND b IN ($2, $3)" {
		t.Errorf("got %q", got)
	}
	if got := rebind("no params"); got != "no params" {
		t.Errorf("got %q", got)
	}
}
