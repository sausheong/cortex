package store

import (
	"errors"
	"testing"

	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/model"
)

func TestBlindNameNormalisation(t *testing.T) {
	uk, err := keys.NewUK()
	if err != nil {
		t.Fatal(err)
	}
	h := &Handle{UserID: "u1", EncState: "encrypted", Key: uk}
	a := string(h.BlindNorm("Zoë"))
	for _, v := range []string{"ZOË", " zoë ", "Zoë"} { // last is decomposed
		if string(h.BlindNorm(v)) != a {
			t.Fatalf("%q must blind-equal Zoë", v)
		}
	}
	if string(h.BlindNorm("Zoe")) == a {
		t.Fatal("different letters must differ")
	}
	if NormName(" Zoë ") != "zoë" {
		t.Fatalf("NormName = %q", NormName(" Zoë "))
	}
}

func TestVecRoundTrip(t *testing.T) {
	v := []float32{0.5, -1.25, 3e-7, 0}
	got, err := DecodeVec(EncodeVec(v))
	if err != nil || len(got) != len(v) {
		t.Fatal(err, got)
	}
	for i := range v {
		if got[i] != v[i] {
			t.Fatalf("elem %d: %v != %v", i, got[i], v[i])
		}
	}
	if _, err := DecodeVec([]byte{1, 2, 3}); err == nil {
		t.Fatal("want error on ragged bytes")
	}
}

func TestRowFormGuard(t *testing.T) {
	enc := &Handle{EncState: "encrypted"}
	if !errors.Is(enc.CheckRowForm(false), model.ErrPlaintextRow) {
		t.Fatal("encrypted must reject plaintext rows")
	}
	if enc.CheckRowForm(true) != nil {
		t.Fatal("encrypted accepts enc rows")
	}
	for _, s := range []string{"", "none", "migrating"} {
		if (&Handle{EncState: s}).CheckRowForm(false) != nil {
			t.Fatalf("state %q must accept legacy rows", s)
		}
	}
}

func TestKeylessHelpersReturnErrLocked(t *testing.T) {
	h := &Handle{UserID: "u", EncState: "encrypted"}
	if !errors.Is(h.RequireKey(), model.ErrLocked) {
		t.Fatal("RequireKey")
	}
	if _, err := h.SealStr("t", "r", "c", "x"); !errors.Is(err, model.ErrLocked) {
		t.Fatal("SealStr", err)
	}
	if _, err := h.OpenStr("t", "r", "c", []byte("zz")); !errors.Is(err, model.ErrLocked) {
		t.Fatal("OpenStr", err)
	}
}

func TestOpenStrNullCiphertext(t *testing.T) {
	uk, _ := keys.NewUK()
	h := &Handle{UserID: "u", EncState: "encrypted", Key: uk}
	if _, err := h.OpenStr("t", "r", "c", nil); !errors.Is(err, ErrCiphertextMissing) {
		t.Fatal(err)
	}
}
