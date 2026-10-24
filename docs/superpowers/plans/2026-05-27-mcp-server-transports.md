# Cortex MCP Server — stdio + Streamable HTTP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `cortex-mcp` serve either stdio or streamable HTTP from one binary via `--transport`, add `merge` and `lint` MCP tools, and delete the now-redundant `cortex-http` REST binary.

**Architecture:** Refactor existing inline tool registration into a `registerTools()` function reused by both transports. Add a thin streamable-HTTP server wrapping `mark3labs/mcp-go`'s `NewStreamableHTTPServer`, with optional bearer-token auth and a `/healthz` endpoint. Default invocation `cortex-mcp` (no flags) keeps current stdio behavior so existing Claude Desktop configs don't break.

**Tech Stack:** Go 1.25+, `github.com/mark3labs/mcp-go` v0.47.1 (already a dep), stdlib `net/http` and `crypto/subtle` for auth.

**Spec reference:** `docs/superpowers/specs/2026-05-27-mcp-server-transports-design.md`

---

## File Structure

| File | Status | Responsibility |
|---|---|---|
| `cmd/cortex-mcp/main.go` | Modify | Slim main: flag parsing, openCortex, transport dispatch. |
| `cmd/cortex-mcp/tools.go` | Create | `registerTools(s, cx)` builds all 10 tools. `jsonResult()` helper lives here. |
| `cmd/cortex-mcp/auth.go` | Create | `bearerAuthMiddleware`, `isLoopback`. |
| `cmd/cortex-mcp/serve_stdio.go` | Create | `serveStdio(s)` wrapper around `server.ServeStdio`. |
| `cmd/cortex-mcp/serve_http.go` | Create | `serveHTTP(s, addr, token)` — streamable-HTTP + healthz + warning. |
| `cmd/cortex-mcp/tools_test.go` | Create | Hermetic tests for `registerTools` + per-tool round-trip via streamable HTTP test server. |
| `cmd/cortex-mcp/auth_test.go` | Create | `bearerAuthMiddleware` + `isLoopback` tests. |
| `cmd/cortex-mcp/main_test.go` | Create | Flag-parsing tests. |
| `cmd/cortex-http/main.go` | Delete | Replaced by MCP-over-streamable-HTTP. |
| `Makefile` | Modify | Drop `cortex-http` build/install targets. |
| `README.md` | Modify | Remove `cortex-http` references; document `--transport`. |

---

## Task 1: Extract `registerTools()` from main.go (pure refactor, no behavior change)

This is a mechanical move. All 8 existing tools and the `jsonResult` helper move into a new `tools.go`. `main.go` calls `registerTools()` instead of inline `s.AddTool(...)` blocks.

**Files:**
- Create: `cmd/cortex-mcp/tools.go`
- Modify: `cmd/cortex-mcp/main.go`

- [ ] **Step 1: Create tools.go with all 8 tools**

