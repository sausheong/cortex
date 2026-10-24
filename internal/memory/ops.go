// Package memory implements memories, chunks and their embeddings: storage,
// keyword and vector search, hybrid recall, bulk reads and re-embedding, on
// top of the shared store handle.
package memory

import "github.com/sausheong/cortex/internal/store"

// Ops carries the operations of this package. It wraps a store handle and is
// cheap to construct per call.
type Ops struct{ *store.Handle }

// New returns the memory operations bound to h.
func New(h *store.Handle) Ops { return Ops{h} }
