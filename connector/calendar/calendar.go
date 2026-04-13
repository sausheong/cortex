package calendar

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/sausheong/cortex"
	cal "google.golang.org/api/calendar/v3"
)

// Connector syncs events from Google Calendar into a Cortex knowledge graph.
// It expects a pre-built *calendar.Service — the caller handles OAuth2.
type Connector struct {
	service    *cal.Service
	calendarID string
}

// Option configures a Calendar Connector.
type Option func(*Connector)

// New creates a new Calendar Connector.
func New(service *cal.Service, opts ...Option) *Connector {
	c := &Connector{service: service, calendarID: "primary"}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// WithCalendarID sets the Google Calendar ID (default "primary").
func WithCalendarID(id string) Option {
	return func(c *Connector) { c.calendarID = id }
}

// syncState is the JSON state stored via Cortex sync state.
type syncState struct {
	SyncToken string    `json:"sync_token"`
	LastSync  time.Time `json:"last_sync"`
}

// Sync fetches events from Google Calendar and ingests them into the
// knowledge graph. On first run, it fetches events from the last 30 days.
// On subsequent runs, it uses a sync token for incremental sync.
func (c *Connector) Sync(ctx context.Context, cx *cortex.Cortex) error {
	// Load sync state.
	var state syncState
	raw, err := cx.GetSyncState(ctx, "calendar")
	if err != nil {
		return fmt.Errorf("calendar: get sync state: %w", err)
	}
	if raw != "" {
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			return fmt.Errorf("calendar: parse sync state: %w", err)
		}
	}

	var events []*cal.Event
	var nextSyncToken string

	if state.SyncToken != "" {
		// Incremental sync using sync token.
		call := c.service.Events.List(c.calendarID).SyncToken(state.SyncToken)
		if err := call.Pages(ctx, func(page *cal.Events) error {
			events = append(events, page.Items...)
			nextSyncToken = page.NextSyncToken
			return nil
		}); err != nil {
			return fmt.Errorf("calendar: incremental sync: %w", err)
		}
	} else {
		// First sync: list events from the last 30 days.
		timeMin := time.Now().Add(-30 * 24 * time.Hour).Format(time.RFC3339)
		call := c.service.Events.List(c.calendarID).
			TimeMin(timeMin).
			SingleEvents(true).
			OrderBy("startTime")
		if err := call.Pages(ctx, func(page *cal.Events) error {
			events = append(events, page.Items...)
			nextSyncToken = page.NextSyncToken
			return nil
		}); err != nil {
			return fmt.Errorf("calendar: initial sync: %w", err)
		}
	}

	// Process each event.
	for _, event := range events {
		if event.Status == "cancelled" {
			continue
		}

		// Create event entity.
		summary := event.Summary
		if summary == "" {
			summary = "(No title)"
		}

		eventEntity := &cortex.Entity{
			Type:   "event",
			Name:   summary,
			Source: "calendar",
		}
		if err := cx.PutEntity(ctx, eventEntity); err != nil {
			return fmt.Errorf("calendar: put event entity %q: %w", summary, err)
		}

		// Parse attendees and create person entities + relationships.
		attendees := ParseAttendees(event)
		for _, a := range attendees {
			personEntity := &cortex.Entity{
				Type:   "person",
				Name:   a.Name,
				Source: "calendar",
				Attributes: map[string]any{
					"email": a.Email,
				},
			}
			if err := cx.PutEntity(ctx, personEntity); err != nil {
				return fmt.Errorf("calendar: put person entity %q: %w", a.Name, err)
			}

			rel := &cortex.Relationship{
				SourceID: personEntity.ID,
				TargetID: eventEntity.ID,
				Type:     "attended",
				Source:   "calendar",
			}
			if err := cx.PutRelationship(ctx, rel); err != nil {
				return fmt.Errorf("calendar: put relationship: %w", err)
			}
		}

		// Build event text and remember it if there is description.
		start, end := parseEventTimes(event)
		attendeeNames := make([]string, len(attendees))
		for i, a := range attendees {
			attendeeNames[i] = a.Name
		}

		if event.Description != "" || len(attendees) > 0 {
			eventText := BuildEventText(summary, event.Description, start, end, attendeeNames)
			if err := cx.Remember(ctx, eventText, cortex.WithSource("calendar")); err != nil {
				return fmt.Errorf("calendar: remember event %q: %w", summary, err)
			}
		}
	}

	// Save sync state.
	syncStart := time.Now().UTC()
	newState := syncState{
		LastSync: syncStart,
	}
	if nextSyncToken != "" {
		newState.SyncToken = nextSyncToken
	}
	stateJSON, err := json.Marshal(newState)
	if err != nil {
		return fmt.Errorf("calendar: marshal sync state: %w", err)
	}
	if err := cx.SetSyncState(ctx, "calendar", string(stateJSON)); err != nil {
		return fmt.Errorf("calendar: set sync state: %w", err)
	}

	return nil
}

// BuildEventText assembles a calendar event into a text block suitable for ingestion.
func BuildEventText(summary, description string, start, end time.Time, attendees []string) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Event: %s\n", summary))
	if !start.IsZero() {
		sb.WriteString(fmt.Sprintf("Start: %s\n", start.Format(time.RFC3339)))
	}
	if !end.IsZero() {
		sb.WriteString(fmt.Sprintf("End: %s\n", end.Format(time.RFC3339)))
	}
	if len(attendees) > 0 {
		sb.WriteString(fmt.Sprintf("Attendees: %s\n", strings.Join(attendees, ", ")))
	}
	if description != "" {
		sb.WriteString(fmt.Sprintf("\n%s", description))
	}
	return sb.String()
}

// Attendee holds a parsed attendee's name and email.
type Attendee struct {
	Name  string
	Email string
}

// ParseAttendees extracts attendee information from a calendar event.
func ParseAttendees(event *cal.Event) []Attendee {
	if event.Attendees == nil {
		return nil
	}
	var attendees []Attendee
	for _, a := range event.Attendees {
		name := a.DisplayName
		if name == "" {
			// Use the local part of the email as the name.
			if at := strings.Index(a.Email, "@"); at > 0 {
				name = a.Email[:at]
			} else {
				name = a.Email
			}
		}
		attendees = append(attendees, Attendee{
			Name:  name,
			Email: a.Email,
		})
	}
	return attendees
}

// parseEventTimes extracts start and end times from a calendar event.
func parseEventTimes(event *cal.Event) (start, end time.Time) {
	if event.Start != nil {
		if event.Start.DateTime != "" {
			start, _ = time.Parse(time.RFC3339, event.Start.DateTime)
		} else if event.Start.Date != "" {
			start, _ = time.Parse("2006-01-02", event.Start.Date)
		}
	}
	if event.End != nil {
		if event.End.DateTime != "" {
			end, _ = time.Parse(time.RFC3339, event.End.DateTime)
		} else if event.End.Date != "" {
			end, _ = time.Parse("2006-01-02", event.End.Date)
		}
	}
	return start, end
}
