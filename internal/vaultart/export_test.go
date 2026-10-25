package vaultart

import (
	"math/rand"

	"github.com/sausheong/cortex/internal/workset"
)

// ClusterShuffled clusters with the eligible ids presented in a random order.
func ClusterShuffled(set *workset.Set, s Settings, seed int64) []Topic {
	g := buildTopicGraph(set)
	r := rand.New(rand.NewSource(seed))
	r.Shuffle(len(g.ids), func(i, j int) { g.ids[i], g.ids[j] = g.ids[j], g.ids[i] })
	return clusterOn(g, s.Normalize())
}
