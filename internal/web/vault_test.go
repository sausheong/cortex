package web

import (
	"archive/zip"
	"bytes"
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
	_ = e.cx(b.ID).PutEntity(t.Context(), &cortex.Entity{Type: "person", Name: "Secret Bob"})
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
