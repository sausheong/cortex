package mcp

import (
	"context"
	"errors"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/internal/accounts"
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
}

// get returns the handle for this call, or a ready tool error result.
func (h handles) get(ctx context.Context) (*cortex.Cortex, *mcp.CallToolResult) {
	if v, ok := ctx.Value(handleKey{}).(handleVal); ok {
		if v.err != nil {
			if errors.Is(v.err, accounts.ErrNoProvider) {
				return nil, mcp.NewToolResultError("set up your AI provider at " + strings.TrimRight(h.publicURL, "/") + "/settings")
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
