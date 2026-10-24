// Package graph implements the knowledge graph: entities, aliases and
// identity resolution, relationships, traversal, soft delete and restore,
// graph normalisation and lint, on top of the shared store handle.
package graph

import "github.com/sausheong/cortex/internal/store"

// Ops carries the operations of this package. It wraps a store handle and is
// cheap to construct per call.
type Ops struct{ *store.Handle }

// New returns the graph operations bound to h.
func New(h *store.Handle) Ops { return Ops{h} }
