package cortex

import (
	"strings"
	"testing"
	"time"
)

func TestCurrentlyValidClause(t *testing.T) {
	got := currentlyValidClause("m")
	want := "(m.expired_at IS NULL AND m.invalid_at IS NULL)"
	if got != want {
		t.Fatalf("currentlyValidClause(\"m\") = %q, want %q", got, want)
	}
	// Empty alias → unqualified columns.
	gotBare := currentlyValidClause("")
	if strings.Contains(gotBare, ".") {
		t.Fatalf("empty alias should produce unqualified columns, got %q", gotBare)
	}
}

func TestValidAsOfClause(t *testing.T) {
	ts := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clause, args := validAsOfClause("m", ts)
	if !strings.Contains(clause, "m.created_at <= ?") ||
		!strings.Contains(clause, "m.expired_at IS NULL OR m.expired_at > ?") ||
		!strings.Contains(clause, "m.valid_at IS NULL OR m.valid_at <= ?") ||
		!strings.Contains(clause, "m.invalid_at IS NULL OR m.invalid_at > ?") {
		t.Fatalf("validAsOfClause missing expected sub-clauses: %q", clause)
	}
	if len(args) != 4 {
		t.Fatalf("expected 4 args (one per placeholder), got %d", len(args))
	}
	for i, a := range args {
		if a != ts {
			t.Fatalf("arg %d = %v, want %v", i, a, ts)
		}
	}
}
