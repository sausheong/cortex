// Package ingest implements Remember and Forget: extraction of content into
// entities, relationships, memories and chunks, and their removal, on top of
// the graph and memory packages.
package ingest

import "github.com/sausheong/cortex/internal/store"

// Ops carries the operations of this package. It wraps a store handle and is
// cheap to construct per call.
type Ops struct{ *store.Handle }

// New returns the ingest operations bound to h.
func New(h *store.Handle) Ops { return Ops{h} }
