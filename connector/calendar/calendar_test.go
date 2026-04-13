package calendar

import (
	"testing"
	"time"

	cal "google.golang.org/api/calendar/v3"
)

func TestBuildEventText(t *testing.T) {
	start := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	end := time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC)
	attendees := []string{"Alice Smith", "Bob Jones"}

	result := BuildEventText(
		"Team Standup",
		"Daily standup meeting",
		start,
		end,
		attendees,
	)

	expected := "Event: Team Standup\n" +
		"Start: 2024-01-15T10:00:00Z\n" +
		"End: 2024-01-15T11:00:00Z\n" +
		"Attendees: Alice Smith, Bob Jones\n" +
		"\nDaily standup meeting"

	if result != expected {
		t.Errorf("BuildEventText:\ngot:\n%s\nwant:\n%s", result, expected)
	}
}

func TestBuildEventTextNoDescription(t *testing.T) {
	start := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	end := time.Date(2024, 1, 15, 11, 0, 0, 0, time.UTC)

	result := BuildEventText("Lunch", "", start, end, nil)

	expected := "Event: Lunch\n" +
		"Start: 2024-01-15T10:00:00Z\n" +
		"End: 2024-01-15T11:00:00Z\n"

	if result != expected {
		t.Errorf("BuildEventText:\ngot:\n%s\nwant:\n%s", result, expected)
	}
}

func TestBuildEventTextZeroTimes(t *testing.T) {
	result := BuildEventText("All Day Event", "No times specified", time.Time{}, time.Time{}, []string{"Alice"})

	expected := "Event: All Day Event\n" +
		"Attendees: Alice\n" +
		"\nNo times specified"

	if result != expected {
		t.Errorf("BuildEventText:\ngot:\n%s\nwant:\n%s", result, expected)
	}
}

func TestParseAttendees(t *testing.T) {
	event := &cal.Event{
		Attendees: []*cal.EventAttendee{
			{DisplayName: "Alice Smith", Email: "alice@example.com"},
			{DisplayName: "Bob Jones", Email: "bob@example.com"},
			{DisplayName: "", Email: "charlie@example.com"}, // no display name
		},
	}

	attendees := ParseAttendees(event)
	if len(attendees) != 3 {
		t.Fatalf("expected 3 attendees, got %d", len(attendees))
	}

	tests := []struct {
		idx   int
		name  string
		email string
	}{
		{0, "Alice Smith", "alice@example.com"},
		{1, "Bob Jones", "bob@example.com"},
		{2, "charlie", "charlie@example.com"}, // local part as name
	}

	for _, tt := range tests {
		if attendees[tt.idx].Name != tt.name {
			t.Errorf("attendee[%d].Name = %q, want %q", tt.idx, attendees[tt.idx].Name, tt.name)
		}
		if attendees[tt.idx].Email != tt.email {
			t.Errorf("attendee[%d].Email = %q, want %q", tt.idx, attendees[tt.idx].Email, tt.email)
		}
	}
}

func TestParseAttendeesNil(t *testing.T) {
	event := &cal.Event{}
	attendees := ParseAttendees(event)
	if attendees != nil {
		t.Errorf("expected nil attendees, got %v", attendees)
	}
}

func TestParseAttendeesEmpty(t *testing.T) {
	event := &cal.Event{
		Attendees: []*cal.EventAttendee{},
	}
	attendees := ParseAttendees(event)
	if len(attendees) != 0 {
		t.Errorf("expected 0 attendees, got %d", len(attendees))
	}
}
