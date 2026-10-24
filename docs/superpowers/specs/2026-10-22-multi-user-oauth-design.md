# Multi-user cortex with assistant OAuth — Design Spec

**Date:** 2026-10-22
**Status:** Approved (design); pending implementation plan
**Author:** brainstormed with Claude

## Summary

Turn `https://cortex.sausheong.dev` into a **multi-user memory service** that
any MCP-capable assistant (ChatGPT, claude.ai, Claude Desktop, others) and the
user's own agents can read from and write to.

- Anyone signs in with **Google**. Each Google account gets its own isolated
  brain.
- Each user brings **their own AI provider**: base URL, API key, chat model and
  embedding model.
- Assistants connect via **OAuth 2.1 per the MCP authorization spec**. The
  user's own tools use **named agent tokens**.
- Web pages let the user manage AI settings, connections, memories and
  storage, and run **compaction** with a preview.

This is project 3 ("reach") from the cortex roadmap. It builds on the Postgres
storage work (branch `postgres-storage`, spec
`2026-10-22-postgres-storage-design.md`). That work is already deployed to the
droplet, with cortex behind Caddy and Postgres on localhost.

## Why

The owner wants every assistant and agent they use to share one memory of who
they are. Today only clients holding the single static bearer token can
connect, and ChatGPT and claude.ai cannot present static tokens: they require
OAuth. Building OAuth into cortex (rather than an external proxy) was chosen
for three reasons: one standard covers every assistant, the owner's agents keep
simple tokens, and access is visible and revocable in one place.

When the owner chose Google sign-in, they also chose to open cortex to
**anyone with a Google account**, each getting their own brain.

## What the user experiences (owner's requirements)

- **Reading.** Connected assistants consult cortex **by default** whenever an
  answer depends on the user's preferences, how they would do things, or what
  or whom they know. That covers most questions; only generic ones are skipped.
- **Writing.** Assistants **save** anything new they learn about the user:
  preferences, connections (people, organisations), capabilities, background,
  identity. What ChatGPT learns, Claude knows next time.
- **Full access.** Connected assistants may read, add, delete and merge.
- **Sign-in.** Google only. Open sign-up, with no storage limits for now; usage
  is shown to the user.
- **Bring your own AI provider.** Each user sets base URL, API key, chat model
  and embedding model. The default embedding model is
  **`text-embedding-3-small`**.
- **Memories page.** Shows storage used and every memory, with manual deletion
  and **bulk delete** (by the assistant that wrote it, by age, by search, or
  everything).
- **Compaction.** Merges duplicates, combines related small memories and rolls
  up old one-off events. It **shows a preview of the proposed changes for
  approval**, and is **undoable for 30 days**.

The approved wireframes (pages 1–5) are in
`.superpowers/brainstorm/*/content/pages-overview.html` and `memories-page.html`.
They show **sign in**, **AI provider settings**, **approve an assistant**
(consent), **connections**, and **memories & storage**.

## Goals

1. Strict per-user isolation: no API, MCP tool, page or background job can
   read or change another user's data.
2. Assistants connect with OAuth 2.1 + PKCE following the MCP authorization
   spec, and work end-to-end with **ChatGPT** (Plugins → Add custom MCP server)
   and **claude.ai** custom connectors.
3. The owner's existing brain, the Claude Code tunnel setup, and the existing
   token keep working after migration.
4. Every write records **which connection wrote it**. Users can see, filter,
   bulk-delete and revoke by connection.
5. Every deletion via assistants, UI or compaction is recoverable for 30
   days, then purged.
6. It runs on the current smallest droplet (512 MB + 2 GB swap).

## Non-goals

- Sharing data between users, teams, or admin views of other users' data.
- Billing, quotas or per-user storage limits (usage is displayed only).
- Sign-in methods other than Google.
- Machine-to-machine OAuth grants (client credentials). Agent tokens cover
  that, and ChatGPT doesn't support them anyway.
- Project 2 (ingesting ChatGPT/Claude Code history, self-model). It is a
  separate spec, but assistant writes feed the same profile.
- Native mobile apps, or a separate SPA frontend.

## Anti-goals (failure even if it "works")

- **Any cross-user data leak**, including through search ranking, vector
  indexes, profiles, maintenance jobs, logs or error messages.
