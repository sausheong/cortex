package web

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/sausheong/cortex/internal/vaultart"
)

var topicHref = regexp.MustCompile(`href="(/vault/topics/[^"]+)"`)

// seedTopic makes two concepts sharing enough memories to form a topic and
// returns the ids and the topic path read from Vault home.
func (a *aEnv) seedTopic(x, y string) (xid, yid, path string) {
	a.t.Helper()
	cx := a.seed("concept", x, 0)
	cy := a.seed("concept", y, 0)
	for i := 0; i < 3; i++ {
		a.mem("tea and coffee "+string(rune('a'+i)), cx.ID, cy.ID)
	}
	_, body := a.get("/vault")
	m := topicHref.FindStringSubmatch(body)
	if m == nil {
		a.t.Fatalf("no topic on home: %s", body)
	}
	return cx.ID, cy.ID, m[1]
}

func TestGraphEntityHasSeeArticleWhenQualifying(t *testing.T) {
	a := newAEnv(t)
	s := a.seed("person", "Zxqlink Person", 3)
	resp, body := a.get("/graph?center=" + s.ID)
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if !strings.Contains(body, `<a href="`+s.Path()+`">See article</a>`) {
		t.Fatalf("no See article link to %s: %s", s.Path(), body)
	}
	if n := a.callCount(); n != 0 {
		t.Fatalf("graph called the model %d times", n)
	}
}

func TestGraphEntityNoLinkWhenNotQualifying(t *testing.T) {
	a := newAEnv(t)
	s := a.seed("person", "Zxqquiet Person", 1)
	_, body := a.get("/graph?center=" + s.ID)
	if !strings.Contains(body, "Zxqquiet Person") {
		t.Fatalf("panel missing: %s", body)
	}
	if strings.Contains(body, "See article") || strings.Contains(body, `href="/vault/`) {
		t.Fatalf("link for a non-qualifying entity: %s", body)
	}
}

func TestGraphConceptLinksToTopicArticle(t *testing.T) {
	a := newAEnv(t)
	xid, yid, path := a.seedTopic("Zxqtea", "Zxqcoffee")
	for _, id := range []string{xid, yid} {
		_, body := a.get("/graph?center=" + id)
		if !strings.Contains(body, `<a href="`+path+`">See article</a>`) {
			t.Fatalf("concept %s does not link the topic %s: %s", id, path, body)
		}
	}
}

func TestMemoriesSeeArticleLink(t *testing.T) {
	a := newAEnv(t)
	s := a.seed("person", "Zxqmem Person", 3)
	low := a.seed("person", "Zxqmemlow Person", 1)
	_, body := a.get("/memories?entity=" + s.ID)
	if !strings.Contains(body, `<a href="`+s.Path()+`">See article</a>`) {
		t.Fatalf("no link: %s", body)
	}
	_, body = a.get("/memories?entity=" + low.ID)
	if strings.Contains(body, "See article") {
		t.Fatalf("link for non-qualifying entity: %s", body)
	}
	_, body = a.get("/memories")
	if strings.Contains(body, "See article") {
		t.Fatal("link on the unfiltered list")
	}
	xid, _, path := a.seedTopic("Zxqtea", "Zxqcoffee")
	_, body = a.get("/memories?entity=" + xid)
	if !strings.Contains(body, `<a href="`+path+`">See article</a>`) {
		t.Fatalf("topic member does not link its topic: %s", body)
	}
}

var legacyMdLink = regexp.MustCompile(`(?i)(href|src|action)="[^"]*/vault/[^"]*\.md[^"]*"`)

func TestNoLegacyVaultMdLinksAnywhere(t *testing.T) {
	a := newAEnv(t)
	s := a.seed("person", "Zxqmd Person", 3)
	xid, _, _ := a.seedTopic("Zxqtea", "Zxqcoffee")
	owner := a.ownerPath()
	for _, p := range []string{"/graph", "/graph?center=" + s.ID, "/graph?center=" + xid, "/graph?center=" + owner.ID,
		"/graph?q=Zxqmd", "/memories", "/memories?entity=" + s.ID, "/memories?entity=" + xid} {
		resp, body := a.get(p)
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d", p, resp.StatusCode)
		}
		if m := legacyMdLink.FindString(body); m != "" {
			t.Errorf("%s has a legacy .md vault link: %s", p, m)
		}
		if strings.Contains(body, `.md"`) && strings.Contains(body, "/vault/") {
			t.Errorf("%s mentions a .md vault path", p)
		}
	}
	// The static script builds no vault links either.
	resp, js := a.get("/static/graph.js")
	if resp.StatusCode == 200 && strings.Contains(js, "/vault") {
		t.Error("graph.js builds vault links")
	}
}

func (a *aEnv) articleRows() int {
	a.t.Helper()
	rows, err := a.c.StoreHandle().ListArticleMeta(a.t.Context())
	if err != nil {
		a.t.Fatal(err)
	}
	return len(rows)
}

