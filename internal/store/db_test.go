package store

import "testing"

func TestRebind(t *testing.T) {
	if got := Rebind("a = ? AND b IN (?, ?)"); got != "a = $1 AND b IN ($2, $3)" {
		t.Errorf("got %q", got)
	}
	if got := Rebind("no params"); got != "no params" {
		t.Errorf("got %q", got)
	}
}
