package web

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/model"
)

// Home reads only stored state: the cached profile, cached usage, connection
// and memory rows, and the maintenance record. It never builds a profile,
// measures storage or calls the AI provider, so it stays cheap on every load.

const (
	homeProfileLines = 5
	homeNewPerGroup  = 1
	homeFallbackDays = 7
	homeIdleDays     = 30
	homeQuoteRunes   = 140
	homeVisitCookie  = "cortex_home"
)

type homeProfile struct {
	Err     bool
	Ready   bool // a distilled profile exists
	Lines   []string
	More    []string
	BuiltAt string
}

type homeItem struct{ Text, Ago, Abs, ISO string }

type homeGroup struct {
	Name, Href string
	Count      int
	Items      []homeItem
}

type homeNew struct {
	Err bool
	// Heading is "New since your last visit", or "Recently added" on a first visit.
	Heading string
	Empty   string // set when there is nothing to list
	Groups  []homeGroup
	Total   int    // memories across all groups
	Window  string // "since your last visit" or "in the last 7 days"
}

type homeAssistant struct {
	Name, Used, UsedISO string
	Written7            int
	Status              string // statusActive | statusIdle | statusUnused
}

type homeAssistants struct {
	Err  bool
	Rows []homeAssistant
}

type homeHealth struct {
	Err bool
	// Maintenance
	MaintState string // "none" | "ok" | "partial" | "failed"
	MaintText  string
	MaintShort string // "ran 3 hours ago", for the status strip
	// Provider
	ProviderOK   bool
	ProviderText string
	// Storage
	StorageText string
	// Recently deleted
	DeletedCount int
	DeletedText  string
	// OKLine is set when nothing needs attention; Home then shows it instead
	// of the four rows.
	OKLine string
}

type homeView struct {
	Strip      []string
	Profile    homeProfile
	New        homeNew
	Assistants homeAssistants
	Health     homeHealth
}

// homeVisitStamp returns when the viewer last opened Home before this visit
// (zero on the first). Two cookies hold the current and the previous visit,
// so a reload within visitGap does not clear "new since". They are tied to
// the user ID, so another account in the same browser starts fresh.
func (s *server) homeVisitStamp(w http.ResponseWriter, r *http.Request, userID string, now time.Time, stamp bool) time.Time {
	read := func(name string) time.Time {
		ck, err := r.Cookie(name)
		if err != nil {
			return time.Time{}
		}
		id, ts, ok := strings.Cut(ck.Value, ":")
		if !ok || id != userID {
			return time.Time{}
		}
		n, err := strconv.ParseInt(ts, 10, 64)
		if err != nil {
			return time.Time{}
		}
		return time.Unix(n, 0)
	}
	set := func(name string, t time.Time) {
		http.SetCookie(w, &http.Cookie{Name: name, Value: userID + ":" + strconv.FormatInt(t.Unix(), 10), Path: "/",
			MaxAge: 365 * 24 * 3600, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	}
	cur, prev := read(homeVisitCookie), read(homeVisitCookie+"_prev")
	if !stamp {
		return prev
	}
	switch {
	case cur.IsZero():
		set(homeVisitCookie, now)
		return time.Time{}
	case now.Sub(cur) > visitGap:
		set(homeVisitCookie+"_prev", cur)
		set(homeVisitCookie, now)
		return cur
	}
	return prev
}

func (s *server) home(w http.ResponseWriter, r *http.Request) {
	u, ok := s.lookup(r, true)
	if !ok {
		s.render(w, r, http.StatusOK, "signin", nil)
		return
	}
	defer u.Key.Zero()
	ctx := r.Context()
	now := s.now()
	prev := s.homeVisitStamp(w, r, u.UserID, now, r.Method == http.MethodGet)
	cx := s.root.ForUser(u.UserID)
	conns, connErr := s.st.ListConnections(ctx, u.UserID)

	v := homeView{}
	v.Profile = s.homeProfile(ctx, cx)
	v.New = s.homeNew(ctx, cx, conns, connErr, prev, now)
	v.Assistants = s.homeAssistants(ctx, cx, conns, connErr, now)
	v.Health = s.homeHealth(ctx, cx, u.UserID, u.Key, now)
	v.Strip = homeStrip(v)
	s.render(w, r, http.StatusOK, "home", v)
}

// homeStrip is the glance line: a few plain clauses, each left out when its
// data could not be read or does not exist yet.
func homeStrip(v homeView) []string {
	var parts []string
	if !v.Assistants.Err && len(v.Assistants.Rows) > 0 {
		var n = map[string]int{}
		for _, a := range v.Assistants.Rows {
			n[a.Status]++
		}
		total := len(v.Assistants.Rows)
		// The clauses always add up to the rows listed below.
		var bits []string
		if n[statusActive] > 0 {
			bits = append(bits, strconv.Itoa(n[statusActive])+" active")
		}
		if n[statusIdle] > 0 {
			bits = append(bits, strconv.Itoa(n[statusIdle])+" idle")
		}
		if n[statusUnused] > 0 {
			bits = append(bits, strconv.Itoa(n[statusUnused])+" not used yet")
		}
		if n[statusUnused] == total {
			parts = append(parts, plural(total, "assistant", "assistants")+" connected, not used yet")
		} else if total == n[statusActive] {
			parts = append(parts, plural(total, "assistant", "assistants")+" active")
		} else {
			parts = append(parts, plural(total, "assistant", "assistants")+": "+strings.Join(bits, ", "))
		}
	}
	if !v.New.Err {
		if v.New.Total == 0 {
			parts = append(parts, "Nothing new "+v.New.Window)
		} else {
			parts = append(parts, plural(v.New.Total, "new memory", "new memories")+" "+v.New.Window)
		}
	}
	if v.Health.MaintShort != "" {
		parts = append(parts, "Last maintenance "+v.Health.MaintShort)
	}
	return parts
}

func (s *server) homeProfile(ctx context.Context, cx *cortex.Cortex) homeProfile {
	p, ok, err := cx.CachedProfile(ctx)
	if err != nil {
		s.cfg.Logger.Error("home profile", "err", err)
		return homeProfile{Err: true}
	}
	if !ok || !p.Distilled || len(p.Static) == 0 {
		return homeProfile{}
	}
	h := homeProfile{Ready: true, BuiltAt: relTime(s.now(), p.BuiltAt)}
	for i, l := range p.Static {
		if i < homeProfileLines {
			h.Lines = append(h.Lines, l)
		} else {
			h.More = append(h.More, l)
		}
	}
	return h
}

// quote shortens long memory text to one readable line.
func quote(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= homeQuoteRunes {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:homeQuoteRunes])) + "…"
}

