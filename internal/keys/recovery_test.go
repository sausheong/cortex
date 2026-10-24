package keys

import (
	"bytes"
	"strings"
	"testing"
)

func TestRecoveryKeyRoundTrip(t *testing.T) {
	raw, disp, err := NewRecoveryKey()
	if err != nil || len(raw) != 16 {
		t.Fatal(err, len(raw))
	}
	groups := strings.Split(disp, "-")
	for i, g := range groups {
		if i < len(groups)-1 && len(g) != 4 {
			t.Fatalf("group %q", g)
		}
		if len(g) > 4 || g == "" {
			t.Fatalf("group %q", g)
		}
	}
	for _, in := range []string{disp, strings.ToLower(disp), " " + strings.ReplaceAll(disp, "-", " ") + " ", strings.ReplaceAll(disp, "-", "")} {
		got, err := ParseRecoveryKey(in)
		if err != nil || !bytes.Equal(got, raw) {
			t.Fatalf("%q: %v", in, err)
		}
	}
}

func TestRecoveryKeyRejectsBadLength(t *testing.T) {
	for _, n := range []int{15, 17} {
		s := strings.Repeat("A", 0)
		_ = s
		enc := encodeRecovery(make([]byte, n))
		if _, err := ParseRecoveryKey(enc); err == nil {
			t.Fatalf("%d accepted", n)
		}
	}
	if _, err := ParseRecoveryKey("!!!!"); err == nil {
		t.Fatal("garbage accepted")
	}
}
