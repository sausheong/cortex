package main

import "testing"

func TestFlagsAcceptDB(t *testing.T) {
	for _, name := range []string{"generate", "run"} {
		fs := newFlags(name)
		if err := fs.Parse([]string{"--db", "postgres://x/y"}); err != nil {
			t.Errorf("%s: --db rejected: %v", name, err)
		}
	}
}
