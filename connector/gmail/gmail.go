package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/mail"
	"strings"

	"github.com/sausheong/cortex"
	gm "google.golang.org/api/gmail/v1"
)

// Connector syncs emails from Gmail into a Cortex knowledge graph.
// It expects a pre-built *gmail.Service — the caller handles OAuth2.
type Connector struct {
	service    *gm.Service
	userID     string
	maxResults int64
}

// Option configures a Gmail Connector.
type Option func(*Connector)

// New creates a new Gmail Connector.
func New(service *gm.Service, opts ...Option) *Connector {
	c := &Connector{service: service, userID: "me", maxResults: 50}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// WithUserID sets the Gmail user ID (default "me").
func WithUserID(id string) Option {
	return func(c *Connector) { c.userID = id }
}

// WithMaxResults sets the maximum number of messages to fetch on initial sync.
func WithMaxResults(n int64) Option {
	return func(c *Connector) { c.maxResults = n }
}

// syncState is the JSON state stored via Cortex sync state.
type syncState struct {
	HistoryID uint64 `json:"history_id"`
}

// Sync fetches emails from Gmail and ingests them into the knowledge graph.
// On first run, it fetches recent messages. On subsequent runs, it uses the
// Gmail history API for incremental sync.
func (c *Connector) Sync(ctx context.Context, cx *cortex.Cortex) error {
	// Load sync state.
	var state syncState
	raw, err := cx.GetSyncState(ctx, "gmail")
	if err != nil {
		return fmt.Errorf("gmail: get sync state: %w", err)
	}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			return fmt.Errorf("gmail: parse sync state: %w", err)
		}
	}

	var messageIDs []string
	var latestHistoryID uint64

	if state.HistoryID == 0 {
		// First sync: list recent messages.
		resp, err := c.service.Users.Messages.List(c.userID).
			MaxResults(c.maxResults).Do()
		if err != nil {
			return fmt.Errorf("gmail: list messages: %w", err)
		}
		for _, m := range resp.Messages {
			messageIDs = append(messageIDs, m.Id)
		}
	} else {
		// Incremental sync via history.
		resp, err := c.service.Users.History.List(c.userID).
			StartHistoryId(state.HistoryID).
			HistoryTypes("messageAdded").Do()
		if err != nil {
			return fmt.Errorf("gmail: list history: %w", err)
		}
		for _, h := range resp.History {
			for _, m := range h.MessagesAdded {
				messageIDs = append(messageIDs, m.Message.Id)
			}
		}
		if resp.HistoryId > 0 {
			latestHistoryID = resp.HistoryId
		}
	}

	// Fetch and process each message.
	for _, msgID := range messageIDs {
		msg, err := c.service.Users.Messages.Get(c.userID, msgID).
			Format("full").Do()
		if err != nil {
			return fmt.Errorf("gmail: get message %s: %w", msgID, err)
		}

		// Track the latest history ID.
		if msg.HistoryId > latestHistoryID {
			latestHistoryID = msg.HistoryId
		}

		from := GetHeader(msg, "From")
		to := GetHeader(msg, "To")
		cc := GetHeader(msg, "Cc")
		subject := GetHeader(msg, "Subject")
		date := GetHeader(msg, "Date")
		body := ExtractBody(msg)

		// Create person entities from email addresses.
		allAddrs := from
		if to != "" {
			allAddrs += ", " + to
		}
		if cc != "" {
			allAddrs += ", " + cc
		}

		for _, raw := range strings.Split(allAddrs, ",") {
			raw = strings.TrimSpace(raw)
			if raw == "" {
				continue
			}
			name, email := ParseEmailAddress(raw)
			if email == "" {
				continue
			}
			e := &cortex.Entity{
				Type:   "person",
				Name:   name,
				Source: "gmail",
				Attributes: map[string]any{
					"email": email,
				},
			}
			if err := cx.PutEntity(ctx, e); err != nil {
				return fmt.Errorf("gmail: put entity %q: %w", name, err)
			}
		}

		// Build email text and remember it.
		emailText := BuildEmailText(from, to, subject, date, body)
		if err := cx.Remember(ctx, emailText, cortex.WithSource("gmail")); err != nil {
			return fmt.Errorf("gmail: remember email %s: %w", msgID, err)
		}
	}

	// Save sync state with latest history ID.
	if latestHistoryID > 0 {
		stateJSON, err := json.Marshal(syncState{HistoryID: latestHistoryID})
		if err != nil {
			return fmt.Errorf("gmail: marshal sync state: %w", err)
		}
		if err := cx.SetSyncState(ctx, "gmail", string(stateJSON)); err != nil {
			return fmt.Errorf("gmail: set sync state: %w", err)
		}
	}

	return nil
}

// ParseEmailAddress parses a raw email string (e.g. "Alice <alice@example.com>")
// and returns the display name and email address. If the name is empty, the
// local part of the email is used. For malformed input, the raw string is
// returned as the name with an empty email.
func ParseEmailAddress(raw string) (name, email string) {
	raw = strings.TrimSpace(raw)
	addr, err := mail.ParseAddress(raw)
	if err != nil {
		return raw, ""
	}
	email = addr.Address
	name = addr.Name
	if name == "" {
		// Use the local part of the email as the name.
		if at := strings.Index(email, "@"); at > 0 {
			name = email[:at]
		} else {
			name = email
		}
	}
	return name, email
}

// BuildEmailText assembles an email into a text block suitable for ingestion.
func BuildEmailText(from, to, subject, date, body string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("From: %s\n", from))
	sb.WriteString(fmt.Sprintf("To: %s\n", to))
	sb.WriteString(fmt.Sprintf("Subject: %s\n", subject))
	sb.WriteString(fmt.Sprintf("Date: %s\n", date))
	sb.WriteString("\n")
	sb.WriteString(body)
	return sb.String()
}

// ExtractBody extracts the text body from a Gmail message. It prefers
// text/plain parts, falls back to the message snippet.
func ExtractBody(msg *gm.Message) string {
	if msg.Payload == nil {
		return msg.Snippet
	}

	// Check top-level body if it has data.
	if msg.Payload.MimeType == "text/plain" && msg.Payload.Body != nil && msg.Payload.Body.Data != "" {
		if decoded, err := base64.URLEncoding.DecodeString(msg.Payload.Body.Data); err == nil {
			return string(decoded)
		}
	}

	// Walk parts looking for text/plain.
	for _, part := range msg.Payload.Parts {
		if part.MimeType == "text/plain" && part.Body != nil && part.Body.Data != "" {
			if decoded, err := base64.URLEncoding.DecodeString(part.Body.Data); err == nil {
				return string(decoded)
			}
		}
	}

	// Fallback to snippet.
	return msg.Snippet
}

// GetHeader retrieves a header value from a Gmail message by name.
func GetHeader(msg *gm.Message, name string) string {
	if msg.Payload == nil {
		return ""
	}
	for _, h := range msg.Payload.Headers {
		if strings.EqualFold(h.Name, name) {
			return h.Value
		}
	}
	return ""
}
