package cortex

import (
	"strings"
	"testing"
	"time"
)

func TestRenderProfileMarkdown(t *testing.T) {
	p := Profile{
		Name:      "Alice",
		Static:    []string{"Staff engineer", "Likes Go"},
		Dynamic:   []string{"Working on payments"},
		BuiltAt:   time.Date(2026, 6, 27, 0, 0, 0, 0, time.UTC),
		Distilled: true,
	}
	md := RenderProfileMarkdown(p)
	for _, want := range []string{"# Profile: Alice", "## Static", "Staff engineer", "## Dynamic", "Working on payments"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}
}

func TestRenderProfileMarkdown_Empty(t *testing.T) {
	md := RenderProfileMarkdown(Profile{Name: "Bob"})
	if !strings.Contains(md, "# Profile: Bob") {
		t.Errorf("missing header in:\n%s", md)
	}
	if !strings.Contains(md, "_none_") {
		t.Errorf("expected _none_ placeholder for empty sections:\n%s", md)
	}
}

func TestRenderProfileReportMarkdown(t *testing.T) {
	md := RenderProfileReportMarkdown(ProfileReport{Scanned: 3, Rebuilt: 2, Skipped: []string{"x"}})
	for _, want := range []string{"Scanned: 3", "Rebuilt: 2", "Skipped: 1"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}
}
