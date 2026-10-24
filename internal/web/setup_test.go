package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/sausheong/cortex/internal/model"
)

func TestNewUserSetupStep1IsPasskey(t *testing.T) {
	p := newPKWeb(t)
	_, body := p.e.get("/")
	i := strings.Index(body, "Protect your memory with a passkey")
	if i < 0 || !strings.Contains(body, "0 of 4 done") || !strings.Contains(body, `href="/enroll"`) {
		t.Fatalf("step 1 missing: %s", body)
	}
	// It is the first step, ahead of the provider step.
	if j := strings.Index(body, "Set up your AI provider"); j < i {
		t.Fatal("passkey step is not first")
	}
	// The later steps offer no way in until the passkey is set up.
	if strings.Contains(body, `href="/settings">Open settings`) || strings.Contains(body, `href="/connections">Connect one`) {
		t.Fatal("later steps are reachable before enrollment")
	}
	// No banner for a new user: the checklist is the way in.
	if strings.Contains(body, "not encrypted yet") {
		t.Fatal("new user got the owner banner")
	}
	p.enrollAndConfirm(t)
	r, body := p.e.get("/")
	if !strings.Contains(body, "1 of 4 done") {
		t.Fatalf("step not ticked after enrollment: %d %s", r.StatusCode, body)
	}
}

func TestNewUserCannotStoreBeforeEnrollment(t *testing.T) {
	p := newPKWeb(t)
	uid := p.e.userBySub("g-alice").ID
	want := "Protect your memory with a passkey before you save anything or connect an assistant."

	// Provider save.
	resp, body := p.e.post("/settings", settingsForm(p.csrf))
	if resp.StatusCode != http.StatusLocked || !strings.Contains(body, "protect your memory with a passkey first") {
		t.Fatalf("settings: %d %.300s", resp.StatusCode, body)
	}
	if ok, _ := p.e.st.HasSettings(t.Context(), uid); ok {
		t.Fatal("settings stored before enrollment")
	}
	// Connecting an assistant.
	resp, body = p.e.post("/connections/tokens", url.Values{"csrf": {p.csrf}, "name": {"laptop"}})
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, want) {
		t.Fatalf("token: %d %.300s", resp.StatusCode, body)
	}
	var n int
	p.e.st.DB.QueryRow(`SELECT count(*) FROM connections WHERE user_id = $1`, uid).Scan(&n)
	if n != 0 {
		t.Fatal("connection created before enrollment")
	}
	// Remember (the MCP path) goes through ResolveUser.
	_, err := p.e.st.ResolveUser(t.Context(), p.e.root, uid, "", nil)
	if err == nil || err.Error() != model.ErrEnrollRequired.Error() {
		t.Fatalf("ResolveUser: %v", err)
	}
	// After enrollment the same save is no longer refused for that reason.
	p.enrollAndConfirm(t)
	resp, body = p.e.post("/settings", settingsForm(p.csrf))
	if strings.Contains(body, want) || resp.StatusCode == http.StatusForbidden {
		t.Fatalf("still refused after enrollment: %d %.300s", resp.StatusCode, body)
	}
}

// Enrollment creates the owner entity, sealed.
func TestNewUserEnrollmentSealsOwnerEntity(t *testing.T) {
	p := newPKWeb(t)
	uid := p.e.userBySub("g-alice").ID
	p.enrollAndConfirm(t)
	var ct []byte
	if err := p.e.st.DB.QueryRow(`SELECT name_ct FROM entities WHERE user_id = $1 AND is_owner`, uid).Scan(&ct); err != nil {
		t.Fatal(err)
	}
	if len(ct) == 0 {
		t.Fatal("owner entity not sealed")
	}
	if r, _ := p.e.get("/memories"); r.StatusCode != 200 {
		t.Fatalf("/memories after enrollment: %d", r.StatusCode)
	}
}
