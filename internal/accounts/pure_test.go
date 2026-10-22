package accounts

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSealRoundTrip(t *testing.T) {
	s, _ := NewSealer(testKeyHex)
	a, err := s.Seal("sk-hello")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.Seal("sk-hello")
	if a == b || strings.Contains(a, "sk-hello") {
		t.Fatal("nonce must be random and plaintext hidden")
	}
	got, err := s.Open(a)
	if err != nil || got != "sk-hello" {
		t.Fatalf("got %q %v", got, err)
	}
	if _, err := NewSealer("abcd"); err == nil {
		t.Fatal("short key accepted")
	}
}

func TestSealWrongKeyFails(t *testing.T) {
	s, _ := NewSealer(testKeyHex)
	o, _ := NewSealer(strings.Repeat("ab", 32))
	a, _ := s.Seal("x")
	if _, err := o.Open(a); err == nil {
		t.Fatal("wrong key opened")
	}
}

func TestSettingsMasked(t *testing.T) {
	m := Settings{APIKey: "sk-abcdefg3f9a", EmbedAPIKey: "ek-zzzz1234"}.Masked()
	if m.APIKey != "••••3f9a" || m.EmbedAPIKey != "••••1234" {
		t.Fatalf("%q %q", m.APIKey, m.EmbedAPIKey)
	}
}

func TestSettingsValidate(t *testing.T) {
	bad := []string{"http://api.example.com/v1", "https://10.0.0.5/v1", "https://127.0.0.1/v1", "https://169.254.169.254/"}
	for _, u := range bad {
		if err := (Settings{BaseURL: u}).Validate(false); err == nil {
			t.Errorf("%s accepted", u)
		}
		if err := (Settings{BaseURL: "https://ok.example.com", EmbedBaseURL: u}).Validate(false); err == nil {
			t.Errorf("embed %s accepted", u)
		}
	}
	if err := (Settings{BaseURL: "https://ok.example.com", EmbedDims: 100}).Validate(false); err == nil {
		t.Error("dims 100 accepted")
	}
	if err := (Settings{BaseURL: "https://ok.example.com", EmbedDims: 1536}).Validate(false); err != nil {
		t.Errorf("dims 1536: %v", err)
	}
	if err := (Settings{BaseURL: "http://localhost:11434/v1"}).Validate(true); err != nil {
		t.Errorf("allowPrivate: %v", err)
	}
	if err := (Settings{Provider: "bogus"}).Validate(false); err == nil {
		t.Error("bogus provider accepted")
	}
}

func TestClientIPUsesXFFOnlyFromLoopback(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "10.0.0.9:5555"
	r.Header.Set("X-Forwarded-For", "1.2.3.4")
	if got := ClientIP(r); got != "10.0.0.9" {
		t.Fatalf("got %s", got)
	}
	r.RemoteAddr = "127.0.0.1:5555"
	r.Header.Set("X-Forwarded-For", "9.9.9.9, 1.2.3.4")
	if got := ClientIP(r); got != "1.2.3.4" {
		t.Fatalf("got %s", got)
	}
}

func TestLimiterBlocksAfterBurst(t *testing.T) {
	now := time.Unix(1000, 0)
	l := NewLimiter(30, 3, func() time.Time { return now })
	for i := 0; i < 3; i++ {
		if !l.Allow("k") {
			t.Fatalf("burst %d denied", i)
		}
	}
	if l.Allow("k") {
		t.Fatal("4th allowed")
	}
	if !l.Allow("other") {
		t.Fatal("other key denied")
	}
	now = now.Add(time.Minute)
	if !l.Allow("k") {
		t.Fatal("no refill after a minute")
	}
}

var _ = http.StatusOK
