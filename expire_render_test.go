package cortex

import (
	"strings"
	"testing"
	"time"
)

func TestRenderExpireMarkdown(t *testing.T) {
	r := ExpireReport{
		Scanned: 2, Expired: 2,
		Changes: []ExpireChange{
			{ID: "m1", Content: "meeting yesterday", ForgetAfter: time.Date(2026, 6, 26, 0, 0, 0, 0, time.UTC)},
		},
	}
	md := RenderExpireMarkdown(r)
	for _, want := range []string{"# Expire Report", "Scanned: 2", "Expired: 2", "meeting yesterday", "m1"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}
}

func TestRenderExpireMarkdown_DryRun(t *testing.T) {
	md := RenderExpireMarkdown(ExpireReport{DryRun: true})
	if !strings.Contains(md, "Dry run") {
		t.Errorf("expected dry-run note in:\n%s", md)
	}
}