Create `cmd/cortex-mcp/tools.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/sausheong/cortex"
)

// registerTools registers all cortex MCP tools on the given server.
// Both stdio and streamable-HTTP transports share this exact tool set.
func registerTools(s *server.MCPServer, cx *cortex.Cortex) {
	s.AddTool(
		mcp.NewTool("remember",
			mcp.WithDescription("Store content in the knowledge graph. Extracts entities, relationships, memories, and chunks."),
			mcp.WithString("content", mcp.Required(), mcp.Description("The text content to remember")),
			mcp.WithString("source", mcp.Description("Optional source label for the content")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			content, err := req.RequireString("content")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			var opts []cortex.RememberOption
			if src := req.GetString("source", ""); src != "" {
				opts = append(opts, cortex.WithSource(src))
			}
			if err := cx.Remember(ctx, content, opts...); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText("remembered"), nil
		},
	)

	s.AddTool(
		mcp.NewTool("recall",
			mcp.WithDescription("Recall information from the knowledge graph using multi-strategy retrieval."),
			mcp.WithString("query", mcp.Required(), mcp.Description("The query to search for")),
			mcp.WithNumber("limit", mcp.Description("Maximum number of results (default 20)")),
			mcp.WithNumber("min_confidence", mcp.Description("Filter results below this confidence threshold (0.0-1.0). Default 0 = no filter.")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			query, err := req.RequireString("query")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			var opts []cortex.RecallOption
			if limit := req.GetInt("limit", 0); limit > 0 {
				opts = append(opts, cortex.WithLimit(limit))
			}
			if mc := req.GetFloat("min_confidence", 0); mc > 0 {
				if mc > 1 {
					return mcp.NewToolResultError("min_confidence must be between 0 and 1"), nil
				}
				opts = append(opts, cortex.WithMinConfidence(mc))
			}
			results, err := cx.Recall(ctx, query, opts...)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(results)
		},
	)

	s.AddTool(
		mcp.NewTool("forget",
			mcp.WithDescription("Remove knowledge from the graph by entity ID or source."),
			mcp.WithString("entity_id", mcp.Description("Entity ID to forget")),
			mcp.WithString("source", mcp.Description("Source label to forget")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			entityID := req.GetString("entity_id", "")
			source := req.GetString("source", "")
			if entityID == "" && source == "" {
				return mcp.NewToolResultError("either entity_id or source is required"), nil
			}
			filter := cortex.Filter{EntityID: entityID, Source: source}
			if err := cx.Forget(ctx, filter); err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultText("forgotten"), nil
		},
	)

	s.AddTool(
		mcp.NewTool("get_entity",
			mcp.WithDescription("Retrieve an entity by its ID."),
			mcp.WithString("id", mcp.Required(), mcp.Description("The entity ID")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			id, err := req.RequireString("id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			entity, err := cx.GetEntity(ctx, id)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(entity)
		},
	)

	s.AddTool(
		mcp.NewTool("find_entities",
			mcp.WithDescription("Find entities matching optional filters (type, name, source)."),
			mcp.WithString("type", mcp.Description("Filter by entity type")),
			mcp.WithString("name", mcp.Description("Filter by name (LIKE pattern)")),
			mcp.WithString("source", mcp.Description("Filter by source")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			filter := cortex.EntityFilter{
				Type:     req.GetString("type", ""),
				NameLike: req.GetString("name", ""),
				Source:   req.GetString("source", ""),
			}
			entities, err := cx.FindEntities(ctx, filter)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(entities)
		},
	)

	s.AddTool(
		mcp.NewTool("get_relationships",
			mcp.WithDescription("Get relationships for an entity, optionally filtered by type."),
			mcp.WithString("entity_id", mcp.Required(), mcp.Description("The entity ID")),
			mcp.WithString("type", mcp.Description("Filter by relationship type")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			entityID, err := req.RequireString("entity_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			var filters []cortex.RelFilter
			if relType := req.GetString("type", ""); relType != "" {
				filters = append(filters, cortex.RelTypeFilter(relType))
			}
			rels, err := cx.GetRelationships(ctx, entityID, filters...)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(rels)
		},
	)

	s.AddTool(
		mcp.NewTool("traverse",
			mcp.WithDescription("Traverse the knowledge graph from a starting entity using BFS."),
			mcp.WithString("start_id", mcp.Required(), mcp.Description("Starting entity ID")),
			mcp.WithNumber("depth", mcp.Description("Traversal depth (default 1)")),
			mcp.WithString("edge_types", mcp.Description("Comma-separated edge types to follow")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			startID, err := req.RequireString("start_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			var opts []cortex.TraverseOption
			if depth := req.GetInt("depth", 0); depth > 0 {
				opts = append(opts, cortex.WithDepth(depth))
			}
			if edgeTypesStr := req.GetString("edge_types", ""); edgeTypesStr != "" {
				types := strings.Split(edgeTypesStr, ",")
				for i := range types {
					types[i] = strings.TrimSpace(types[i])
				}
				opts = append(opts, cortex.WithEdgeTypes(types...))
			}
			graph, err := cx.Traverse(ctx, startID, opts...)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(graph)
		},
	)

	s.AddTool(
		mcp.NewTool("search",
			mcp.WithDescription("Search the knowledge graph using keyword, vector, or memory search."),
			mcp.WithString("query", mcp.Required(), mcp.Description("The search query")),
			mcp.WithString("mode", mcp.Required(), mcp.Description("Search mode"), mcp.Enum("keyword", "vector", "memory")),
			mcp.WithNumber("limit", mcp.Description("Maximum number of results (default 10)")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			query, err := req.RequireString("query")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			mode, err := req.RequireString("mode")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			limit := req.GetInt("limit", 10)

			switch mode {
			case "keyword":
				chunks, err := cx.SearchKeyword(ctx, query, limit)
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				return jsonResult(chunks)
			case "vector":
				chunks, err := cx.SearchVector(ctx, query, limit)
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				return jsonResult(chunks)
			case "memory":
				memories, err := cx.SearchMemories(ctx, query, limit)
				if err != nil {
					return mcp.NewToolResultError(err.Error()), nil
				}
				return jsonResult(memories)
			default:
				return mcp.NewToolResultError(fmt.Sprintf("unknown search mode: %s", mode)), nil
			}
		},
	)
}

func jsonResult(v any) (*mcp.CallToolResult, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcp.NewToolResultError(fmt.Sprintf("json marshal error: %v", err)), nil
	}
	return mcp.NewToolResultText(string(b)), nil
}
```

- [ ] **Step 2: Slim main.go to call registerTools**

Replace the entire `main()` body in `cmd/cortex-mcp/main.go` (currently lines 22–248) with:

```go
func main() {
	cx := openCortex()
	defer cx.Close()

	s := server.NewMCPServer("cortex", "1.0.0", server.WithToolCapabilities(false))
	registerTools(s, cx)

	if err := server.ServeStdio(s); err != nil {
		fmt.Fprintf(os.Stderr, "mcp server error: %v\n", err)
		os.Exit(1)
	}
}
```

Also delete the now-orphaned `jsonResult` function at the bottom of main.go (moved to tools.go) and remove unused imports (`context`, `encoding/json`, `strings`, `github.com/mark3labs/mcp-go/mcp`). Keep `openCortex` and `configureEmbedder` unchanged.

After the edit, the unused-import set in main.go is likely: remove `context`, `encoding/json`, `strings`, `github.com/mark3labs/mcp-go/mcp`. Run `goimports` mentally or use `go build` to verify.

- [ ] **Step 3: Build and run existing tests**

Run:
```bash
go build ./cmd/cortex-mcp/
go test ./...
```
Expected: build succeeds; all tests pass (no new tests yet, but nothing should regress).

