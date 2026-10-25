package workset_test

import (
	"testing"

	"github.com/sausheong/cortex/internal/memory"
	"github.com/sausheong/cortex/internal/model"
	"github.com/sausheong/cortex/internal/workset"
)

func TestVersionChangesOnWrite(t *testing.T) {
	setup(t)
	env := openEnc(t)
	ent, _ := seed(t, env)
	a, err := workset.For(bg, env.H)
	if err != nil {
		t.Fatal(err)
	}
	b, err := workset.For(bg, env.H)
	if err != nil {
		t.Fatal(err)
	}
	if a.Version() == "" || a.Version() != b.Version() {
		t.Fatalf("version unstable across reads: %q vs %q", a.Version(), b.Version())
	}
	m := &model.Memory{Content: "another fact", Source: "t", EntityIDs: []string{ent}}
	if err := memory.New(env.H).PutMemory(bg, m); err != nil {
		t.Fatal(err)
	}
	c, err := workset.For(bg, env.H)
	if err != nil {
		t.Fatal(err)
	}
	if c.Version() == a.Version() {
		t.Fatalf("version did not change after a write: %q", c.Version())
	}
	if a.Version() != b.Version() {
		t.Fatal("an existing snapshot's version must not move")
	}
}
