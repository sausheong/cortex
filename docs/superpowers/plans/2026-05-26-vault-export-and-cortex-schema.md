# Vault Export and CORTEX.md Schema Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `cortex export` (graph → Obsidian-compatible markdown vault) and `cortex init-schema` (copies a generic CORTEX.md agent contract) commands.

**Architecture:** New `vault/` package owns rendering and manifest management. It depends only on cortex's existing public API (`FindEntities`, `GetRelationships`, `GetMemoriesByEntity`). The CLI delegates to it from new `cmd/cortex/export.go` and a sibling `init_schema.go` that uses `//go:embed` for the static CORTEX.md template. No changes to the core `cortex` package or storage schema.

**Tech Stack:** Go 1.25.1, `encoding/json` (manifest), `crypto/sha256` (change detection), `embed` (static template), `gopkg.in/yaml.v3` if needed for frontmatter (will use a hand-rolled minimal YAML writer to avoid new dependency — see Task 3). Existing repo testing pattern: `go test ./...` with table-driven tests.

**Spec:** `docs/superpowers/specs/2026-05-26-vault-export-and-cortex-schema-design.md`

---

## File Structure

**New files:**

| File | Responsibility |
|---|---|
| `vault/layout.go` | `slug()`, type-to-folder map, collision-disambiguated filenames |
| `vault/layout_test.go` | Unit tests for layout |
| `vault/manifest.go` | `Manifest` struct, JSON load/save, `PageEntry` |
| `vault/manifest_test.go` | Round-trip, corrupt-file handling |
| `vault/render.go` | `renderEntity()`, `renderSource()`, `renderIndex()`, log line formatter |
| `vault/render_test.go` | Golden-file tests for each renderer |
| `vault/testdata/*.md` | Golden expected outputs |
| `vault/vault.go` | `Options`, `Stats`, `Export()` orchestration |
| `vault/vault_test.go` | Integration tests against in-memory cortex |
| `cmd/cortex/export.go` | `cmdExport()` — flag parsing, calls `vault.Export()` |
| `cmd/cortex/export_test.go` | CLI flag-parsing test |
| `cmd/cortex/init_schema.go` | `cmdInitSchema()` + embedded template |
| `cmd/cortex/init_schema_test.go` | Overwrite-refusal test |
| `docs/CORTEX.md` | Static generic agent contract (embedded into binary) |

**Modified files:**

| File | Change |
|---|---|
| `cmd/cortex/main.go` | Add `export` and `init-schema` cases to the command switch; update `printUsage()` |
| `README.md` | Add a short section documenting the two new commands |

---

## Task 1: Layout — slug, folder map, collision-disambiguated filenames

**Files:**
- Create: `vault/layout.go`
- Test: `vault/layout_test.go`

- [ ] **Step 1: Write the failing tests**

Create `vault/layout_test.go`:

```go
package vault

import "testing"

func TestSlug(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"simple", "Alice Chen", "alice-chen"},
		{"punctuation", "O'Brien, Inc.", "o-brien-inc"},
		{"runs collapsed", "foo    bar___baz", "foo-bar-baz"},
		{"trim edges", "---hello---", "hello"},
		{"unicode kept lowercased", "Café", "café"},
		{"all symbols falls back to empty", "!!!", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := slug(tt.in)
			if got != tt.want {
				t.Errorf("slug(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFolderForType(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"person", "people"},
		{"organization", "organizations"},
		{"concept", "concepts"},
		{"event", "events"},
		{"document", "documents"},
		{"project", "projects"},     // unknown → +s
		{"Person", "people"},        // case-insensitive lookup
	}
	for _, tt := range tests {
		got := folderForType(tt.in)
		if got != tt.want {
			t.Errorf("folderForType(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPageFilename_NoCollision(t *testing.T) {
	got := pageFilename("Alice Chen", "ent_01H9X7K2M3N4P5Q6R7S8T9V0W1", false)
	if got != "alice-chen.md" {
		t.Errorf("got %q, want alice-chen.md", got)
	}
}

func TestPageFilename_Collision(t *testing.T) {
	// When colliding=true, every page gets the suffix.
	a := pageFilename("Java", "ent_01HAAAAAAAAAAAAAAAAAAAAAA1", true)
	b := pageFilename("Java", "ent_01HBBBBBBBBBBBBBBBBBBBBBB2", true)
	if a == b {
		t.Fatalf("colliding entities got same filename: %q", a)
	}
	if a != "java-01haaa.md" {
		t.Errorf("a = %q, want java-01haaa.md", a)
	}
	if b != "java-01hbbb.md" {
		t.Errorf("b = %q, want java-01hbbb.md", b)
	}
}

func TestPageFilename_EmptySlugFallsBackToID(t *testing.T) {
	got := pageFilename("!!!", "ent_01H9X7K2M3N4P5Q6R7S8T9V0W1", false)
	if got != "ent_01h9.md" {
		t.Errorf("got %q, want ent_01h9.md", got)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./vault/...`
Expected: build error — package `vault` does not exist.

- [ ] **Step 3: Implement `vault/layout.go`**

```go
// Package vault projects a cortex knowledge graph onto a browsable
// markdown vault (Obsidian-compatible). It owns rendering and an
// on-disk manifest that tracks which entity rendered to which file.
//
// vault is a one-way projection: cortex remains the source of truth.
// Edits to vault files are never read back.
package vault

import (
	"strings"
	"unicode"
)

// slug normalizes a human name into a filename-safe slug.
// Rules: lowercase, runs of non-alphanumeric → single '-', trim '-'.
// Returns empty string if the input has no alphanumeric content;
// callers should fall back to entity ID in that case.
func slug(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s))
	prevDash := true // suppress leading '-'
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

// typeFolderMap is the canonical pluralization for built-in entity types.
// Keys are lowercased.
var typeFolderMap = map[string]string{
	"person":       "people",
	"organization": "organizations",
	"concept":      "concepts",
	"event":        "events",
	"document":     "documents",
}

// folderForType returns the vault subdirectory name for an entity type.
// Unknown types fall through to lowercased(type) + "s".
func folderForType(t string) string {
	lc := strings.ToLower(t)
	if f, ok := typeFolderMap[lc]; ok {
		return f
	}
	return lc + "s"
}

// pageFilename builds the markdown filename for an entity.
// If colliding is true, the short ID suffix is always appended; this
// is how the exporter keeps colliding names stable across runs
// (every page in a collision set gets a suffix, never just some).
func pageFilename(name, id string, colliding bool) string {
	base := slug(name)
	if base == "" {
		// Fall back to first 8 chars of ID (lowercased).
		base = shortID(id, 8)
		return base + ".md"
	}
	if colliding {
		return base + "-" + shortID(id, 6) + ".md"
	}
	return base + ".md"
}

func shortID(id string, n int) string {
	id = strings.ToLower(id)
	if len(id) <= n {
		return id
	}
	return id[:n]
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./vault/... -v`
Expected: all 4 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add vault/layout.go vault/layout_test.go
git commit -m "$(cat <<'EOF'
feat(vault): add layout primitives — slug, folder map, page filename

