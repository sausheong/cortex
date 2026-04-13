# Cortex Connectors Implementation Plan (Plan 3 of 3)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add conversation, Gmail, and Google Calendar connectors so cortex can ingest from chat logs, email, and calendar events.

**Architecture:** Each connector is an independent package under `connector/`. They implement the `Connector` interface (`Sync(ctx, *Cortex) error`) or provide inline ingestion methods. Each handles its own source-specific parsing and calls the core `Remember` API or structured graph methods.

**Tech Stack:** Go stdlib for conversation connector, `google.golang.org/api/gmail/v1` for Gmail, `google.golang.org/api/calendar/v3` for Calendar, `golang.org/x/oauth2` for Google OAuth2.

---

## File Structure

```
cortex/
├── connector/
│   ├── connector.go           # (existing) Connector interface
│   ├── markdown/              # (existing)
│   ├── conversation/
│   │   ├── conversation.go    # Inline message ingestion
│   │   └── conversation_test.go
│   ├── gmail/
│   │   ├── gmail.go           # Gmail API sync
│   │   └── gmail_test.go
│   └── calendar/
│       ├── calendar.go        # Google Calendar sync
│       └── calendar_test.go
```

---

### Task 1: Conversation Connector

**Files:**
- Create: `connector/conversation/conversation.go`
- Create: `connector/conversation/conversation_test.go`

The conversation connector is different from others — it's designed for **inline use** from agents, not batch sync. It accepts chat messages and extracts entities/relationships/memories from them.

```go
package conversation

type Message struct {
    Role    string // "user", "assistant", "system"
    Content string
}

type Connector struct{}

func New() *Connector

// Ingest processes messages and stores extracted knowledge in cortex.
// Each message is passed through Remember with source "conversation".
func (c *Connector) Ingest(ctx context.Context, cx *cortex.Cortex, messages []Message) error
```

`Ingest` iterates over messages, concatenates them into a single text block (with role prefixes), and calls `cx.Remember(ctx, text, WithSource("conversation"), WithContentType("conversation"))`.

Tests:
- TestIngestSingleMessage — ingest one message, verify Remember was called (use a mock extractor that returns known entities, verify entities exist)
- TestIngestMultipleMessages — ingest 3 messages, verify all content is processed
- TestIngestEmpty — empty message slice, no error

- [ ] **Step 1: Write tests**
- [ ] **Step 2: Run tests, verify they fail**
- [ ] **Step 3: Implement conversation connector**
- [ ] **Step 4: Run tests, verify they pass**
- [ ] **Step 5: Commit**

```bash
git add connector/conversation/
git commit -m "feat: add conversation connector for inline message ingestion"
```

---

### Task 2: Gmail Connector

**Files:**
- Create: `connector/gmail/gmail.go`
- Create: `connector/gmail/gmail_test.go`

The Gmail connector syncs emails via the Gmail API. It uses OAuth2 for authentication.

```go
package gmail

type Connector struct {
    service    *gmail.Service
    userID     string  // typically "me"
}

// New creates a Gmail connector with an authenticated Gmail service.
func New(service *gmail.Service, opts ...Option) *Connector

// Sync fetches emails since the last sync and ingests them into cortex.
// Uses Gmail history ID for incremental sync.
func (c *Connector) Sync(ctx context.Context, cx *cortex.Cortex) error
```

**Sync flow:**
1. Load sync state from cortex (`GetSyncState(ctx, "gmail")`) — contains last history ID
2. If no history ID (first sync), list recent messages (e.g., last 50)
3. If has history ID, use `history.list` to get changes since then
4. For each message:
   a. Fetch full message via `messages.get`
   b. **Deterministic extraction**: parse From, To, Cc headers → create person entities with email attributes
   c. Extract subject and body text
   d. Call `cx.Remember(ctx, emailText, WithSource("gmail"))` for LLM extraction of relationships/memories
5. Save new history ID to sync state

