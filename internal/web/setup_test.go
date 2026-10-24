package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/sausheong/cortex/internal/model"
)

// makeLegacy moves the encryption-required marker into the future, so every
// existing user counts as created before it (the owner's situation).
func (e *env) makeLegacy() {
	e.t.Helper()
	if _, err := e.st.DB.Exec(`UPDATE cortex_meta SET value = (now() + interval '1 day')::text WHERE key = 'encryption_required_since'`); err != nil {
		e.t.Fatal(err)
	}
}

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
	if resp.StatusCode != http.StatusForbidden || !strings.Contains(body, want) {
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

func TestSignUpRacingMarkerIsNew(t *testing.T) {
	p := newPKWeb(t)
	uid := p.e.userBySub("g-alice").ID
	// The marker and the sign-up share one instant: at or after is new.
	if _, err := p.e.st.DB.Exec(`UPDATE cortex_meta SET value = (SELECT created_at::text FROM users WHERE id = $1) WHERE key = 'encryption_required_since'`, uid); err != nil {
		t.Fatal(err)
	}
	req, err := p.e.st.EnrollRequired(t.Context(), uid)
	if err != nil || !req {
		t.Fatalf("created at the marker instant: required=%v err=%v", req, err)
	}
	// One microsecond before the user was created is still new; one after is legacy.
	if _, err := p.e.st.DB.Exec(`UPDATE cortex_meta SET value = (SELECT (created_at + interval '1 microsecond')::text FROM users WHERE id = $1) WHERE key = 'encryption_required_since'`, uid); err != nil {
		t.Fatal(err)
	}
	if req, _ := p.e.st.EnrollRequired(t.Context(), uid); req {
		t.Fatal("a user created before the marker is legacy")
	}
}

func TestOwnerSeesBannerUntilEnrolled(t *testing.T) {
	p := newPKWeb(t)
	p.e.makeLegacy()
	uid := p.e.userBySub("g-alice").ID
	for _, path := range []string{"/", "/memories", "/connections", "/settings", "/account"} {
		r, body := p.e.get(path)
		if r.StatusCode != 200 || !strings.Contains(body, "not encrypted yet") || !strings.Contains(body, `href="/enroll"`) {
			t.Fatalf("%s: banner missing (%d)", path, r.StatusCode)
		}
	}
	// Not on the enrollment page itself, and the owner is never refused.
	if _, body := p.e.get("/enroll"); strings.Contains(body, "not encrypted yet") {
		t.Fatal("banner on the enrollment page")
	}
	if req, _ := p.e.st.EnrollRequired(t.Context(), uid); req {
		t.Fatal("legacy user must not be forced to enroll")
	}
	if _, body := p.e.get("/"); strings.Contains(body, "Protect your memory with a passkey") {
		t.Fatal("owner got the new-user checklist step")
	}
	p.enrollAndConfirm(t)
	for _, path := range []string{"/", "/memories", "/account"} {
		if _, body := p.e.get(path); strings.Contains(body, "not encrypted yet") {
			t.Fatalf("%s: banner after enrollment", path)
		}
	}
}

func TestNoBannerOrStepWhenEnrollmentOff(t *testing.T) {
	e := newEnv(t)
	e.signIn(alice(), "")
	for _, path := range []string{"/", "/account", "/memories"} {
		_, body := e.get(path)
		if strings.Contains(body, "not encrypted yet") || strings.Contains(body, "Protect your memory with a passkey") {
			t.Fatalf("%s shows enrollment UI with the flag off", path)
		}
	}
	if !strings.Contains(func() string { _, b := e.get("/"); return b }(), "0 of 3 done") {
		t.Fatal("checklist should keep three steps")
	}
	if req, _ := e.st.EnrollRequired(t.Context(), e.userBySub("g-alice").ID); req {
		t.Fatal("nobody is forced to enroll while enrollment is off")
	}
}

// A new account's owner entity is written in plaintext at sign-up. Enrollment
// seals it, or every read of the now-encrypted account would be refused.
func TestNewUserEnrollmentSealsOwnerEntity(t *testing.T) {
	p := newPKWeb(t)
	uid := p.e.userBySub("g-alice").ID
	p.enrollAndConfirm(t)
	var enc bool
	var name *string
	var ct []byte
	if err := p.e.st.DB.QueryRow(`SELECT enc, name, name_ct FROM entities WHERE user_id = $1 AND is_owner`, uid).Scan(&enc, &name, &ct); err != nil {
		t.Fatal(err)
	}
	if !enc || name != nil || len(ct) == 0 {
		t.Fatalf("owner entity not sealed: enc=%v name=%v ct=%d", enc, name, len(ct))
	}
	if r, _ := p.e.get("/memories"); r.StatusCode != 200 {
		t.Fatalf("/memories after enrollment: %d", r.StatusCode)
	}
}
