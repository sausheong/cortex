package keys

import (
	"bytes"
	"errors"
	"sync"
	"testing"
)

func mustUK(t *testing.T) *UK {
	t.Helper()
	u, err := NewUK()
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestSealOpenRoundTrip(t *testing.T) {
	u := mustUK(t)
	plain := []byte("hello world")
	ct, err := u.Seal("u1", "memories", "r1", "content", plain)
	if err != nil {
		t.Fatal(err)
	}
	if ct[0] != 0x01 {
		t.Fatalf("version byte %x", ct[0])
	}
	if len(ct) != 1+12+len(plain)+16 {
		t.Fatalf("len %d", len(ct))
	}
	got, err := u.Open("u1", "memories", "r1", "content", ct)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("open: %v %q", err, got)
	}
}

func TestAADBindsRowAndUser(t *testing.T) {
	u := mustUK(t)
	ct, _ := u.Seal("u1", "t", "r1", "c", []byte("x"))
	for _, a := range [][4]string{{"u2", "t", "r1", "c"}, {"u1", "t2", "r1", "c"}, {"u1", "t", "r2", "c"}, {"u1", "t", "r1", "c2"}} {
		if _, err := u.Open(a[0], a[1], a[2], a[3], ct); !errors.Is(err, ErrDecrypt) {
			t.Fatalf("%v: want ErrDecrypt got %v", a, err)
		}
	}
	ct2, _ := u.Seal("u1", "t", "r1", "c", []byte("x"))
	if bytes.Equal(ct, ct2) {
		t.Fatal("nonce reused")
	}
}

func TestOpenRejectsTamperAndTruncation(t *testing.T) {
	u := mustUK(t)
	ct, _ := u.Seal("u", "t", "r", "c", []byte("payload"))
	for i := range ct {
		b := append([]byte(nil), ct...)
		b[i] ^= 1
		if _, err := u.Open("u", "t", "r", "c", b); err == nil {
			t.Fatalf("flip %d accepted", i)
		}
	}
	for n := 0; n < 1+12+16; n++ {
		if _, err := u.Open("u", "t", "r", "c", ct[:n]); err == nil {
			t.Fatalf("trunc %d accepted", n)
		}
	}
	if _, err := u.Open("u", "t", "r", "c", nil); err == nil {
		t.Fatal("nil accepted")
	}
}

func TestBlindDeterministicAndKeyed(t *testing.T) {
	a, b := mustUK(t), mustUK(t)
	x := a.Blind("zoe")
	if len(x) != 16 || !bytes.Equal(x, a.Blind("zoe")) {
		t.Fatal("not deterministic/16")
	}
	if bytes.Equal(x, a.Blind("zoe2")) || bytes.Equal(x, b.Blind("zoe")) {
		t.Fatal("collision")
	}
}

func TestSubkeysIndependent(t *testing.T) {
	u := mustUK(t)
	if bytes.Equal(u.dataKey, u.blindKey) || bytes.Equal(u.dataKey, u.key[:]) {
		t.Fatal("subkeys equal")
	}
	ct, _ := u.Seal("u", "t", "r", "c", []byte("x"))
	swapped := &UK{key: u.key, dataKey: u.blindKey, blindKey: u.dataKey}
	if _, err := swapped.Open("u", "t", "r", "c", ct); err == nil {
		t.Fatal("swapped roles opened")
	}
	if bytes.HasPrefix(WrapKeySecret("session", "s"), u.Blind("s")) {
		t.Fatal("blind is wrap prefix")
	}
}

func TestAADLengthPrefixNoAmbiguity(t *testing.T) {
	if bytes.Equal(aad("a|b", "c"), aad("a", "b|c")) {
		t.Fatal("ambiguous aad")
	}
	u := mustUK(t)
	ct, _ := u.Seal("a|b", "c", "r", "col", []byte("x"))
	if _, err := u.Open("a", "b|c", "r", "col", ct); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("got %v", err)
	}
}

func TestZeroRacesSafelyWithSealOpen(t *testing.T) {
	u := mustUK(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				ct, err := u.Seal("u", "t", "r", "c", []byte("x"))
				if err == nil {
					_, _ = u.Open("u", "t", "r", "c", ct)
				}
				_ = u.Blind("x")
				_ = u.Clone()
				_ = u.Bytes()
			}
		}()
	}
	u.Zero()
	wg.Wait()
	if _, err := u.Seal("u", "t", "r", "c", []byte("x")); err == nil {
		t.Fatal("seal after zero")
	}
	if _, err := u.Open("u", "t", "r", "c", []byte{1}); err == nil {
		t.Fatal("open after zero")
	}
}

func TestZeroClearsDerivedKeys(t *testing.T) {
	u := mustUK(t)
	data, blind := u.dataKey, u.blindKey
	u.Zero()
	for _, k := range [][]byte{data, blind, u.key[:]} {
		if !bytes.Equal(k, make([]byte, len(k))) {
			t.Fatal("not zeroed")
		}
	}
}

func TestZeroClearsKey(t *testing.T) {
	u := mustUK(t)
	u.Zero()
	if _, err := u.Seal("u", "t", "r", "c", []byte("x")); err == nil {
		t.Fatal("seal ok after zero")
	}
	if u.Bytes() != nil || u.Blind("x") != nil || u.Clone() != nil {
		t.Fatal("expected nil after zero")
	}
}

func TestFromBytesAndClone(t *testing.T) {
	u := mustUK(t)
	v, err := FromBytes(u.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	ct, _ := u.Seal("u", "t", "r", "c", []byte("x"))
	if _, err := v.Open("u", "t", "r", "c", ct); err != nil {
		t.Fatal(err)
	}
	c := u.Clone()
	u.Zero()
	if _, err := c.Open("u", "t", "r", "c", ct); err != nil {
		t.Fatal("clone depends on original")
	}
	if _, err := FromBytes(make([]byte, 31)); !errors.Is(err, ErrFormat) {
		t.Fatal("want ErrFormat")
	}
}