func TestMergeInvalidatesKeepArticleDroppedPath404(t *testing.T) {
	a := newAEnv(t)
	keep := a.seed("person", "Zxqkeep Robert", 3)
	drop := a.seed("person", "Zxqdrop Bobby", 3)
	a.open(keep.Path())
	a.open(drop.Path())
	if n := a.callCount(); n != 2 {
		t.Fatalf("setup calls = %d", n)
	}
	csrf := a.csrf()
	_, body := a.post("/graph/merge", url.Values{"csrf": {csrf}, "keep": {keep.ID}, "drop": {drop.ID}})
	m := regexp.MustCompile(`name="confirm_count" value="(\d+)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no confirm count: %s", body)
	}
	resp, _ := a.post("/graph/merge", url.Values{"csrf": {csrf}, "keep": {keep.ID}, "drop": {drop.ID}, "confirm_count": {m[1]}})
	if resp.StatusCode != 303 {
		t.Fatalf("merge: %d", resp.StatusCode)
	}
	if resp, _ := a.get(drop.Path()); resp.StatusCode != 404 {
		t.Fatalf("dropped path: %d", resp.StatusCode)
	}
	before := a.callCount()
	_, body = a.open(keep.Path())
	if got := a.callCount() - before; got != 1 {
		t.Fatalf("keep rewrote %d times, want 1", got)
	}
	if !strings.Contains(body, "A short lead about Zxqkeep Robert.") {
		t.Fatalf("keep article missing: %s", body)
	}
	// Opening again changes nothing.
	a.open(keep.Path())
	if got := a.callCount() - before; got != 1 {
		t.Fatalf("second open called the model again: %d", got)
	}
}

func TestDeleteEntityInvalidatesArticle(t *testing.T) {
	a := newAEnv(t)
	s := a.seed("person", "Zxqdel Person", 3)
	a.open(s.Path())
	csrf := a.csrf()
	_, body := a.post("/graph/delete", url.Values{"csrf": {csrf}, "id": {s.ID}})
	m := regexp.MustCompile(`name="confirm_count" value="(\d+)"`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no confirm count: %s", body)
	}
	if resp, _ := a.post("/graph/delete", url.Values{"csrf": {csrf}, "id": {s.ID}, "confirm_count": {m[1]}}); resp.StatusCode != 303 {
		t.Fatalf("delete: %d", resp.StatusCode)
	}
	if resp, _ := a.get(s.Path()); resp.StatusCode != 404 {
		t.Fatalf("deleted subject path: %d", resp.StatusCode)
	}
	if _, home := a.get("/vault"); strings.Contains(home, "Zxqdel Person") {
		t.Fatal("deleted subject still on Vault home")
	}
	// A soft-deleted entity can be restored, so its article row stays until
	// the entity is purged for good.
	if n, err := a.c.StoreHandle().PurgeOrphanArticles(t.Context(), a.c.UserID()); err != nil || n != 0 {
		t.Fatalf("purge removed a restorable article: %d %v", n, err)
	}
}

func TestThresholdDropHidesButKeepsCachedArticle(t *testing.T) {
	a := newAEnv(t)
	s := a.seed("person", "Zxqkept Person", 3)
	a.open(s.Path())
	calls := a.callCount()
	meta, _ := a.c.StoreHandle().ListArticleMeta(t.Context())
	if len(meta) != 1 {
		t.Fatalf("rows = %d", len(meta))
	}
	written := meta[0].WrittenAt

	set := vaultart.DefaultSettings()
	set.Threshold = 5
	if err := vaultart.SaveSettings(t.Context(), a.c.StoreHandle(), set); err != nil {
		t.Fatal(err)
	}
	if _, home := a.get("/vault"); strings.Contains(home, "Zxqkept Person") {
		t.Fatal("entry still on home above the threshold")
	}
	if resp, _ := a.get(s.Path()); resp.StatusCode != 404 {
		t.Fatalf("direct URL above threshold: %d", resp.StatusCode)
	}
	if n := a.articleRows(); n != 1 {
		t.Fatalf("cached row dropped: %d", n)
	}

	set.Threshold = 3
	if err := vaultart.SaveSettings(t.Context(), a.c.StoreHandle(), set); err != nil {
		t.Fatal(err)
	}
	resp, body := a.get(s.Path())
	if resp.StatusCode != 200 || !strings.Contains(body, "A short lead about Zxqkept Person.") {
		t.Fatalf("restored threshold: %d %s", resp.StatusCode, body)
	}
	a.srv.art.Wait()
	if a.callCount() != calls {
		t.Fatalf("restoring the threshold called the model (%d -> %d)", calls, a.callCount())
	}
	meta, _ = a.c.StoreHandle().ListArticleMeta(t.Context())
	if len(meta) != 1 || !meta[0].WrittenAt.Equal(written) {
		t.Fatal("article was rewritten")
	}
}

func TestVanishedTopicArticleIs404AndPurged(t *testing.T) {
	a := newAEnv(t)
	xid, yid, path := a.seedTopic("Zxqtea", "Zxqcoffee")
	a.open(path)
	if n := a.articleRows(); n != 1 {
		t.Fatalf("topic article not written: %d rows", n)
	}
	for _, id := range []string{xid, yid} {
		if _, err := a.c.SoftDeleteEntity(t.Context(), id, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if resp, _ := a.get(path); resp.StatusCode != 404 {
		t.Fatalf("vanished topic path: %d", resp.StatusCode)
	}
	if n, err := a.c.StoreHandle().PurgeOrphanArticles(t.Context(), a.c.UserID()); err != nil || n != 1 {
		t.Fatalf("purge = %d, %v", n, err)
	}
	if n := a.articleRows(); n != 0 {
		t.Fatalf("rows left: %d", n)
	}
}
