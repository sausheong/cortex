package testutil

import (
	"testing"

	"github.com/sausheong/cortex"
)

func TestForEachModeRunsBothArms(t *testing.T) {
	ForEachMode(t, func(t *testing.T, c *cortex.Cortex) {
		if c.UserID() == "" {
			t.Fatal("no user")
		}
	})
	cx, uk := OpenEncrypted(t)
	if !cx.Encrypted() || uk == nil {
		t.Fatal("OpenEncrypted must return an encrypted handle")
	}
}
