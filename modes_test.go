package cortex

import (
	"context"
	"testing"

	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/store"
)

// testMode is "encrypted" while forEachMode runs. The package-internal tests
// cannot use internal/testutil.ForEachMode (testutil imports this package), so
// they run through forEachMode and the openers below, which wrap the freshly
// opened handle with modeHandle. Plaintext accounts no longer exist (migration
// 012): every account is encrypted.
var testMode string

// forEachMode runs fn as the subtest "encrypted".
func forEachMode(t *testing.T, fn func(*testing.T)) {
	t.Helper()
	t.Run("encrypted", func(t *testing.T) {
		testMode = "encrypted"
		t.Cleanup(func() { testMode = "" })
		fn(t)
	})
}

// modeHandle creates a fresh user in state 'encrypted' and returns a handle
// for it holding a user key.
func modeHandle(t *testing.T, c *Cortex) *Cortex { return modeHandleTB(t, c) }

func modeHandleTB(t testing.TB, c *Cortex) *Cortex {
	t.Helper()
	u, err := c.CreateUser(context.Background(), "enc-sub", "enc@x.test", "enc", true)
	if err != nil {
		t.Fatal(err)
	}
	return keyedHandle(t, c, u.ID)
}

// keyedHandle makes user id an encrypted account and returns a handle for it
// holding a fresh user key. opts apply to the returned handle.
func keyedHandle(t testing.TB, root *Cortex, id string, opts ...Option) *Cortex {
	t.Helper()
	cx, _ := keyedHandleUK(t, root, id, opts...)
	return cx
}

func keyedHandleUK(t testing.TB, root *Cortex, id string, opts ...Option) (*Cortex, *keys.UK) {
	t.Helper()
	if _, err := root.h.DB.ExecContext(context.Background(), `UPDATE users SET encryption_state = 'encrypted' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	uk, err := keys.NewUK()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(uk.Zero)
	cx := root.ForUser(id, opts...).WithKey(uk, "encrypted")
	t.Cleanup(cx.Release)
	return cx, uk
}

// sealOwner creates the user's sealed owner entity the way enrollment does and
// records it in users.owner_entity_id.
func sealOwner(t testing.TB, root *Cortex, uk *keys.UK, userID, display string) {
	t.Helper()
	ctx := context.Background()
	id := store.NewID()
	seal := func(col, v string) []byte {
		ct, err := uk.Seal(userID, store.TblEntities, id, col, []byte(v))
		if err != nil {
			t.Fatal(err)
		}
		return ct
	}
	if _, err := root.h.DB.ExecContext(ctx,
		`INSERT INTO entities (id, user_id, is_owner, name_ct, type_ct, source_ct, attributes_ct, name_blind, type_blind)
		 VALUES (?, ?, TRUE, ?, ?, ?, ?, ?, ?)`,
		id, userID, seal("name", display), seal("type", "person"), seal("source", "owner"), seal("attributes", "{}"),
		uk.Blind(store.NormName(display)), uk.Blind(store.NormName("person"))); err != nil {
		t.Fatal(err)
	}
	if _, err := root.h.DB.ExecContext(ctx, `UPDATE users SET owner_entity_id = ? WHERE id = ?`, id, userID); err != nil {
		t.Fatal(err)
	}
}
