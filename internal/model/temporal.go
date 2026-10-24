package model

import (
	"fmt"
	"strings"
	"time"
)

// Qualify prefixes a column with the table alias and a dot, or returns the
// bare column when alias is empty.
func Qualify(alias, col string) string {
	if alias == "" {
		return col
	}
	return alias + "." + col
}

// CurrentlyValidClause returns a SQL boolean fragment (no leading AND) that
// is true for memories that are currently valid: not retired by the system
// (expired_at IS NULL) and not event-invalidated (invalid_at IS NULL).
// Memories written by the standard ingest path have both NULL and so are
// always current. alias is the table alias (e.g. "m"); "" yields unqualified
// column names.
func CurrentlyValidClause(alias string) string {
	return fmt.Sprintf("(%s IS NULL AND %s IS NULL)",
		Qualify(alias, "expired_at"), Qualify(alias, "invalid_at"))
}

// ValidAsOfClause returns a SQL boolean fragment (no leading AND) plus its
// bound args, selecting memories valid as of time t: known to the system by
// t, not retired by t, and whose event-validity window contains t. The four
// args are all t, in placeholder order.
func ValidAsOfClause(alias string, t time.Time) (string, []any) {
	clause := fmt.Sprintf(
		"(%s <= ? AND (%s IS NULL OR %s > ?) AND (%s IS NULL OR %s <= ?) AND (%s IS NULL OR %s > ?))",
		Qualify(alias, "created_at"),
		Qualify(alias, "expired_at"), Qualify(alias, "expired_at"),
		Qualify(alias, "valid_at"), Qualify(alias, "valid_at"),
		Qualify(alias, "invalid_at"), Qualify(alias, "invalid_at"),
	)
	return clause, []any{t, t, t, t}
}

// ValidInRangeClause returns a SQL boolean fragment (no leading AND) plus its
// args, selecting memories whose event-validity window [valid_at, invalid_at)
// overlaps the half-open query window [from, to). NULL valid_at means
// "valid since the beginning"; NULL invalid_at means "still valid". A nil
// from/to leaves that side of the window open. With both nil it returns
// "1=1" (no narrowing). This filters the EVENT-TIME dimension only; it does
// not consider expired_at (system retirement), which callers apply
// separately via the currently-valid predicate.
//
// Overlap of [valid_at, invalid_at) with [from, to):
//
//	(valid_at IS NULL OR valid_at < to) AND (invalid_at IS NULL OR invalid_at > from)
//
// Placeholders appear in the order: to, then from.
func ValidInRangeClause(alias string, from, to *time.Time) (string, []any) {
	var parts []string
	var args []any
	if to != nil {
		parts = append(parts, fmt.Sprintf("(%s IS NULL OR %s < ?)",
			Qualify(alias, "valid_at"), Qualify(alias, "valid_at")))
		args = append(args, *to)
	}
	if from != nil {
		parts = append(parts, fmt.Sprintf("(%s IS NULL OR %s > ?)",
			Qualify(alias, "invalid_at"), Qualify(alias, "invalid_at")))
		args = append(args, *from)
	}
	if len(parts) == 0 {
		return "1=1", nil
	}
	return "(" + strings.Join(parts, " AND ") + ")", args
}

// TemporalMode selects which validity predicate a memory read applies, and
// optionally an event-time window to narrow by.
type TemporalMode struct {
	IncludeInvalid bool
	AsOf           *time.Time
	RangeFrom      *time.Time // optional event-time window start (3.2)
	RangeTo        *time.Time // optional event-time window end (3.2)
}

// Clause returns the SQL fragment (no leading AND) and its args for this
// mode. Base predicate by precedence: includeInvalid → "1=1"; asOf →
// validAsOfClause; else currentlyValidClause. When an event-time window
// (rangeFrom/rangeTo) is set, the window overlap Clause is AND-ed onto the
// base predicate so a time-scoped question narrows by event-time while still
// honoring retirement (unless includeInvalid/asOf already governs).
func (m TemporalMode) Clause(alias string) (string, []any) {
	var base string
	var args []any
	switch {
	case m.IncludeInvalid:
		base = "1=1"
	case m.AsOf != nil:
		base, args = ValidAsOfClause(alias, *m.AsOf)
	default:
		base = CurrentlyValidClause(alias)
	}

	if m.RangeFrom == nil && m.RangeTo == nil {
		return base, args
	}
	rClause, rArgs := ValidInRangeClause(alias, m.RangeFrom, m.RangeTo)
	args = append(args, rArgs...)
	return "(" + base + " AND " + rClause + ")", args
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
