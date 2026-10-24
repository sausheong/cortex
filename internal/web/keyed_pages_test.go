package web

import (
	"strings"
	"testing"

	"github.com/sausheong/cortex"
)

// Regression (found by the Task 18 perf run): content pages built their
// handle with ForUser and no key, so every encrypted user got ErrLocked (a
// 500 on /vault) even with an unlocked session. Each content page must read
// an encrypted user's data through the session key.
func TestContentPagesReadEncryptedDataWithSessionKey(t *testing.T) {
	e, _, secret, uk := encryptedSession(t)
	defer uk.Zero()
	e.setCookie(unlockCookie, secret)
	uid := e.userBySub("g-alice").ID
	cx := e.root.ForUser(uid).WithKey(uk, "encrypted")
	defer cx.Release()
	ent := &cortex.Entity{Type: "person", Name: "Zxqgraphperson"}
	if err := cx.PutEntity(t.Context(), ent); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutMemory(t.Context(), &cortex.Memory{Content: "Zxqmemorytext is remembered", EntityIDs: []string{ent.ID}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ path, want string }{
		{"/memories", "Zxqmemorytext"},
		{"/vault", "Zxqgraphperson"},
		{"/graph", ""},
		{"/graph/data?center=" + ent.ID, "Zxqgraphperson"},
		{"/memories/deleted", ""},
		{"/", ""},
		{"/connections", ""},
		{"/account", ""},
	} {
		resp, body := e.get(c.path)
		if resp.StatusCode != 200 {
			t.Errorf("GET %s = %d %.120s", c.path, resp.StatusCode, body)
			continue
		}
		if c.want != "" && !strings.Contains(body, c.want) {
			t.Errorf("GET %s does not show the encrypted content %q", c.path, c.want)
		}
	}
}
