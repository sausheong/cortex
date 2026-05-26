package vault

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sausheong/cortex"
)

var fixedExportTime = time.Date(2026, 5, 26, 14, 32, 10, 0, time.UTC)

func TestRenderEntity_Full(t *testing.T) {
	ent := cortex.Entity{
		ID:        "ent_01H9X7K2M3N4P5Q6R7S8T9V0W1",
		Type:      "person",
		Name:      "Alice Chen",
		Source:    "notes/2026-04-02-coffee.md",
		Attributes: map[string]any{"role": "engineer", "team": "payments"},
		CreatedAt: time.Date(2026, 4, 1, 10, 23, 11, 0, time.UTC),
		UpdatedAt: time.Date(2026, 5, 20, 14, 55, 2, 0, time.UTC),
	}
	memories := []cortex.Memory{
		{Content: "Alice is joining Stripe next month", Source: "notes/2026-04-02-coffee.md"},
		{Content: "Worked at Square 2019–2024", Source: "linkedin-import"},
	}
	outRels := []resolvedRel{
		{Type: "works_at", OtherPath: "organizations/stripe", OtherName: "Stripe"},
		{Type: "knows", OtherPath: "people/bob-singh", OtherName: "Bob Singh"},
	}
	inRels := []resolvedRel{
		{Type: "attended", OtherPath: "events/2026-04-02-board-meeting", OtherName: "2026-04-02 board meeting"},
	}
	sources := []string{"2026-04-02-coffee-notes", "linkedin-import"}

	got := renderEntity(ent, memories, outRels, inRels, sources, fixedExportTime)
	assertGolden(t, "entity_full.golden.md", got)
}

func TestRenderEntity_MinimalSkipsEmptySections(t *testing.T) {
	ent := cortex.Entity{
		ID:        "ent_01HSOLO",
		Type:      "person",
		Name:      "Solo",
		CreatedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		UpdatedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
	}
	got := renderEntity(ent, nil, nil, nil, nil, fixedExportTime)
	assertGolden(t, "entity_minimal.golden.md", got)
}

// assertGolden compares `got` to the file at testdata/<name>.
// To regenerate goldens after an intentional render change: flip
// updateGoldens to true, run `go test ./vault/...`, then flip it back
// before committing. (Kept as a manual toggle to keep tests dependency-free.)
var updateGoldens = false

func assertGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if updateGoldens {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	if got != string(want) {
		t.Errorf("rendered output mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", name, got, string(want))
	}
}

func TestRenderSource(t *testing.T) {
	p := sourcePage{
		Source: "notes/2026-04-02-coffee.md",
		Entities: []sourceEntity{
			{Path: "people/alice-chen", Name: "Alice Chen"},
			{Path: "organizations/stripe", Name: "Stripe"},
		},
		Memories: []string{
			"Alice is joining Stripe next month",
			"Stripe is hiring aggressively in payments",
		},
	}
	got := renderSource(p, fixedExportTime)
	assertGolden(t, "source.golden.md", got)
}

func TestRenderIndex(t *testing.T) {
	groups := []indexGroup{
		{
			Heading: "People",
			Items: []indexItem{
				{Path: "people/alice-chen", Name: "Alice Chen", Summary: "engineer at payments"},
			},
		},
		{
			Heading: "Organizations",
			Items: []indexItem{
				{Path: "organizations/stripe", Name: "Stripe"},
			},
		},
	}
	got := renderIndex(groups, fixedExportTime)
	assertGolden(t, "index.golden.md", got)
}

func TestFormatLogLine(t *testing.T) {
	ts := time.Date(2026, 5, 26, 14, 32, 10, 0, time.UTC)
	got := formatLogLine(ts, "export", "47 pages written, 3 archived")
	want := "## [2026-05-26T14:32:10Z] export | 47 pages written, 3 archived\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
