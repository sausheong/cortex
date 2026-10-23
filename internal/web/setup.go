package web

import (
	"context"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/web/pages"
)

// setupState reads the first-run checklist from real state: a provider with
// an API key, a live connection, a stored memory. It returns nil when all
// three are done (or when state cannot be read), so the checklist disappears.
func (s *server) setupState(ctx context.Context, userID string) *pages.Setup {
	var st pages.Setup
	if set, err := s.st.GetSettings(ctx, userID); err == nil && set.APIKey != "" {
		st.Provider = true
	}
	if conns, err := s.st.ListConnections(ctx, userID); err == nil {
		for _, c := range conns {
			if c.RevokedAt == nil && c.RemovedAt == nil {
				st.Connect = true
				break
			}
		}
	} else {
		return nil
	}
	if _, total, err := s.root.ForUser(userID).ListMemories(ctx, cortex.MemoryListFilter{}, 1, 0); err == nil {
		st.Memory = total > 0
	} else {
		return nil
	}
	for _, b := range []bool{st.Provider, st.Connect, st.Memory} {
		if b {
			st.Done++
		}
	}
	if st.Done == 3 {
		return nil
	}
	return &st
}
