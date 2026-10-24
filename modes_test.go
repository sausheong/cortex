package cortex

import (
	"context"
	"testing"

	"github.com/sausheong/cortex/internal/keys"
)

// testMode is "" (legacy, plaintext) or "encrypted". The package-internal
// tests cannot use internal/testutil.ForEachMode (testutil imports this
// package), so they run through forEachMode and the openers below, which
// wrap the freshly opened handle with modeHandle. Tests that read content
// columns with raw SQL stay single-mode.
var testMode string

// forEachMode runs fn as subtests "legacy" and "encrypted".
func forEachMode(t *testing.T, fn func(*testing.T)) {
	t.Helper()
	for _, m := range []string{"legacy", "encrypted"} {
		t.Run(m, func(t *testing.T) {
			if m == "encrypted" {
				testMode = "encrypted"
				t.Cleanup(func() { testMode = "" })
			}
			fn(t)
		})
	}
}

// modeHandle returns c as is in legacy mode. In encrypted mode it creates a
// fresh user in state 'encrypted' (without the plaintext owner entity that
// CreateUser writes, which the row-form guard rejects until the Task 16
// enrollment encrypts it) and returns a handle for it holding a user key.
func modeHandle(t *testing.T, c *Cortex) *Cortex { return modeHandleTB(t, c) }

func modeHandleTB(t testing.TB, c *Cortex) *Cortex {
	t.Helper()
	if testMode != "encrypted" {
		return c
	}
	ctx := context.Background()
	u, err := c.CreateUser(ctx, "enc-sub", "enc@x.test", "enc", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE users SET owner_entity_id = NULL, encryption_state = 'encrypted' WHERE id = ?`,
	} {
		if _, err := c.h.DB.ExecContext(ctx, q, u.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.h.DB.ExecContext(ctx, `DELETE FROM entities WHERE user_id = ? AND enc IS NOT TRUE`, u.ID); err != nil {
		t.Fatal(err)
	}
	uk, err := keys.NewUK()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(uk.Zero)
	cx := c.ForUser(u.ID).WithKey(uk, "encrypted")
	t.Cleanup(cx.Release)
	return cx
}