func connLabel(byID map[string]accounts.Connection, id string) string {
	if id == "" {
		return "Added by hand or imported"
	}
	c, ok := byID[id]
	if !ok {
		return "Another source"
	}
	if c.RemovedAt != nil {
		return c.Name + " (removed)"
	}
	return c.Name
}

func (s *server) homeNew(ctx context.Context, cx *cortex.Cortex, conns []accounts.Connection, connErr error, prev, now time.Time) homeNew {
	h := homeNew{Heading: "New since your last visit", Window: "since your last visit"}
	since := prev
	if prev.IsZero() {
		h.Heading = "Recently added"
		h.Window = "in the last 7 days"
		since = now.AddDate(0, 0, -homeFallbackDays)
	}
	// Whole seconds, so the Memories page behind each link (which takes an
	// RFC3339 instant) counts exactly the same memories as "N new".
	since = since.Truncate(time.Second)
	if connErr != nil {
		s.cfg.Logger.Error("home connections", "err", connErr)
		h.Err = true
		return h
	}
	groups, err := cx.NewMemoriesSince(ctx, since, homeNewPerGroup)
	if err != nil {
		s.cfg.Logger.Error("home new", "err", err)
		h.Err = true
		return h
	}
	if len(groups) == 0 {
		if prev.IsZero() {
			h.Empty = "Nothing added in the last 7 days."
		} else {
			h.Empty = "Nothing new since " + relTime(now, prev) + "."
		}
		return h
	}
	byID := map[string]accounts.Connection{}
	for _, c := range conns {
		byID[c.ID] = c
	}
	stamp := since.UTC().Format(time.RFC3339)
	for _, g := range groups {
		h.Total += g.Count
		hg := homeGroup{Name: connLabel(byID, g.ConnectionID), Count: g.Count}
		q := url.Values{}
		q.Set("by", cmp.Or(g.ConnectionID, cortex.NoConnection))
		q.Set("since", stamp)
		q.Set("sort", "newest")
		hg.Href = "/memories?" + q.Encode()
		for _, m := range g.Latest {
			hg.Items = append(hg.Items, homeItem{Text: quote(m.Content), Ago: relTime(now, m.CreatedAt),
				Abs: absTime(m.CreatedAt), ISO: m.CreatedAt.UTC().Format(time.RFC3339)})
		}
		h.Groups = append(h.Groups, hg)
	}
	return h
}

func (s *server) homeAssistants(ctx context.Context, cx *cortex.Cortex, conns []accounts.Connection, connErr error, now time.Time) homeAssistants {
	if connErr != nil {
		return homeAssistants{Err: true}
	}
	week, err := cx.WrittenSince(ctx, now.AddDate(0, 0, -7))
	if err != nil {
		s.cfg.Logger.Error("home assistants", "err", err)
		return homeAssistants{Err: true}
	}
	var live []accounts.Connection
	for _, c := range conns {
		if c.RevokedAt == nil && c.RemovedAt == nil {
			live = append(live, c)
		}
	}
	// Most recently used first; never-used last (stable keeps newest-created first).
	sort.SliceStable(live, func(i, j int) bool {
		a, b := live[i].LastUsed, live[j].LastUsed
		switch {
		case a == nil:
			return false
		case b == nil:
			return true
		}
		return a.After(*b)
	})
	var h homeAssistants
	for _, c := range live {
		row := homeAssistant{Name: c.Name, Written7: week[c.ID], Status: assistantStatus(c, now)}
		if c.LastUsed == nil {
			row.Used = "Not used yet"
		} else {
			row.Used = "Used " + relTime(now, *c.LastUsed)
			row.UsedISO = c.LastUsed.UTC().Format(time.RFC3339)
		}
		h.Rows = append(h.Rows, row)
	}
	return h
}