- [ ] **Step 4: Commit**

```bash
git add cmd/cortex-mcp/tools.go cmd/cortex-mcp/main.go
git commit -m "refactor(cortex-mcp): extract tool registry into registerTools()"
```

---

## Task 2: `isLoopback` helper + tests

Standalone helper used by `serveHTTP` to decide whether to log the no-auth warning.

**Files:**
- Create: `cmd/cortex-mcp/auth.go` (helper only; middleware added in Task 3)
- Create: `cmd/cortex-mcp/auth_test.go`

- [ ] **Step 1: Write the failing test**

Create `cmd/cortex-mcp/auth_test.go`:

```go
package main

import "testing"

func TestIsLoopback(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:8080", true},
		{"localhost:8080", true},
		{"[::1]:8080", true},
		{"0.0.0.0:8080", false},
		{":8080", false},
		{"192.168.1.5:8080", false},
		{"example.com:8080", false},
	}
	for _, c := range cases {
		got := isLoopback(c.addr)
		if got != c.want {
			t.Errorf("isLoopback(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
go test ./cmd/cortex-mcp/ -run TestIsLoopback
```
Expected: FAIL with `undefined: isLoopback`.

- [ ] **Step 3: Implement isLoopback**

Create `cmd/cortex-mcp/auth.go`:

```go
package main

import (
	"net"
)

// isLoopback reports whether the given host:port address binds only the
// loopback interface. Empty host (":8080") binds all interfaces in Go's
// http.ListenAndServe, so it's treated as non-loopback.
func isLoopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}
```

- [ ] **Step 4: Run test to verify it passes**

```bash
go test ./cmd/cortex-mcp/ -run TestIsLoopback
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/cortex-mcp/auth.go cmd/cortex-mcp/auth_test.go
git commit -m "feat(cortex-mcp): add isLoopback helper for bind-address classification"
```

---

## Task 3: Bearer auth middleware + tests

**Files:**
- Modify: `cmd/cortex-mcp/auth.go`
- Modify: `cmd/cortex-mcp/auth_test.go`

- [ ] **Step 1: Add the failing tests**

Append to `cmd/cortex-mcp/auth_test.go`:

```go
import (
	"io"
	"net/http"
	"net/http/httptest"
)

// (move the import block above the existing one; or merge into a grouped import.)

func TestBearerAuthMiddleware_NoToken(t *testing.T) {
	// When token is empty we should not be calling the middleware at all,
	// but verify defensive behavior: empty token + empty header still rejects.
	called := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	})
	h := bearerAuthMiddleware(next, "")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if called {
		t.Fatal("downstream handler should not be called")
	}
}

func TestBearerAuthMiddleware_ValidToken(t *testing.T) {
	const tok = "s3cret"
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	h := bearerAuthMiddleware(next, tok)
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
}

func TestBearerAuthMiddleware_MissingHeader(t *testing.T) {
	const tok = "s3cret"
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("downstream handler should not be called")
	})
	h := bearerAuthMiddleware(next, tok)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if string(body) != `{"error":"unauthorized"}` {
		t.Fatalf("unexpected body: %q", string(body))
	}
	if rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("expected JSON content-type, got %q", rec.Header().Get("Content-Type"))
	}
}

func TestBearerAuthMiddleware_WrongToken(t *testing.T) {
	const tok = "s3cret"
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("downstream handler should not be called")
	})
	h := bearerAuthMiddleware(next, tok)
	req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestBearerAuthMiddleware_MalformedHeader(t *testing.T) {
	const tok = "s3cret"
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("downstream handler should not be called")
	})
	h := bearerAuthMiddleware(next, tok)
	cases := []string{
		"Bearer" + tok, // no space between scheme and token
		"Basic " + tok, // wrong scheme
		tok,            // raw token, no scheme
	}
	for _, hv := range cases {
		req := httptest.NewRequest(http.MethodPost, "/mcp", nil)
		req.Header.Set("Authorization", hv)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("header %q: expected 401, got %d", hv, rec.Code)
		}
	}
}
```

Note: the second `import` block must be merged with the existing one in auth_test.go. Final import block should be:

```go
import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./cmd/cortex-mcp/ -run TestBearerAuth
```
Expected: FAIL with `undefined: bearerAuthMiddleware`.

- [ ] **Step 3: Implement the middleware**

Append to `cmd/cortex-mcp/auth.go`:

```go
import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// (merge with the existing "net" import block.)

// bearerAuthMiddleware wraps next with a constant-time Bearer-token check
// against expected. Returns 401 with a JSON {"error":"unauthorized"} body
// on any mismatch (missing header, wrong scheme, wrong token).
func bearerAuthMiddleware(next http.Handler, expected string) http.Handler {
	want := []byte(expected)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		const prefix = "Bearer "
		if !strings.HasPrefix(h, prefix) ||
			subtle.ConstantTimeCompare([]byte(h[len(prefix):]), want) != 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}
```

Final imports in auth.go (merged):

```go
import (
	"crypto/subtle"
	"net"
	"net/http"
	"strings"
)
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./cmd/cortex-mcp/
```
Expected: PASS for all `TestBearerAuth*` and `TestIsLoopback` tests.

- [ ] **Step 5: Commit**

