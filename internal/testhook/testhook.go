// Package testhook lets test code outside package cortex reach the unexported
// store handle of a *cortex.Cortex without widening the public API. Package
// cortex installs both hooks from an init function; tests reach them through
// internal/testutil (H and Wrap).
package testhook

import "github.com/sausheong/cortex/internal/store"

var (
	// Handle returns the store handle behind a *cortex.Cortex.
	Handle func(c any) *store.Handle
	// Wrap builds a *cortex.Cortex around h.
	Wrap func(h *store.Handle) any
)
