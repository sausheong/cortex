package web

import (
	"archive/zip"
	"bytes"
	"io"
	"net/http"
	"testing"

	"github.com/sausheong/cortex"
)

// vaultEnv signs alice in with a small graph: Alice Chen works at Stripe.
func vaultEnv(t *testing.T) (*env, *cortex.Cortex, string) {
	t.Helper()
	e, c, id := memEnv(t)
	ctx := t.Context()
	alice := &cortex.Entity{Type: "person", Name: "Alice Chen", Source: "notes/intro.md", Attributes: map[string]any{"role": "Engineer"}}
	stripe := &cortex.Entity{Type: "organization", Name: "Stripe"}
	for _, en := range []*cortex.Entity{alice, stripe} {
		if err := c.PutEntity(ctx, en); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.PutRelationship(ctx, &cortex.Relationship{SourceID: alice.ID, TargetID: stripe.ID, Type: "works_at"}); err != nil {
		t.Fatal(err)
	}
	m := &cortex.Memory{Content: "Alice joined Stripe", EntityIDs: []string{alice.ID, stripe.ID}, Source: "notes/intro.md", Confidence: 1}
	if err := c.PutMemory(ctx, m); err != nil {
		t.Fatal(err)
	}
	return e, c, id
}

func TestVaultRequiresSignIn(t *testing.T) {
	e := newEnv(t)
	for _, p := range []string{"/vault", "/vault/index.md", "/vault/download"} {
		resp, _ := e.get(p)
		if resp.StatusCode != http.StatusFound {
			t.Errorf("%s: status %d, want redirect to sign-in", p, resp.StatusCode)
		}
	}
}

func readZip(t *testing.T, b []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		d, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(d)
	}
	return out
}

func TestSafeZipName(t *testing.T) {
	for n, want := range map[string]bool{"people/a.md": true, "index.md": true, "../x/a.md": false, "/a.md": false,
		"a\\b.md": false, "a/../b.md": false, "a\x00.md": false, "a//b.md": false} {
		if safeZipName(n) != want {
			t.Errorf("safeZipName(%q) != %v", n, want)
		}
	}
}