First piece of the vault export package. Pure functions, no I/O.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 2: Manifest — JSON read/write for incremental change detection

**Files:**
- Create: `vault/manifest.go`
- Test: `vault/manifest_test.go`

- [ ] **Step 1: Write the failing tests**

Create `vault/manifest_test.go`:

```go
package vault

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestManifest_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".cortex-export.json")

	m := newManifest()
	m.Pages["ent_01"] = PageEntry{
		Path:        "people/alice.md",
		ContentHash: "sha256:abc",
		ExportedAt:  time.Date(2026, 5, 26, 14, 32, 10, 0, time.UTC),
	}
	if err := saveManifest(path, m); err != nil {
		t.Fatalf("save: %v", err)
	}

	loaded, err := loadManifest(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Version != manifestVersion {
		t.Errorf("version = %d, want %d", loaded.Version, manifestVersion)
	}
	if got := loaded.Pages["ent_01"].Path; got != "people/alice.md" {
		t.Errorf("path = %q, want people/alice.md", got)
	}
	if got := loaded.Pages["ent_01"].ContentHash; got != "sha256:abc" {
		t.Errorf("hash = %q, want sha256:abc", got)
	}
}

func TestManifest_MissingFileReturnsEmpty(t *testing.T) {
	m, err := loadManifest(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file should be empty, not error: %v", err)
	}
	if m.Version != manifestVersion {
		t.Errorf("version = %d, want %d", m.Version, manifestVersion)
	}
	if len(m.Pages) != 0 {
		t.Errorf("expected empty pages map, got %d entries", len(m.Pages))
	}
}

func TestManifest_CorruptFileReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".cortex-export.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := loadManifest(path)
	if err == nil {
		t.Fatal("expected error for corrupt manifest, got nil")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./vault/... -run Manifest -v`
Expected: build error — undefined: newManifest, saveManifest, loadManifest, etc.

- [ ] **Step 3: Implement `vault/manifest.go`**

```go
package vault

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

// manifestVersion is bumped if the on-disk format changes incompatibly.
const manifestVersion = 1

// ManifestFilename is the on-disk name of the manifest, stored at the
// vault root.
const ManifestFilename = ".cortex-export.json"

// Manifest tracks which entities have been rendered to which files,
// and the content hash of the rendered page. It enables incremental
// export by allowing the exporter to skip writing pages whose content
// has not changed since the last run.
type Manifest struct {
	Version    int                   `json:"version"`
	ExportedAt time.Time             `json:"exported_at"`
	Pages      map[string]PageEntry  `json:"pages"`
}

// PageEntry is a single entity's record in the manifest.
type PageEntry struct {
	Path        string    `json:"path"`         // relative to vault root, forward slashes
	ContentHash string    `json:"content_hash"` // "sha256:" + hex
	ExportedAt  time.Time `json:"exported_at"`
}

func newManifest() *Manifest {
	return &Manifest{
		Version: manifestVersion,
		Pages:   map[string]PageEntry{},
	}
}

// loadManifest reads the manifest at path. A missing file returns an
// empty manifest, not an error — the first export run has no prior state.
// A corrupt or unreadable file returns an error.
func loadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return newManifest(), nil
		}
		return nil, fmt.Errorf("vault: read manifest %s: %w", path, err)
	}
	m := newManifest()
	if err := json.Unmarshal(data, m); err != nil {
		return nil, fmt.Errorf("vault: parse manifest %s: %w", path, err)
	}
	if m.Pages == nil {
		m.Pages = map[string]PageEntry{}
	}
	return m, nil
}

// saveManifest writes the manifest to path, creating parent dirs.
// The file is written atomically via temp + rename.
func saveManifest(path string, m *Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("vault: marshal manifest: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("vault: write manifest tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("vault: rename manifest: %w", err)
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./vault/... -run Manifest -v`
Expected: all 3 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add vault/manifest.go vault/manifest_test.go
git commit -m "$(cat <<'EOF'
feat(vault): add manifest read/write for incremental export

JSON file at vault root tracks entity ID → file path + content hash.
Missing file is treated as empty (first export). Corrupt file errors.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 3: Render — entity page with frontmatter, sections, wikilinks

**Files:**
- Create: `vault/render.go`
- Test: `vault/render_test.go`
- Test data: `vault/testdata/entity_full.golden.md`, `vault/testdata/entity_minimal.golden.md`

This task introduces a minimal hand-rolled YAML frontmatter writer (no new dependency). The repo currently has no YAML library; adding one for a one-way write of flat key-value with a small map nested under `attributes:` would be overkill.

- [ ] **Step 1: Write the failing test and golden files**

Create `vault/testdata/entity_full.golden.md`:

```markdown
---
cortex_id: ent_01H9X7K2M3N4P5Q6R7S8T9V0W1
type: person
name: Alice Chen
source: notes/2026-04-02-coffee.md
created_at: 2026-04-01T10:23:11Z
updated_at: 2026-05-20T14:55:02Z
attributes:
  role: engineer
  team: payments
exported_at: 2026-05-26T14:32:10Z
---

# Alice Chen

## Memories

- Alice is joining Stripe next month — `notes/2026-04-02-coffee.md`
- Worked at Square 2019–2024 — `linkedin-import`

## Relationships

- works_at → [[organizations/stripe|Stripe]]
- knows → [[people/bob-singh|Bob Singh]]

## Backlinks

- [[events/2026-04-02-board-meeting|2026-04-02 board meeting]] — attended

## Sources

- [[sources/2026-04-02--coffee-notes]]
- [[sources/linkedin-import]]
```

Create `vault/testdata/entity_minimal.golden.md`:

```markdown
---
cortex_id: ent_01HSOLO
type: person
name: Solo
created_at: 2026-05-01T00:00:00Z
updated_at: 2026-05-01T00:00:00Z
exported_at: 2026-05-26T14:32:10Z
---

# Solo
```

Create `vault/render_test.go`:

```go
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
	sources := []string{"2026-04-02--coffee-notes", "linkedin-import"}

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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./vault/... -run RenderEntity -v`
Expected: build error — undefined: renderEntity, resolvedRel.

- [ ] **Step 3: Implement `vault/render.go`**

