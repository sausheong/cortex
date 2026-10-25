package vaultart_test

import (
	"testing"
	"time"

	"github.com/sausheong/cortex/internal/graph"
	"github.com/sausheong/cortex/internal/vaultart"
)

// Merging two written subjects: the kept entity's fingerprint no longer
// matches its article (so it rewrites, nothing is copied), the dropped one is
// no longer a subject, and its article row goes once the entity is purged.
func TestMergeInvalidatesKeepIndexAndDropGone(t *testing.T) {
	env := open(t)
	s := vaultart.DefaultSettings()
	keep := ent(t, env.H, "person", "Robert", nil)
	drop := ent(t, env.H, "person", "Bobby", nil)
	mems(t, env.H, keep, 3)
	mems(t, env.H, drop, 3)
	writeRow(t, env, personSub(keep, "Robert"), s)
	writeRow(t, env, personSub(drop, "Bobby"), s)
	ix := buildIx(t, env, "", s, time.Now())
	if e := entryFor(ix, keep); e == nil || e.Changed || entryFor(ix, drop) == nil {
		t.Fatalf("setup: %+v", entries(ix))
	}

	if _, err := graph.New(env.H).MergeEntities(bg, keep, drop); err != nil {
		t.Fatal(err)
	}
	ix = buildIx(t, env, "", s, time.Now())
	e := entryFor(ix, keep)
	if e == nil || !e.Changed {
		t.Fatalf("keep should be marked changed: %+v", e)
	}
	if entryFor(ix, drop) != nil {
		t.Fatal("dropped entity still listed")
	}
	if _, ok := ix.PathFor(drop); ok {
		t.Fatal("dropped entity still has a path")
	}

	// The merge soft-deletes the dropped entity: its article stays while the
	// merge can be undone, and goes when the entity is purged.
	if n, err := env.Keyless.PurgeOrphanArticles(bg, env.UserID); err != nil || n != 0 {
		t.Fatalf("purge before hard delete: %d %v", n, err)
	}
	env.Exec(`UPDATE entities SET deleted_at = now() - interval '31 days' WHERE id = $1`, drop)
	if _, err := graph.New(env.H).PurgeDeleted(bg, 30*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if n, err := env.Keyless.PurgeOrphanArticles(bg, env.UserID); err != nil || n != 1 {
		t.Fatalf("purge after hard delete: %d %v", n, err)
	}
	rows, _ := env.H.ListArticleMeta(bg)
	if len(rows) != 1 || rows[0].SubjectID != keep {
		t.Fatalf("rows left: %+v", rows)
	}
}

// A threshold change hides a written subject but keeps its row, and restoring
// the threshold finds the article current without any write.
func TestThresholdChangeKeepsRowAndFingerprint(t *testing.T) {
	env := open(t)
	s := vaultart.DefaultSettings()
	id := ent(t, env.H, "person", "Ada", nil)
	mems(t, env.H, id, 3)
	writeRow(t, env, personSub(id, "Ada"), s)
	hi := s
	hi.Threshold = 5
	if ix := buildIx(t, env, "", hi, time.Now()); entryFor(ix, id) != nil {
		t.Fatal("still listed above the threshold")
	}
	if n, err := env.Keyless.PurgeOrphanArticles(bg, env.UserID); err != nil || n != 0 {
		t.Fatalf("purge: %d %v", n, err)
	}
	ix := buildIx(t, env, "", s, time.Now())
	if e := entryFor(ix, id); e == nil || e.WrittenAt == nil || e.Changed {
		t.Fatalf("restored entry: %+v", e)
	}
}