**Header parsing:**
- Parse `From: "Alice Smith" <alice@example.com>` → Entity{Type: "person", Name: "Alice Smith", Attributes: {"email": "alice@example.com"}}
- Same for To and Cc

Since the Gmail API requires OAuth2 credentials and a real Google account, the tests should:
- Test header parsing (deterministic, no API needed)
- Test the Sync flow with a mock Gmail service (if feasible) or skip integration tests

Tests:
- TestParseEmailAddress — parse "Alice Smith <alice@example.com>" → name and email
- TestParseEmailAddressNoName — parse "alice@example.com" → use local part as name
- TestBuildEmailText — verify email text assembly from headers + body

- [ ] **Step 1: Add Google API dependencies**

Run: `go get google.golang.org/api/gmail/v1 golang.org/x/oauth2/google`

- [ ] **Step 2: Write tests for parsing functions**
- [ ] **Step 3: Implement Gmail connector**
- [ ] **Step 4: Run tests, verify pass**
- [ ] **Step 5: Commit**

```bash
git add connector/gmail/ go.mod go.sum
git commit -m "feat: add Gmail connector with OAuth2 and incremental sync"
```

---

### Task 3: Calendar Connector

**Files:**
- Create: `connector/calendar/calendar.go`
- Create: `connector/calendar/calendar_test.go`

The Calendar connector syncs events from Google Calendar.

```go
package calendar

type Connector struct {
    service    *calendar.Service
    calendarID string // typically "primary"
}

func New(service *calendar.Service, opts ...Option) *Connector

func (c *Connector) Sync(ctx context.Context, cx *cortex.Cortex) error
```

**Sync flow:**
1. Load sync state from cortex — contains last sync token
2. List events (using sync token for incremental, or `timeMin` for first sync)
3. For each event:
   a. **Deterministic extraction**: attendees → person entities, event → event entity
   b. Create "attended" relationships between each attendee and the event
   c. If event has description, call `cx.Remember` for LLM extraction
4. Save next sync token

Tests:
- TestBuildEventText — verify event text assembly from summary, description, attendees
- TestParseAttendees — parse attendee list into entity names + emails

- [ ] **Step 1: Add Google Calendar dependency**

Run: `go get google.golang.org/api/calendar/v3`

- [ ] **Step 2: Write tests**
- [ ] **Step 3: Implement Calendar connector**
- [ ] **Step 4: Run tests, verify pass**
- [ ] **Step 5: Commit**

```bash
git add connector/calendar/ go.mod go.sum
git commit -m "feat: add Google Calendar connector with incremental sync"
```

---

### Task 4: Update CLI and Servers

**Files:**
- Modify: `cmd/cortex/main.go`
- Modify: `cmd/cortex-mcp/main.go`
- Modify: `cmd/cortex-http/main.go`

Add `sync gmail` and `sync calendar` commands to the CLI. Add corresponding MCP tools and HTTP endpoints for triggering syncs.

Note: Gmail and Calendar connectors require OAuth2 credentials. The CLI should support a `cortex auth google` command that runs the OAuth2 flow and saves the token. For now, the connectors accept a pre-built `*gmail.Service` / `*calendar.Service`, so the CLI integration can be deferred if OAuth2 flow is too complex for this plan.

**Minimum viable:** Add CLI commands that print "Gmail/Calendar sync requires Google OAuth2 credentials. See README for setup." until OAuth2 is fully wired up. The connector packages themselves are complete and usable programmatically.

- [ ] **Step 1: Update CLI with placeholder sync commands**
- [ ] **Step 2: Update README connectors table**
- [ ] **Step 3: Commit**

```bash
git add cmd/ README.md
git commit -m "feat: add Gmail and Calendar sync commands (requires OAuth2 setup)"
```

---

### Task 5: Final Verification

- [ ] **Step 1: Run all tests**
- [ ] **Step 2: go mod tidy**
- [ ] **Step 3: Build all binaries**
- [ ] **Step 4: Commit if needed**