```bash
git add cmd/cortex-mcp/auth.go cmd/cortex-mcp/auth_test.go
git commit -m "feat(cortex-mcp): add bearer-token auth middleware"
```

---

## Task 4: `serve_stdio.go` and `serve_http.go` (transport wrappers)

Stdio is a one-line wrapper; HTTP needs the streamable handler, `/healthz`, and the no-auth warning.

**Files:**
- Create: `cmd/cortex-mcp/serve_stdio.go`
- Create: `cmd/cortex-mcp/serve_http.go`

- [ ] **Step 1: Create serve_stdio.go**

```go
package main

import (
	"fmt"
	"os"

	"github.com/mark3labs/mcp-go/server"
)

// serveStdio runs the MCP server on stdin/stdout. Blocks until the connection
// closes or errors.
func serveStdio(s *server.MCPServer) error {
	fmt.Fprintln(os.Stderr, "cortex-mcp: stdio transport ready")
	return server.ServeStdio(s)
}
```

- [ ] **Step 2: Write failing tests for serve_http (mux composition + warning)**

Create `cmd/cortex-mcp/serve_http_test.go`:

```go
package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/server"
)

func TestBuildHTTPMux_HealthzNeverAuthenticated(t *testing.T) {
	s := server.NewMCPServer("test", "0.0.0", server.WithToolCapabilities(false))
	mux := buildHTTPMux(s, "any-token")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("healthz with no auth header should return 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Errorf("unexpected healthz body: %q", rec.Body.String())
	}
}

func TestBuildHTTPMux_MCPAuthGated(t *testing.T) {
	s := server.NewMCPServer("test", "0.0.0", server.WithToolCapabilities(false))
	mux := buildHTTPMux(s, "s3cret")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`)))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("/mcp without auth should be 401, got %d", rec.Code)
	}
}

