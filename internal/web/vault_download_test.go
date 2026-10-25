package web

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func (a *aEnv) download() (*http.Response, []byte) {
	a.t.Helper()
	resp, err := a.c2().Get(a.ts.URL + "/vault/download")
	if err != nil {
		a.t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, b
}

func (a *aEnv) c2() *http.Client { return a.env.c }

func TestDownloadZipEntriesSafeNames(t *testing.T) {
	a := newAEnv(t)
	a.seed("person", "../../etc/passwd", 3)
	a.seed("person", "Evil\x00Name\\x", 3)
	a.seed("organization", "Acme/Co", 3)
	resp, b := a.download()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "application/zip" ||
		!strings.Contains(resp.Header.Get("Content-Disposition"), "cortex-vault-") {
		t.Fatalf("headers: %d %v", resp.StatusCode, resp.Header)
	}
	files := readZip(t, b)
	if _, ok := files["index.md"]; !ok || len(files) < 4 {
		t.Fatalf("entries: %d", len(files))
	}
	for n := range files {
		if !safeZipName(n) || !strings.HasSuffix(n, ".md") {
			t.Errorf("unsafe entry %q", n)
		}
	}
	if a.callCount() != 0 {
		t.Fatal("download called the model")
	}
}

func TestDownloadZip300FilesUnderLimit(t *testing.T) {
	a := newAEnv(t)
	var first []seeded
	for i := 0; i < 300; i++ {
		s := a.seed("person", fmt.Sprintf("Person %03d", i), 3)
		if i < 5 {
			first = append(first, s)
		}
		if i < 100 { // many memories per article, and written articles with footnotes
			for j := 0; j < 20; j++ {
				a.mem(fmt.Sprintf("Extra fact %d about person %d, with some padding text to make it longer.", j, i), s.ID)
			}
		}
	}
	for _, s := range first { // some articles are written, the rest are stubs
		a.open(s.Path())
	}
	calls := a.callCount()
	resp, b := a.download()
	if resp.StatusCode != 200 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	files := readZip(t, b)
	if len(files) < 301 {
		t.Fatalf("files = %d, want at least 301", len(files))
	}
	if len(b) > 16<<20 {
		t.Fatalf("zip is %d bytes", len(b))
	}
	if a.callCount() != calls {
		t.Fatal("download called the model")
	}
}

func TestDownloadRateLimited(t *testing.T) {
	a := newAEnv(t)
	a.seed("person", "Ada", 3)
	var last int
	for i := 1; i <= 7; i++ {
		resp, _ := a.get("/vault/download")
		last = resp.StatusCode
		if i == 1 && last != 200 {
			t.Fatalf("first download: %d", last)
		}
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("7th download: %d, want 429", last)
	}
}

func TestDownloadLockedRedirects(t *testing.T) {
	a := newAEnv(t)
	a.seed("person", "Ada", 3)
	a.dropCookie(unlockCookie)
	resp, _ := a.get("/vault/download")
	loc := resp.Header.Get("Location")
	if resp.StatusCode != http.StatusSeeOther || !strings.HasPrefix(loc, "/unlock?next=") {
		t.Fatalf("locked: %d %q", resp.StatusCode, loc)
	}
}
