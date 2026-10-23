package web

import (
	"archive/zip"
	"bytes"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/vault"
)

// vaultEnv signs alice in with a small graph: Alice Chen works at Stripe.
func vaultEnv(t *testing.T) (*env, *cortex.Cortex, string) {
	t.Helper()
	e, c, id := memEnv(t)
	ctx := t.Context()
	alice := &cortex.Entity{Type: "person", Name: "Alice Chen", Source: "notes/intro.md", Attributes: map[string]any{"role": "Engineer"}}
	stripe := &cortex.Entity{Type: "organization", Name: "Stripe"}
	for _, en := range []*cortex.Entity{alice, stripe} {
		if err := c.PutEntity(ctx, en); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.PutRelationship(ctx, &cortex.Relationship{SourceID: alice.ID, TargetID: stripe.ID, Type: "works_at"}); err != nil {
		t.Fatal(err)
	}
	m := &cortex.Memory{Content: "Alice joined Stripe", EntityIDs: []string{alice.ID, stripe.ID}, Source: "notes/intro.md", Confidence: 1}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	return e, c, id
}

func TestVaultRequiresSignIn(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/vault", "/vault/index.md", "/vault/download"} {
		resp, _ := e.get(p)
		if resp.StatusCode != http.StatusFound {
			t.Errorf("%s: status %d, want redirect to sign-in", p, resp.StatusCode)
		}
	}
}

func TestVaultListScopedAndFiltered(t *testing.T) {
	e, _, _ := vaultEnv(t)
	b, _ := e.root.CreateUser(t.Context(), "g-bob", "bob@x.test", "Bob", true)
	if err := e.root.ForUser(b.ID).PutEntity(t.Context(), &cortex.Entity{Type: "person", Name: "Secret Bob"}); err != nil {
		t.Fatal(err)
	}
	resp, body := e.get("/vault")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	for _, want := range []string{"people/alice-chen.md", "organizations/stripe.md", "index.md", "sources/notes-intro-md.md", "People", "Organisations"} {
		if !strings.Contains(body, want) {
			t.Errorf("list missing %q", want)
		}
	}
	if strings.Contains(body, "Secret Bob") || strings.Contains(body, "secret-bob") {
		t.Fatal("list shows another user's file")
	}
	// Index is first.
	if strings.Index(body, `href="/vault/index.md"`) > strings.Index(body, `href="/vault/organizations/stripe.md"`) {
		t.Error("index.md should come first")
	}
	_, body = e.get("/vault?q=stri")
	if !strings.Contains(body, "organizations/stripe.md") || strings.Contains(body, "people/alice-chen.md") {
		t.Errorf("filter wrong: %s", body)
	}
	_, body = e.get("/vault?folder=people")
	if !strings.Contains(body, "people/alice-chen.md") || strings.Contains(body, "organizations/stripe.md") {
		t.Errorf("folder wrong")
	}
	// Alice cannot open Bob's page even with the exact path.
	if resp, _ := e.get("/vault/people/secret-bob.md"); resp.StatusCode != 404 {
		t.Fatalf("foreign page: status %d, want 404", resp.StatusCode)
	}
}