```go
package vault

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/sausheong/cortex"
)

// resolvedRel is a relationship with both ends resolved to vault paths
// and display names. The exporter resolves these once when building the
// in-memory page set; renderers consume them directly.
type resolvedRel struct {
	Type      string // e.g. "works_at"
	OtherPath string // e.g. "organizations/stripe" (no .md suffix)
	OtherName string // display name for wikilink alias
}

// renderEntity produces the markdown content of an entity page.
// Sources is a list of source identifiers (filenames or labels) that
// contributed to this entity; they are rendered as wikilinks to source
// pages. Pass nil/empty slices to omit sections.
func renderEntity(
	e cortex.Entity,
	memories []cortex.Memory,
	outRels []resolvedRel,
	inRels []resolvedRel,
	sources []string,
	exportedAt time.Time,
) string {
	var b strings.Builder

	// Frontmatter.
	b.WriteString("---\n")
	fmt.Fprintf(&b, "cortex_id: %s\n", e.ID)
	fmt.Fprintf(&b, "type: %s\n", e.Type)
	fmt.Fprintf(&b, "name: %s\n", yamlString(e.Name))
	if e.Source != "" {
		fmt.Fprintf(&b, "source: %s\n", yamlString(e.Source))
	}
	fmt.Fprintf(&b, "created_at: %s\n", e.CreatedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "updated_at: %s\n", e.UpdatedAt.UTC().Format(time.RFC3339))
	if len(e.Attributes) > 0 {
		b.WriteString("attributes:\n")
		for _, k := range sortedKeys(e.Attributes) {
			fmt.Fprintf(&b, "  %s: %s\n", k, yamlString(fmt.Sprint(e.Attributes[k])))
		}
	}
	fmt.Fprintf(&b, "exported_at: %s\n", exportedAt.UTC().Format(time.RFC3339))
	b.WriteString("---\n\n")

	// Title.
	fmt.Fprintf(&b, "# %s\n", e.Name)

	// Sections. Each omitted if empty.
	if len(memories) > 0 {
		b.WriteString("\n## Memories\n\n")
		for _, m := range memories {
			if m.Source != "" {
				fmt.Fprintf(&b, "- %s — `%s`\n", m.Content, m.Source)
			} else {
				fmt.Fprintf(&b, "- %s\n", m.Content)
			}
		}
	}
	if len(outRels) > 0 {
		b.WriteString("\n## Relationships\n\n")
		for _, r := range outRels {
			fmt.Fprintf(&b, "- %s → [[%s|%s]]\n", r.Type, r.OtherPath, r.OtherName)
		}
	}
	if len(inRels) > 0 {
		b.WriteString("\n## Backlinks\n\n")
		for _, r := range inRels {
			fmt.Fprintf(&b, "- [[%s|%s]] — %s\n", r.OtherPath, r.OtherName, r.Type)
		}
	}
	if len(sources) > 0 {
		b.WriteString("\n## Sources\n\n")
		for _, s := range sources {
			fmt.Fprintf(&b, "- [[sources/%s]]\n", slug(s))
		}
	}

	return b.String()
}

// yamlString wraps a value in double quotes if it contains characters
// that would confuse a YAML parser. For our purposes (single-line strings
// from entity names and attribute values), this catches the common cases
// without pulling in a YAML library.
func yamlString(s string) string {
	if needsYAMLQuoting(s) {
		// Escape backslashes and double quotes.
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `"`, `\"`)
		return `"` + s + `"`
	}
	return s
}

func needsYAMLQuoting(s string) bool {
	if s == "" {
		return true
	}
	if strings.ContainsAny(s, ":#&*!|>'\"%@`\n\r\t") {
		return true
	}
	// Leading/trailing whitespace or special leading chars.
	if s[0] == ' ' || s[0] == '-' || s[0] == '?' || s[len(s)-1] == ' ' {
		return true
	}
	return false
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./vault/... -run RenderEntity -v`
Expected: both tests PASS.

If a golden mismatches, inspect the diff — the rendered output is canonical; update goldens only if the output is genuinely correct.

- [ ] **Step 5: Commit**

```bash
git add vault/render.go vault/render_test.go vault/testdata/
git commit -m "$(cat <<'EOF'
feat(vault): render entity pages with frontmatter and wikilinks

Hand-rolled minimal YAML writer for frontmatter (no new dependency).
Sections omitted when empty. Golden-file tests cover full + minimal cases.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 4: Render — source page, index, log line

**Files:**
- Modify: `vault/render.go`
- Modify: `vault/render_test.go`
- Test data: `vault/testdata/source.golden.md`, `vault/testdata/index.golden.md`

- [ ] **Step 1: Write the failing tests and goldens**

Create `vault/testdata/source.golden.md`:

```markdown
---
source: notes/2026-04-02-coffee.md
exported_at: 2026-05-26T14:32:10Z
---

# notes/2026-04-02-coffee.md

## Entities

- [[people/alice-chen|Alice Chen]]
- [[organizations/stripe|Stripe]]

## Memories

- Alice is joining Stripe next month
- Stripe is hiring aggressively in payments
```

Create `vault/testdata/index.golden.md`:

```markdown
---
exported_at: 2026-05-26T14:32:10Z
---

# Index

## People

- [[people/alice-chen|Alice Chen]] — engineer at payments

## Organizations

- [[organizations/stripe|Stripe]]

---

See also: [[log]]
```

Append to `vault/render_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./vault/... -run "RenderSource|RenderIndex|FormatLogLine" -v`
Expected: build error — undefined: sourcePage, renderSource, indexGroup, etc.

- [ ] **Step 3: Extend `vault/render.go`**

Append to `vault/render.go`:

```go
// sourcePage is the input to renderSource: one source identifier and
// the entities + memories it contributed to.
type sourcePage struct {
	Source   string
	Entities []sourceEntity
	Memories []string
}

type sourceEntity struct {
	Path string
	Name string
}

func renderSource(p sourcePage, exportedAt time.Time) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "source: %s\n", yamlString(p.Source))
	fmt.Fprintf(&b, "exported_at: %s\n", exportedAt.UTC().Format(time.RFC3339))
	b.WriteString("---\n\n")
	fmt.Fprintf(&b, "# %s\n", p.Source)
	if len(p.Entities) > 0 {
		b.WriteString("\n## Entities\n\n")
		for _, e := range p.Entities {
			fmt.Fprintf(&b, "- [[%s|%s]]\n", e.Path, e.Name)
		}
	}
	if len(p.Memories) > 0 {
		b.WriteString("\n## Memories\n\n")
		for _, m := range p.Memories {
			fmt.Fprintf(&b, "- %s\n", m)
		}
	}
	return b.String()
}

// indexGroup is one section in index.md (e.g. all People entities).
type indexGroup struct {
	Heading string
	Items   []indexItem
}

type indexItem struct {
	Path    string
	Name    string
	Summary string // optional one-line description
}

func renderIndex(groups []indexGroup, exportedAt time.Time) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "exported_at: %s\n", exportedAt.UTC().Format(time.RFC3339))
	b.WriteString("---\n\n# Index\n")
	for _, g := range groups {
		fmt.Fprintf(&b, "\n## %s\n\n", g.Heading)
		for _, it := range g.Items {
			if it.Summary != "" {
				fmt.Fprintf(&b, "- [[%s|%s]] — %s\n", it.Path, it.Name, it.Summary)
			} else {
				fmt.Fprintf(&b, "- [[%s|%s]]\n", it.Path, it.Name)
			}
		}
	}
	b.WriteString("\n---\n\nSee also: [[log]]\n")
	return b.String()
}

// formatLogLine builds a single log.md entry. Always terminated with \n.
func formatLogLine(ts time.Time, op, summary string) string {
	return fmt.Sprintf("## [%s] %s | %s\n", ts.UTC().Format(time.RFC3339), op, summary)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./vault/... -v`
Expected: all render tests PASS (entity + source + index + log line).

- [ ] **Step 5: Commit**

```bash
git add vault/render.go vault/render_test.go vault/testdata/source.golden.md vault/testdata/index.golden.md
git commit -m "$(cat <<'EOF'
feat(vault): render source pages, index, and log lines

Source pages list entities and memories that came from one source.
Index groups entities by type with optional one-line summaries.
Log lines use grep-parseable prefix per the llm-wiki convention.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 5: Exporter — orchestration with incremental + dry-run + archive

**Files:**
- Create: `vault/vault.go`
- Test: `vault/vault_test.go`

- [ ] **Step 1: Write the failing tests**

Create `vault/vault_test.go`:

```go
package vault

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sausheong/cortex"
)

