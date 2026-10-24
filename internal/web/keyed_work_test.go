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

func TestHomeShowsMigrationStatus(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	uid := e.userBySub("g-alice").ID
	e.setProvider(uid)
	if _, err := e.st.DB.Exec(`UPDATE users SET encryption_state = 'migrating' WHERE id = $1`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.DB.Exec(`INSERT INTO jobs (id, user_id, kind, status, progress_done, progress_total) VALUES ('j_m', $1, 'encmigrate', 'running', 40, 1200)`, uid); err != nil {
		t.Fatal(err)
	}
	_, body := e.get("/")
	if !strings.Contains(body, "Encrypting your memory: 40 of 1,200") {
		t.Fatalf("migration line missing: %.600s", body)
	}
	e.st.DB.Exec(`UPDATE jobs SET status = 'queued' WHERE id = 'j_m'`)
	if _, body = e.get("/"); !strings.Contains(body, "waiting for your next visit") {
		t.Fatal("parked migration text missing")
	}
	e.st.DB.Exec(`UPDATE users SET encryption_state = 'none'`)
	if _, body = e.get("/"); strings.Contains(body, "Encrypting your memory") {
		t.Fatal("migration line shown for a non-migrating user")
	}
}