- Storing users' AI keys in plaintext, or showing them back in full.
- Assistants silently and irrecoverably destroying memories.
- Breaking the owner's working Claude Code connection during the migration.
- Users paying for each other's AI usage (each user's calls use only their own
  key; cortex never falls back to the operator's key for another user).

## Design

### 1. Users and isolation

- New `users` table: `id`, `google_sub` (unique), `email`, `name`,
  `created_at`, `deleted_at`.
- **Every data table gets `user_id NOT NULL`** with a foreign key to users and
  `ON DELETE CASCADE`: entities, relationships, memories, memory_entities,
  memory_edges, chunks, embeddings, sync_state. Uniqueness constraints are
  rescoped per user: `entities (user_id, name, type)`.
- The `*Cortex` handle becomes user-bound. There is one shared store, and a
  `ForUser(userID)` (name left to the builder) yields a handle whose every
  query is scoped to that user. Unscoped data access is impossible from MCP and
  web handlers by construction.
- **Isolation test suite (required).** Seed two users with overlapping content
  (same entity names, similar memories, same embedding dims). Then assert that
  every public API, every MCP tool, every page handler and the maintenance and
  compaction jobs, run as user A, never return or modify user B's rows.
  Include vector search and recall ranking.
- Postgres row-level security as defence-in-depth is **left to the builder**.
  The test suite is the non-negotiable guarantee.
- The "owner" concept (`Source: "owner"` person entity, used by profiles)
  becomes **per user**: each user's own person entity, created at first sign-in
  from their Google name and email.

### 2. Vectors with per-user dimensions

Users choose different embedding models, so vector sizes differ.

- `embeddings.embedding` becomes untyped `halfvec`, plus a `dims` column.
- HNSW indexes are **partial expression indexes per (dims, ref_type)**, for
  example `USING hnsw ((embedding::halfvec(512)) halfvec_cosine_ops) WHERE dims = 512 AND ref_type = 'memory'`.
  They are created on demand the first time a dims value appears.
- Vector queries use the matching expression, filter `user_id`, and keep
  `hnsw.iterative_scan` (strict order) so per-user filtering still returns
  enough rows.
