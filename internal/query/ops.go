// Package query implements curated graph queries (WhoIs, Describe, Path, Timeline, WhatChanged, Link), on top of the shared store handle.
package query

import "github.com/sausheong/cortex/internal/store"

// Ops carries the operations of this package. It wraps a store handle and is
// cheap to construct per call.
type Ops struct{ *store.Handle }

// New returns the query operations bound to h.
func New(h *store.Handle) Ops { return Ops{h} }