// seedBrain creates a temp brain.db with a small fixed graph and returns
// an open *cortex.Cortex plus a cleanup func. No LLM / embedder configured.
func seedBrain(t *testing.T) (*cortex.Cortex, func()) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "brain.db")
	cx, err := cortex.Open(dbPath)
	if err != nil {
		t.Fatalf("open cortex: %v", err)
	}
	ctx := context.Background()
	alice := &cortex.Entity{Type: "person", Name: "Alice Chen", Source: "notes/intro.md"}
	stripe := &cortex.Entity{Type: "organization", Name: "Stripe"}
	if err := cx.PutEntity(ctx, alice); err != nil {
		t.Fatal(err)
	}
	if err := cx.PutEntity(ctx, stripe); err != nil {
		t.Fatal(err)
	}
	rel := &cortex.Relationship{SourceID: alice.ID, TargetID: stripe.ID, Type: "works_at"}
	if err := cx.PutRelationship(ctx, rel); err != nil {
		t.Fatal(err)
	}
	mem := &cortex.Memory{Content: "Alice joined Stripe", EntityIDs: []string{alice.ID, stripe.ID}, Source: "notes/intro.md"}
	if err := cx.PutMemory(ctx, mem); err != nil {
		t.Fatal(err)
	}
	return cx, func() { _ = cx.Close() }
}

func TestExport_BasicTree(t *testing.T) {
	cx, cleanup := seedBrain(t)
	defer cleanup()
	vaultDir := t.TempDir()

	stats, err := Export(context.Background(), cx, Options{VaultDir: vaultDir})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if stats.Written < 2 {
		t.Errorf("Written = %d, want >= 2 (alice + stripe)", stats.Written)
	}

	mustExist(t, vaultDir, "people/alice-chen.md")
	mustExist(t, vaultDir, "organizations/stripe.md")
	mustExist(t, vaultDir, "index.md")
	mustExist(t, vaultDir, "log.md")
	mustExist(t, vaultDir, ManifestFilename)

	// Source page exists for "notes/intro.md".
	entries, _ := os.ReadDir(filepath.Join(vaultDir, "sources"))
	if len(entries) == 0 {
		t.Error("expected at least one source page")
	}
}

func TestExport_Idempotent(t *testing.T) {
	cx, cleanup := seedBrain(t)
	defer cleanup()
	vaultDir := t.TempDir()
	ctx := context.Background()

	first, err := Export(ctx, cx, Options{VaultDir: vaultDir})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Export(ctx, cx, Options{VaultDir: vaultDir})
	if err != nil {
		t.Fatal(err)
	}
	if second.Written != 0 {
		t.Errorf("second run Written = %d, want 0", second.Written)
	}
	if second.Skipped < first.Written {
		t.Errorf("second run Skipped = %d, want >= %d", second.Skipped, first.Written)
	}
}

func TestExport_DeletedEntityIsArchived(t *testing.T) {
	cx, cleanup := seedBrain(t)
	defer cleanup()
	vaultDir := t.TempDir()
	ctx := context.Background()

	if _, err := Export(ctx, cx, Options{VaultDir: vaultDir}); err != nil {
		t.Fatal(err)
	}
	// Forget Alice (delete by source — she's the only entity from notes/intro.md).
	if err := cx.Forget(ctx, cortex.Filter{Source: "notes/intro.md"}); err != nil {
		t.Fatal(err)
	}
	stats, err := Export(ctx, cx, Options{VaultDir: vaultDir})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Archived == 0 {
		t.Error("expected at least one archived page")
	}
	// Original page is gone.
	if _, err := os.Stat(filepath.Join(vaultDir, "people/alice-chen.md")); !os.IsNotExist(err) {
		t.Error("alice page should have been moved to _archive")
	}
	// Archive folder exists.
	entries, _ := os.ReadDir(filepath.Join(vaultDir, "_archive"))
	if len(entries) == 0 {
		t.Error("_archive should have at least one timestamped subdir")
	}
}

func TestExport_Full_RewritesEverything(t *testing.T) {
	cx, cleanup := seedBrain(t)
	defer cleanup()
	vaultDir := t.TempDir()
	ctx := context.Background()

	first, err := Export(ctx, cx, Options{VaultDir: vaultDir})
	if err != nil {
		t.Fatal(err)
	}
	full, err := Export(ctx, cx, Options{VaultDir: vaultDir, Full: true})
	if err != nil {
		t.Fatal(err)
	}
	if full.Written != first.Written {
		t.Errorf("full run Written = %d, want %d (same as first)", full.Written, first.Written)
	}
	if full.Skipped != 0 {
		t.Errorf("full run Skipped = %d, want 0", full.Skipped)
	}
}

