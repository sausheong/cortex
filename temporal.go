package cortex

import (
	"fmt"
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
