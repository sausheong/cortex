# Cortex MCP Server — stdio + Streamable HTTP Transports

**Date:** 2026-05-27
**Status:** Draft (approved during brainstorming)
**Author:** sausheong + Claude

## Background

`cortex-mcp` already exists as a stdio-only MCP server (using `mark3labs/mcp-go` v0.47.1) exposing 8 tools: `remember`, `recall`, `forget`, `get_entity`, `find_entities`, `get_relationships`, `traverse`, `search`. A parallel `cortex-http` binary exposes the same operations as a plain REST API — not MCP.

Two gaps:

1. **No HTTP transport for MCP.** Clients that can't or won't drive a subprocess (remote agents, browser-side dev tools, cross-host setups) have no way to reach the graph over MCP.
2. **Missing tools.** `merge` and `lint` were explicitly deferred from their respective specs as "MCP/HTTP exposure" follow-ups. An agent can recall and remember but can't trigger maintenance.

This spec replaces the REST binary with MCP-over-streamable-HTTP, folds both transports into one binary, and adds the two missing tools.

## Goals

- Single `cortex-mcp` binary serves either stdio or streamable HTTP, selected by `--transport`.
- Tool definitions live in one place and are reused across transports.
- Add `merge` and `lint` MCP tools.
- Safe localhost-default for HTTP; optional bearer-token auth for wider exposure.
- Zero breaking changes to the default invocation: `cortex-mcp` with no flags still serves stdio.

## Non-goals

- TLS termination (use a reverse proxy).
- OAuth / OIDC / any auth beyond a static bearer token.
- Per-tool authorization (token grants all-or-nothing).
- Resource subscriptions or server-initiated notifications.
- The legacy MCP HTTP+SSE transport (streamable-HTTP supersedes it).
- Rate limiting.
- CORS (no browser is going to speak MCP directly).
- Exposing `export`, `init-schema`, or `config` as MCP tools.
- Migrating REST API consumers (none known — single-user tool).

## Architecture

```
cmd/cortex-mcp/
├── main.go         (slim) — flag parsing, transport dispatch, openCortex
├── tools.go        (new) — registerTools(s, cx) builds the 10 tools, called once
├── serve_stdio.go  (new) — wraps server.ServeStdio
├── serve_http.go   (new) — wraps server.NewStreamableHTTPServer + mux + healthz
├── auth.go         (new) — bearerAuthMiddleware (constant-time compare)
└── tools_test.go   (new) — hermetic tests for both transports
```

`cmd/cortex-http/` is deleted in this work.

The same `*server.MCPServer` instance, built by `registerTools()`, is handed to either `serveStdio()` or `serveHTTP()`. Tool definitions are not duplicated.

## CLI

```
cortex-mcp [--db <path>] [--transport <stdio|http>] [--addr <host:port>]
```

| Flag | Env | Default | Used when |
|---|---|---|---|
| `--transport` | `CORTEX_TRANSPORT` | `stdio` | always |
| `--addr` | `CORTEX_ADDR` | `127.0.0.1:8080` | `transport=http` |
| `--db` | `CORTEX_DB` | `brain.db` | always (existing flag, unchanged) |
| _(env only)_ | `CORTEX_AUTH_TOKEN` | _empty_ | `transport=http` |

Flags win over env. Existing LLM/embedder env vars (`LLM_PROVIDER`, `OPENAI_API_KEY`, `ANTHROPIC_API_KEY`, `EMBEDDING_MODEL`, etc.) are unchanged from current `cortex-mcp`.

Invocation examples:

```bash
cortex-mcp                                      # stdio (default — current behavior)
cortex-mcp --transport stdio                    # explicit stdio
cortex-mcp --transport http                     # MCP at http://127.0.0.1:8080/mcp
cortex-mcp --transport http --addr 0.0.0.0:9000 # bind all interfaces, port 9000

CORTEX_AUTH_TOKEN=s3cret cortex-mcp --transport http   # bearer-token auth required
```

Startup logs (stderr, one line):

```
cortex-mcp: stdio transport ready
cortex-mcp: streamable-http transport ready on 127.0.0.1:8080/mcp
```

If `transport=http` and the resolved bind host is non-loopback (anything other than `127.0.0.1`, `localhost`, `::1`) AND `CORTEX_AUTH_TOKEN` is empty, additionally print:

