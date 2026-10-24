package web

import (
	"context"

	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/keys"
	"github.com/sausheong/cortex/internal/web/pages"
)

// setupState reads the first-run checklist from real state: a provider with
// an API key, a live connection, a stored memory. It returns nil when all
// three are done (or when state cannot be read), so the checklist disappears.
func (s *server) setupState(ctx context.Context, userID string) *pages.Setup {
	st, _ := s.setupRead(ctx, userID, nil)
	return st
}

// setupRead is setupState plus whether every step is done.
//
// uk, when the request carries the user's key, lets the memory step read an
// encrypted account; without it that step cannot be read and the checklist
// stays out of the way.
func (s *server) setupRead(ctx context.Context, userID string, uk *keys.UK) (*pages.Setup, bool) {
	var st pages.Setup
	info, err := s.st.EnrollInfo(ctx, userID)
	if err != nil {
		return nil, false
	}
	// An account that had to enroll gets the passkey as step 1, and keeps it
	// ticked afterwards so the numbering does not shift under the user.
	st.Total = 3
	if s.st.EnrollEnabled() && info.Cohort {
		st.ShowPasskey, st.Total = true, 4
		if info.State != "none" {
			st.Passkey = true
			st.Done++
		} else {
			st.Gated = true
		}
	}
	var active bool // a live assistant that is in use, in the terms Home uses
	if ok, err := s.st.HasSettings(ctx, userID); err == nil && ok {
		st.Provider = true
	}
	if conns, err := s.st.ListConnections(ctx, userID); err == nil {
		for _, c := range conns {
			if c.RevokedAt == nil && c.RemovedAt == nil {
				st.Connect = true
				if assistantStatus(c, s.now()) == statusActive {
					active = true
				}
			}
		}
	} else {
		return nil, false
	}
	cx := s.root.ForUser(userID)
	if uk != nil && info.State != "none" {
		cx = cx.WithKey(uk, info.State)
		defer cx.Release()
	}
	if _, total, err := cx.ListMemories(ctx, cortex.MemoryListFilter{}, 1, 0); err == nil {
		st.Memory = total > 0
	} else {
		return nil, false
	}
	for _, b := range []bool{st.Provider, st.Connect, st.Memory} {
		if b {
			st.Done++
		}
	}
	if st.Done == st.Total {
		if !active {
			// Connected but unused or idle: not "set up" in the strip's terms,
			// and nothing left to tick, so show neither the checklist nor the line.
			return nil, false
		}
		return nil, true
	}
	return &st, false
}
