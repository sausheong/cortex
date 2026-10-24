package web

import (
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

var memTextRe = regexp.MustCompile(`<span class="mem-text">row-(\d+)[^<]*</span>`)

func shownRows(body string) []string {
	var out []string
	for _, m := range memTextRe.FindAllStringSubmatch(body, -1) {
		out = append(out, m[1])
	}
	return out
}

// seedAt writes a memory and backdates it.
func seedAt(t *testing.T, e *env, uid, content string, at time.Time) string {
	t.Helper()
	id := putMem(t, e.root.ForUser(uid), content)
	if _, err := e.st.DB.Exec(`UPDATE memories SET created_at=$1 WHERE id=$2 AND user_id=$3`, at, id, uid); err != nil {
		t.Fatal(err)
	}
	return id
}

// seedN creates n memories row-001.. with created_at one hour apart, row-001 oldest.
func seedN(t *testing.T, e *env, uid string, n int) {
	t.Helper()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= n; i++ {
		seedAt(t, e, uid, fmt.Sprintf("row-%03d", i), base.Add(time.Duration(i)*time.Hour))
	}
	e.now = time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
}

func TestMemoriesPagingBoundaries(t *testing.T) {
	e, _, id := memEnv(t)
	seedN(t, e, id, 120)
	_, body := e.get("/memories")
	rows := shownRows(body)
	if len(rows) != 50 || rows[0] != "120" || rows[49] != "071" {
		t.Fatalf("page 1: %d rows %v", len(rows), rows)
	}
	if !strings.Contains(body, "Showing 1–50 of 120") {
		t.Fatal("missing showing text")
	}
	if !strings.Contains(body, `aria-current="page"`) || !strings.Contains(body, `rel="next"`) || strings.Contains(body, `rel="prev"`) {
		t.Fatal("pager controls wrong on first page")
	}
	_, body = e.get("/memories?page=3")
	rows = shownRows(body)
	if len(rows) != 20 || !strings.Contains(body, "Showing 101–120 of 120") || strings.Contains(body, `rel="next"`) || !strings.Contains(body, `rel="prev"`) {
		t.Fatalf("last page wrong: %d", len(rows))
	}
	// Past the last page clamps to the last page; below 1 clamps to the first.
	_, body = e.get("/memories?page=99")
	if !strings.Contains(body, "Showing 101–120 of 120") {
		t.Fatal("page beyond last not clamped")
	}
	for _, pg := range []string{"99999999999999999999", "9223372036854775807", "16777217"} {
		resp, b := e.get("/memories?page=" + pg)
		if resp.StatusCode != 200 || !strings.Contains(b, "Showing 101–120 of 120") {
			t.Fatalf("page=%s: code %d not clamped", pg, resp.StatusCode)
		}
	}
	_, body = e.get("/memories?page=-4")
	if !strings.Contains(body, "Showing 1–50 of 120") {
		t.Fatal("page below 1 not clamped")
	}
	// Exactly one full page: no pager numbers, no page 2.
	e2, _, id2 := memEnv(t)
	seedN(t, e2, id2, 50)
	_, body = e2.get("/memories?page=2")
	if strings.Contains(body, `class="pages"`) || !strings.Contains(body, "Showing 1–50 of 50") {
		t.Fatal("single full page wrong")
	}
}

func TestMemoriesThousandsSeparator(t *testing.T) {
	if fmtInt(1274) != "1,274" || fmtInt(999) != "999" || fmtInt(1000000) != "1,000,000" {
		t.Fatal("fmtInt")
	}
}

func TestMemoriesPageSizeValidation(t *testing.T) {
	e, _, id := memEnv(t)
	seedN(t, e, id, 130)
	for q, want := range map[string]int{"": 50, "size=25": 25, "size=100": 100, "size=200": 130, "size=37": 50, "size=0": 50, "size=abc": 50, "size=-5": 50, "size=9999": 50} {
		_, body := e.get("/memories?" + q)
		if got := len(shownRows(body)); got != want {
			t.Errorf("%q: %d rows, want %d", q, got, want)
		}
	}
	_, body := e.get("/memories?size=25&page=2")
	if !strings.Contains(body, "Showing 26–50 of 130") || !strings.Contains(body, "size=25&amp;page=3") {
		t.Fatalf("size not kept in links: %s", body)
	}
}

func TestMemoriesSortBothWaysStable(t *testing.T) {
	e, _, id := memEnv(t)
	seedN(t, e, id, 5)
	// Three rows share one timestamp: order must still be stable (by id).
	same := time.Date(2026, 3, 3, 3, 0, 0, 0, time.UTC)
	for _, n := range []string{"row-201", "row-202", "row-203"} {
		seedAt(t, e, id, n, same)
	}
	_, a := e.get("/memories")
	_, b := e.get("/memories")
	if !reflect.DeepEqual(shownRows(a), shownRows(b)) {
		t.Fatal("unstable")
	}
	_, nw := e.get("/memories?sort=newest")
	_, od := e.get("/memories?sort=oldest")
	nr, or := shownRows(nw), shownRows(od)
	if !reflect.DeepEqual(shownRows(a), nr) || len(nr) != 8 || nr[0] == or[0] {
		t.Fatalf("default is not newest: %v / %v", nr, or)
	}
	// Oldest first: the five January rows ascend, then the March trio.
	if strings.Join(or[:5], ",") != "001,002,003,004,005" {
		t.Fatalf("oldest order: %v", or)
	}
	// Ties break on id the same way in both directions.
	if !reflect.DeepEqual(nr[:3], or[5:]) {
		t.Fatalf("tie order differs between directions: %v vs %v", nr[:3], or[5:])
	}
	// Paging follows the sort.
	_, p := e.get("/memories?sort=oldest&size=25&page=1")
	if shownRows(p)[0] != "001" || !strings.Contains(p, "sort=oldest") {
		t.Fatal("sort not kept")
	}
}

func TestMemoriesDateRangeInclusiveEdges(t *testing.T) {
	e, _, id := memEnv(t)
	d := func(day, h, m, s int) time.Time { return time.Date(2026, 5, day, h, m, s, 0, time.UTC) }
	seedAt(t, e, id, "row-001", d(9, 23, 59, 59))
	seedAt(t, e, id, "row-002", d(10, 0, 0, 0))
	seedAt(t, e, id, "row-003", d(12, 23, 59, 59))
	seedAt(t, e, id, "row-004", d(13, 0, 0, 0))
	e.now = time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	_, body := e.get("/memories?from=2026-05-10&to=2026-05-12")
	if got := strings.Join(shownRows(body), ","); got != "003,002" {
		t.Fatalf("UTC edges: %s", got)
	}
	_, body = e.get("/memories?from=2026-05-10")
	if got := strings.Join(shownRows(body), ","); got != "004,003,002" {
		t.Fatalf("from only: %s", got)
	}
	_, body = e.get("/memories?to=2026-05-10")
	if got := strings.Join(shownRows(body), ","); got != "002,001" {
		t.Fatalf("to only: %s", got)
	}
	_, body = e.get("/memories?from=2026-05-12&to=2026-05-10")
	if !strings.Contains(body, "From is after To") || len(shownRows(body)) != 0 {
		t.Fatal("reversed range should explain itself")
	}
	if !strings.Contains(body, "Dates are whole days in UTC") {
		t.Fatal("tz hint missing")
	}
}

func TestMemoriesDateRangeUsesBrowserOffset(t *testing.T) {
	e, _, id := memEnv(t)
	seedAt(t, e, id, "row-001", time.Date(2026, 5, 9, 15, 59, 59, 0, time.UTC)) // 23:59:59 on 9th at UTC+8
	seedAt(t, e, id, "row-002", time.Date(2026, 5, 9, 16, 0, 0, 0, time.UTC))   // 00:00 on 10th
	seedAt(t, e, id, "row-003", time.Date(2026, 5, 10, 15, 59, 59, 0, time.UTC))
	seedAt(t, e, id, "row-004", time.Date(2026, 5, 10, 16, 0, 0, 0, time.UTC)) // 11th
	e.now = time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	u, _ := url.Parse(e.ts.URL)
	e.jar.SetCookies(u, []*http.Cookie{{Name: tzCookie, Value: "-480", Path: "/"}})
	_, body := e.get("/memories?from=2026-05-10&to=2026-05-10")
	if got := strings.Join(shownRows(body), ","); got != "003,002" {
		t.Fatalf("UTC+8 day: %s", got)
	}
	if !strings.Contains(body, "UTC&#43;8") {
		t.Fatal("hint should name the offset")
	}
	// Garbage or out-of-range offsets fall back to UTC.
	e.jar.SetCookies(u, []*http.Cookie{{Name: tzCookie, Value: "99999", Path: "/"}})
	_, body = e.get("/memories?from=2026-05-10&to=2026-05-10")
	if !strings.Contains(body, "whole days in UTC") {
		t.Fatal("bad offset must fall back to UTC")
	}
}

func TestMemoriesDatePresets(t *testing.T) {
	e, _, id := memEnv(t)
	now := time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	e.now = now
	day := func(back int, h int) time.Time { return time.Date(2026, 6, 15-back, h, 0, 0, 0, time.UTC) }
	seedAt(t, e, id, "row-001", day(0, 1))                                   // today
	seedAt(t, e, id, "row-002", day(6, 0))                                   // first day of 7d window
	seedAt(t, e, id, "row-003", day(7, 23))                                  // just outside 7d
	seedAt(t, e, id, "row-004", day(29, 0))                                  // first day of 30d window
	seedAt(t, e, id, "row-005", day(30, 23))                                 // just outside 30d
	seedAt(t, e, id, "row-006", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) // first day of year
	seedAt(t, e, id, "row-007", time.Date(2025, 12, 31, 23, 0, 0, 0, time.UTC))
	for q, want := range map[string]string{
		"range=7d":    "001,002",
		"range=30d":   "001,002,003,004",
		"range=year":  "001,002,003,004,005,006",
		"range=bogus": "001,002,003,004,005,006,007",
		"":            "001,002,003,004,005,006,007",
	} {
		_, body := e.get("/memories?" + q + "&size=200&sort=oldest")
		got := shownRows(body)
		// sort=oldest lists ascending by time; compare as sets in id order.
		set := map[string]bool{}
		for _, g := range got {
			set[g] = true
		}
		w := strings.Split(want, ",")
		if len(set) != len(w) {
			t.Errorf("%s: got %v want %v", q, got, w)
			continue
		}
		for _, x := range w {
			if !set[x] {
				t.Errorf("%s: missing %s in %v", q, x, got)
			}
		}
	}
	_, body := e.get("/memories?range=7d")
	for _, want := range []string{`value="2026-06-09"`, `value="2026-06-15"`, `aria-current="true"`, "Last 30 days", "This year", "Any time"} {
		if !strings.Contains(body, want) {
			t.Fatalf("preset page missing %q", want)
		}
	}
	// A preset keeps the other filters; Any time clears dates only.
	_, body = e.get("/memories?q=row&range=7d&sort=oldest")
	if !strings.Contains(body, `href="/memories?q=row&amp;range=30d&amp;sort=oldest"`) || !strings.Contains(body, `href="/memories?q=row&amp;sort=oldest"`) {
		t.Fatalf("preset links lose filters: %s", body)
	}
}

func TestMemoriesFiltersCombined(t *testing.T) {
	e, _, id := memEnv(t)
	conn, err := e.st.CreateConnection(t.Context(), id, "agent", "laptop", "")
	if err != nil {
		t.Fatal(err)
	}
	lap := e.root.ForUser(id).WithConnection(conn.ID)
	e.now = time.Date(2026, 6, 15, 12, 0, 0, 0, time.UTC)
	mk := func(c interface{}, text string, at time.Time) {}
	_ = mk
	a := putMem(t, lap, "row-001 apples")
	b := putMem(t, lap, "row-002 apples")
	c := putMem(t, e.root.ForUser(id), "row-003 apples")
	d := putMem(t, lap, "row-004 pears")
	for i, m := range []string{a, b, c, d} {
		if _, err := e.st.DB.Exec(`UPDATE memories SET created_at=$1 WHERE id=$2`, time.Date(2026, 6, 10+i*0, 12, 0, 0, 0, time.UTC).Add(time.Duration(i)*24*time.Hour), m); err != nil {
			t.Fatal(err)
		}
	}
	// a=10th, b=11th, c=12th, d=13th
	_, body := e.get("/memories?q=apples&by=" + conn.ID + "&from=2026-06-11&to=2026-06-13&sort=oldest&size=25")
	if got := strings.Join(shownRows(body), ","); got != "002" {
		t.Fatalf("combined: %s", got)
	}
	if !strings.Contains(body, "Showing 1–1 of 1") && !strings.Contains(body, "Showing 1–1 of 1") {
		t.Fatal("count must match the filter")
	}
}

func TestMemoriesSelectAllScopedToPageAndDeleteCountsExactly(t *testing.T) {
	e, c, id := memEnv(t)
	seedN(t, e, id, 60)
	_, body := e.get("/memories?size=25&sort=oldest&page=2&select=page")
	if strings.Count(body, `name="ids" value`) != 25 || strings.Count(body, " checked>") != 25 || !strings.Contains(body, "25 selected") {
		t.Fatal("select all must tick exactly the rows on this page")
	}
	rows := shownRows(body)
	if rows[0] != "026" || rows[24] != "050" {
		t.Fatalf("wrong page rows %v", rows)
	}
	for _, part := range []string{"sort=oldest", "size=25", "page=2", "select=page"} {
		if !strings.Contains(body, `id="selall"`) || !strings.Contains(body, part) {
			t.Fatalf("view lost %s", part)
		}
	}
	// Submit exactly those ids: server counts them itself.
	ids := regexp.MustCompile(`name="ids" value="([^"]+)"`).FindAllStringSubmatch(body, -1)
	form := url.Values{"csrf": {e.csrf()}, "scope": {"selected"}}
	for _, m := range ids {
		form.Add("ids", m[1])
	}
	resp, cbody := e.post("/memories/delete", form)
	if resp.StatusCode != http.StatusOK || !strings.Contains(cbody, "Delete 25 memories?") {
		t.Fatalf("confirm: %d %s", resp.StatusCode, cbody)
	}
	if liveCount(t, c) != 60 {
		t.Fatal("preview deleted something")
	}
	form.Set("confirm_count", "25")
	if resp, _ = e.post("/memories/delete", form); resp.StatusCode != http.StatusSeeOther || liveCount(t, c) != 35 {
		t.Fatalf("delete: %d live %d", resp.StatusCode, liveCount(t, c))
	}
}

func TestMemoriesPagingIsolation(t *testing.T) {
	e, _, id := memEnv(t)
	seedN(t, e, id, 10)
	b, err := e.root.CreateUser(t.Context(), "g-b", "b@x.test", "B", true)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 70; i++ {
		seedAt(t, e, b.ID, fmt.Sprintf("row-9%02d", i), time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	}
	_, body := e.get("/memories?size=25&page=2")
	if len(shownRows(body)) != 0 && strings.Contains(body, "row-9") {
		t.Fatal("saw another user's rows")
	}
	_, body = e.get("/memories?size=200")
	if strings.Contains(body, "row-9") || !strings.Contains(body, "Showing 1–10 of 10") {
		t.Fatal("totals or rows leak across users")
	}
}

func TestMemoriesDeleteKeepsView(t *testing.T) {
	e, _, id := memEnv(t)
	seedN(t, e, id, 60)
	_, body := e.get("/memories?size=25&sort=oldest&page=2&q=row")
	if !strings.Contains(body, `name="view" value="q=row&amp;size=25&amp;sort=oldest&amp;page=2"`) {
		t.Fatal("delete forms must carry the view")
	}
	ids := regexp.MustCompile(`name="ids" value="([^"]+)"`).FindAllStringSubmatch(body, 2)
	form := url.Values{"csrf": {e.csrf()}, "scope": {"selected"}, "view": {"q=row&size=25&sort=oldest&page=2&evil=1"}, "confirm_count": {"2"}}
	for _, m := range ids {
		form.Add("ids", m[1])
	}
	resp, _ := e.post("/memories/delete", form)
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.Contains(loc, "sort=oldest") || !strings.Contains(loc, "page=2") || !strings.Contains(loc, "batch=") || strings.Contains(loc, "evil") {
		t.Fatalf("redirect %d %q", resp.StatusCode, loc)
	}
	// Validation error re-render keeps the filter too.
	resp, body = e.post("/memories/delete", url.Values{"csrf": {e.csrf()}, "scope": {"selected"}, "view": {"sort=oldest&size=25"}})
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(body, `<option value="oldest" selected>`) {
		t.Fatalf("error render lost view: %d", resp.StatusCode)
	}
}

func TestMemoriesGarbageDateIgnoredWithMessage(t *testing.T) {
	e, _, id := memEnv(t)
	seedN(t, e, id, 3)
	_, body := e.get("/memories?from=garbage")
	if !strings.Contains(body, "was not understood") || strings.Contains(body, "garbage") {
		t.Fatal("garbage date must be cleared with a message")
	}
}

// since composes with from/to (strictly after the instant), and by=none lists
// memories no connection wrote.
func TestMemoriesSinceAndByNone(t *testing.T) {
	e, c, id := memEnv(t)
	_, conn, _ := e.st.CreateAgentToken(t.Context(), id, "Agent", nil)
	putMem(t, c, "direct early")
	time.Sleep(20 * time.Millisecond)
	mark := time.Now().UTC().Truncate(time.Second).Add(time.Second)
	time.Sleep(time.Until(mark.Add(20 * time.Millisecond)))
	putMem(t, c, "direct late")
	e.write(id, conn.ID, "agent late")
	q := func(v url.Values) string { _, b := e.get("/memories?" + v.Encode()); return b }
	since := mark.Format(time.RFC3339)
	b := q(url.Values{"since": {since}})
	if !strings.Contains(b, "direct late") || !strings.Contains(b, "agent late") || strings.Contains(b, "direct early") {
		t.Fatalf("since: %s", b)
	}
	b = q(url.Values{"since": {since}, "by": {"none"}})
	if !strings.Contains(b, "direct late") || strings.Contains(b, "agent late") || strings.Contains(b, "direct early") {
		t.Fatalf("since+by=none: %s", b)
	}
	b = q(url.Values{"by": {"none"}})
	if !strings.Contains(b, "direct early") || strings.Contains(b, "agent late") {
		t.Fatalf("by=none: %s", b)
	}
	// Composes with a day range: an earlier From does not widen since.
	day := mark.Format("2006-01-02")
	b = q(url.Values{"since": {since}, "from": {day}, "to": {day}})
	if strings.Contains(b, "direct early") || !strings.Contains(b, "agent late") {
		t.Fatalf("since+from/to: %s", b)
	}
	// Paging links keep since, and a bad value is ignored.
	if b = q(url.Values{"since": {since}, "size": {"25"}}); !strings.Contains(b, "since") {
		t.Fatal("since not carried in the view")
	}
	if b = q(url.Values{"since": {"garbage"}}); !strings.Contains(b, "direct early") {
		t.Fatalf("bad since should be ignored: %s", b)
	}
}

// Deleting the last page's rows sends the viewer to the new last page, with the
// filter, sort and page size intact and the Undo notice showing.
func TestMemoriesDeleteRedirectClampsPage(t *testing.T) {
	e, _, id := memEnv(t)
	seedN(t, e, id, 60)
	view := "q=row&size=25&sort=oldest&page=3"
	_, body := e.get("/memories?" + view)
	ids := regexp.MustCompile(`name="ids" value="([^"]+)"`).FindAllStringSubmatch(body, -1)
	if len(ids) != 10 {
		t.Fatalf("page 3 rows = %d, want 10", len(ids))
	}
	form := url.Values{"csrf": {e.csrf()}, "scope": {"selected"}, "view": {view}, "confirm_count": {"10"}}
	for _, m := range ids {
		form.Add("ids", m[1])
	}
	resp, _ := e.post("/memories/delete", form)
	loc := resp.Header.Get("Location")
	u, err := url.Parse(loc)
	if resp.StatusCode != http.StatusSeeOther || err != nil {
		t.Fatalf("redirect %d %q", resp.StatusCode, loc)
	}
	q := u.Query()
	if q.Get("q") != "row" || q.Get("size") != "25" || q.Get("sort") != "oldest" || q.Get("page") != "2" || q.Get("batch") == "" {
		t.Fatalf("redirect should keep the view with page clamped to 2: %q", loc)
	}
	_, body = e.get(loc)
	if !strings.Contains(body, `id="undo-notice"`) || !strings.Contains(body, "Deleted 10 memories") || !strings.Contains(body, "Showing 26–50 of 50") {
		t.Fatalf("destination: %.600s", body)
	}
	// An emptied filter result drops back to page 1.
	_, body = e.get("/memories?q=row&size=25&page=2")
	rows := regexp.MustCompile(`name="ids" value="([^"]+)"`).FindAllStringSubmatch(body, -1)
	form = url.Values{"csrf": {e.csrf()}, "scope": {"selected"}, "view": {"q=row&size=25&page=2"}, "confirm_count": {strconv.Itoa(len(rows))}}
	for _, m := range rows {
		form.Add("ids", m[1])
	}
	resp, _ = e.post("/memories/delete", form)
	if got := resp.Header.Get("Location"); strings.Contains(got, "page=") {
		t.Fatalf("page 1 needs no page param: %q", got)
	}
}

func TestBulkDeleteConfirmShowsWhatGoes(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	ua := e.userBySub("g-alice")
	for i := 0; i < 8; i++ {
		e.write(ua.ID, "", fmt.Sprintf("tea fact number %d", i))
	}
	long := strings.Repeat("long ", 60)
	e.write(ua.ID, "", "tea "+long)
	_, body := e.post("/memories/delete", url.Values{"csrf": {e.csrf()}, "scope": {"matching"}, "match": {"tea"}})
	if !strings.Contains(body, "Delete 9 memories?") || !strings.Contains(body, "and 4 more") {
		t.Fatalf("title/more missing: %s", body)
	}
	if n := strings.Count(body, "tea fact number") + strings.Count(body, "tea long"); n != 5 {
		t.Errorf("want 5 snippets listed, got %d", n)
	}
	if strings.Contains(body, strings.TrimSpace(long)) {
		t.Error("long memory not truncated")
	}
}
