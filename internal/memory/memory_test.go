package memory

import "testing"

func TestOrTSQuery(t *testing.T) {
	expr, args := orTSQuery("alice stripe", 1)
	wantExpr := "(plainto_tsquery('english', $1) || plainto_tsquery('english', $2))"
	if expr != wantExpr {
		t.Errorf("expr = %q, want %q", expr, wantExpr)
	}
	if len(args) != 2 || args[0] != "alice" || args[1] != "stripe" {
		t.Errorf("args = %v", args)
	}
	expr, args = orTSQuery("a", 3)
	if expr != "(plainto_tsquery('english', $3))" || len(args) != 1 {
		t.Errorf("single: %q %v", expr, args)
	}
	for _, in := range []string{"", "   \t\n"} {
		expr, args = orTSQuery(in, 1)
		if expr != "" || args != nil {
			t.Errorf("empty input %q: got %q %v", in, expr, args)
		}
	}
}
