package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/extractor/deterministic"
)

// newTestCortex returns a cortex backed by a hermetic SQLite database in
// t.TempDir, using only the deterministic extractor (no LLM, no embedder).
//
// We deliberately avoid ":memory:" because modernc.org/sqlite uses a connection
// pool, and a bare ":memory:" gives each pooled connection its own private
// database. cortex.Recall fans sub-queries out to goroutines, so a chunk
// written on one connection in Remember() is invisible to a goroutine that
// lands on a different connection — causing flaky empty results.
func newTestCortex(t *testing.T) *cortex.Cortex {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "cortex.db")
	cx, err := cortex.Open(dbPath, cortex.WithExtractor(deterministic.New()))
	if err != nil {
		t.Fatalf("cortex.Open: %v", err)
	}
	t.Cleanup(func() { cx.Close() })
	return cx
}

// newTestClient builds an MCPServer with all tools registered, wraps it in
// the streamable-HTTP test server, and returns a connected MCP client.
func newTestClient(t *testing.T, cx *cortex.Cortex) *client.Client {
	t.Helper()
	s := server.NewMCPServer("cortex-test", "0.0.0", server.WithToolCapabilities(false))
	registerTools(s, cx)
	httpServer := server.NewTestStreamableHTTPServer(s)
	t.Cleanup(httpServer.Close)

	tr, err := transport.NewStreamableHTTP(httpServer.URL + "/mcp")
	if err != nil {
		t.Fatalf("transport.NewStreamableHTTP: %v", err)
	}
	c := client.NewClient(tr)
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("client.Start: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "test", Version: "0.0.0"}
	if _, err := c.Initialize(context.Background(), initReq); err != nil {
		t.Fatalf("client.Initialize: %v", err)
	}
	return c
}

func TestRegisterTools_AllToolsListed(t *testing.T) {
	cx := newTestCortex(t)
	c := newTestClient(t, cx)

	resp, err := c.ListTools(context.Background(), mcp.ListToolsRequest{})
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	got := map[string]bool{}
	for _, tl := range resp.Tools {
		got[tl.Name] = true
	}
	// Task 6 verifies the 8 existing tools. Tasks 7-8 will extend this to 10.
	want := []string{"remember", "recall", "forget", "get_entity", "find_entities", "get_relationships", "traverse", "search"}
	for _, name := range want {
		if !got[name] {
			t.Errorf("missing tool: %s", name)
		}
	}
}

func TestRecallTool_RoundTrip(t *testing.T) {
	cx := newTestCortex(t)
	ctx := context.Background()
	if err := cx.Remember(ctx, "Alice works at Stripe."); err != nil {
		t.Fatalf("seed Remember: %v", err)
	}

	c := newTestClient(t, cx)
	callReq := mcp.CallToolRequest{}
	callReq.Params.Name = "recall"
	callReq.Params.Arguments = map[string]any{"query": "Alice"}

	resp, err := c.CallTool(ctx, callReq)
	if err != nil {
		t.Fatalf("CallTool recall: %v", err)
	}
	if resp.IsError {
		t.Fatalf("recall returned error: %+v", resp.Content)
	}
	text := textContent(t, resp)
	if !strings.Contains(text, "Alice") {
		t.Errorf("recall result does not contain Alice: %s", text)
	}
}

func TestFindEntitiesTool(t *testing.T) {
	cx := newTestCortex(t)
	ctx := context.Background()
	if err := cx.PutEntity(ctx, &cortex.Entity{Type: "person", Name: "Alice"}); err != nil {
		t.Fatalf("PutEntity: %v", err)
	}

	c := newTestClient(t, cx)
	callReq := mcp.CallToolRequest{}
	callReq.Params.Name = "find_entities"
	callReq.Params.Arguments = map[string]any{"type": "person"}

	resp, err := c.CallTool(ctx, callReq)
	if err != nil {
		t.Fatalf("CallTool find_entities: %v", err)
	}
	if resp.IsError {
		t.Fatalf("find_entities returned error: %+v", resp.Content)
	}
	var entities []cortex.Entity
	if err := json.Unmarshal([]byte(textContent(t, resp)), &entities); err != nil {
		t.Fatalf("unmarshal entities: %v", err)
	}
	if len(entities) != 1 || entities[0].Name != "Alice" {
		t.Errorf("unexpected entities: %+v", entities)
	}
}

// textContent extracts the first TextContent from a tool result.
// mcp-go v0.47.1 AsTextContent returns (*TextContent, bool).
func textContent(t *testing.T, r *mcp.CallToolResult) string {
	t.Helper()
	if len(r.Content) == 0 {
		t.Fatal("tool result has no content")
	}
	tc, ok := mcp.AsTextContent(r.Content[0])
	if !ok {
		t.Fatalf("expected text content, got %T", r.Content[0])
	}
	return tc.Text
}

func TestMergeTool_RoundTrip(t *testing.T) {
	cx := newTestCortex(t)
	ctx := context.Background()
	keep := &cortex.Entity{Type: "person", Name: "Alice Chen"}
	drop := &cortex.Entity{Type: "person", Name: "alice chen"}
	if err := cx.PutEntity(ctx, keep); err != nil {
		t.Fatalf("PutEntity keep: %v", err)
	}
	if err := cx.PutEntity(ctx, drop); err != nil {
		t.Fatalf("PutEntity drop: %v", err)
	}
	if err := cx.PutRelationship(ctx, &cortex.Relationship{
		SourceID: drop.ID, TargetID: keep.ID, Type: "duplicate_of",
	}); err != nil {
		t.Fatalf("PutRelationship: %v", err)
	}

	c := newTestClient(t, cx)
	callReq := mcp.CallToolRequest{}
	callReq.Params.Name = "merge"
	callReq.Params.Arguments = map[string]any{
		"keep_id": keep.ID,
		"drop_id": drop.ID,
	}
	resp, err := c.CallTool(ctx, callReq)
	if err != nil {
		t.Fatalf("CallTool merge: %v", err)
	}
	if resp.IsError {
		t.Fatalf("merge returned error: %+v", resp.Content)
	}
	var stats cortex.MergeStats
	if err := json.Unmarshal([]byte(textContent(t, resp)), &stats); err != nil {
		t.Fatalf("unmarshal MergeStats: %v", err)
	}
	if stats.Relationships < 1 {
		t.Errorf("expected at least 1 relationship re-targeted, got %d", stats.Relationships)
	}
	// Drop entity should be gone.
	if _, err := cx.GetEntity(ctx, drop.ID); err == nil {
		t.Errorf("expected drop entity %s to be deleted", drop.ID)
	}
}

func TestMergeTool_DryRun(t *testing.T) {
	cx := newTestCortex(t)
	ctx := context.Background()
	keep := &cortex.Entity{Type: "person", Name: "Alice Chen"}
	drop := &cortex.Entity{Type: "person", Name: "alice chen"}
	if err := cx.PutEntity(ctx, keep); err != nil {
		t.Fatalf("PutEntity keep: %v", err)
	}
	if err := cx.PutEntity(ctx, drop); err != nil {
		t.Fatalf("PutEntity drop: %v", err)
	}

	c := newTestClient(t, cx)
	callReq := mcp.CallToolRequest{}
	callReq.Params.Name = "merge"
	callReq.Params.Arguments = map[string]any{
		"keep_id": keep.ID,
		"drop_id": drop.ID,
		"dry_run": true,
	}
	resp, err := c.CallTool(ctx, callReq)
	if err != nil {
		t.Fatalf("CallTool merge dry-run: %v", err)
	}
	if resp.IsError {
		t.Fatalf("merge dry-run returned error: %+v", resp.Content)
	}
	// Drop entity should STILL exist.
	if _, err := cx.GetEntity(ctx, drop.ID); err != nil {
		t.Errorf("dry-run should not delete drop entity: %v", err)
	}
}
