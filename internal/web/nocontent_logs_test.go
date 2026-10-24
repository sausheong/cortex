package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/extractor/deterministic"
)

type unitEmb struct{}

func (unitEmb) Dimensions() int { return 512 }
func (unitEmb) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		v := make([]float32, 512)
		v[i%512] = 1
		out[i] = v
	}
	return out, nil
}

// TestNoContentInLogs runs enroll, unlock, remember, recall, search and the
// content pages for an encrypted user, with every logger captured, and
// asserts that no content, query, key material, session or unlock secret,
// request body field or next= target reaches a log line.
func TestNoContentInLogs(t *testing.T) {
	p := newPKWeb(t)
	en := p.enrollAndConfirm(t)
	sessionSecret := p.e.cookieVal(sessionCookie)
	unlockSecret := p.e.cookieVal(unlockCookie)

	var mu sync.Mutex
	var cl strings.Builder
	logf := func(f string, a ...any) {
		mu.Lock()
		defer mu.Unlock()
		cl.WriteString(fmt.Sprintf(f, a...))
		cl.WriteString("\n")
	}

	uid := p.e.userBySub("g-alice").ID
	ctx := t.Context()
	uk, err := p.e.st.UnlockWithRecovery(ctx, uid, en.recovery)
	if err != nil {
		t.Fatal(err)
	}
	defer uk.Zero()
	cx := p.e.root.ForUser(uid, cortex.WithLogger(logf), cortex.WithEmbedder(unitEmb{}), cortex.WithExtractor(deterministic.New())).
		WithKey(uk, "encrypted")
	defer cx.Release()

	const (
		secretMem   = "Zanzibar-quokka-7781 prefers marmalade on Tuesdays."
		secretQuery = "xylophone-query-5532"
	)
	if err := cx.Remember(ctx, secretMem); err != nil {
		t.Fatal(err)
	}
	_, _ = cx.Recall(ctx, secretQuery)
	_, _ = cx.SearchMemories(ctx, secretQuery, 5)
	_, _ = cx.SearchKeyword(ctx, secretQuery, 5)
	_, _ = cx.SearchVector(ctx, secretQuery, 5)
	_, _ = cx.SearchEntities(ctx, secretQuery, nil, "", 5)

	// Web pages with a query, a hostile next= and failed unlock attempts.
	p.e.get("/memories?q=" + url.QueryEscape(secretQuery))
	p.e.get("/graph")
	p.e.get("/vault")
	p.e.get("/account")
	p.e.get("/unlock?next=//leaky-next.test/x")
	p.e.post("/unlock/recovery", url.Values{"csrf": {p.csrf}, "recovery": {"WRONG-RECOVERY-KEY"}, "next": {"//leaky-next.test"}})
	_, ceremony, assertion, _ := p.unlockPrep(t, nil)
	badPRF := b64(make([]byte, 32))
	p.postJSON(t, "/unlock/finish", map[string]any{"ceremony": ceremony, "assertion": json.RawMessage(assertion), "prf_output": badPRF, "next": "//leaky-next.test"})
	p.postJSON(t, "/enroll/begin", map[string]any{})

	mu.Lock()
	all := p.logs.String() + "\n" + cl.String()
	mu.Unlock()
	needles := map[string]string{
		"memory content": secretMem, "memory token": "Zanzibar", "query": secretQuery,
		"prf": b64(en.prf), "recovery": en.recovery, "recovery nodash": strings.ReplaceAll(en.recovery, "-", ""),
		"session secret": sessionSecret, "unlock secret": unlockSecret, "wrong recovery": "WRONG-RECOVERY-KEY",
		"next target": "leaky-next", "next=": "next=", "ceremony": ceremony, "bad prf": badPRF, "uk bytes": string(uk.Bytes()),
	}
	for name, n := range needles {
		if n != "" && strings.Contains(all, n) {
			t.Errorf("logs contain %s (%q)", name, n)
		}
	}
}