```
warning: HTTP transport bound to non-loopback address without CORTEX_AUTH_TOKEN — anyone on the network can call destructive tools
```

The server still starts — the user may be running behind their own reverse proxy.

## Transport details

### stdio

Unchanged from current behavior. `serveStdio()` is a thin wrapper:

```go
func serveStdio(s *server.MCPServer) error {
    fmt.Fprintln(os.Stderr, "cortex-mcp: stdio transport ready")
    return server.ServeStdio(s)
}
```

### Streamable HTTP

```go
func serveHTTP(s *server.MCPServer, addr, token string) error {
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

    if !isLoopback(addr) && token == "" {
        fmt.Fprintln(os.Stderr, "warning: HTTP transport bound to non-loopback address without CORTEX_AUTH_TOKEN — anyone on the network can call destructive tools")
    }

    fmt.Fprintf(os.Stderr, "cortex-mcp: streamable-http transport ready on %s/mcp\n", addr)
    return http.ListenAndServe(addr, mux)
}
```

`isLoopback` parses `addr` (`host:port`), splits the host, and returns true for `127.0.0.1`, `localhost`, `::1`. Anything else is non-loopback. An empty host (e.g. `":8080"`) is treated as non-loopback because Go's `http.ListenAndServe(":8080", ...)` binds all interfaces.

Session mode: mcp-go's default (stateful per-session via `Mcp-Session-Id` header). No code in cortex depends on session state — the shared `*cortex.Cortex` is safe for concurrent use, verified by the existing test suite.

`/mcp` is the only MCP path. `/healthz` is the only auxiliary endpoint. No `/` index, no `/metrics`, no static assets.

## Authentication

Bearer-token middleware, used only when `CORTEX_AUTH_TOKEN` is non-empty, and wrapped only around `/mcp` (never `/healthz`):

```go
func bearerAuthMiddleware(next http.Handler, token string) http.Handler {
    expected := []byte(token)
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        h := r.Header.Get("Authorization")
        const prefix = "Bearer "
        if !strings.HasPrefix(h, prefix) ||
            subtle.ConstantTimeCompare([]byte(h[len(prefix):]), expected) != 1 {
            w.Header().Set("Content-Type", "application/json")
            w.WriteHeader(http.StatusUnauthorized)
            _, _ = w.Write([]byte(`{"error":"unauthorized"}`))
            return
        }
        next.ServeHTTP(w, r)
    })
}
```

Stdio ignores the token entirely — the parent process that spawned the subprocess is already trusted.

## Tools

All 8 existing tools are preserved verbatim. Tool registration moves from inline calls in `main.go` to a single `registerTools(s *server.MCPServer, cx *cortex.Cortex)` function in `tools.go`.

Two new tools:

### `merge`

Wraps `cortex.MergeEntities` / `MergeEntitiesDryRun`.

| Param | Type | Required | Description |
|---|---|---|---|
| `keep_id` | string | yes | Entity ID to keep |
| `drop_id` | string | yes | Entity ID to merge into keep, then delete |
| `dry_run` | bool | no | If true, simulate and roll back without modifying the graph (default false) |

```go
mcp.NewTool("merge",
    mcp.WithDescription("Merge one entity into another, re-targeting all relationships, memory links, and chunks. Use to clean up duplicate entities surfaced by lint."),
    mcp.WithString("keep_id", mcp.Required(), mcp.Description("Entity ID to keep")),
    mcp.WithString("drop_id", mcp.Required(), mcp.Description("Entity ID to merge into keep and delete")),
    mcp.WithBoolean("dry_run", mcp.Description("Simulate the merge and roll back; returns the stats without modifying the graph")),
)
```

Returns the `cortex.MergeStats` struct as JSON via `jsonResult()`.

### `lint`

Wraps `cortex.Lint`.

| Param | Type | Required | Description |
|---|---|---|---|
| `low_confidence` | bool | no | Include low-confidence memories section (default false) |
| `low_confidence_threshold` | number | no | Threshold in 0..1; implies `low_confidence=true` (default 0.3) |
| `format` | string enum `json`\|`markdown` | no | Default `json` — return the `LintReport` struct so agents can iterate it. `markdown` returns the rendered report. |

