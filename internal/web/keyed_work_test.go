package web

import (
	"strings"
	"sync"
	"testing"

	"github.com/sausheong/cortex/internal/keys"
)

func TestKeyedRequestTriggersBackgroundHookWithLiveKey(t *testing.T) {
	e, _, secret, uk := encryptedSession(t)
	defer uk.Zero()
	e.setCookie(unlockCookie, secret)
	var mu sync.Mutex
	var clones []*keys.UK
	e.srv.opts.OnKeyed = func(userID string, k *keys.UK) {
		mu.Lock()
		defer mu.Unlock()
		if userID != e.userBySub("g-alice").ID || k == nil || k.Bytes() == nil {
			t.Errorf("hook got user=%q key=%v", userID, k)
		}
		clones = append(clones, k.Clone())
	}
	e.get("/memories")
	e.get("/")
	mu.Lock()
	defer mu.Unlock()
	if len(clones) != 2 {
		t.Fatalf("hook calls = %d, want 2 (memories and home)", len(clones))
	}
	for _, c := range clones {
		// The handler's own copy was zeroed on return; the hook's clone is not.
		if c.Bytes() == nil {
			t.Fatal("clone taken inside the hook died with the request")
		}
		c.Zero()
	}
}

func TestMemoriesShowsParkedBanner(t *testing.T) {
	e, _, secret, uk := encryptedSession(t)
	defer uk.Zero()
	e.setCookie(unlockCookie, secret)
	uid := e.userBySub("g-alice").ID
	if _, err := e.st.DB.Exec(`INSERT INTO jobs (id, user_id, kind, status) VALUES ('j_p', $1, 'remember', 'queued')`, uid); err != nil {
		t.Fatal(err)
	}
	_, body := e.get("/memories")
	if !strings.Contains(body, "Waiting for your next visit to finish processing") {
		t.Fatalf("parked banner missing: %.400s", body)
	}
	if strings.Contains(body, "—") {
		t.Fatal("em dash in page")
	}
}
