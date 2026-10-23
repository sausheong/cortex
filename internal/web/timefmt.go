package web

import (
	"net/http"
	"strconv"
	"time"
)

// relTime says how long ago t was, in plain words.
func relTime(now, t time.Time) string {
	if t.After(now) {
		return "just now"
	}
	return ago(now.Sub(t))
}

// absTime is the exact time, for tooltips.
func absTime(t time.Time) string { return t.UTC().Format("2 Jan 2006, 15:04 UTC") }

const visitCookie = "cortex_seen"

// visitGap is how long apart two page loads must be to count as separate
// visits, so a reload does not clear the "New" markers.
const visitGap = 30 * time.Minute

// lastVisit returns when the viewer last looked at memories (zero if never).
// When stamp is set and the last visit is older than visitGap it records now.
func (s *server) lastVisit(w http.ResponseWriter, r *http.Request, now time.Time, stamp bool) time.Time {
	var seen, prev time.Time
	if ck, err := r.Cookie(visitCookie); err == nil {
		if cur, err := strconv.ParseInt(ck.Value, 10, 64); err == nil {
			seen = time.Unix(cur, 0)
		}
		if ck2, err := r.Cookie(visitCookie + "_prev"); err == nil {
			if p, err := strconv.ParseInt(ck2.Value, 10, 64); err == nil {
				prev = time.Unix(p, 0)
			}
		}
	}
	if !stamp {
		if !prev.IsZero() {
			return prev
		}
		return time.Time{}
	}
	set := func(name string, t time.Time) {
		http.SetCookie(w, &http.Cookie{Name: name, Value: strconv.FormatInt(t.Unix(), 10), Path: "/memories",
			MaxAge: 365 * 24 * 3600, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	}
	if seen.IsZero() {
		set(visitCookie, now)
		return time.Time{}
	}
	if now.Sub(seen) > visitGap {
		set(visitCookie+"_prev", seen)
		set(visitCookie, now)
		return seen
	}
	return prev
}