```go
mcp.NewTool("lint",
    mcp.WithDescription("Scan the graph for cleanup candidates: orphans, near-duplicates, dead sources, unlinked memories."),
    mcp.WithBoolean("low_confidence", mcp.Description("Include low-confidence memories section")),
    mcp.WithNumber("low_confidence_threshold", mcp.Description("Confidence threshold 0-1; implies low_confidence=true")),
    mcp.WithString("format", mcp.Description("Output format"), mcp.Enum("json", "markdown")),
)
```

If `format=markdown`, the handler calls `cortex.RenderLintMarkdown(report)` and returns the string via `mcp.NewToolResultText`. Otherwise returns the `LintReport` struct as JSON.

If `low_confidence_threshold` is provided and `low_confidence` is unset, the handler treats it as enabled (matches CLI behavior).

## Errors

Tool errors continue using `mcp.NewToolResultError(err.Error())` — consistent with the existing 8 tools. Framing-level transport errors (malformed JSON-RPC, broken connections) are handled by `mcp-go`'s `StreamableHTTPServer`; we don't intercept them.

Auth errors are JSON `{"error":"unauthorized"}` with HTTP 401, before the request reaches mcp-go.

## Removal of `cortex-http`

`cmd/cortex-http/main.go` is deleted in this work. Rationale: it duplicates every operation the MCP transport now covers, but in a separate protocol with separate tool definitions to maintain. No known consumers.

Updates this implies:
- `README.md`: remove any mention of `cortex-http`; document `cortex-mcp --transport http`.
- `Makefile`: remove the `cortex-http` build target if one exists; ensure `cortex-mcp` builds cleanly.

## Testing strategy

All hermetic — deterministic extractor, no LLM, no network. Seed via `PutEntity` / `PutRelationship` / `PutMemory`.

| # | Test | Asserts |
|---|---|---|
| 1 | `registerTools` registers exactly 10 tools | Tool names match the expected set; calling it twice on the same server is safe (or documented as not). |
| 2 | Stdio smoke | Build server, dispatch `initialize` + `tools/list` over a piped stdio pair; assert tool names. |
| 3 | Streamable HTTP smoke | Use `server.NewTestStreamableHTTPServer`, POST `initialize` + `tools/list`, verify 10 tools. |
| 4 | HTTP — `recall` round-trip | Seed a memory, call `recall` tool over HTTP, decode result, assert content. |
| 5 | HTTP — `merge` round-trip | Seed two entities + a relationship, call `merge`, assert returned `MergeStats` and verify graph state after. |
| 6 | HTTP — `merge` dry-run | Same seed, `dry_run=true`; stats returned but graph unchanged. |
| 7 | HTTP — `lint` JSON | Returns parseable `LintReport`. |
| 8 | HTTP — `lint` markdown | Output contains `# Cortex Lint Report`. |
| 9 | HTTP — `lint` low-confidence threshold implies enable | `low_confidence_threshold=0.5` (no `low_confidence` flag) populates `LowConfidenceMemories`. |
| 10 | Auth — no token configured | Request with no `Authorization` header → 200. |
| 11 | Auth — token configured + valid bearer | 200. |
| 12 | Auth — token configured + missing bearer | 401 with `{"error":"unauthorized"}` body. |
| 13 | Auth — token configured + wrong bearer | 401. |
| 14 | Auth — token configured + malformed header (`"Bearertok"` no space) | 401. |
| 15 | `/healthz` never authenticated | Returns 200 even when token is set and request has no auth. |
| 16 | `isLoopback` helper | `"127.0.0.1:8080"`, `"localhost:8080"`, `"[::1]:8080"` → true. `"0.0.0.0:8080"`, `":8080"`, `"192.168.1.5:8080"` → false. |
| 17 | Non-loopback bind without token logs warning | Capture stderr; assert warning string present. Loopback bind without token does NOT log warning. |
| 18 | Flag parsing | `--transport http --addr :9000`; env override behavior (flag beats env); unknown flag returns non-zero exit. |

## Rollout

- Zero schema changes.
- One new dependency surface: `net/http` (stdlib), `crypto/subtle` (stdlib). No new go.mod entries.
- Default invocation `cortex-mcp` (no args) still serves stdio — existing Claude Desktop / IDE configurations continue working with no change.
- `cortex-http` binary disappears. If anyone has it wired up locally, their `make` will fail loudly with an unknown target — preferable to silently keeping a stale binary around.

## Open questions

None blocking. Spec complete enough to plan from.
