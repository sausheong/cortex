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
	// Suffix takes the last 6 chars of the ID (ULID tail = pure randomness),
	// lowercased.
	a := pageFilename("Java", "ent_01HAAAAAAAAAAAAAAAAAAAAAA1", true)
	b := pageFilename("Java", "ent_01HBBBBBBBBBBBBBBBBBBBBBB2", true)
	if a == b {
		t.Fatalf("colliding entities got same filename: %q", a)
	}
	if a != "java-aaaaa1.md" {
		t.Errorf("a = %q, want java-aaaaa1.md", a)
	}
	if b != "java-bbbbb2.md" {
		t.Errorf("b = %q, want java-bbbbb2.md", b)
	}
}

func TestPageFilename_Collision_SameTimestamp(t *testing.T) {
	// Two ULIDs sharing the first 10 chars after the prefix (same-tick
	// creation). Taking suffix chars rather than prefix chars is what
	// keeps these distinguishable.
	const prefix = "ent_01H7ZZZZZZ" // shared 10-char timestamp prefix
	a := pageFilename("Java", prefix+"AAAAAAAAAAAA1", true)
	b := pageFilename("Java", prefix+"BBBBBBBBBBBB2", true)
	if a == b {
		t.Fatalf("same-timestamp collision produced identical filenames: %q", a)
	}
}

func TestPageFilename_EmptySlugFallsBackToID(t *testing.T) {
	got := pageFilename("!!!", "ent_01H9X7K2M3N4P5Q6R7S8T9V0W1", false)
	if got != "ent_01h9.md" {
		t.Errorf("got %q, want ent_01h9.md", got)
	}
}
