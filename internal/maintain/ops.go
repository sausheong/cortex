// Package maintain implements the reconsolidation passes: reconcile, relate, decay, expire, compaction, profile refresh and the combined Maintain pass, on top of the shared store handle.
package maintain

import "github.com/sausheong/cortex/internal/store"

// Ops carries the operations of this package. It wraps a store handle and is
// cheap to construct per call.
type Ops struct{ *store.Handle }

// New returns the maintain operations bound to h.
func New(h *store.Handle) Ops { return Ops{h} }