func TestExport_DryRun_NoFilesWritten(t *testing.T) {
	cx, cleanup := seedBrain(t)
	defer cleanup()
	vaultDir := t.TempDir()

	stats, err := Export(context.Background(), cx, Options{VaultDir: vaultDir, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if stats.Written == 0 {
		t.Error("DryRun should still report Written count")
	}
	// Manifest must NOT exist.
	if _, err := os.Stat(filepath.Join(vaultDir, ManifestFilename)); !os.IsNotExist(err) {
		t.Error("DryRun must not write manifest")
	}
	// No entity pages on disk.
	entries, _ := os.ReadDir(filepath.Join(vaultDir, "people"))
	if len(entries) > 0 {
		t.Error("DryRun must not write entity pages")
	}
}

func TestExport_LogAppends(t *testing.T) {
	cx, cleanup := seedBrain(t)
	defer cleanup()
	vaultDir := t.TempDir()
	ctx := context.Background()

	if _, err := Export(ctx, cx, Options{VaultDir: vaultDir}); err != nil {
		t.Fatal(err)
	}
	if _, err := Export(ctx, cx, Options{VaultDir: vaultDir}); err != nil {
		t.Fatal(err)
	}
	logBytes, err := os.ReadFile(filepath.Join(vaultDir, "log.md"))
	if err != nil {
		t.Fatal(err)
	}
	count := strings.Count(string(logBytes), "## [")
	if count != 2 {
		t.Errorf("expected 2 log entries after 2 runs, got %d", count)
	}
}

func mustExist(t *testing.T, root, rel string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
		t.Errorf("expected %s to exist: %v", rel, err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./vault/... -run Export -v`
Expected: build error — undefined: Export, Options, Stats.

- [ ] **Step 3: Implement `vault/vault.go`**

```go
package vault

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sausheong/cortex"
)

// Options controls Export.
type Options struct {
	VaultDir string // required
	Full     bool   // false = incremental (skip unchanged pages)
	DryRun   bool   // compute stats but write nothing
}

// Stats reports what Export did (or would do, under DryRun).
type Stats struct {
	Written  int
	Skipped  int
	Archived int
	Errors   []error
}

// Export projects the cortex graph onto a markdown vault.
// See docs/superpowers/specs/2026-05-26-vault-export-and-cortex-schema-design.md
// for the full algorithm.
func Export(ctx context.Context, c *cortex.Cortex, opts Options) (Stats, error) {
	var stats Stats

	if opts.VaultDir == "" {
		return stats, fmt.Errorf("vault: VaultDir is required")
	}
	if !opts.DryRun {
		if err := os.MkdirAll(opts.VaultDir, 0o755); err != nil {
			return stats, fmt.Errorf("vault: create dir: %w", err)
		}
	}

	manifestPath := filepath.Join(opts.VaultDir, ManifestFilename)
	manifest, err := loadManifest(manifestPath)
	if err != nil && !opts.Full {
		return stats, err
	}
	if opts.Full {
		manifest = newManifest()
	}

	// Pull the full graph. FindEntities with empty filter returns everything.
	entities, err := c.FindEntities(ctx, cortex.EntityFilter{})
	if err != nil {
		return stats, fmt.Errorf("vault: list entities: %w", err)
	}

	// Build name-collision sets per (type folder, slug). An entity is
	// "colliding" if there is at least one other entity in the same folder
	// whose slug matches. Order entities by ID for stable iteration.
	sort.Slice(entities, func(i, j int) bool { return entities[i].ID < entities[j].ID })
	folderSlugCount := map[string]int{}
	for _, e := range entities {
		k := folderForType(e.Type) + "/" + slug(e.Name)
		folderSlugCount[k]++
	}

	// First pass: determine each entity's vault path (so backlinks can resolve).
	type pageMeta struct {
		ent  cortex.Entity
		path string // "people/alice-chen" (no .md)
	}
	pageByID := make(map[string]pageMeta, len(entities))
	for _, e := range entities {
		colliding := folderSlugCount[folderForType(e.Type)+"/"+slug(e.Name)] > 1
		fn := pageFilename(e.Name, e.ID, colliding)
		pageByID[e.ID] = pageMeta{
			ent:  e,
			path: folderForType(e.Type) + "/" + strings.TrimSuffix(fn, ".md"),
		}
	}

	exportedAt := time.Now().UTC()

	// Second pass: render and write each entity page.
	// Also collect source → entities/memories for source pages, and index data.
	sourceEntities := map[string][]sourceEntity{}
	sourceMemories := map[string][]string{}
	indexByType := map[string][]indexItem{}

	for _, e := range entities {
		meta := pageByID[e.ID]
		memories, err := c.GetMemoriesByEntity(ctx, e.ID)
		if err != nil {
			stats.Errors = append(stats.Errors, fmt.Errorf("memories for %s: %w", e.ID, err))
			continue
		}
		rels, err := c.GetRelationships(ctx, e.ID)
		if err != nil {
			stats.Errors = append(stats.Errors, fmt.Errorf("rels for %s: %w", e.ID, err))
			continue
		}

		var outRels, inRels []resolvedRel
		for _, r := range rels {
			if r.SourceID == e.ID {
				other, ok := pageByID[r.TargetID]
				if !ok {
					continue
				}
				outRels = append(outRels, resolvedRel{Type: r.Type, OtherPath: other.path, OtherName: other.ent.Name})
			} else {
				other, ok := pageByID[r.SourceID]
				if !ok {
					continue
				}
				inRels = append(inRels, resolvedRel{Type: r.Type, OtherPath: other.path, OtherName: other.ent.Name})
			}
		}

		// Collect distinct source identifiers contributing to this entity:
		// the entity's own source, plus each memory's source.
		sourceSet := map[string]struct{}{}
		if e.Source != "" {
			sourceSet[e.Source] = struct{}{}
		}
		for _, m := range memories {
			if m.Source != "" {
				sourceSet[m.Source] = struct{}{}
			}
		}
		sources := keysSorted(sourceSet)

		content := renderEntity(e, memories, outRels, inRels, sources, exportedAt)
		hash := hashContent(content)
		entry, exists := manifest.Pages[e.ID]
		desiredRel := meta.path + ".md"

		// Decide: skip (hash match + path match) vs write.
		if exists && entry.ContentHash == hash && entry.Path == desiredRel && !opts.Full {
			stats.Skipped++
		} else {
			if !opts.DryRun {
				// If path moved (rename), delete old file before writing new one.
				if exists && entry.Path != desiredRel {
					_ = os.Remove(filepath.Join(opts.VaultDir, entry.Path))
				}
				outPath := filepath.Join(opts.VaultDir, desiredRel)
				if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
					stats.Errors = append(stats.Errors, fmt.Errorf("mkdir %s: %w", filepath.Dir(outPath), err))
					continue
				}
				if err := os.WriteFile(outPath, []byte(content), 0o644); err != nil {
					stats.Errors = append(stats.Errors, fmt.Errorf("write %s: %w", outPath, err))
					continue
				}
			}
			manifest.Pages[e.ID] = PageEntry{
				Path:        desiredRel,
				ContentHash: hash,
				ExportedAt:  exportedAt,
			}
			stats.Written++
		}

		// Update source-page aggregates.
		for s := range sourceSet {
			sourceEntities[s] = append(sourceEntities[s], sourceEntity{Path: meta.path, Name: e.Name})
		}
		for _, m := range memories {
			if m.Source != "" {
				sourceMemories[m.Source] = append(sourceMemories[m.Source], m.Content)
			}
		}

		// Index aggregate.
		heading := capitalize(folderForType(e.Type))
		summary := indexSummary(e)
		indexByType[heading] = append(indexByType[heading], indexItem{
			Path: meta.path, Name: e.Name, Summary: summary,
		})
	}

	// Archive stale pages: any manifest entry whose ID is no longer in the graph.
	if len(entities) > 0 || opts.Full {
		liveIDs := map[string]struct{}{}
		for _, e := range entities {
			liveIDs[e.ID] = struct{}{}
		}
		var staleIDs []string
		for id := range manifest.Pages {
			if _, ok := liveIDs[id]; !ok {
				staleIDs = append(staleIDs, id)
			}
		}
		if len(staleIDs) > 0 {
			archiveDir := filepath.Join(opts.VaultDir, "_archive", exportedAt.Format("2006-01-02T15-04-05"))
			for _, id := range staleIDs {
				entry := manifest.Pages[id]
				if !opts.DryRun {
					src := filepath.Join(opts.VaultDir, entry.Path)
					dst := filepath.Join(archiveDir, entry.Path)
					if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
						stats.Errors = append(stats.Errors, err)
						continue
					}
					if err := os.Rename(src, dst); err != nil && !os.IsNotExist(err) {
						stats.Errors = append(stats.Errors, err)
						continue
					}
				}
				delete(manifest.Pages, id)
				stats.Archived++
			}
		}
	}

	// Write source pages.
	if !opts.DryRun {
		sourcesDir := filepath.Join(opts.VaultDir, "sources")
		if err := os.MkdirAll(sourcesDir, 0o755); err != nil {
			stats.Errors = append(stats.Errors, err)
		} else {
			for s, ents := range sourceEntities {
				sort.Slice(ents, func(i, j int) bool { return ents[i].Name < ents[j].Name })
				p := sourcePage{Source: s, Entities: ents, Memories: dedupSorted(sourceMemories[s])}
				content := renderSource(p, exportedAt)
				outPath := filepath.Join(sourcesDir, slug(s)+".md")
				if err := os.WriteFile(outPath, []byte(content), 0o644); err != nil {
					stats.Errors = append(stats.Errors, err)
				}
			}
		}
	}

	// Write index.
	if !opts.DryRun {
		var groups []indexGroup
		for _, h := range sortedKeysStringSlice(indexByType) {
			items := indexByType[h]
			sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
			groups = append(groups, indexGroup{Heading: h, Items: items})
		}
		content := renderIndex(groups, exportedAt)
		if err := os.WriteFile(filepath.Join(opts.VaultDir, "index.md"), []byte(content), 0o644); err != nil {
			stats.Errors = append(stats.Errors, err)
		}
	}

	// Append log entry.
	if !opts.DryRun {
		summary := fmt.Sprintf("%d pages written, %d skipped, %d archived", stats.Written, stats.Skipped, stats.Archived)
		line := formatLogLine(exportedAt, "export", summary)
		f, err := os.OpenFile(filepath.Join(opts.VaultDir, "log.md"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			stats.Errors = append(stats.Errors, err)
		} else {
			_, _ = f.WriteString(line)
			_ = f.Close()
		}
	}

	// Save manifest.
	if !opts.DryRun {
		manifest.ExportedAt = exportedAt
		if err := saveManifest(manifestPath, manifest); err != nil {
			stats.Errors = append(stats.Errors, err)
		}
	}

	return stats, nil
}

func hashContent(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func keysSorted(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysStringSlice(m map[string][]indexItem) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func dedupSorted(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	for _, s := range in {
		seen[s] = struct{}{}
	}
	out := keysSorted(seen)
	return out
}

// indexSummary derives a one-line summary for an index entry from the
// entity's attributes. Returns "" if no useful summary can be built.
func indexSummary(e cortex.Entity) string {
	role, _ := e.Attributes["role"].(string)
	team, _ := e.Attributes["team"].(string)
	switch {
	case role != "" && team != "":
		return role + " at " + team
	case role != "":
		return role
	case team != "":
		return team
	}
	return ""
}

// capitalize uppercases the first rune of s; the rest is unchanged.
// Replaces strings.Title (deprecated in Go 1.18+); avoids the
// golang.org/x/text dependency for a trivially small need.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./vault/... -v`
Expected: all tests in vault/ PASS (layout + manifest + render + export integration).

If any flake on timestamps in the archive dir name (collision when two test runs land in the same second), the test uses fresh tempdirs so this shouldn't happen — but worth re-running once if you see a transient failure.

- [ ] **Step 5: Commit**

```bash
git add vault/vault.go vault/vault_test.go
git commit -m "$(cat <<'EOF'
feat(vault): add Export() — incremental, dry-run, archive support

Hash-on-render change detection. Rename moves files; delete archives.
Source pages and index regenerated each run; log.md appended to.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 6: CLI — `cortex export` command

**Files:**
- Create: `cmd/cortex/export.go`
- Create: `cmd/cortex/export_test.go`
- Modify: `cmd/cortex/main.go` (add case + usage)

- [ ] **Step 1: Write the failing test**

Create `cmd/cortex/export_test.go`:

```go
package main

import "testing"

func TestParseExportArgs_Defaults(t *testing.T) {
	opts, err := parseExportArgs(nil)
	if err != nil {
		t.Fatal(err)
	}
	if opts.VaultDir != "./vault" {
		t.Errorf("VaultDir = %q, want ./vault", opts.VaultDir)
	}
	if opts.Full {
		t.Error("Full should default to false")
	}
	if opts.DryRun {
		t.Error("DryRun should default to false")
	}
}

func TestParseExportArgs_AllFlags(t *testing.T) {
	opts, err := parseExportArgs([]string{"--vault", "/tmp/v", "--full", "--dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.VaultDir != "/tmp/v" {
		t.Errorf("VaultDir = %q", opts.VaultDir)
	}
	if !opts.Full {
		t.Error("Full should be true")
	}
	if !opts.DryRun {
		t.Error("DryRun should be true")
	}
}

func TestParseExportArgs_UnknownFlagErrors(t *testing.T) {
	_, err := parseExportArgs([]string{"--unknown"})
	if err == nil {
		t.Error("expected error for unknown flag")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/cortex/... -run ParseExportArgs -v`
Expected: build error — undefined: parseExportArgs.

- [ ] **Step 3: Create `cmd/cortex/export.go`**

```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/sausheong/cortex/vault"
)

type exportOptions struct {
	VaultDir string
	Full     bool
	DryRun   bool
}

func parseExportArgs(args []string) (exportOptions, error) {
	opts := exportOptions{VaultDir: "./vault"}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--vault":
			if i+1 >= len(args) {
				return opts, fmt.Errorf("--vault requires a path argument")
			}
			opts.VaultDir = args[i+1]
			i++
		case "--full":
			opts.Full = true
		case "--dry-run":
			opts.DryRun = true
		default:
			return opts, fmt.Errorf("unknown flag: %s", args[i])
		}
	}
	return opts, nil
}

func cmdExport() {
	args := os.Args[2:]
	opts, err := parseExportArgs(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	cx := openCortex()
	defer cx.Close()

	stats, err := vault.Export(context.Background(), cx, vault.Options{
		VaultDir: opts.VaultDir,
		Full:     opts.Full,
		DryRun:   opts.DryRun,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "export failed: %v\n", err)
		os.Exit(1)
	}

	verb := "wrote"
	if opts.DryRun {
		verb = "would write"
	}
	fmt.Printf("%s %d pages, skipped %d, archived %d (vault: %s)\n",
		verb, stats.Written, stats.Skipped, stats.Archived, opts.VaultDir)
	for _, e := range stats.Errors {
		fmt.Fprintf(os.Stderr, "  warning: %v\n", e)
	}
}
```

- [ ] **Step 4: Wire into `cmd/cortex/main.go`**

Edit `cmd/cortex/main.go` to add the `export` case in the switch (alongside `remember`, `recall`, etc.):

Find the existing switch (around line 49):

```go
	switch cmd {
	case "init":
		cmdInit()
	case "remember":
		cmdRemember()
	case "recall":
		cmdRecall()
	case "sync":
		cmdSync()
	case "entity":
		cmdEntity()
	case "forget":
		cmdForget()
	case "config":
		cmdConfig()
	default:
```

Add `case "export": cmdExport()` and `case "init-schema": cmdInitSchema()` (the latter is added in Task 7 — adding it here too is fine; it just won't be wired yet). For this task add only the export case:

```go
	case "export":
		cmdExport()
```

Also extend `printUsage()` to document the new command. In the `Commands:` block add:

```
  export [--vault <dir>] [--full] [--dry-run]
                                 Export the knowledge graph as an Obsidian-compatible markdown vault
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./cmd/cortex/... -run ParseExportArgs -v`
Expected: all 3 tests PASS.

Then verify the full build:

Run: `go build ./...`
Expected: builds clean.

- [ ] **Step 6: Smoke test against a real brain**

Run:

```bash
go build -o /tmp/cortex-test ./cmd/cortex
mkdir -p /tmp/cortex-vault-test
cd /tmp && /tmp/cortex-test --db /tmp/cortex-smoke.db init
/tmp/cortex-test --db /tmp/cortex-smoke.db export --vault /tmp/cortex-vault-test
ls -R /tmp/cortex-vault-test
```

Expected: directory created with `index.md`, `log.md`, `.cortex-export.json`. No errors. Empty graph → empty index.

Cleanup:

```bash
rm -rf /tmp/cortex-vault-test /tmp/cortex-smoke.db /tmp/cortex-test
```

- [ ] **Step 7: Commit**

```bash
git add cmd/cortex/export.go cmd/cortex/export_test.go cmd/cortex/main.go
git commit -m "$(cat <<'EOF'
feat(cli): add cortex export command

Wires the vault package into the CLI. Default vault dir is ./vault.
Flags: --vault <path>, --full, --dry-run.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 7: CORTEX.md template + `cortex init-schema` command

**Files:**
- Create: `docs/CORTEX.md` (the embedded template)
- Create: `cmd/cortex/init_schema.go`
- Create: `cmd/cortex/init_schema_test.go`
- Modify: `cmd/cortex/main.go` (add case + usage)

- [ ] **Step 1: Write `docs/CORTEX.md`**

Create `docs/CORTEX.md`. This is the actual text users will see; write it carefully:

```markdown
# CORTEX.md

This file tells AI agents how to use [cortex](https://github.com/sausheong/cortex) as the knowledge layer for this project. Read it at the start of every session.

## What cortex is

Cortex is a personal knowledge graph stored in SQLite. It holds typed entities (people, organizations, concepts, events, documents), typed relationships between them, and short memory statements about them. Everything you, the agent, have learned about this project's domain is in there.

Treat cortex as your long-term memory. Read from it before answering questions. Write to it when something worth keeping is said.

## The three operations

- **`recall <query>`** — search the graph. Returns ranked results from memories, keyword matches, vector similarity, and graph traversal, merged via reciprocal rank fusion. Use before answering any non-trivial question.
- **`remember <text>`** — extract entities, relationships, and memories from the text and add them to the graph. Use after the user shares a new fact.
- **`forget --source <s>` / `--entity <id>`** — remove knowledge. Use when the user corrects a prior belief or asks you to delete something.

These are available via:

- **CLI** — `cortex recall "..."`, `cortex remember "..."`, etc.
- **MCP** — the `cortex-mcp` server exposes them as MCP tools.
- **HTTP** — `cortex-http` exposes them as REST endpoints.

Use whichever surface is configured in this project.

## When to recall

Recall before:

- Answering any "what do I know about X" question, where X is a person, organization, project, or concept.
- Making a recommendation that might have history (e.g. "should we use library Y?" — check if there's a prior decision).
- Starting a conversation about an entity the user has mentioned before, even in passing.

The cost of an extra recall is low; the cost of contradicting a prior decision because you didn't check is high.

## When to remember

Remember when:

- The user shares a new fact about themselves, a person, an organization, or a project.
- A decision is made (architectural, product, personnel).
- An event happens (a meeting, a launch, a milestone).
- The user corrects a prior belief — first `forget`, then `remember` the new fact.

Use the user's own words when possible. The extraction pipeline will pull out entities and relationships.

## What NOT to remember

Cortex is for knowledge that compounds, not for ephemeral state. Do **not** store:

- Code patterns, conventions, or architecture — these are in the codebase; read the source.
- Git history or who changed what — `git log` is authoritative.
- Step-by-step debugging recipes — the fix is in the code; the commit message has the context.
- Anything documented in this project's `CLAUDE.md`, `README.md`, or similar.
- In-progress task state, current conversation context, "I'm about to do X" notes — these don't survive a session and shouldn't try.

If you're tempted to remember something but it fails these tests, don't.

## The vault

If `cortex export --vault ./vault` has been run for this project, a browsable markdown projection of the graph lives at `./vault/`. It's Obsidian-compatible: each entity is its own page with frontmatter, wikilinks to related entities, backlinks, and source attribution.

Read the vault when you want broad context on an entity — it's easier to skim than running multiple `recall` calls. Use `recall` when you need ranked search results or you don't know the entity's name.

The vault is generated; don't edit it by hand. Changes to the graph are made via `remember` / `forget`.

## Workflow loop

A healthy knowledge cycle looks like:

1. **Ingest** — when the user shares something worth keeping, `remember` it. After meaningful sessions, run `cortex export` to refresh the vault.
2. **Query** — before answering, `recall` (or read the vault). Cite what you find. If your answer relied on graph knowledge, say so.
3. **Lint** — periodically (or when the user asks), look for contradictions in memories, orphan entities, stale claims, and missing relationships. Suggest cleanups.

The user is responsible for what gets stored. You are responsible for keeping the graph useful — well-summarized, well-linked, and free of cruft.

## Customization

This file is yours to edit. Add project-specific guidance: which entity types matter most here, which sources are authoritative, when to escalate to the user before remembering, custom relationship types you want extracted. The static template ships generic on purpose — make it specific.
```

- [ ] **Step 2: Write the failing CLI test**

Create `cmd/cortex/init_schema_test.go`:

```go
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitSchema_WritesTemplate(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "CORTEX.md")
	if err := writeSchemaTo(target, false); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "# CORTEX.md") {
		t.Error("expected written file to contain '# CORTEX.md' heading")
	}
}

func TestInitSchema_RefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "CORTEX.md")
	if err := os.WriteFile(target, []byte("pre-existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := writeSchemaTo(target, false)
	if err == nil {
		t.Fatal("expected error when target exists without --force")
	}
	// Verify content was not changed.
	data, _ := os.ReadFile(target)
	if string(data) != "pre-existing" {
		t.Errorf("file was overwritten without --force: %q", string(data))
	}
}

func TestInitSchema_ForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "CORTEX.md")
	if err := os.WriteFile(target, []byte("pre-existing"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeSchemaTo(target, true); err != nil {
		t.Fatalf("force should succeed: %v", err)
	}
	data, _ := os.ReadFile(target)
	if string(data) == "pre-existing" {
		t.Error("file should have been overwritten with --force")
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `go test ./cmd/cortex/... -run InitSchema -v`
Expected: build error — undefined: writeSchemaTo.

- [ ] **Step 4: Create `cmd/cortex/init_schema.go`**

```go
package main

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

//go:embed CORTEX.md.template
var cortexSchemaTemplate []byte

func cmdInitSchema() {
	args := os.Args[2:]
	var target string
	force := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--force":
			force = true
		default:
			if target != "" {
				fmt.Fprintf(os.Stderr, "unexpected argument: %s\n", args[i])
				os.Exit(1)
			}
			target = args[i]
		}
	}
	if target == "" {
		target = "CORTEX.md"
	}
	// If target is a directory, append CORTEX.md.
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		target = filepath.Join(target, "CORTEX.md")
	}
	if err := writeSchemaTo(target, force); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Wrote %s\n", target)
}

// writeSchemaTo writes the embedded CORTEX.md template to path.
// If the file already exists, it returns an error unless force is true.
func writeSchemaTo(path string, force bool) error {
	if !force {
		_, err := os.Stat(path)
		if err == nil {
			return fmt.Errorf("%s already exists (use --force to overwrite)", path)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, cortexSchemaTemplate, 0o644)
}
```

The `//go:embed` directive needs the embedded file at a path that is a sibling of the .go file (Go's embed doesn't traverse `..`). Move the template:

```bash
cp docs/CORTEX.md cmd/cortex/CORTEX.md.template
```

(`docs/CORTEX.md` is the canonical, human-edited source. `cmd/cortex/CORTEX.md.template` is the embed copy. The embed sibling file should be `.template` to make its purpose obvious and to avoid people mistakenly editing it directly. Add a brief note at the top of the template comment indicating this.)

To keep the two in sync, add a `make` step or a CI check later. For now, document it in the file. Add to the top of `cmd/cortex/CORTEX.md.template`:

```
<!--
  This file is embedded into the cortex binary at build time.
  Edit docs/CORTEX.md (the canonical source) and copy here:
    cp docs/CORTEX.md cmd/cortex/CORTEX.md.template
-->
```

- [ ] **Step 5: Wire into `cmd/cortex/main.go`**

Add the `init-schema` case to the switch in `cmd/cortex/main.go` and extend usage. In the switch (after `config`):

```go
	case "init-schema":
		cmdInitSchema()
```

In `printUsage()`, add:

```
  init-schema [<dir>] [--force]
                                 Write CORTEX.md (generic agent contract) to <dir> (default: cwd)
```

- [ ] **Step 6: Run tests and build**

Run: `go test ./cmd/cortex/... -run InitSchema -v`
Expected: all 3 InitSchema tests PASS.

Run: `go build ./...`
Expected: builds clean.

- [ ] **Step 7: Smoke test**

Run:

```bash
go build -o /tmp/cortex-test ./cmd/cortex
mkdir -p /tmp/cortex-schema-test
/tmp/cortex-test init-schema /tmp/cortex-schema-test
head -5 /tmp/cortex-schema-test/CORTEX.md
# Try to overwrite without --force:
/tmp/cortex-test init-schema /tmp/cortex-schema-test  # should fail
# With --force:
/tmp/cortex-test init-schema /tmp/cortex-schema-test --force  # should succeed
```

Expected output for `head -5`:
```
<!--
  This file is embedded into the cortex binary at build time.
  Edit docs/CORTEX.md (the canonical source) and copy here:
    cp docs/CORTEX.md cmd/cortex/CORTEX.md.template
-->
```

(or `# CORTEX.md` depending on whether the embed comment is included — both are acceptable, just note which you see).

Cleanup: `rm -rf /tmp/cortex-schema-test /tmp/cortex-test`.

- [ ] **Step 8: Commit**

```bash
git add docs/CORTEX.md cmd/cortex/CORTEX.md.template cmd/cortex/init_schema.go cmd/cortex/init_schema_test.go cmd/cortex/main.go
git commit -m "$(cat <<'EOF'
feat(cli): add cortex init-schema command + CORTEX.md template

Generic agent contract embedded via //go:embed. init-schema copies it
into a target directory; refuses to overwrite without --force.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Task 8: README — document new commands

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Read the current README CLI Reference section**

Run: `grep -n "^##" README.md | head -20`

Identify the "CLI Reference" section heading.

- [ ] **Step 2: Add documentation for `export` and `init-schema`**

Append the following inside the CLI Reference section (after the existing commands, before the next `##` heading):

````markdown
### `cortex export`

Project the knowledge graph as an Obsidian-compatible markdown vault.

```bash
cortex export [--vault <dir>] [--full] [--dry-run]
```

- `--vault <dir>` — output directory (default: `./vault`).
- `--full` — rewrite every page regardless of whether content has changed.
- `--dry-run` — compute what would change and report stats; write nothing.

The vault is laid out as `vault/people/`, `vault/organizations/`, `vault/concepts/`, etc., with one markdown page per entity. An `index.md` catalog and a chronological `log.md` live at the vault root. A hidden `.cortex-export.json` manifest tracks content hashes so subsequent runs only rewrite pages whose content has actually changed. Entities deleted from the graph are moved to `vault/_archive/<timestamp>/`.

Each page carries YAML frontmatter (including the entity's full `cortex_id`) and renders memories, outbound relationships, backlinks, and source attribution. Wikilinks use explicit paths (`[[people/alice-chen|Alice Chen]]`), so the vault is browsable in Obsidian, in a plain editor, or via `grep`.

### `cortex init-schema`

Write a generic agent contract (`CORTEX.md`) into a project directory.

```bash
cortex init-schema [<dir>] [--force]
```

The template explains to any LLM-based agent how to use cortex as the knowledge layer for the project: when to `recall`, when to `remember`, what not to store, and how the vault fits in. Refuses to overwrite an existing `CORTEX.md` unless `--force` is passed. After running, edit the file to add project-specific conventions.
````

- [ ] **Step 3: Verify the README still builds**

Run: `go test ./...`
Expected: all tests still pass (README change doesn't affect tests, but worth confirming the workspace is healthy).

- [ ] **Step 4: Commit**

```bash
git add README.md
git commit -m "$(cat <<'EOF'
docs(readme): document cortex export and init-schema commands

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Done

After Task 8, the user can:

```bash
cortex init                                  # existing
cortex remember "Alice joined Stripe..."     # existing
cortex export --vault ./vault                # NEW: browsable wiki
cortex init-schema                           # NEW: drops CORTEX.md
```

…and an AI agent reading `CORTEX.md` at session start knows exactly how cortex fits into the project.
