package accounts

import (
	"testing"
	"time"
)

func TestHourlyLimiter(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	l := NewHourlyLimiter(6, 6, func() time.Time { return now })
	for i := 0; i < 6; i++ {
		if !l.Allow("u") {
			t.Fatalf("burst call %d refused", i)
		}
	}
	if l.Allow("u") {
		t.Fatal("seventh call allowed")
	}
	if !l.Allow("other") {
		t.Fatal("keys are not independent")
	}
	// Six an hour is one every ten minutes.
	now = now.Add(9 * time.Minute)
	if l.Allow("u") {
		t.Fatal("allowed before a token refilled")
	}
	now = now.Add(2 * time.Minute)
	if !l.Allow("u") {
		t.Fatal("not allowed after a token refilled")
	}
	if l.Allow("u") {
		t.Fatal("second token appeared too soon")
	}
	// A full hour restores the whole burst, never more.
	now = now.Add(3 * time.Hour)
	for i := 0; i < 6; i++ {
		if !l.Allow("u") {
			t.Fatalf("refilled call %d refused", i)
		}
	}
	if l.Allow("u") {
		t.Fatal("burst exceeded after long idle")
	}
}
