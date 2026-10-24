package web

import (
	"net/http"
	"strconv"
	"strings"
)

// A destructive action is two requests: the first (no confirm_count) renders
// a confirmation page stating the exact count and consequence; the second
// carries confirm_count and is executed only when it still equals the live
// count. If the count moved in between, the user sees the new number and
// confirms again.

// confirmField is a hidden field carried from the first request to the second.
type confirmField struct{ Name, Value string }

type confirmView struct {
	Title       string
	Consequence string
	Details     []string
	// Items previews what is being deleted; More is the "and N more" line.
	Items []string
	More  string
	Action      string
	Fields      []confirmField
	Count       int
	Phrase      string // typed confirmation required when set
	PhraseErr   string
	Error       string
	Button      string
	CancelHref  string
}

// expectCount reads confirm_count; ok is false when absent or malformed.
func expectCount(r *http.Request) (n int, ok bool) {
	v := strings.TrimSpace(r.PostFormValue("confirm_count"))
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
}

// phraseOK compares a typed confirmation, ignoring case and outer space.
func phraseOK(r *http.Request, phrase string) bool {
	return strings.EqualFold(strings.TrimSpace(r.PostFormValue("confirm")), phrase)
}

// plural returns "1 memory" or "12 memories".
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmtInt(n) + " " + many
}

// changedMsg is shown when the count moved between the two requests.
func changedMsg(was, now int, noun string) string {
	return "The number of " + noun + " changed from " + fmtInt(was) + " to " + fmtInt(now) + ". Check the new count and confirm again."
}

func (s *server) renderConfirm(w http.ResponseWriter, r *http.Request, status int, v confirmView) {
	s.render(w, r, status, "confirm", v)
}
