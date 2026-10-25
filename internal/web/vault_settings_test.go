package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/sausheong/cortex/internal/vaultart"
)

func vaultForm(csrf, threshold string, connected bool, length string) url.Values {
	v := url.Values{"csrf": {csrf}, "threshold": {threshold}, "length": {length}}
	if connected {
		v.Set("include_connected", "on")
	}
	return v
}

func (a *aEnv) vaultSettings() vaultart.Settings {
	a.t.Helper()
	s, err := vaultart.LoadSettings(a.t.Context(), a.c.StoreHandle())
	if err != nil {
		a.t.Fatal(err)
	}
	return s
}

func TestSettingsVaultSectionDefaults(t *testing.T) {
	a := newAEnv(t)
	_, body := a.get("/settings")
	for _, want := range []string{
		`action="/settings/vault"`, `name="threshold"`, `value="3"`,
		"Include everything directly connected to me", `name="include_connected"`,
		"Article length", `value="short"`, `value="standard" selected`, `value="long"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if !regexp.MustCompile(`name="include_connected"[^>]* checked`).MatchString(body) {
		t.Error("include_connected should default to checked")
	}
	if strings.Contains(body, "articles written") || strings.Contains(body, "cannot write") {
		t.Error("usage or note shown on a fresh account")
	}
}

func TestSettingsVaultSaveChangesQualification(t *testing.T) {
	a := newAEnv(t)
	s := a.seed("person", "Zxqthresh Person", 3)
	a.open(s.Path())
	_, body := a.get("/vault")
	if !strings.Contains(body, "Zxqthresh Person") {
		t.Fatal("entry should show at the default threshold")
	}
	resp, _ := a.post("/settings/vault", vaultForm(a.csrf(), "5", true, "short"))
	if resp.StatusCode != http.StatusSeeOther || resp.Header.Get("Location") != "/settings?vault=saved#vault" {
		t.Fatalf("status %d loc %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	if got := a.vaultSettings(); got.Threshold != 5 || got.Length != "short" || !got.IncludeConnected {
		t.Fatalf("saved %+v", got)
	}
	_, body = a.get("/vault")
	if strings.Contains(body, "Zxqthresh Person") {
		t.Fatal("home should hide the entry below the raised threshold")
	}
	n, err := a.c.StoreHandle().ListArticleMeta(a.t.Context())
	if err != nil || len(n) != 1 {
		t.Fatalf("article rows: %d %v", len(n), err)
	}
	_, body = a.get("/settings?vault=saved")
	if !strings.Contains(body, "Vault settings saved.") {
		t.Error("saved notice missing")
	}
}

func TestSettingsVaultInvalidThresholdInline(t *testing.T) {
	a := newAEnv(t)
	for _, bad := range []string{"0", "51", "abc", ""} {
		resp, body := a.post("/settings/vault", vaultForm(a.csrf(), bad, false, "long"))
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%q: status %d", bad, resp.StatusCode)
		}
		if !strings.Contains(body, `id="err-threshold"`) || !strings.Contains(body, "whole number from 1 to 50") {
			t.Fatalf("%q: inline error missing", bad)
		}
		if !strings.Contains(body, `aria-invalid="true"`) {
			t.Errorf("%q: aria-invalid missing", bad)
		}
		if !strings.Contains(body, "Your previous Vault settings are unchanged.") {
			t.Errorf("%q: unchanged note missing", bad)
		}
	}
	if got := a.vaultSettings(); got != vaultart.DefaultSettings() {
		t.Fatalf("settings changed: %+v", got)
	}
}

func TestProviderSaveDoesNotWipeVaultSettings(t *testing.T) {
	a := newAEnv(t)
	if err := vaultart.SaveSettings(a.t.Context(), a.c.StoreHandle(),
		vaultart.Settings{Threshold: 7, IncludeConnected: false, Length: "long"}); err != nil {
		t.Fatal(err)
	}
	resp, body := a.post("/settings", settingsForm(a.csrf()))
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("provider save: %d %.200s", resp.StatusCode, body)
	}
	got := a.vaultSettings()
	if got.Threshold != 7 || got.IncludeConnected || got.Length != "long" {
		t.Fatalf("vault settings wiped: %+v", got)
	}
}

func TestSettingsVaultUsageLineCountsAttemptsAndFailures(t *testing.T) {
	a := newAEnv(t)
	h := a.c.StoreHandle()
	one := a.seed("person", "Zxqone Person", 3)
	two := a.seed("person", "Zxqtwo Person", 3)
	a.open(one.Path())
	a.open(two.Path()) // two real attempts, 15 tokens each, then pushed out below
	for i := 0; i < 8; i++ {
		if err := h.RecordWrite(a.t.Context(), 100, true); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := h.RecordWrite(a.t.Context(), 50, false); err != nil {
			t.Fatal(err)
		}
	}
	_, body := a.get("/settings")
	for _, want := range []string{"2 articles written", "about 900 tokens in the last 10 attempts", "2 failed"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in usage line", want)
		}
	}
}

func TestSettingsVaultTokensOmittedWhenZero(t *testing.T) {
	a := newAEnv(t)
	s := a.seed("person", "Zxqzero Person", 3)
	a.open(s.Path())
	h := a.c.StoreHandle()
	// Push the real (token-bearing) attempt out of the last ten.
	for i := 0; i < 10; i++ {
		if err := h.RecordWrite(a.t.Context(), 0, true); err != nil {
			t.Fatal(err)
		}
	}
	_, body := a.get("/settings")
	if !strings.Contains(body, "1 article written") {
		t.Fatalf("count missing: %s", body)
	}
	if strings.Contains(body, "tokens in the last") || strings.Contains(body, " failed") {
		t.Fatal("tokens or failed shown when zero")
	}
}

func TestSettingsVaultProviderCannotWriteNote(t *testing.T) {
	a := newAEnv(t)
	_, body := a.get("/settings")
	if strings.Contains(body, "cannot write articles") {
		t.Fatal("note shown while the model can write")
	}
	a.root.SetLLM(noWriter{a.mock})
	_, body = a.get("/settings")
	if !strings.Contains(body, "Your chat model cannot write articles. Choose another model above.") {
		t.Fatalf("note missing: %s", body)
	}
}

func TestSettingsVaultRequiresCSRF(t *testing.T) {
	a := newAEnv(t)
	resp, _ := a.post("/settings/vault", vaultForm("", "9", true, "long"))
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", resp.StatusCode)
	}
	if got := a.vaultSettings(); got != vaultart.DefaultSettings() {
		t.Fatalf("saved without csrf: %+v", got)
	}
}

func TestSettingsPagesCSPClean(t *testing.T) {
	a := newAEnv(t)
	a.root.SetLLM(noWriter{a.mock})
	pages := []string{"/settings", "/settings?vault=saved"}
	for _, p := range pages {
		resp, body := a.get(p)
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d", p, resp.StatusCode)
		}
		if styleAttr.MatchString(body) || strings.Contains(body, "<style") || handlerRe.MatchString(body) {
			t.Errorf("%s: inline style or handler", p)
		}
	}
	_, body := a.post("/settings/vault", vaultForm(a.csrf(), "x", true, "long"))
	if styleAttr.MatchString(body) || handlerRe.MatchString(body) {
		t.Error("error page has inline style or handler")
	}
}