func TestBuildHTTPMux_NoTokenSkipsAuth(t *testing.T) {
	s := server.NewMCPServer("test", "0.0.0", server.WithToolCapabilities(false))
	mux := buildHTTPMux(s, "")

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`)))
	// Without a token, the request reaches the streamable HTTP handler and gets
	// whatever mcp-go decides (likely a 4xx/5xx for malformed input), but
	// crucially NOT a 401 from our middleware.
	if rec.Code == http.StatusUnauthorized {
		t.Errorf("/mcp without configured token should not return 401, got %d", rec.Code)
	}
}

func TestMaybeWarnNonLoopback(t *testing.T) {
	cases := []struct {
		name    string
		addr    string
		token   string
		warnSub string // substring expected in output; "" = expect no warning
	}{
		{"non-loopback no token warns", "0.0.0.0:8080", "", "non-loopback"},
		{"non-loopback with token silent", "0.0.0.0:8080", "tok", ""},
		{"loopback no token silent", "127.0.0.1:8080", "", ""},
		{"loopback with token silent", "127.0.0.1:8080", "tok", ""},
		{"empty host treated as non-loopback warns", ":8080", "", "non-loopback"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			maybeWarnNonLoopback(&buf, c.addr, c.token)
			out := buf.String()
			if c.warnSub == "" {
				if out != "" {
					t.Errorf("expected no warning, got %q", out)
				}
			} else if !strings.Contains(out, c.warnSub) {
				t.Errorf("expected warning containing %q, got %q", c.warnSub, out)
			}
		})
	}
}
```

- [ ] **Step 3: Run tests to verify they fail**

```bash
go test ./cmd/cortex-mcp/ -run "TestBuildHTTPMux|TestMaybeWarnNonLoopback"
```
Expected: FAIL with `undefined: buildHTTPMux` and `undefined: maybeWarnNonLoopback`.

- [ ] **Step 4: Create serve_http.go**

```go
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/mark3labs/mcp-go/server"
)

// buildHTTPMux composes the streamable-HTTP MCP handler with optional bearer-
// token auth and an unauthenticated /healthz endpoint. Factored out of
// serveHTTP so it can be tested without binding a port.
func buildHTTPMux(s *server.MCPServer, token string) *http.ServeMux {
	streamable := server.NewStreamableHTTPServer(s)

	mux := http.NewServeMux()
	var mcpHandler http.Handler = streamable
	if token != "" {
		mcpHandler = bearerAuthMiddleware(streamable, token)
	}
	mux.Handle("/mcp", mcpHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	return mux
}

// maybeWarnNonLoopback writes a warning to w when binding to a non-loopback
// address without an auth token. Extracted for testability.
func maybeWarnNonLoopback(w io.Writer, addr, token string) {
	if !isLoopback(addr) && token == "" {
		fmt.Fprintln(w, "warning: HTTP transport bound to non-loopback address without CORTEX_AUTH_TOKEN — anyone on the network can call destructive tools")
	}
}

// serveHTTP runs the MCP server on the streamable-HTTP transport at /mcp on
// the given address. If token is non-empty, /mcp requires a matching
// Authorization: Bearer header. /healthz is always unauthenticated.
func serveHTTP(s *server.MCPServer, addr, token string) error {
	mux := buildHTTPMux(s, token)
	maybeWarnNonLoopback(os.Stderr, addr, token)
	fmt.Fprintf(os.Stderr, "cortex-mcp: streamable-http transport ready on %s/mcp\n", addr)
	return http.ListenAndServe(addr, mux)
}
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./cmd/cortex-mcp/
```
Expected: PASS for `TestBuildHTTPMux_*` and `TestMaybeWarnNonLoopback`, plus all earlier tests.

- [ ] **Step 6: Commit**

```bash
git add cmd/cortex-mcp/serve_stdio.go cmd/cortex-mcp/serve_http.go cmd/cortex-mcp/serve_http_test.go
git commit -m "feat(cortex-mcp): add stdio and streamable-HTTP transport wrappers"
```

---

## Task 5: Flag parsing in main.go + dispatch

**Files:**
- Modify: `cmd/cortex-mcp/main.go`
- Create: `cmd/cortex-mcp/main_test.go`

- [ ] **Step 1: Write failing flag-parsing tests**

Create `cmd/cortex-mcp/main_test.go`:

```go
package main

import (
	"os"
	"testing"
)

func TestParseFlags_Defaults(t *testing.T) {
	cfg, err := parseFlags([]string{"cortex-mcp"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.transport != "stdio" {
		t.Errorf("default transport = %q, want %q", cfg.transport, "stdio")
	}
	if cfg.addr != "127.0.0.1:8080" {
		t.Errorf("default addr = %q, want %q", cfg.addr, "127.0.0.1:8080")
	}
}

func TestParseFlags_TransportHTTP(t *testing.T) {
	cfg, err := parseFlags([]string{"cortex-mcp", "--transport", "http"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.transport != "http" {
		t.Errorf("transport = %q, want %q", cfg.transport, "http")
	}
}

func TestParseFlags_AddrOverride(t *testing.T) {
	cfg, err := parseFlags([]string{"cortex-mcp", "--transport", "http", "--addr", "0.0.0.0:9000"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.addr != "0.0.0.0:9000" {
		t.Errorf("addr = %q, want %q", cfg.addr, "0.0.0.0:9000")
	}
}

func TestParseFlags_EnvFallback(t *testing.T) {
	t.Setenv("CORTEX_TRANSPORT", "http")
	t.Setenv("CORTEX_ADDR", ":9999")
	cfg, err := parseFlags([]string{"cortex-mcp"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.transport != "http" {
		t.Errorf("transport from env = %q, want %q", cfg.transport, "http")
	}
	if cfg.addr != ":9999" {
		t.Errorf("addr from env = %q, want %q", cfg.addr, ":9999")
	}
}

func TestParseFlags_FlagBeatsEnv(t *testing.T) {
	t.Setenv("CORTEX_TRANSPORT", "http")
	cfg, err := parseFlags([]string{"cortex-mcp", "--transport", "stdio"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.transport != "stdio" {
		t.Errorf("flag should beat env: got %q, want %q", cfg.transport, "stdio")
	}
}

func TestParseFlags_InvalidTransport(t *testing.T) {
	_, err := parseFlags([]string{"cortex-mcp", "--transport", "carrier-pigeon"})
	if err == nil {
		t.Fatal("expected error for invalid transport")
	}
}

func TestParseFlags_UnknownFlag(t *testing.T) {
	_, err := parseFlags([]string{"cortex-mcp", "--what"})
	if err == nil {
		t.Fatal("expected error for unknown flag")
	}
}

func TestParseFlags_TokenFromEnv(t *testing.T) {
	t.Setenv("CORTEX_AUTH_TOKEN", "s3cret")
	cfg, err := parseFlags([]string{"cortex-mcp"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.token != "s3cret" {
		t.Errorf("token = %q, want %q", cfg.token, "s3cret")
	}
}

func TestMain(m *testing.M) {
	// Clear any CORTEX_* env vars that might leak from the surrounding shell
	// (don't want them to influence default-value tests).
	os.Unsetenv("CORTEX_TRANSPORT")
	os.Unsetenv("CORTEX_ADDR")
	os.Unsetenv("CORTEX_AUTH_TOKEN")
	os.Exit(m.Run())
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./cmd/cortex-mcp/ -run TestParseFlags
```
Expected: FAIL with `undefined: parseFlags`.

- [ ] **Step 3: Implement parseFlags and rewire main**

Replace `cmd/cortex-mcp/main.go` `main()` function. Final main.go top:

```go
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"

	oai "github.com/sashabaranov/go-openai"
	"github.com/mark3labs/mcp-go/server"
	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/extractor/deterministic"
	"github.com/sausheong/cortex/extractor/hybrid"
	llmext "github.com/sausheong/cortex/extractor/llmext"
	anthropicllm "github.com/sausheong/cortex/llm/anthropic"
	oaillm "github.com/sausheong/cortex/llm/openai"
)

type config struct {
	transport string
	addr      string
	token     string
}

func parseFlags(args []string) (config, error) {
	// Defaults from env (or hard-coded fallback).
	cfg := config{
		transport: envOr("CORTEX_TRANSPORT", "stdio"),
		addr:      envOr("CORTEX_ADDR", "127.0.0.1:8080"),
		token:     os.Getenv("CORTEX_AUTH_TOKEN"),
	}

	fs := flag.NewFlagSet("cortex-mcp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	transport := fs.String("transport", cfg.transport, "transport: stdio | http")
	addr := fs.String("addr", cfg.addr, "host:port for http transport")

	if err := fs.Parse(args[1:]); err != nil {
		return config{}, err
	}
	cfg.transport = *transport
	cfg.addr = *addr

	switch cfg.transport {
	case "stdio", "http":
	default:
		return config{}, fmt.Errorf("invalid --transport %q (must be stdio or http)", cfg.transport)
	}
	return cfg, nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	cfg, err := parseFlags(os.Args)
	if err != nil {
		os.Exit(2)
	}

	cx := openCortex()
	defer cx.Close()

	s := server.NewMCPServer("cortex", "1.0.0", server.WithToolCapabilities(false))
	registerTools(s, cx)

	var serveErr error
	switch cfg.transport {
	case "stdio":
		serveErr = serveStdio(s)
	case "http":
		serveErr = serveHTTP(s, cfg.addr, cfg.token)
	}
	if serveErr != nil {
		fmt.Fprintf(os.Stderr, "mcp server error: %v\n", serveErr)
		os.Exit(1)
	}
}
```

The existing `openCortex` and `configureEmbedder` functions remain unchanged below this. Note: also remove the existing `dbPath` resolution code at the top of `openCortex` — the `CORTEX_DB` env var is already supported there in the current code and stays as-is.

Important: keep the existing imports `oai`, `oaillm`, `anthropicllm`, `cortex`, etc. used by `openCortex` / `configureEmbedder` — only add `flag` to the import block. Remove `strings` if no longer used elsewhere (it isn't — that import moved to tools.go).

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./cmd/cortex-mcp/
```
Expected: PASS for all parse-flag tests; existing auth tests still pass.

- [ ] **Step 5: Build and run a smoke check**

```bash
go build -o /tmp/cortex-mcp ./cmd/cortex-mcp/
echo '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | /tmp/cortex-mcp 2>/dev/null | head -c 500
```
Expected: JSON response listing 8 tools.

- [ ] **Step 6: Commit**

```bash
git add cmd/cortex-mcp/main.go cmd/cortex-mcp/main_test.go
git commit -m "feat(cortex-mcp): add --transport flag (stdio default, http alternative)"
```

---

## Task 6: End-to-end HTTP transport test (8 existing tools)

Verifies that registering tools on a server, wrapping with `NewTestStreamableHTTPServer`, and calling tools over HTTP returns expected results. This test exists before we add the new tools so we can catch regressions.

**Files:**
- Create: `cmd/cortex-mcp/tools_test.go`

- [ ] **Step 1: Write the failing test**

Create `cmd/cortex-mcp/tools_test.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
	"github.com/sausheong/cortex"
	"github.com/sausheong/cortex/extractor/deterministic"
)

// newTestCortex returns a cortex backed by an in-memory SQLite database,
// using only the deterministic extractor (no LLM, no embedder).
func newTestCortex(t *testing.T) *cortex.Cortex {
	t.Helper()
	cx, err := cortex.Open(":memory:", cortex.WithExtractor(deterministic.New()))
	if err != nil {
		t.Fatalf("cortex.Open: %v", err)
	}
	t.Cleanup(func() { cx.Close() })
	return cx
}

// newTestServer builds an MCPServer with all tools registered, wraps it in
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
```

- [ ] **Step 2: Run tests to verify they pass**

```bash
go test ./cmd/cortex-mcp/ -run "TestRegisterTools|TestRecallTool|TestFindEntitiesTool"
```
Expected: PASS for all three. If `mcp.AsTextContent` is the wrong helper name in this mcp-go version, the substitute is a type assertion `r.Content[0].(mcp.TextContent)` — adjust during implementation if the import-time check shows it.

- [ ] **Step 3: Commit**

```bash
git add cmd/cortex-mcp/tools_test.go
git commit -m "test(cortex-mcp): add end-to-end streamable-HTTP transport tests"
```

---

## Task 7: Add `merge` tool

Wraps `cortex.MergeEntities` / `MergeEntitiesDryRun`.

**Files:**
- Modify: `cmd/cortex-mcp/tools.go`
- Modify: `cmd/cortex-mcp/tools_test.go`

- [ ] **Step 1: Write failing tests**

Append to `cmd/cortex-mcp/tools_test.go`:

```go
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
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./cmd/cortex-mcp/ -run TestMergeTool
```
Expected: FAIL — `merge` tool doesn't exist yet; server returns an error result.

- [ ] **Step 3: Add the merge tool to tools.go**

Append inside `registerTools()` in `cmd/cortex-mcp/tools.go`, after the `search` tool block:

```go
	s.AddTool(
		mcp.NewTool("merge",
			mcp.WithDescription("Merge one entity into another, re-targeting all relationships, memory links, and chunks. Use to clean up duplicate entities surfaced by lint."),
			mcp.WithString("keep_id", mcp.Required(), mcp.Description("Entity ID to keep")),
			mcp.WithString("drop_id", mcp.Required(), mcp.Description("Entity ID to merge into keep and delete")),
			mcp.WithBoolean("dry_run", mcp.Description("Simulate the merge and roll back; returns the stats without modifying the graph")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			keepID, err := req.RequireString("keep_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			dropID, err := req.RequireString("drop_id")
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			dryRun := req.GetBool("dry_run", false)
			var stats cortex.MergeStats
			if dryRun {
				stats, err = cx.MergeEntitiesDryRun(ctx, keepID, dropID)
			} else {
				stats, err = cx.MergeEntities(ctx, keepID, dropID)
			}
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return jsonResult(stats)
		},
	)
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./cmd/cortex-mcp/ -run TestMergeTool
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/cortex-mcp/tools.go cmd/cortex-mcp/tools_test.go
git commit -m "feat(cortex-mcp): add merge tool"
```

---

## Task 8: Add `lint` tool with json/markdown format

**Files:**
- Modify: `cmd/cortex-mcp/tools.go`
- Modify: `cmd/cortex-mcp/tools_test.go`

- [ ] **Step 1: Write failing tests**

Append to `cmd/cortex-mcp/tools_test.go`:

```go
func TestLintTool_JSON(t *testing.T) {
	cx := newTestCortex(t)
	ctx := context.Background()
	// Seed an orphan entity (no relationships, no memory links).
	orphan := &cortex.Entity{Type: "concept", Name: "Mauve"}
	if err := cx.PutEntity(ctx, orphan); err != nil {
		t.Fatalf("PutEntity orphan: %v", err)
	}

	c := newTestClient(t, cx)
	callReq := mcp.CallToolRequest{}
	callReq.Params.Name = "lint"
	callReq.Params.Arguments = map[string]any{} // default format=json

	resp, err := c.CallTool(ctx, callReq)
	if err != nil {
		t.Fatalf("CallTool lint: %v", err)
	}
	if resp.IsError {
		t.Fatalf("lint returned error: %+v", resp.Content)
	}
	var report cortex.LintReport
	if err := json.Unmarshal([]byte(textContent(t, resp)), &report); err != nil {
		t.Fatalf("unmarshal LintReport: %v", err)
	}
	foundOrphan := false
	for _, e := range report.Orphans {
		if e.ID == orphan.ID {
			foundOrphan = true
			break
		}
	}
	if !foundOrphan {
		t.Errorf("expected orphan %s in report, got %+v", orphan.ID, report.Orphans)
	}
}

func TestLintTool_Markdown(t *testing.T) {
	cx := newTestCortex(t)
	c := newTestClient(t, cx)
	callReq := mcp.CallToolRequest{}
	callReq.Params.Name = "lint"
	callReq.Params.Arguments = map[string]any{"format": "markdown"}

	resp, err := c.CallTool(context.Background(), callReq)
	if err != nil {
		t.Fatalf("CallTool lint markdown: %v", err)
	}
	if resp.IsError {
		t.Fatalf("lint markdown returned error: %+v", resp.Content)
	}
	text := textContent(t, resp)
	if !strings.Contains(text, "# Cortex Lint Report") {
		t.Errorf("markdown output missing header: %s", text)
	}
}

func TestLintTool_ThresholdImpliesLowConfidence(t *testing.T) {
	cx := newTestCortex(t)
	ctx := context.Background()
	// Seed a memory with explicit low confidence so it shows up under threshold 0.5.
	mem := &cortex.Memory{Content: "shaky claim", Confidence: 0.2}
	if err := cx.PutMemory(ctx, mem); err != nil {
		t.Fatalf("PutMemory: %v", err)
	}

	c := newTestClient(t, cx)
	callReq := mcp.CallToolRequest{}
	callReq.Params.Name = "lint"
	callReq.Params.Arguments = map[string]any{
		"format":                   "json",
		"low_confidence_threshold": 0.5,
		// note: low_confidence flag NOT set
	}
	resp, err := c.CallTool(ctx, callReq)
	if err != nil {
		t.Fatalf("CallTool lint: %v", err)
	}
	if resp.IsError {
		t.Fatalf("lint returned error: %+v", resp.Content)
	}
	var report cortex.LintReport
	if err := json.Unmarshal([]byte(textContent(t, resp)), &report); err != nil {
		t.Fatalf("unmarshal LintReport: %v", err)
	}
	if len(report.LowConfidenceMemories) == 0 {
		t.Errorf("threshold should imply low_confidence=true; got empty LowConfidenceMemories")
	}
}
```

Also extend `TestRegisterTools_AllToolsListed` (already in tools_test.go from Task 6) to include `merge` and `lint`. Replace the `want` slice:

```go
	want := []string{
		"remember", "recall", "forget",
		"get_entity", "find_entities", "get_relationships",
		"traverse", "search",
		"merge", "lint",
	}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./cmd/cortex-mcp/ -run "TestLintTool|TestRegisterTools_AllToolsListed"
```
Expected: FAIL — `lint` tool doesn't exist; tool-list assertion fails for `lint`.

- [ ] **Step 3: Add the lint tool**

Append inside `registerTools()` in `cmd/cortex-mcp/tools.go`, after the `merge` tool block:

```go
	s.AddTool(
		mcp.NewTool("lint",
			mcp.WithDescription("Scan the graph for cleanup candidates: orphans, near-duplicates, dead sources, unlinked memories."),
			mcp.WithBoolean("low_confidence", mcp.Description("Include low-confidence memories section")),
			mcp.WithNumber("low_confidence_threshold", mcp.Description("Confidence threshold 0-1; implies low_confidence=true")),
			mcp.WithString("format", mcp.Description("Output format: json (default) or markdown"), mcp.Enum("json", "markdown")),
		),
		func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			var opts []cortex.LintOption
			thresholdSet := req.GetFloat("low_confidence_threshold", -1) >= 0
			if thresholdSet {
				t := req.GetFloat("low_confidence_threshold", 0)
				if t < 0 || t > 1 {
					return mcp.NewToolResultError("low_confidence_threshold must be between 0 and 1"), nil
				}
				opts = append(opts, cortex.WithLowConfidenceThreshold(t))
			} else if req.GetBool("low_confidence", false) {
				opts = append(opts, cortex.WithLowConfidence())
			}
			report, err := cx.Lint(ctx, opts...)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			format := req.GetString("format", "json")
			switch format {
			case "json", "":
				return jsonResult(report)
			case "markdown":
				return mcp.NewToolResultText(cortex.RenderLintMarkdown(report)), nil
			default:
				return mcp.NewToolResultError(fmt.Sprintf("unknown format: %s", format)), nil
			}
		},
	)
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./cmd/cortex-mcp/
```
Expected: PASS for all tests, including the extended tool-list assertion (10 tools now).

- [ ] **Step 5: Commit**

```bash
git add cmd/cortex-mcp/tools.go cmd/cortex-mcp/tools_test.go
git commit -m "feat(cortex-mcp): add lint tool with json/markdown output"
```

---

## Task 9: Delete `cortex-http` binary and update Makefile

**Files:**
- Delete: `cmd/cortex-http/`
- Modify: `Makefile`

- [ ] **Step 1: Delete cortex-http**

```bash
rm -rf cmd/cortex-http/
```

- [ ] **Step 2: Update Makefile**

Edit `Makefile`. Remove the `cortex-http` build line, the `cortex-http` install line, and the `run-http` target. Final relevant sections:

```make
.PHONY: all build clean test test-v test-cover vet tidy install run-mcp run-mcp-http

build:
	@mkdir -p $(BINARY_DIR)
	go build -o $(BINARY_DIR)/cortex ./cmd/cortex/
	go build -o $(BINARY_DIR)/cortex-mcp ./cmd/cortex-mcp/

install: build
	cp $(BINARY_DIR)/cortex $(BINARY_DIR)/cortex-mcp /usr/local/bin/

run-mcp: build
	$(BINARY_DIR)/cortex-mcp

run-mcp-http: build
	$(BINARY_DIR)/cortex-mcp --transport http
```

Drop `run-http` entirely. Drop any line that references `cortex-http`. (The existing `run-mcp` target stays as-is; the `run-mcp-http` target is a convenience addition.)

- [ ] **Step 3: Verify**

```bash
make clean && make build && make test
```
Expected: builds `cortex` and `cortex-mcp` (no cortex-http), tests pass.

- [ ] **Step 4: Commit**

```bash
git add cmd/cortex-http Makefile
git commit -m "chore: remove cortex-http binary (replaced by cortex-mcp --transport http)"
```

---

## Task 10: Update README

**Files:**
- Modify: `README.md`

- [ ] **Step 1: Find and update references**

Use `grep -n "cortex-http" README.md` to locate every reference. Update each based on these patterns:

- Build matrix or feature list mentioning `cortex-http`: drop the line.
- Quickstart showing `bin/cortex-http`: replace with `bin/cortex-mcp --transport http` showing equivalent output.
- Project tree (around line 608 in current README): drop the `cortex-http/` line.

Also add a short section under the MCP server documentation describing the new flag and the env vars. Locate the section that documents `cortex-mcp` (around line 313 per the grep result earlier). Add immediately after the existing stdio Claude-Desktop example:

```markdown
### HTTP transport

Cortex can also serve MCP over streamable HTTP for clients that can't or won't drive a subprocess:

```bash
# Default: listens on 127.0.0.1:8080, MCP endpoint at /mcp
bin/cortex-mcp --transport http

# Bind elsewhere
bin/cortex-mcp --transport http --addr 0.0.0.0:9000

# With bearer-token auth (required if you expose the port beyond localhost)
CORTEX_AUTH_TOKEN=$(openssl rand -hex 32) bin/cortex-mcp --transport http
```

Health check:

```bash
curl http://127.0.0.1:8080/healthz
# {"status":"ok"}
```

Env-var equivalents: `CORTEX_TRANSPORT`, `CORTEX_ADDR`, `CORTEX_AUTH_TOKEN`. Flags win over env. Stdio remains the default — `cortex-mcp` with no arguments behaves exactly as before.
```

- [ ] **Step 2: Verify**

```bash
grep -n "cortex-http" README.md
```
Expected: no matches.

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "docs: document cortex-mcp --transport http, remove cortex-http references"
```

---

## Self-Review (controller checklist)

Run these after all tasks complete:

1. **`go test ./...`** — full suite green.
2. **`go vet ./...`** — no diagnostics.
3. **`go build ./...`** — builds clean; only `cortex` and `cortex-mcp` produced.
4. **Stdio smoke** — `echo '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' | bin/cortex-mcp` lists 10 tools.
5. **HTTP smoke** — `bin/cortex-mcp --transport http &` then `curl http://127.0.0.1:8080/healthz` returns `{"status":"ok"}`.
6. **Auth smoke** — `CORTEX_AUTH_TOKEN=t bin/cortex-mcp --transport http &` then `curl -X POST http://127.0.0.1:8080/mcp` returns 401; with `-H "Authorization: Bearer t"` returns a JSON-RPC error (not 401).