- *(Claude's call)* At this scale (a few users on one small droplet) this is
  adequate. If one large user degrades another's latency, revisit with
  per-user partitioning. The builder records the measured query latency with 2
  users × 100K vectors.
- **Changing the embedding model** starts a background re-embed of that user's
  whole brain with their key, with progress shown on the settings page. Vector
  search keeps using the old vectors until the new set is complete, then swaps
  atomically. If it fails midway, it can be resumed.
- *(Claude's call)* To support the swap, `embeddings` carries a
  `generation int`, and each user row has `active_embedding_generation`. A
  re-embed writes generation N+1, and every vector query filters
  `generation = active`. When the new set is complete, cortex flips the pointer
  and deletes generation N. This works even when old and new models have the
  same dims. Uniqueness is `(user_id, ref_type, ref_id, generation)`.
- **The owner's existing brain already matches the default.** The droplet's
  vectors are `text-embedding-3-small` at 512 dims (`cortex_meta.embedding_dims
  = 512`; no `EMBEDDING_*` or `LLM_*` overrides are set on the server). So
  migration 002 makes them generation 1 for the bootstrap user, and no
  re-embed is needed.

### 3. Google sign-in and web sessions

- Google OIDC (`openid email profile`). Users are keyed by Google `sub`; email
  and name are refreshed at each sign-in.
- First sign-in creates the user, their person entity, and an empty brain.
  The user is sent to AI settings, because nothing works without a provider.
- Sessions use a secure, HttpOnly, SameSite=Lax cookie, with CSRF protection on
  every form POST.
- The sign-in page carries a short notice that the service operator can
  technically access stored data. *(Claude's call: honest disclosure for an
  open service.)*
- **Owner setup step:** create a Google OAuth client (Web) in Google Cloud
  Console with redirect URI `https://cortex.sausheong.dev/auth/google/callback`.
  The client ID and secret go in `/etc/cortex/cortex.env`. `docs/deploy.md`
  gives exact steps.
- **Done by the owner:** the Google OAuth client exists. Its credentials are
  in the repo's gitignored `.env` as `GOOGLE_OAUTH_CLIENT_ID` and
  `GOOGLE_OAUTH_CLIENT_SECRET`. `scripts/deploy-app.sh` must forward
  `GOOGLE_OAUTH_*` (and `CORTEX_BOOTSTRAP_EMAIL`) into the server env file,
  the same way it forwards `OPENAI_*`. The builder confirms the redirect URI
  above is registered on that client.

### 4. Per-user AI provider settings

- Fields: base URL (default `https://api.openai.com/v1`), API key, chat model,
  embedding model (default `text-embedding-3-small` at 512 dims). Advanced
  options: a separate embedding base URL and key, for users whose chat provider
  has no embeddings API. *(Claude's call.)* All endpoints are
  **OpenAI-compatible**; that covers OpenAI, Ollama-style gateways, LiteLLM,
  OpenRouter and similar.
- *(Claude's call)* Chat also offers a **provider type** of
  `openai-compatible` (the default) or `anthropic`, since cortex already
  ships an Anthropic LLM client. Embeddings are always OpenAI-compatible. The
  owner's server uses OpenAI defaults, so their bootstrap settings are
  `openai-compatible`, the OpenAI base URL, the existing key, cortex's current
  default chat model, and `text-embedding-3-small`/512.
- "Test & save" makes one chat call and one embedding call before saving, and
  shows a clear error if either fails.
- Keys are **encrypted at rest** (AES-256-GCM, key from `CORTEX_SECRET_KEY` in
  the server env file). They are shown back masked (last 4 characters only).
- Every LLM and embedding call for a user, whether from ingest, recall,
  maintenance, compaction or re-embed, uses that user's settings. A user with no
  valid settings gets a clear MCP error ("set up your AI provider at <url>").
  The operator's key is never used for them.

### 5. Connections: assistants (OAuth) and agent tokens

**OAuth for assistants.** Cortex acts as authorization server and resource
server for its own `/mcp`. It follows the MCP authorization spec:
- `/.well-known/oauth-protected-resource` and
  `/.well-known/oauth-authorization-server` metadata.
- `401` responses on `/mcp` carry a `WWW-Authenticate` header pointing at the
  resource metadata.
- Client registration via **CIMD** (preferred by ChatGPT) and **dynamic client
  registration**.
- Authorization code + **PKCE (S256)**.
- `offline_access` refresh tokens, so assistants don't lose access.
- Access tokens are short-lived (1 h). Refresh tokens rotate (90-day idle
  expiry).
- Tokens are opaque random values, stored **hashed**.
- `/authorize` requires a Google session, then shows the consent page:
  "ChatGPT wants to use your cortex", listing read / save / delete-and-merge
  with 30-day recovery, and Allow / Deny.
- Library vs. hand-rolled is left to the builder, but it must pass the MCP
  Inspector auth flow and real connections from ChatGPT and claude.ai.
- *(Claude's call)* OAuth details:
  - **Scopes:** one scope, `cortex` (full access, matching the owner's
    decision), plus `offline_access`. Consent is all-or-nothing.
  - **Fetching CIMD client metadata:** https only, the resolved address must
    not be private, loopback or link-local, 5 s timeout, 64 KB cap, cached
    per its HTTP cache headers (max 24 h).
  - **Dynamically registered clients** that never complete an authorization
    are deleted after 24 h. Authorized clients live until revoked.
  - **One connection per (user, client_id).** Re-authorizing the same client
    reuses the existing connection, issues new tokens and keeps provenance
    counts. Revoking deletes the grant; a later reconnect creates a new
    connection.

**Agent tokens.** These are for the user's own tools: Claude Code, scripts and
agents like felix.
- They are created on the Connections page with a name, shown **once**, and
  stored hashed.
- Each is used as `Authorization: Bearer` on `/mcp`, exactly as today.

**The connection is the unit of identity.** Every OAuth grant and agent token is
a *connection* owned by one user, with: name (the client's name for OAuth,
user-chosen for tokens), kind, created, last used, and count of items written.
- **Revoke** invalidates its tokens immediately.
- Revoke offers "also remove everything it wrote", which is a soft delete
  (§7).

**Provenance.** Memories, entities, relationships and chunks get a
`written_by_connection_id`. It is set on every write through MCP, and null for
web-UI and import writes, which are labelled "you" and "import" respectively.

**Migration of the existing token.** The current server-wide
`CORTEX_AUTH_TOKEN` becomes an agent token named "claude-code (tunnel)" owned by
the owner's user. The server-wide token setting is then removed. The owner's
Claude Code config keeps working unchanged.

### 6. MCP behaviour for assistants

- **A new `context` tool.** Input: the user's question or topic. Output, in one
  call: the user's profile (static + dynamic) plus the top relevant memories
  and entities for the question, with abstention when nothing is relevant. It is
  designed to be cheap enough to call on most turns.
- **Tool descriptions are rewritten** to drive the owner's intended behaviour:
  - call `context` at the start of a conversation and before answering
    anything that depends on the user's preferences, way of doing things, or
    people and things they know;
  - call `remember` when the conversation reveals a new preference,
    connection, capability, background fact or identity fact;
  - don't store generic chit-chat or secrets.
- **Recommended custom-instruction text** for ChatGPT and Claude is shown on the
  Connections page and in the docs. It's a few sentences telling the assistant
  to always consult and update cortex as above. This is the strongest lever,
  because cortex cannot push context unprompted.
- Existing tools (remember, recall, forget, get_entity, find_entities,
  get_relationships, traverse, search, merge, lint, profile) are all scoped to
  the token's user. `forget` and `merge` via MCP are soft (§7).

### 7. Recoverable deletion, memories page and storage

- **Soft delete.** Add `deleted_at` (and `deleted_by` with values
  user/connection/compaction) on memories, entities, relationships and chunks.
  Soft-deleted rows are invisible to everything, including `as_of` history
  queries, except the "Recently deleted" view.
- Deletions via MCP, UI, bulk delete, revoke-and-remove and compaction are all
  soft. A nightly purge hard-deletes rows older than 30 days.
- Restore is available per item and per bulk operation. *(Claude's call: bulk
  operations are recorded as a batch, so one click restores the whole batch.)*
- *(Claude's call)* Soft-delete semantics:
  - Unique constraints are **partial**, `WHERE deleted_at IS NULL`. So a user
    can delete "Alice" and later create a new "Alice".
  - If restoring an entity collides with a live entity of the same
    (name, type), the restored one is **merged into the live one** using the
    existing merge logic, recorded in the restore batch.
  - Soft-deleting an entity hides its relationships and its links from
    memories, by filtering, without separately flagging those rows. A memory
    whose only links are to deleted entities is still shown.
  - Each delete operation stamps a `delete_batch_id`. Restore reverses exactly
    that batch, which is what makes compaction undo exact.
  - Soft delete is orthogonal to the bitemporal columns (`valid_at`,
    `invalid_at`, `expired_at`). Those keep their meaning for live rows.
    Deleted rows are excluded from every query, including `as_of`.
- **Memories page** (wireframe 5):
  - usage totals: storage MB, memories, people & things, documents, recently
    deleted;
  - search plus filters by who wrote it and by date;
  - paged list with per-item delete;
  - bulk delete: written by X, older than N, matching search, select-all;
  - "Clear my whole brain" with a typed confirmation;
  - a Recently deleted view with restore.
- **Storage figure** is the per-user approximate on-disk size (the sum of the
  user's rows' column sizes, including embeddings). Exact method left to the
  builder; it's fine if computed nightly and cached.
- **Account deletion** in settings soft-deletes the account, then hard-purges
  all of the user's data after 30 days. Signing in again within 30 days
  restores it. *(Claude's call.)*

### 8. Compaction with preview

- Started from the Memories page ("Compact"). It runs in the background with
  the user's key and builds a **proposal**. Each proposed change is one of:
  - merge duplicates into one;
  - combine related small memories into one clearer memory;
  - roll up old one-off episodic memories into a summary.

  Each change shows its source memories and the resulting text.
- The user reviews the proposal and can accept all, reject all, or untick
  individual changes, then applies it.
- On apply: new memories are written, linked to their sources with `derives`
  memory edges, and the sources are soft-deleted with `deleted_by=compaction`
  (restorable for 30 days). Undo of the whole compaction run is one action for
  30 days.
- Static (identity/preference) memories may be merged with each other, but are
  never rolled up into an episodic summary.
- It reuses the existing reconcile, dedup and relate machinery where it fits.

### 9. Background jobs

- The nightly `cortex maintain` (systemd timer) iterates users who have valid AI
  settings and runs maintenance for each with that user's key. It also runs the
  30-day purge and the storage figures.
- Per-user failures (an invalid key, for example) are isolated, logged, and
  shown on that user's settings page ("last maintenance failed: …"). They never
  stop the run for other users.
- Background work (re-embed, compaction proposals, maintenance) is bounded to
  a small worker pool *(Claude's call: 1–2 workers on 512 MB)*.

### 10. Web UI

- Server-rendered pages from the cortex binary itself, using Go `html/template`
  and embedded static assets, with no separate frontend build or deploy.
  Light progressive enhancement is fine. *(Claude's call.)*
- Pages: sign in, AI provider settings, consent, connections, memories &
  storage (incl. Recently deleted and compaction review), and account settings
  (delete account).
- All pages are served under the same Caddy site. `/mcp`, `/healthz`, the OAuth
  endpoints and the pages share one listener.

### 11. CLI and migration

- **Migration 002**:
  - creates `users`, connections and OAuth tables;
  - adds `user_id`, provenance and soft-delete columns, and backfills existing
    rows to a **bootstrap user**;
  - rescopes unique constraints;
  - converts `embeddings` to untyped `halfvec` + `dims` with the per-dims
    partial indexes.
- The **bootstrap user** is identified by `CORTEX_BOOTSTRAP_EMAIL`, which is
  **`sausheong@gmail.com`** (owner's decision). On first Google sign-in with that email, the Google `sub` binds
  to the bootstrap user, so the owner lands in their existing brain.
  - Binding requires Google's `email_verified = true`. Only Google can verify
    that address, so no one else can claim the brain.
  - Before binding, the bootstrap user exists with no `google_sub`, and its
    agent tokens (including the migrated tunnel token) work normally.
  - After binding, `CORTEX_BOOTSTRAP_EMAIL` is ignored. The
  existing owner entity becomes that user's person entity, and the existing
  `CORTEX_AUTH_TOKEN` becomes their "claude-code (tunnel)" agent token.
- **Ingestion from files** (`sync`, `import-sqlite`, file connectors) stays
  an **operator-only CLI** feature, run with `--user`. Web users add knowledge
  through assistants (MCP `remember`). Chunks (documents) are reachable through
  `recall`/`search` results, scoped per user, as today. *(Claude's call.)*
- **Server-side admin CLI** commands take `--user <email>`
  (`import-sqlite`, `maintain`, `profile`, `recall` etc.). `cortex users list`
  shows users, created date and storage. These are operator tools run over
  SSH on the droplet.
- The bootstrap user's AI settings are pre-filled from the current server env
  (`OPENAI_API_KEY` etc.), encrypted into their settings row, so the owner's
  brain keeps working immediately. After that, the operator key is not used for
  anyone else.

### 12. Security notes

- Rate-limit the sign-in, OAuth token and registration endpoints.
- Validate `redirect_uri` exactly, and bind PKCE and state.
- Store refresh and agent tokens only as hashes.
- Never log tokens, keys or memory contents at info level.
- Response bodies and errors never reveal whether another user's data exists.

## Suggested build order

Each step is shippable and testable on its own; the builder may split these
into several plans.

1. Multi-user data layer: migration 002 with the bootstrap user, per-user
   vectors with generations, and the **soft-delete and provenance columns**
   (so that later steps never ship hard deletes over MCP). Includes the
   isolation test suite.
2. Google sign-in, sessions, and AI provider settings (encrypted keys,
   per-user LLM wiring, re-embed on model change).
3. Connections: agent tokens (incl. migrating the existing token), provenance
   on writes, and MCP scoping by token. Includes the `context` tool and
   rewritten tool descriptions.
4. OAuth 2.1 authorization server, consent page, and verified connections from
   ChatGPT and claude.ai.
5. Connections page: list, revoke, revoke-and-remove.
6. Purge job, Memories page, bulk delete, Recently deleted, restore, storage
   figures, account deletion.
7. Compaction with preview and undo.
8. Per-user nightly maintenance and job isolation; docs (deploy steps for
   Google OAuth, custom-instruction text).

## Acceptance

1. The isolation suite passes for every API, tool, page and job.
2. The owner signs in with Google and sees their existing brain; the Claude
   Code tunnel connection still works without config changes.
3. ChatGPT (Plugins → Add custom MCP server, OAuth) connects. With the
   recommended custom instructions, a question about the owner's preferences
   triggers `context`. A newly stated preference is saved via `remember` and is
   visible on the Memories page as "written by ChatGPT". claude.ai does the
   same.
4. A second Google account signs up, gets an empty brain, configures its own
   key, and sees nothing of the owner's data. Its AI calls hit only its own key.
   This is verified with a stub provider in tests and a real check on the
   droplet.
5. Revoke-and-remove on a connection hides everything it wrote; restore brings
   it back; after 30 days (simulated in tests) the rows are purged.
6. Compaction shows a preview. Applying it reduces the memory count; undo
   restores the originals exactly.
7. Changing the embedding model re-embeds in the background, recall keeps
   working during it, and recall uses the new vectors after it.
8. The droplet stays within memory with the service, Postgres and Caddy idle
   (record `free -h`), and recall p95 on the droplet stays under 500 ms at the
   owner's data size, excluding provider API time.

## Left to the builder

- OAuth library choice and token storage details (within the requirements
  above).
- Whether to add Postgres RLS as defence-in-depth.
- Exact storage-size computation.
- Page styling (keep it plain and consistent; the wireframes set the content).
- Exact HNSW partial index management for new dims values.
- How the compaction prompts are structured.

## Owner decisions, 2026-10-22

Recorded after the verified critique of the implementation plan. These override
any earlier text in this spec that conflicts with them.

1. **Owner decision, 2026-10-22: provider URLs (§4).** Per-user chat and
   embedding base URLs must be public `https`. Private, loopback, link-local,
   CGNAT and ULA addresses and cloud metadata (`169.254.169.254`) are blocked at
   dial time. Redirects are off, response size is capped, and error text is
   generic (no upstream bodies or addresses). An operator-only server env
   switch, `CORTEX_ALLOW_PRIVATE_PROVIDERS=1`, can later allow local providers
   such as Ollama.
2. **Owner decision, 2026-10-22: merge is undoable (§7).** Merge is truly
   undoable for 30 days. The delete batch records the retargeted and removed
   link and relationship rows (and the keeper's pre-merge row), so
   `RestoreBatch` reverses a merge exactly.
3. **Owner decision, 2026-10-22: account deletion (§7).** Deleting an account
   cuts access immediately: all connections and tokens are revoked, sessions
   are deleted, and jobs skip the user. The data stays restorable for 30 days by
   signing in again; the restored account has no connections and the user
   reconnects.
4. **Owner decision, 2026-10-22: consent screen (§5).** Consent shows the client
   name and the redirect host (and the CIMD host). Dynamically registered
   clients are labelled "unverified". Redirect URIs must be `https`, plus
   `http` to `localhost`/`127.0.0.1` loopback for local tools. Consent is always
   shown, even on re-authorization. The validated authorize request is
   persisted server-side. All pages send `frame-ancestors 'none'` and
   `X-Frame-Options: DENY`.
5. **Owner decision, 2026-10-22: `--user` (§11).** The admin CLI `--user` flag
   accepts a user ID or a verified email only. On ambiguity it refuses and
   lists the IDs. `cortex users list` shows IDs.
6. **Owner decision, 2026-10-22: legacy HTTP transport (§11).** `cortex mcp
   --transport http` stays for local use but refuses to start without
   `CORTEX_AUTH_TOKEN`. It is single-user (the bootstrap user).
7. **Owner decision, 2026-10-22: rollout.** Deploy the new binary first:
   `setup-server.sh` writes units without restarting, and `deploy-app.sh` does a
   single restart. Migrations run as an explicit step with a long timeout.
   Keep a `pg_dump` and the previous binary (`cortex.prev`) for rollback.
   Rehearse migration 002 on a restored copy of the live dump first and record
   its duration. About one minute of tunnel downtime is acceptable.
8. **Owner decision, 2026-10-22: recall during re-embed (§2).** Recall keeps
   working during a re-embed. The active generation's embedder settings are
   stored and used by the resolver until the flip. The flip runs under a
   per-user advisory lock that writers also take. Soft-deleted rows are
   re-embedded too.
