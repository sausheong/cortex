package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/model"
)

func (a *aEnv) corrections(entityID string) []cortex.Memory {
	a.t.Helper()
	ms, _, err := a.c.ListMemories(a.t.Context(), cortex.MemoryListFilter{EntityID: entityID}, 200, 0)
	if err != nil {
		a.t.Fatal(err)
	}
	var out []cortex.Memory
	for _, m := range ms {
		if m.Source == model.SourceVaultCorrection {
			out = append(out, m)
		}
	}
	return out
}

func TestCorrectionFlowSavesAndShows(t *testing.T) {
	a := newAEnv(t)
	s := a.seed("person", "Zxqada Lovelace", 3)
	a.open(s.Path())
	resp, _ := a.post(s.Path()+"/correct", url.Values{"csrf": {a.csrf()}, "text": {"Ada now lives in Zxqporto."}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != s.Path()+"?note=saved" {
		t.Fatalf("status %d loc %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if n := len(a.corrections(s.ID)); n != 1 {
		t.Fatalf("corrections saved: %d", n)
	}
	// The next open rewrites with the correction in the prompt.
	_, body := a.get(s.Path() + "?note=saved")
	if !strings.Contains(body, "Saved. This article will be rewritten the next time you open it.") {
		t.Fatalf("flash missing on writing page: %s", body)
	}
	a.srv.art.Wait()
	_, body = a.get(s.Path())
	if !strings.Contains(body, "Correct this") || !strings.Contains(body, "Your corrections (1)") || !strings.Contains(body, "Ada now lives in Zxqporto.") {
		t.Fatalf("article lacks corrections: %s", body)
	}
	found := false
	for _, m := range a.mock.LastInput.Memories {
		found = found || (m.Correction && strings.Contains(m.Content, "Zxqporto"))
	}
	if !found {
		t.Fatal("correction did not reach the prompt")
	}
	for _, bad := range []string{`style="`, ` onclick=`, ` onsubmit=`} {
		if strings.Contains(body, bad) {
			t.Errorf("CSP: %q in page", bad)
		}
	}
}

func TestCorrectionWaitingNoteWhileStale(t *testing.T) {
	a := newAEnv(t)
	s := a.seed("person", "Zxqada Lovelace", 3)
	a.open(s.Path())
	a.mock.WriteArticleFn = func(ctx context.Context, system string, in cortex.ArticleInput, length string) (cortex.Article, cortex.ArticleUsage, error) {
		return cortex.Article{}, cortex.ArticleUsage{}, errors.New("down")
	}
	a.post(s.Path()+"/correct", url.Values{"csrf": {a.csrf()}, "text": {"A fix."}})
	_, body := a.open(s.Path())
	if !strings.Contains(body, "A correction is waiting to be included") {
		t.Fatalf("waiting note missing: %s", body)
	}
}

func TestCorrectionUndoViaWeb(t *testing.T) {
	a := newAEnv(t)
	s := a.seed("person", "Zxqada Lovelace", 3)
	a.open(s.Path())
	csrf := a.csrf()
	a.post(s.Path()+"/correct", url.Values{"csrf": {csrf}, "text": {"Ada likes Zxqtea."}})
	a.srv.art.Wait()
	ms := a.corrections(s.ID)
	if len(ms) != 1 {
		t.Fatalf("setup: %d", len(ms))
	}
	resp, _ := a.post(s.Path()+"/undo", url.Values{"csrf": {csrf}, "memory": {ms[0].ID}})
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != s.Path()+"?note=undone" {
		t.Fatalf("status %d loc %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if n := len(a.corrections(s.ID)); n != 0 {
		t.Fatalf("still live: %d", n)
	}
	// A foreign or ordinary memory is refused.
	resp, _ = a.post(s.Path()+"/undo", url.Values{"csrf": {csrf}, "memory": {s.Mems[0]}})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("ordinary memory: %d", resp.StatusCode)
	}
}

func TestCorrectionRequiresCSRFAndAuth(t *testing.T) {
	a := newAEnv(t)
	s := a.seed("person", "Zxqada Lovelace", 3)
	if resp, _ := a.post(s.Path()+"/correct", url.Values{"text": {"x"}}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no csrf: %d", resp.StatusCode)
	}
	if resp, _ := a.post(s.Path()+"/correct", url.Values{"csrf": {"wrong"}, "text": {"x"}}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bad csrf: %d", resp.StatusCode)
	}
	if n := len(a.corrections(s.ID)); n != 0 {
		t.Fatalf("saved without csrf: %d", n)
	}
	a.newClient()
	if resp, _ := a.post(s.Path()+"/correct", url.Values{"text": {"x"}}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", resp.StatusCode)
	}
}

func TestUndoRequiresCSRFAndAuth(t *testing.T) {
	a := newAEnv(t)
	s := a.seed("person", "Zxqada Lovelace", 3)
	a.post(s.Path()+"/correct", url.Values{"csrf": {a.csrf()}, "text": {"A fix."}})
	a.srv.art.Wait()
	ms := a.corrections(s.ID)
	if len(ms) != 1 {
		t.Fatalf("setup: %d", len(ms))
	}
	if resp, _ := a.post(s.Path()+"/undo", url.Values{"memory": {ms[0].ID}}); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no csrf: %d", resp.StatusCode)
	}
	if n := len(a.corrections(s.ID)); n != 1 {
		t.Fatal("undone without csrf")
	}
	a.newClient()
	if resp, _ := a.post(s.Path()+"/undo", url.Values{"memory": {ms[0].ID}}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("signed out: %d", resp.StatusCode)
	}
}

func TestCorrectRateLimited(t *testing.T) {
	a := newAEnv(t)
	s := a.seed("person", "Zxqada Lovelace", 3)
	csrf := a.csrf()
	limited := 0
	for i := 0; i < 40; i++ {
		resp, body := a.post(s.Path()+"/correct", url.Values{"csrf": {csrf}, "text": {"Fix number " + strings.Repeat("x", i+1)}})
		if resp.StatusCode == http.StatusTooManyRequests {
			limited++
			if !strings.Contains(body, "Try again in a little while") {
				t.Fatalf("message: %s", body)
			}
			break
		}
	}
	if limited == 0 {
		t.Fatal("never rate limited")
	}
	if n := len(a.corrections(s.ID)); n < 10 || n > 30 {
		t.Fatalf("saved before limit: %d", n)
	}
}

func TestCorrectionEmptyAndTooLongViaWeb(t *testing.T) {
	a := newAEnv(t)
	s := a.seed("person", "Zxqada Lovelace", 3)
	csrf := a.csrf()
	for _, txt := range []string{"", "   ", strings.Repeat("x", 2001)} {
		resp, _ := a.post(s.Path()+"/correct", url.Values{"csrf": {csrf}, "text": {txt}})
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%d chars: status %d", len(txt), resp.StatusCode)
		}
	}
	if n := len(a.corrections(s.ID)); n != 0 {
		t.Fatalf("saved: %d", n)
	}
}
