// Package tenancy implements user accounts, owner entities, per-user usage
// accounting and memory listing on top of the shared store handle.
package tenancy

import "github.com/sausheong/cortex/internal/store"

// Ops carries the operations of this package. It wraps a store handle and is
// cheap to construct per call.
type Ops struct{ *store.Handle }

// New returns the tenancy operations bound to h.
func New(h *store.Handle) Ops { return Ops{h} }
