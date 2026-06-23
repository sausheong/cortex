package cortex

import (
	"fmt"
	"strings"
	"time"
)

// qualify prefixes a column with the table alias and a dot, or returns the
// bare column when alias is empty.
func qualify(alias, col string) string {
	if alias == "" {
		return col
	}
	return alias + "." + col
}

// currentlyValidClause returns a SQL boolean fragment (no leading AND) that
// is true for memories that are currently valid: not retired by the system
// (expired_at IS NULL) and not event-invalidated (invalid_at IS NULL).
// Memories written by the standard ingest path have both NULL and so are
// always current. alias is the table alias (e.g. "m"); "" yields unqualified
// column names.
func currentlyValidClause(alias string) string {
	return fmt.Sprintf("(%s IS NULL AND %s IS NULL)",
		qualify(alias, "expired_at"), qualify(alias, "invalid_at"))
}

// validAsOfClause returns a SQL boolean fragment (no leading AND) plus its
// bound args, selecting memories valid as of time t: known to the system by
// t, not retired by t, and whose event-validity window contains t. The four
// args are all t, in placeholder order.
func validAsOfClause(alias string, t time.Time) (string, []any) {
	clause := fmt.Sprintf(
		"(%s <= ? AND (%s IS NULL OR %s > ?) AND (%s IS NULL OR %s <= ?) AND (%s IS NULL OR %s > ?))",
		qualify(alias, "created_at"),
		qualify(alias, "expired_at"), qualify(alias, "expired_at"),
		qualify(alias, "valid_at"), qualify(alias, "valid_at"),
		qualify(alias, "invalid_at"), qualify(alias, "invalid_at"),
	)
	return clause, []any{t, t, t, t}
}

// temporalMode selects which validity predicate a memory read applies.
type temporalMode struct {
	includeInvalid bool
	asOf           *time.Time
}

// clause returns the SQL fragment (no leading AND) and its args for this
// mode. includeInvalid → always-true ("1=1", no args). asOf set →
// validAsOfClause. Otherwise → currentlyValidClause.
func (m temporalMode) clause(alias string) (string, []any) {
	if m.includeInvalid {
		return "1=1", nil
	}
	if m.asOf != nil {
		return validAsOfClause(alias, *m.asOf)
	}
	return currentlyValidClause(alias), nil
}

// eventTimeLayouts are the date formats ParseEventTime accepts, tried in
// order. More specific formats come first so "2026-03-15" is not matched as
// a bare year. All are interpreted in UTC; a month-only or year-only date
// resolves to the first day (and first month) of the period.
var eventTimeLayouts = []string{
	time.RFC3339,
	"2006-01-02",
	"2006-01",
	"January 2006",
	"Jan 2006",
	"2006",
}

// ParseEventTime turns a natural-language or ISO date string into a UTC
// *time.Time, or nil when the string is empty or unparseable. It is the
// lenient front-door for LLM-supplied event times (memory valid_at): the
// extractor may emit "March 2026", "2026-03", an RFC3339 stamp, or nothing
// at all, and any failure degrades to nil (NULL in storage) rather than
// failing the ingest.
func ParseEventTime(s string) *time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, layout := range eventTimeLayouts {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			u := t.UTC()
			return &u
		}
	}
	return nil
}
