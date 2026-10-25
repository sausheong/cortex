package vaultart

import (
	"math/rand"
	"time"

	"github.com/sausheong/cortex/internal/keys"

	"github.com/sausheong/cortex/internal/workset"
)

// ClusterShuffled clusters with the eligible ids presented in a random order.
func ClusterShuffled(set *workset.Set, s Settings, seed int64) []Topic {
	g := buildTopicGraph(set)
	r := rand.New(rand.NewSource(seed))
	r.Shuffle(len(g.ids), func(i, j int) { g.ids[i], g.ids[j] = g.ids[j], g.ids[i] })
	return clusterOn(g, s.Normalize())
}

// Writer test hooks.

// SetKeyHook receives each writer's own key clone as it is created.
func (w *Writer) SetKeyHook(f func(*keys.UK)) { w.keyHook = f }

// SetBeforeRun runs at the top of every writer goroutine.
func (w *Writer) SetBeforeRun(f func()) { w.beforeRun = f }

// InjectInflight plants an in-flight entry started at the given time.
func (w *Writer) InjectInflight(userID, subjectID string, started time.Time) {
	w.mu.Lock()
	w.inflight[userID+"|"+subjectID] = &entry{userID: userID, started: started}
	w.mu.Unlock()
}

// InflightCount reports the in-flight entries.
func (w *Writer) InflightCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.inflight)
}