func TestVaultSortAndPaging(t *testing.T) {
	e, c, _ := vaultEnv(t)
	for i := 0; i < 130; i++ {
		if err := c.PutEntity(t.Context(), &cortex.Entity{Type: "concept", Name: fmt.Sprintf("Topic %03d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	_, body := e.get("/vault?folder=concepts")
	if !strings.Contains(body, "concepts/topic-000.md") || strings.Contains(body, "concepts/topic-129.md") || !strings.Contains(body, "page=2") {
		t.Fatalf("page 1 wrong")
	}
	_, body = e.get("/vault?folder=concepts&page=2")
	if !strings.Contains(body, "concepts/topic-129.md") || strings.Contains(body, "concepts/topic-000.md") {
		t.Fatalf("page 2 wrong")
	}
	if _, body = e.get("/vault?sort=updated"); !strings.Contains(body, "Last updated") {
		t.Fatal("sort control missing")
	}
}

func TestVaultPageRendersLinksAndBacklinks(t *testing.T) {
	e, c, _ := vaultEnv(t)
	al, _ := c.FindEntities(t.Context(), cortex.EntityFilter{NameLike: "Alice Chen"})
	m := &cortex.Memory{Content: "Works with [[people/ghost|Ghost Person]] and see [[organizations/stripe|Stripe]]", EntityIDs: []string{al[0].ID}, Confidence: 1}
	if err := c.PutMemory(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	resp, body := e.get("/vault/people/alice-chen.md")
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if !strings.Contains(body, `<a class="wikilink" href="/vault/organizations/stripe.md">Stripe</a>`) {
		t.Errorf("resolved wikilink missing: %s", body)
	}
	if !strings.Contains(body, `<span class="unresolved" title="No page with this name">Ghost Person</span>`) || strings.Contains(body, `href="/vault/people/ghost`) {
		t.Errorf("unresolved link wrong")
	}
	if !strings.Contains(body, "Engineer") || !strings.Contains(body, "cortex_id") || !strings.Contains(body, `class="props"`) {
		t.Errorf("properties block missing")
	}
	if !strings.Contains(body, "/graph?entity="+al[0].ID) || !strings.Contains(body, "/memories?q=Alice&#43;Chen") {
		t.Errorf("graph/memories links missing")
	}
	_, body = e.get("/vault/organizations/stripe.md")
	if !strings.Contains(body, `id="bl-h">Backlinks`) || !strings.Contains(body, `href="/vault/people/alice-chen.md"`) || !strings.Contains(body, "works_at") {
		t.Errorf("backlinks missing: %s", body)
	}
	if strings.Count(body, "Backlinks") != 1 {
		t.Errorf("backlinks section duplicated")
	}
}

func TestVaultViewSourceIsExporterOutput(t *testing.T) {
	e, c, _ := vaultEnv(t)
	set, err := vault.Build(t.Context(), c, e.now)
	if err != nil {
		t.Fatal(err)
	}
	var want string
	for _, f := range set.Files {
		if f.Path == "people/alice-chen.md" {
			want = f.Content
		}
	}
	_, body := e.get("/vault/people/alice-chen.md?view=source")
	m := regexp.MustCompile(`(?s)<pre class="vault-source"[^>]*><code>(.*?)</code></pre>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatal("no source block")
	}
	got := html.UnescapeString(m[1])
	norm := regexp.MustCompile(`exported_at: \S+`)
	if norm.ReplaceAllString(got, "x") != norm.ReplaceAllString(want, "x") {
		t.Fatalf("source differs:\n%s\n---\n%s", got, want)
	}
}

func TestVaultXSSInert(t *testing.T) {
	e, c, _ := vaultEnv(t)
	al, _ := c.FindEntities(t.Context(), cortex.EntityFilter{NameLike: "Alice Chen"})
	evil := "<script>alert(1)</script> <img src=x onerror=alert(2)> [click](javascript:alert(3)) [[<b>x</b>|<script>alert(4)</script>]] <a href=\"javascript:alert(5)\">z</a>"
	if err := c.PutMemory(t.Context(), &cortex.Memory{Content: evil, EntityIDs: []string{al[0].ID}, Confidence: 1}); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/vault/people/alice-chen.md", "/vault/people/alice-chen.md?view=source", "/vault/index.md", "/vault"} {
		_, body := e.get(p)
		low := strings.ToLower(body)
		if regexp.MustCompile(`<[^>]+\sonerror=`).MatchString(low) {
			t.Errorf("%s: live onerror attribute", p)
		}
		for _, bad := range []string{"<script>alert", "<img src=x", "href=\"javascript", "<b>x</b>"} {
			if strings.Contains(low, bad) {
				t.Errorf("%s: contains %q", p, bad)
			}
		}
	}
}

func TestRenderMarkdownSafe(t *testing.T) {
	none := func(string) (string, bool) { return "", false }
	out := string(renderMarkdown("<script>x</script>\n\n[a](javascript:alert(1)) [b](https://ok.test) ![i](data:text/html,x)\n\n<https://auto.test>", none))
	if strings.Contains(out, "<script") || strings.Contains(out, "javascript:") || strings.Contains(out, "data:text") {
		t.Fatalf("unsafe output: %s", out)
	}
	if !strings.Contains(out, `rel="noopener noreferrer nofollow" href="https://ok.test"`) {
		t.Fatalf("external link lost: %s", out)
	}
}

func TestVaultTraversalIs404(t *testing.T) {
	e, _, _ := vaultEnv(t)
	for _, p := range []string{
		"/vault/..%2f..%2fetc%2fpasswd.md", "/vault/%2e%2e/%2e%2e/etc/passwd", "/vault/people/..%2f..%2findex.md.bak",
		"/vault/people/alice-chen", "/vault/people/alice-chen.md/", "/vault/PEOPLE/alice-chen.md", "/vault//people/alice-chen.md",
		"/vault/people%2f..%2fpeople%2falice-chen.md%00", "/vault/.cortex-export.json", "/vault/log.md", "/vault/_archive/x.md",
		"/vault/people/", "/vault/nonexistent.md",
	} {
		resp, body := e.get(p)
		if resp.StatusCode == 200 || strings.Contains(body, "root:") {
			t.Errorf("%s: status %d", p, resp.StatusCode)
		}
	}
	if resp, _ := e.get("/vault/people/alice-chen.md"); resp.StatusCode != 200 {
		t.Fatalf("control: %d", resp.StatusCode)
	}
}

func readZip(t *testing.T, b []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		d, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(d)
	}
	return out
}

func TestVaultZipMatchesExport(t *testing.T) {
	e, c, _ := vaultEnv(t)
	resp, err := e.c.Get(e.ts.URL + "/vault/download")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/zip" ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), "cortex-vault-") || resp.Header.Get("Content-Length") == "" {
		t.Fatalf("headers: %d %v", resp.StatusCode, resp.Header)
	}
	got := readZip(t, b)

	dir := t.TempDir()
	if _, err := vault.Export(t.Context(), c, vault.Options{VaultDir: dir}); err != nil {
		t.Fatal(err)
	}
	norm := regexp.MustCompile(`exported_at: \S+`)
	want := map[string]string{}
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		rel, _ := filepath.Rel(dir, p)
		if err != nil || info.IsDir() || rel == "log.md" || rel == vault.ManifestFilename {
			return nil
		}
		d, _ := os.ReadFile(p)
		want[filepath.ToSlash(rel)] = norm.ReplaceAllString(string(d), "exported_at: X")
		return nil
	})
	if len(got) != len(want) || len(got) < 4 {
		t.Fatalf("files: zip %d, export %d", len(got), len(want))
	}
	for p, w := range want {
		if g, ok := got[p]; !ok || norm.ReplaceAllString(g, "exported_at: X") != w {
			t.Errorf("%s differs or missing", p)
		}
	}
}

func TestVaultZipScopedAndRateLimited(t *testing.T) {
	e, _, _ := vaultEnv(t)
	b, _ := e.root.CreateUser(t.Context(), "g-bob", "bob@x.test", "Bob", true)
	_ = e.root.ForUser(b.ID).PutEntity(t.Context(), &cortex.Entity{Type: "person", Name: "Secret Bob"})
	_, body := e.get("/vault/download")
	for p, c := range readZip(t, []byte(body)) {
		if strings.Contains(p+c, "Secret Bob") {
			t.Fatalf("zip leaks bob in %s", p)
		}
	}
	var last int
	for i := 0; i < 5; i++ {
		resp, _ := e.get("/vault/download")
		last = resp.StatusCode
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("last status %d, want 429", last)
	}
}

func TestVaultCacheInvalidatesOnNewMemory(t *testing.T) {
	e, c, _ := vaultEnv(t)
	_, body := e.get("/vault/people/alice-chen.md")
	if strings.Contains(body, "loves climbing") {
		t.Fatal("precondition")
	}
	_, body = e.get("/vault/people/alice-chen.md") // served from cache
	al, _ := c.FindEntities(t.Context(), cortex.EntityFilter{NameLike: "Alice Chen"})
	if err := c.PutMemory(t.Context(), &cortex.Memory{Content: "loves climbing", EntityIDs: []string{al[0].ID}, Confidence: 1}); err != nil {
		t.Fatal(err)
	}
	_, body = e.get("/vault/people/alice-chen.md")
	if !strings.Contains(body, "loves climbing") {
		t.Fatalf("stale page after new memory")
	}
	// A brand-new entity shows up in the list too.
	_ = c.PutEntity(t.Context(), &cortex.Entity{Type: "event", Name: "Launch Day"})
	if _, body = e.get("/vault"); !strings.Contains(body, "events/launch-day.md") {
		t.Fatal("new entity missing from list")
	}
}

func TestRenderMarkdownEncodedSchemesAndImages(t *testing.T) {
	none := func(string) (string, bool) { return "", false }
	for _, src := range []string{
		"[y](&#106;avascript:1)", "[y](&#x6A;avascript:1)", "[y](jav&#x09;ascript:1)",
		"[y](JaVaScRiPt:1)", "[y](&#x64;ata:text/html,x)", "[y](vbscript:1)", "![y](&#106;avascript:1)",
	} {
		out := strings.ToLower(string(renderMarkdown(src, none)))
		for _, bad := range []string{"javascript", "jav&", "data:", "vbscript", "<img"} {
			if strings.Contains(out, bad) {
				t.Errorf("%q rendered %q (contains %q)", src, out, bad)
			}
		}
	}
	out := string(renderMarkdown("![x](https://evil.test/a.png?q=1)", none))
	if strings.Contains(out, "<img") || !strings.Contains(out, `href="https://evil.test/a.png?q=1"`) {
		t.Fatalf("image not turned into a link: %s", out)
	}
	if out := string(renderMarkdown("[a](/vault/x.md) [m](mailto:a@b.test)", none)); !strings.Contains(out, `href="/vault/x.md"`) || !strings.Contains(out, "mailto:") {
		t.Fatalf("safe links lost: %s", out)
	}
}

func TestRenderMarkdownAutolinks(t *testing.T) {
	none := func(string) (string, bool) { return "", false }
	for _, src := range []string{"<javascript:alert(1)>", "<JaVaScRiPt:alert(1)>", "<data:text/html;base64,AAA>", "<vbscript:x>"} {
		out := strings.ToLower(string(renderMarkdown(src, none)))
		if strings.Contains(out, "href") || strings.Contains(out, "<a") {
			t.Errorf("%q rendered live link: %s", src, out)
		}
	}
	out := string(renderMarkdown("<https://ok.test/x> <me@ok.test>", none))
	if !strings.Contains(out, `rel="noopener noreferrer nofollow" href="https://ok.test/x"`) || !strings.Contains(out, "mailto:me@ok.test") {
		t.Fatalf("safe autolinks lost: %s", out)
	}
}

func TestSafeZipName(t *testing.T) {
	for n, want := range map[string]bool{"people/a.md": true, "index.md": true, "../x/a.md": false, "/a.md": false,
		"a\\b.md": false, "a/../b.md": false, "a\x00.md": false, "a//b.md": false} {
		if safeZipName(n) != want {
			t.Errorf("safeZipName(%q) != %v", n, want)
		}
	}
}
