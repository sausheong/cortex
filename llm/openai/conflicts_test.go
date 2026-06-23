package openai

import "testing"

func TestParseConflictsJSON(t *testing.T) {
	raw := `{"conflicts":[{"stale_id":"a","superseded_by_id":"b","reason":"budget changed"}]}`
	got, err := parseConflictsJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].StaleID != "a" || got[0].SupersededByID != "b" {
		t.Fatalf("unexpected parse: %+v", got)
	}
}

func TestParseConflictsJSON_CodeFence(t *testing.T) {
	raw := "```json\n{\"conflicts\":[]}\n```"
	got, err := parseConflictsJSON(raw)
	if err != nil {
		t.Fatalf("should strip code fence: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty, got %d", len(got))
	}
}
