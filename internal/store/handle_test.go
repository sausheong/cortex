package store

import (
	"errors"
	"sync"
	"testing"

	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/model"
)

func newUK(t *testing.T) *keys.UK {
	t.Helper()
	uk, err := keys.NewUK()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(uk.Zero)
	return uk
}

func TestKeyCopyIsPerRequest(t *testing.T) {
	uk := newUK(t)
	root := &Handle{UserID: "u1"}
	a, b := root.WithKey(uk, "encrypted"), root.WithKey(uk, "encrypted")
	if !a.Encrypted() || root.Encrypted() || root.Key != nil {
		t.Fatal("WithKey must copy, not mutate the root")
	}
	ct, err := b.Seal("t", "r", "c", []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pt, err := b.Open("t", "r", "c", ct)
			if err != nil || string(pt) != "hello" {
				t.Errorf("b open: %q %v", pt, err)
			}
			if _, err := b.Seal("t", "r", "c", []byte("x")); err != nil {
				t.Errorf("b seal: %v", err)
			}
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); a.Release() }()
	wg.Wait()
	if _, err := a.Seal("t", "r", "c", []byte("x")); err == nil {
		t.Fatal("released handle must not seal")
	}
	if uk.Bytes() == nil {
		t.Fatal("Release zeroed the caller's key")
	}
}

func TestHandleSealOpenBindsUser(t *testing.T) {
	uk := newUK(t)
	ha := (&Handle{UserID: "ua"}).WithKey(uk, "encrypted")
	hb := (&Handle{UserID: "ub"}).WithKey(uk, "encrypted")
	ct, err := ha.Seal("memories", "m1", "content", []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hb.Open("memories", "m1", "content", ct); !errors.Is(err, keys.ErrDecrypt) {
		t.Fatalf("cross-user open: %v", err)
	}
	if pt, err := ha.Open("memories", "m1", "content", ct); err != nil || string(pt) != "secret" {
		t.Fatalf("own open: %q %v", pt, err)
	}
	locked := (&Handle{UserID: "ua"}).WithKey(nil, "encrypted")
	if _, err := locked.Seal("t", "r", "c", nil); !errors.Is(err, model.ErrLocked) {
		t.Fatalf("locked seal: %v", err)
	}
	if _, err := locked.Open("t", "r", "c", ct); !errors.Is(err, model.ErrLocked) {
		t.Fatalf("locked open: %v", err)
	}
	if locked.Blind("x") != nil || ha.Blind("x") == nil {
		t.Fatal("blind: nil only when locked")
	}
}

func TestStartedBackgroundCloneSurvivesRequestEnd(t *testing.T) {
	uk := newUK(t)
	req := (&Handle{UserID: "u1"}).WithKey(uk, "encrypted")
	ct, err := req.Seal("t", "r", "c", []byte("bg"))
	if err != nil {
		t.Fatal(err)
	}
	// The handler clones before it returns, then the request ends.
	bg := req.WithKey(req.Key, req.EncState)
	var wg sync.WaitGroup
	started := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer bg.Release()
		<-started
		for i := 0; i < 100; i++ {
			if pt, err := bg.Open("t", "r", "c", ct); err != nil || string(pt) != "bg" {
				t.Errorf("background open: %q %v", pt, err)
				return
			}
		}
	}()
	req.Release()
	close(started)
	wg.Wait()
}
