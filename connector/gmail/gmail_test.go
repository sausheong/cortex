package gmail

import (
	"encoding/base64"
	"testing"

	gm "google.golang.org/api/gmail/v1"
)

func TestParseEmailAddress(t *testing.T) {
	name, email := ParseEmailAddress("Alice Smith <alice@example.com>")
	if name != "Alice Smith" {
		t.Errorf("name = %q, want %q", name, "Alice Smith")
	}
	if email != "alice@example.com" {
		t.Errorf("email = %q, want %q", email, "alice@example.com")
	}
}

func TestParseEmailAddressNoName(t *testing.T) {
	name, email := ParseEmailAddress("alice@example.com")
	if name != "alice" {
		t.Errorf("name = %q, want %q", name, "alice")
	}
	if email != "alice@example.com" {
		t.Errorf("email = %q, want %q", email, "alice@example.com")
	}
}

func TestParseEmailAddressMalformed(t *testing.T) {
	name, email := ParseEmailAddress("not an email")
	if name != "not an email" {
		t.Errorf("name = %q, want %q", name, "not an email")
	}
	if email != "" {
		t.Errorf("email = %q, want empty", email)
	}
}

func TestBuildEmailText(t *testing.T) {
	result := BuildEmailText(
		"alice@example.com",
		"bob@example.com",
		"Hello",
		"Mon, 1 Jan 2024 12:00:00 +0000",
		"Hi Bob, how are you?",
	)

	expected := "From: alice@example.com\nTo: bob@example.com\nSubject: Hello\nDate: Mon, 1 Jan 2024 12:00:00 +0000\n\nHi Bob, how are you?"
	if result != expected {
		t.Errorf("BuildEmailText:\ngot:\n%s\nwant:\n%s", result, expected)
	}
}

func TestGetHeader(t *testing.T) {
	msg := &gm.Message{
		Payload: &gm.MessagePart{
			Headers: []*gm.MessagePartHeader{
				{Name: "From", Value: "alice@example.com"},
				{Name: "To", Value: "bob@example.com"},
				{Name: "Subject", Value: "Test Subject"},
				{Name: "Date", Value: "Mon, 1 Jan 2024 12:00:00 +0000"},
			},
		},
	}

	tests := []struct {
		header string
		want   string
	}{
		{"From", "alice@example.com"},
		{"To", "bob@example.com"},
		{"Subject", "Test Subject"},
		{"Date", "Mon, 1 Jan 2024 12:00:00 +0000"},
		{"from", "alice@example.com"}, // case insensitive
		{"X-Missing", ""},
	}

	for _, tt := range tests {
		got := GetHeader(msg, tt.header)
		if got != tt.want {
			t.Errorf("GetHeader(%q) = %q, want %q", tt.header, got, tt.want)
		}
	}
}

func TestGetHeaderNilPayload(t *testing.T) {
	msg := &gm.Message{}
	got := GetHeader(msg, "From")
	if got != "" {
		t.Errorf("GetHeader with nil payload = %q, want empty", got)
	}
}

func TestExtractBodyPlainText(t *testing.T) {
	body := "Hello, this is the email body."
	encoded := base64.URLEncoding.EncodeToString([]byte(body))

	msg := &gm.Message{
		Payload: &gm.MessagePart{
			MimeType: "text/plain",
			Body: &gm.MessagePartBody{
				Data: encoded,
			},
		},
	}

	got := ExtractBody(msg)
	if got != body {
		t.Errorf("ExtractBody = %q, want %q", got, body)
	}
}

func TestExtractBodyMultipart(t *testing.T) {
	plainBody := "Plain text body"
	encoded := base64.URLEncoding.EncodeToString([]byte(plainBody))

	msg := &gm.Message{
		Snippet: "snippet fallback",
		Payload: &gm.MessagePart{
			MimeType: "multipart/alternative",
			Parts: []*gm.MessagePart{
				{
					MimeType: "text/html",
					Body: &gm.MessagePartBody{
						Data: base64.URLEncoding.EncodeToString([]byte("<p>HTML body</p>")),
					},
				},
				{
					MimeType: "text/plain",
					Body: &gm.MessagePartBody{
						Data: encoded,
					},
				},
			},
		},
	}

	got := ExtractBody(msg)
	if got != plainBody {
		t.Errorf("ExtractBody = %q, want %q", got, plainBody)
	}
}

func TestExtractBodyFallsBackToSnippet(t *testing.T) {
	msg := &gm.Message{
		Snippet: "This is the snippet",
		Payload: &gm.MessagePart{
			MimeType: "multipart/mixed",
			Parts: []*gm.MessagePart{
				{
					MimeType: "text/html",
					Body: &gm.MessagePartBody{
						Data: base64.URLEncoding.EncodeToString([]byte("<p>HTML only</p>")),
					},
				},
			},
		},
	}

	got := ExtractBody(msg)
	if got != "This is the snippet" {
		t.Errorf("ExtractBody = %q, want %q", got, "This is the snippet")
	}
}

func TestExtractBodyNilPayload(t *testing.T) {
	msg := &gm.Message{Snippet: "fallback snippet"}
	got := ExtractBody(msg)
	if got != "fallback snippet" {
		t.Errorf("ExtractBody = %q, want %q", got, "fallback snippet")
	}
}
