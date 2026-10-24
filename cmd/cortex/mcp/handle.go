package mcp

import (
	"context"
	"errors"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
	"github.com/sausheong/cortex/internal/model"
)

type handleKey struct{}

type handleVal struct {
	cx  *cortex.Cortex
	err error
}

// withHandle stores the resolved per-request cortex handle (or the reason
// there is none) in ctx.
func withHandle(ctx context.Context, cx *cortex.Cortex, err error) context.Context {
	return context.WithValue(ctx, handleKey{}, handleVal{cx: cx, err: err})
}

type handles struct {
	fallback  *cortex.Cortex
	publicURL string
	// background, when set, takes remembers too large to do inline.
	background BackgroundRemember
}

// get returns the handle for this call, or a ready tool error result.
func (h handles) get(ctx context.Context) (*cortex.Cortex, *mcp.CallToolResult) {
	if v, ok := ctx.Value(handleKey{}).(handleVal); ok {
		if v.err != nil {
			if errors.Is(v.err, accounts.ErrNoProvider) {
				return nil, mcp.NewToolResultError("set up your AI provider at " + strings.TrimRight(h.publicURL, "/") + "/settings")
			}
			switch {
			case errors.Is(v.err, model.ErrNeedsReconnect):
				return nil, mcp.NewToolResultError(model.ErrNeedsReconnect.Error())
			case errors.Is(v.err, model.ErrTokenExpired):
				return nil, mcp.NewToolResultError("this assistant token has expired; renew it in Connections")
			case errors.Is(v.err, model.ErrLocked):
				return nil, mcp.NewToolResultError("cortex is locked; unlock it with your passkey in the browser, then try again")
			}
			return nil, mcp.NewToolResultError("internal error")
		}
		return v.cx, nil
	}
	if h.fallback == nil {
		return nil, mcp.NewToolResultError("internal error")
	}
	return h.fallback, nil
}
