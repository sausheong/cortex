package mcp

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/sausheong/cortex/internal/model"
)

func resText(r *mcp.CallToolResult) string {
	if len(r.Content) == 0 {
		return ""
	}
	if tc, ok := r.Content[0].(mcp.TextContent); ok {
		return tc.Text
	}
	return ""
}

func TestHandleMapsKeyErrors(t *testing.T) {
	h := handles{publicURL: "https://x.test"}
	for err, want := range map[error]string{
		model.ErrNeedsReconnect: "cortex is now encrypted; reconnect this assistant in Connections",
		model.ErrTokenExpired:   "this assistant token has expired; renew it in Connections",
	} {
		_, res := h.get(withHandle(context.Background(), nil, err))
		if res == nil || !res.IsError || !strings.Contains(resText(res), want) {
			t.Fatalf("%v -> %+v", err, res)
		}
	}
}
