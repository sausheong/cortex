package pages

import "testing"

func TestCurrent(t *testing.T) {
	cases := []struct {
		path, href string
		want       bool
	}{
		{"/memories", "/memories", true},
		{"/memories/delete", "/memories", true},
		{"/memories/deleted", "/memories", false},
		{"/memories/deleted", "/memories/deleted", true},
		{"/compaction/abc", "/compaction", true},
		{"/connections", "/settings", false},
	}
	for _, c := range cases {
		got := Page{Path: c.path}.Current(c.href) != ""
		if got != c.want {
			t.Errorf("Current(%q) on %q = %v, want %v", c.href, c.path, got, c.want)
		}
	}
}
