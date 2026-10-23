package pages

import "testing"

func TestCurrent(t *testing.T) {
	cases := []struct {
		path, href string
		want       bool
	}{
		{"/memories", "/memories", true},
		{"/memories/delete", "/memories", true},
		{"/memories/deleted", "/memories", true},
		{"/compaction", "/memories", true},
		{"/compaction/abc", "/memories", true},
		{"/compaction/abc", "/settings", false},
		{"/connections", "/settings", false},
	}
	for _, c := range cases {
		got := Page{Path: c.path}.Current(c.href) != ""
		if got != c.want {
			t.Errorf("Current(%q) on %q = %v, want %v", c.href, c.path, got, c.want)
		}
	}
}

func TestCurrentSub(t *testing.T) {
	cases := []struct {
		path, href string
		want       bool
	}{
		{"/memories", "/memories", true},
		{"/memories/delete", "/memories", true},
		{"/memories/deleted", "/memories", false},
		{"/memories/deleted", "/memories/deleted", true},
		{"/compaction/abc", "/compaction", true},
		{"/compaction", "/memories", false},
	}
	for _, c := range cases {
		got := Page{Path: c.path}.CurrentSub(c.href) != ""
		if got != c.want {
			t.Errorf("CurrentSub(%q) on %q = %v, want %v", c.href, c.path, got, c.want)
		}
	}
}