// Assistant states. assistantStatus is the one derivation, used by Home, the
// Connections page and the set-up check, so an assistant never reads Active
// on one page and Idle on another.
const (
	statusActive  = "active"
	statusIdle    = "idle"    // no call for homeIdleDays
	statusUnused  = "unused"  // never called
	statusRevoked = "revoked" // access removed
)

func assistantStatus(c accounts.Connection, now time.Time) string {
	switch {
	case c.RevokedAt != nil:
		return statusRevoked
	case c.LastUsed == nil:
		return statusUnused
	case now.Sub(*c.LastUsed) > homeIdleDays*24*time.Hour:
		return statusIdle
	}
	return statusActive
}

// humanBytes renders a size the way people read it.
func humanBytes(n int64) string {
	const k = 1024.0
	f := float64(n)
	switch {
	case f < k:
		return fmt.Sprintf("%d bytes", n)
	case f < k*k:
		return fmt.Sprintf("%.0f KB", f/k)
	case f < k*k*k:
		return fmt.Sprintf("%.1f MB", f/k/k)
	}
	return fmt.Sprintf("%.2f GB", f/k/k/k)
}

func (s *server) homeHealth(ctx context.Context, cx *cortex.Cortex, userID string, uk *keys.UK, now time.Time) homeHealth {
	var h homeHealth
	fail := func(what string, err error) { s.cfg.Logger.Error("home "+what, "err", err); h.Err = true }

	// Nightly maintenance: the stored outcome only.
	var at *time.Time
	var merr *string
	if err := s.st.DB.QueryRowContext(ctx, `SELECT last_maintenance_at, last_maintenance_error FROM users WHERE id = $1`, userID).Scan(&at, &merr); err != nil {
		fail("maintenance", err)
	} else {
		switch {
		case at == nil:
			h.MaintState = "none"
			h.MaintText = "Runs overnight once your AI provider is set up." // refined below once the provider is known
		case merr == nil || *merr == "":
			h.MaintState = "ok"
			h.MaintShort = "ran " + relTime(now, *at)
			h.MaintText = "Ran " + relTime(now, *at) + ". Everything went through."
		case strings.HasPrefix(*merr, "maintenance completed;"):
			h.MaintState = "partial"
			var n int
			_, _ = fmt.Sscanf(*merr, "maintenance completed; %d item(s) skipped", &n)
			h.MaintShort = "ran " + relTime(now, *at) + ", some items skipped"
			h.MaintText = "Ran " + relTime(now, *at) + ". " + plural(n, "item was", "items were") + " skipped after provider errors and will be retried tonight."
		default:
			h.MaintState = "failed"
			h.MaintShort = "failed " + relTime(now, *at)
			h.MaintText = "Failed " + relTime(now, *at) + ": " + *merr + "."
		}
	}

	// Provider: stored settings only, no test call.
	switch set, err := s.st.GetSettings(ctx, userID, uk); {
	case errors.Is(err, accounts.ErrNoSettings):
		h.ProviderText = "Not set up yet."
	case errors.Is(err, model.ErrLocked):
		h.ProviderText = "Unlock to see your provider."
	case err != nil:
		fail("provider", err)
	case set.APIKey == "":
		h.ProviderText = "Not set up yet."
	default:
		h.ProviderOK = true
		h.ProviderText = "Configured"
		if set.ChatModel != "" {
			h.ProviderText += " with " + set.ChatModel
		}
		h.ProviderText += ". It was tested when you saved it " + relTime(now, set.UpdatedAt) + "."
	}

	if h.MaintState == "none" && h.ProviderOK {
		h.MaintText = "Has not run yet. It runs overnight."
	}

	// Storage: the nightly figure only.
	switch us, ok, err := cx.CachedUsage(ctx); {
	case err != nil:
		fail("usage", err)
	case !ok:
		h.StorageText = "Not measured yet. Updated overnight."
	default:
		h.StorageText = humanBytes(us.Bytes) + " across " + plural(us.Memories, "memory", "memories") + "."
	}

	// Recently deleted, awaiting restore.
	if bs, err := cx.ListDeleteBatches(ctx); err != nil {
		fail("deleted", err)
	} else {
		for _, b := range bs {
			h.DeletedCount += b.Memories + b.Entities + b.Relationships
		}
		if h.DeletedCount == 0 {
			h.DeletedText = "Nothing waiting to be restored."
		} else {
			h.DeletedText = plural(h.DeletedCount, "item", "items") + " can still be restored for 30 days."
		}
	}
	if !h.Err && h.ProviderOK && h.DeletedCount == 0 && (h.MaintState == "ok" || h.MaintState == "none") {
		h.OKLine = "Everything is working. Your AI provider is configured and maintenance "
		if h.MaintState == "ok" {
			h.OKLine += h.MaintShort + "."
		} else {
			h.OKLine += "runs overnight."
		}
	}
	return h
}
