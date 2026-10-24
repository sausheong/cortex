package keys

import (
	"bytes"
	"testing"
)

func TestWrapUnwrapRoundTrip(t *testing.T) {
	u := mustUK(t)
	wk := WrapKeySecret("session", "secret")
	w, err := Wrap(wk, "u1", "session", "ref", u)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Unwrap(wk, "u1", "session", "ref", w)
	if err != nil || !bytes.Equal(got.Bytes(), u.Bytes()) {
		t.Fatalf("unwrap: %v", err)
	}
}

func TestUnwrapWrongKeyOrBindingFails(t *testing.T) {
	u := mustUK(t)
	wk := WrapKeySecret("agent", "s")
	w, _ := Wrap(wk, "u1", "agent", "r", u)
	if _, err := Unwrap(WrapKeySecret("agent", "other"), "u1", "agent", "r", w); err == nil {
		t.Fatal("wrong key")
	}
	if _, err := Unwrap(WrapKeySecret("session", "s"), "u1", "agent", "r", w); err == nil {
		t.Fatal("kind-derived key mismatch")
	}
	for _, a := range [][3]string{{"u2", "agent", "r"}, {"u1", "session", "r"}, {"u1", "agent", "r2"}} {
		if _, err := Unwrap(wk, a[0], a[1], a[2], w); err == nil {
			t.Fatalf("%v accepted", a)
		}
	}
	if _, err := Unwrap(wk, "u1", "agent", "r", w[:5]); err == nil {
		t.Fatal("truncated")
	}
	if _, err := Wrap(wk[:5], "u1", "agent", "r", u); err == nil {
		t.Fatal("short wrap key")
	}
}

func TestWrapKeysDomainSeparated(t *testing.T) {
	b := []byte("0123456789abcdef")
	a, c := WrapKeyPasskey(b), WrapKeyRecovery(b)
	if bytes.Equal(a, c) || len(a) != 32 || len(c) != 32 {
		t.Fatal("not separated")
	}
}
