# Multi-user cortex with assistant OAuth — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `https://cortex.sausheong.dev` a multi-user memory service. Users sign in with Google and bring their own AI provider. Assistants connect via MCP OAuth 2.1, and the user's own agents use named tokens. Web pages cover settings, connections, memories and compaction.

**Architecture:**
- The root `cortex` package gains a user-bound handle. `Open` binds to the bootstrap user by default, so single-user library use and existing tests keep working. `(*Cortex).ForUser(id)` yields a handle for another user, and every query filters `user_id`.
- A new `internal/web` package serves the Google sign-in, sessions, pages and the OAuth authorization server. `internal/accounts` holds settings, connections, tokens, crypto and the hardened provider HTTP client.
- The MCP HTTP handler resolves the bearer token (an OAuth access token or an agent token) to `(user, connection)`, then builds a per-request cortex handle with that user's own LLM and embedder.

**Tech Stack:** Go 1.25.1, Postgres 17 + pgvector ≥ 0.8, `github.com/jackc/pgx/v5`, `github.com/mark3labs/mcp-go` v0.47.1 (`server.WithHTTPContextFunc`), `golang.org/x/oauth2` + `github.com/coreos/go-oidc/v3` (Google), Go `html/template` with `embed`, `crypto/aes` GCM.

**Spec:** `docs/superpowers/specs/2026-10-22-multi-user-oauth-design.md` (base: `2026-10-22-postgres-storage-design.md`, branch `postgres-storage`). The spec's "Owner decisions, 2026-10-22" section is binding and summarised in Global Constraints.

**Revision note (2026-10-22):** revised after a verified critique. Changes: one integer-numbered migration for the whole data layer (Task 1), the task count split for reviewability, a startup-order and legacy-transport task (Task 9), a hardened provider client, reversible merge, immediate account-deletion cutoff, a consent-screen policy, a safer rollout with rollback, and per-layer isolation tests.

## Global Constraints

- **No cross-user data access** through any API, MCP tool, page, job, log line or error message. Every ID lookup and update carries a `user_id` predicate. A foreign ID and an absent ID return the same `ErrNotFound` (tests assert `errors.Is` equality and identical message text); web routes return 404 for both.
- Google sign-in only; open sign-up; no quotas.
- Bootstrap user email: `sausheong@gmail.com` (`CORTEX_BOOTSTRAP_EMAIL`). It binds only when Google reports `email_verified=true`.
- Per-user AI settings:
  - base URL (default `https://api.openai.com/v1`), API key, chat model, embedding model (default `text-embedding-3-small` at 512 dims);
  - chat provider type `openai-compatible` (default) or `anthropic`;
  - optional separate embedding base URL and key;
  - `EmbedDims` must be in the allowlist `{256, 384, 512, 768, 1024, 1536, 2048, 3072}` and the embed response length must equal it (checked at save and on every embed). The core store only hard-bounds dims to 1..4000, so tests can use 8 and 16.
  - The operator's key is **never** used for any user except via the bootstrap user's pre-filled settings.
- **Provider URL policy (owner decision 1).** Chat and embed base URLs: public `https` only. The one hardened client (`internal/accounts/safehttp.go`) rejects, at dial time (a `net.Dialer.Control` hook, so DNS rebinding is covered), loopback, private (RFC 1918), link-local (incl. `169.254.169.254`), CGNAT (`100.64.0.0/10`), ULA (`fc00::/7`), unspecified and IPv4-mapped forms of those. Redirects are off, and `Transport.Proxy` is nil (environment proxies are ignored, so the dial check always sees the real target). Response caps are per client: 1 MB for chat, 16 MB for embeddings (a batch of 64 at 3072 dims is about 2.4 MB of JSON), 64 KB for CIMD. Errors shown to users are fixed text ("provider unreachable", "provider rejected the key", "unexpected response") and never echo an upstream body or address. The operator-only env switch `CORTEX_ALLOW_PRIVATE_PROVIDERS=1` lifts the address and `https` restrictions (for local providers such as Ollama); it is read once at `serve` start and is not settable from the web.
- Keys are encrypted with AES-256-GCM using `CORTEX_SECRET_KEY` (32 bytes, hex, in `/etc/cortex/cortex.env`), and are displayed masked (last 4 characters). `cortex serve` refuses to start without it.
- OAuth:
  - one scope `cortex` plus `offline_access`; consent is all-or-nothing;
  - PKCE S256 required; code TTL 60 s; access token TTL 1 h; refresh tokens rotate with a 90-day idle expiry;
  - all tokens are opaque and stored as SHA-256 hashes;
  - CIMD fetch rules: the same hardened client as providers (never lifted by the private-providers switch), https only, 5 s timeout, 64 KB cap, cache ≤ 24 h;
  - DCR clients that are never authorized are deleted after 24 h; the DCR body is capped at 64 KB;
  - one live connection per (user, client_id): a partial unique index `WHERE kind='oauth' AND revoked_at IS NULL`;
  - **Consent policy (owner decision 4).** Consent always shows, even on re-authorization. It shows the client name, the redirect host and (for CIMD) the CIMD host. DCR-registered clients are labelled "unverified". Redirect URIs must be `https`, or `http` to `localhost`/`127.0.0.1`/`[::1]` (loopback for local tools); checked at registration and at authorize. The validated authorize request is persisted server-side (`authorize_requests`) and the consent form posts only its ID plus the CSRF token.
  - Every page sends `Content-Security-Policy: frame-ancestors 'none'` and `X-Frame-Options: DENY`.
- Soft delete with a 30-day restore window, then nightly purge. Every delete stamps a `delete_batch_id`. Unique constraints are partial `WHERE deleted_at IS NULL`. Purge removes a row's `embeddings`, `memory_entities`, `memory_edges` and batch-change records in the same transaction.
- **Merge is undoable for 30 days (owner decision 2).** A merge's batch records the keeper's pre-merge row and every retargeted or removed link and relationship row, so `RestoreBatch` reverses it exactly.
- **Account deletion cuts access immediately (owner decision 3).** It revokes all connections and tokens, deletes sessions, and the user is skipped by jobs. Data is restorable for 30 days by signing in again; the restored account has no connections.
- **Admin CLI `--user` (owner decision 5)** accepts a user ID or a *verified* email only. On ambiguity it refuses and lists the IDs. `users list` shows IDs.
- **Legacy `cortex mcp --transport http` (owner decision 6)** stays for local use but refuses to start without `CORTEX_AUTH_TOKEN`; it is single-user (bootstrap).
- **Recall during re-embed (owner decision 8)** keeps working: the active generation's embedder settings are stored and used by the resolver until the flip; the flip runs under a per-user advisory lock that writers also take; soft-deleted rows are re-embedded too.
- The owner's existing vectors are `text-embedding-3-small`/512. They become generation 1 for the bootstrap user, with no re-embed.
- The existing `CORTEX_AUTH_TOKEN` becomes the bootstrap user's agent token "claude-code (tunnel)". The owner's Claude Code config must keep working unchanged.
- Rate limiting (in-memory token buckets). The client IP is `RemoteAddr`, except when the peer is loopback (Caddy), where it is the last `X-Forwarded-For` entry. Limits: 30 req/min per IP on `/oauth/register`, `/oauth/token`, `/oauth/authorize` and `/auth/google/*`; 10 provider tests/min per user; 30 failed bearer lookups/min per IP.
- Background worker pool: 2 workers.
- Commits: no AI attribution (no Co-Authored-By, no "Generated with").
- Tests run with `make test` (pgvector container). DB tests use `internal/pgtest`.

## Review Focus

1. **A user whose provider key is missing or invalid while the operator's `OPENAI_API_KEY` is set in the server env.** The MCP call returns "set up your AI provider at https://cortex.sausheong.dev/settings", and the stub operator provider receives 0 calls. Test in Task 9.
2. **An unverified bootstrap email.** A Google sign-in with email `sausheong@gmail.com` but `email_verified=false` creates a *new* user and does not bind the bootstrap brain. Test in Task 7.
3. **Restore collision.** Delete entity "Alice", create a new "Alice", then restore the batch. The result is one live "Alice" holding both sets of memories. Test in Task 4.
4. **Recall and writes during a re-embed.** Recall returns old-generation results with the old model until the flip; a memory added between the final scan and the flip is found after it. Tests in Task 8.
5. **The same assistant authorizing twice, and reconnecting after revoke.** A second authorization reuses the connection, so the `written` count is preserved, and still shows consent. After revoke, a new authorization creates a new connection, and the old tokens get 401. Test in Task 10.
6. **A deleted account.** Its bearer tokens, refresh tokens and sessions are rejected at once; a settings-less deleted user is still purged after 30 days. Tests in Tasks 6, 10 and 12.
7. **Provider SSRF.** Chat and embed URLs pointing at loopback, `169.254.169.254` or a redirecting server never connect, and the error text contains no upstream data. Tests in Task 6.
8. **Merge undo.** Restoring a merge batch brings back the dropped entity with its original links and relationships. Test in Task 4.
9. **Startup order.** `cortex serve` runs migrations, then `MigrateBootstrap`, then listens; the old tunnel token gets 200 on `/mcp`. Test in Task 9.

## File Structure

| Path | Responsibility |
|---|---|
| `migrations/002_users.sql` | the whole data-layer migration (Tasks 1, 3, 4, 5): users, `user_id` everywhere, provenance, soft delete, partial uniques, guarded embeddings conversion, `delete_batches`, `delete_batch_changes`, bootstrap user `u_bootstrap` |
| `migrations/003_accounts.sql` | `user_settings`, `connections`, `agent_tokens`, `oauth_clients`, `authorize_requests`, `oauth_codes`, `oauth_tokens`, `sessions`, `login_attempts`, `compactions`, `jobs` |
| `user.go` (new) | `User` type, `ForUser`, user CRUD, bootstrap binding, `ResolveUserRef` |
| `softdelete.go` (new) | soft delete, batches, change records, restore, purge |
| `vector.go` | per-user dims + generations, advisory lock, partial indexes |
| `migrate.go`, `store.go` | `WithMigrationTimeout`, `migrateUpTo` test hook; `ensureVectorSchema` reduced to the pgvector version check |
| all query files | `user_id` scoping, `deleted_at IS NULL`, `written_by_connection_id` |
| `internal/sqliteimport/`, `internal/testutil/`, `cmd/cortex-eval/` | explicit `user_id`; importer takes a user ID |
| `llm/openai/`, `llm/anthropic/` | `WithHTTPClient` option |
| `compaction.go` (new) | proposal / apply / undo |
| `usage.go` (new) | per-user storage figures |
| `internal/accounts/` (new) | `crypto.go`, `settings.go`, `connections.go`, `tokens.go`, `safehttp.go`, `providers.go`, `bootstrap.go`, `ratelimit.go` |
| `internal/web/` (new) | `server.go` (mux, `Options`, headers), `session.go`, `google.go`, `oauth/` (AS), `pages/` (templates + handlers), `static/` |
| `internal/jobs/` (new) | worker pool, index build, re-embed, compaction proposal, nightly maintain |
| `cmd/cortex/mcp/` | token→user resolution, per-request handle, `context` tool, rewritten descriptions, token-required legacy HTTP |
| `cmd/cortex/main.go` | `serve`, `migrate`, `--user`, `users list` |
| `scripts/setup-server.sh`, `scripts/deploy-app.sh`, `scripts/rollback-app.sh`, `docs/deploy.md` | secrets, Google env, rollout, rollback, docs |

---

### Task 1: Migration 002 and per-user scoping of the core store

**Files:** create `migrations/002_users.sql`, `user.go`, `migrate_test.go`. Modify `migrate.go` (`WithMigrationTimeout` option, `migrateUpTo(ctx, n)` test hook), `store.go` (`Open`, `ensureVectorSchema`, sync_state), `entity.go`, `relationship.go`, `memory.go`, `chunk.go`, `memory_edge.go`, `cortex.go`, `vector.go` (minimal, see below), `search.go`, `internal/sqliteimport/import.go`, `internal/testutil/{seed,testutil}.go`, `vector_test.go`.

**Interfaces:**
- Produces:
  - `type User struct{ ID, GoogleSub, Email, Name string; EmailVerified bool; CreatedAt time.Time; DeletedAt *time.Time }`
  - `const BootstrapUserID = "u_bootstrap"`
  - `var ErrNotFound`: the single error for absent and foreign IDs.
  - `func (c *Cortex) UserID() string`
  - `func (c *Cortex) ForUser(id string, opts ...Option) *Cortex`: a shallow copy sharing the db, with its own cfg (`opts` override llm, embedder, extractor).
  - `func (c *Cortex) CreateUser(ctx, googleSub, email, name string, emailVerified bool) (*User, error)`, which also creates the user's person entity (`Source:"owner"`) and stores its ID in `users.owner_entity_id`.
  - `func (c *Cortex) UserByGoogleSub(ctx, sub string) (*User, error)`, `ListUsers(ctx) ([]User, error)`
  - `func (c *Cortex) BindBootstrap(ctx, googleSub, email, name string) (*User, error)`
  - `func (c *Cortex) ResolveUserRef(ctx, ref string) (*User, error)`: matches an ID, else a *verified* email; more than one match returns an error listing the IDs.
  - `func WithMigrationTimeout(d time.Duration) Option` (default 60 s).
  - `Open` binds to `BootstrapUserID`.
- **`migrations/002_users.sql` is the only migration for Tasks 1, 3, 4 and 5. Names are integer-prefixed only (`migrate.go` runs `Atoi` on the prefix; `002b` would make every `Open` fail).** It contains:
  - `users(id, google_sub UNIQUE NULL, email, email_verified, name, created_at, deleted_at, active_embedding_generation INT NOT NULL DEFAULT 1, owner_entity_id, usage_json, last_maintenance_at, last_maintenance_error)`; inserts `u_bootstrap`.
  - `user_id TEXT NOT NULL DEFAULT 'u_bootstrap' REFERENCES users ON DELETE CASCADE` on entities, relationships, memories, memory_entities, memory_edges, chunks, embeddings and sync_state, then the default dropped; btree index on `user_id` per table.
  - `written_by_connection_id`, `deleted_at`, `deleted_by`, `delete_batch_id` on entities, relationships, memories and chunks.
  - `entities` uniqueness replaced by partial unique `(user_id, name, type) WHERE deleted_at IS NULL`; every other unique constraint in `001_init.sql` rescoped by `user_id` the same way; `sync_state` PK `(user_id, connector)`.
  - `delete_batches(id, user_id REFERENCES users ON DELETE CASCADE, kind, created_at, restored_at)` and `delete_batch_changes(id, batch_id REFERENCES delete_batches ON DELETE CASCADE, user_id REFERENCES users ON DELETE CASCADE, table_name, op /* 'retarget'|'delete'|'update' */, row_json JSONB)`.
  - `UPDATE users SET owner_entity_id = (oldest `entities` row with `source='owner'`) WHERE id='u_bootstrap'` (left NULL only if the legacy DB has none; `MigrateBootstrap`, Task 6, then creates it).
  - **Embeddings, a guarded `DO` block:** if `embeddings.embedding` exists (a legacy deployment that ran `ensureVectorSchema`): drop the three `idx_embeddings_hnsw_*` indexes, `ALTER COLUMN embedding TYPE halfvec`, add `dims INT` backfilled from `vector_dims(embedding)` then `SET NOT NULL`, add `generation INT NOT NULL DEFAULT 1`, create `emb_hnsw_<type>_<dims>` partial indexes for the dims found (512 on the droplet), delete the `cortex_meta` `embedding_dims` row. Else (fresh DB, column absent): add `embedding halfvec` (untyped), `dims INT NOT NULL`, `generation INT NOT NULL DEFAULT 1`, with no indexes. Either way: unique `(user_id, ref_type, ref_id, generation)`.
- **`ensureVectorSchema` keeps only the pgvector ≥ 0.8 version check.** Its column/index DDL and the `Open` dims-mismatch check are removed (`c.dims` goes away; Task 3 reads dims per row).
- **Minimal `vector.go` change in this task** so existing tests stay green: embedding writes set `user_id`, `dims = len(vec)`; `SearchVector` filters `user_id` and `dims`, and casts `embedding::halfvec(<dims>)`. Per-user generations and index management are Task 3.
- `PutEntity`'s conflict target becomes `(user_id, name, type) WHERE deleted_at IS NULL`; `SetSyncState` becomes `ON CONFLICT (user_id, connector)`. `PutEntity`'s upsert must not demote an owner entity (`source` keeps `'owner'`).
- `internal/sqliteimport`: `Import(ctx, sqliteDB, pg, userID string, opts)`; it no longer reads the Postgres `cortex_meta embedding_dims` row (migration 002 deletes it and fresh DBs never have it, so the current read at `import.go:324` would fail): dims come from the SQLite source (its own `cortex_meta`, else the length of the first vector) and are written per row; every INSERT lists `user_id`, the embeddings insert sets `user_id`, `dims`, `generation`; the missing-embedding queries and `Report` counts are scoped to `userID`. `internal/testutil` seeds through `ForUser`/the bootstrap handle. `cmd/cortex-eval` opens as the bootstrap user and its `generate.go` SQL gets `user_id = 'u_bootstrap'`.
- Every read adds `user_id = ?` and `deleted_at IS NULL`; every write sets `user_id`; every `UPDATE`/`DELETE` by ID carries `AND user_id = ?`.

- [ ] **Step 1: Write failing tests.**
  - `migrate_test.go`, `TestMigration002FromLegacy`: apply `migrateUpTo(1)` (001 only), execute the legacy vector DDL fixture (`ALTER TABLE embeddings ADD COLUMN embedding halfvec(512) NOT NULL`, the three `idx_embeddings_hnsw_*` indexes, `cortex_meta embedding_dims=512`), insert one row in each of the eight data tables, then run 002. Assert: every row has `user_id='u_bootstrap'`; embeddings have `dims=512`, `generation=1`; `emb_hnsw_memory_512` exists and `idx_embeddings_hnsw_memory` does not; `SearchVector` for the bootstrap user returns the seeded memory; row counts are unchanged.
  - `TestMigration002FreshDB`: `Open` on an empty DB succeeds; `embeddings.embedding` is untyped `halfvec`, `dims` and `generation` exist, no `emb_hnsw_*` index exists; a second `Open` is a no-op.
  - `TestMigration002SetsBootstrapOwnerEntity`: the legacy fixture's `source='owner'` entity becomes `u_bootstrap`'s `owner_entity_id`.
  - `TestMigrationNamesAreIntegers`: every file in `migrations/` matches `^\d+_[a-z_]+\.sql$`.
  - `TestBootstrapDefault`: `Open(...).UserID() == "u_bootstrap"`.
  - `TestPutEntityConflictPerUser`: users A and B each `PutEntity` "Alice" (person) twice; 2 live rows total, 1 per user. `TestSyncStatePerUser`: the same connector name for two users holds two states. `TestPutEntityDoesNotDemoteOwner`.
  - `TestResolveUserRef`: ID resolves; verified email resolves; two users with the same email, one verified, resolves to the verified; two verified, or only unverified duplicates, returns an error whose text lists both IDs; an unknown ref returns `ErrNotFound`.
  - `TestImportSqliteFreshDB` (in `internal/sqliteimport`): import into a fresh DB with no `cortex_meta` dims row succeeds and embeddings carry the source dims.
  - `vector_test.go` rewrite: delete the `Open` dims-mismatch and column-DDL tests; replace with `TestSearchVectorUsesRowDims` (write 8-dim rows, search returns them) and `TestOpenAcceptsAnyEmbedderDims`.
- [ ] **Step 2: Run** `go test -run 'Migration|Bootstrap|PutEntity|SyncState|ResolveUserRef|SearchVector|OpenAccepts' .`. Expected: FAIL.
- [ ] **Step 3: Implement** the migration, `user.go`, the importer/testutil/eval changes, and scoping of the files listed. `dbConn` adds no scoping; each query states `user_id = ?` explicitly. Afterwards, grep for any `FROM|JOIN|UPDATE|DELETE FROM (entities|relationships|memories|chunks|memory_edges|memory_entities|embeddings|sync_state)` lacking `user_id`, repo-wide including `internal/` and `cmd/`.
- [ ] **Step 4: Run** `make test`. Expected: all packages PASS (existing tests run as the bootstrap user; `internal/sqliteimport` and `cmd/cortex-eval` build).
- [ ] **Step 5: Commit** `feat(store): migration 002, users and per-user scoping of the core store`.

### Task 2: Remaining scoped passes and the core isolation suite

**Files:** modify `traverse.go`, `lint.go`, `decay.go`, `expire.go`, `reconcile.go`, `relate.go`, `profile.go`, `recall.go`, `search.go` (any query left unscoped by Task 1), `maintain.go`; create `isolation_test.go`.

**Interfaces:** no new API. Every public `*Cortex` method that takes an ID (`GetEntity`, `Forget`, `MergeEntities`, `MergeEntitiesDryRun`, `InvalidateMemory`, `GetMemoriesByEntity`, `GetRelationships`, `GetMemoryEdges`, `GetMemoryEdgesByType`, `PutRelationship`, `PutMemoryEdge`, `PutMemory`/`PutChunk` with entity or memory references, `Remember` with entity references, `Traverse` start ID, `Profile` target) and the private `getMemoryByID` returns `ErrNotFound` for a foreign ID, identical to the absent-ID result.

- [ ] **Step 1: Write failing tests** in `isolation_test.go`, using `twoUsers(t) (a, b *Cortex)` (`Open(pgtest.DSN(t))`, `CreateUser` twice, `ForUser`). Both users `Remember`/`PutEntity` "Alice" (person) with different memories. A table-driven `TestIsolationForeignIDs` runs each ID-taking method as `a` with b's ID and, in a second pass, with a random absent ID; asserts `errors.Is(err, ErrNotFound)`, equal error text for both passes, and that b's rows are unchanged. Methods: `GetEntity`, `getMemoryByID` (in-package test), `Forget{EntityID}`, `MergeEntities` and `MergeEntitiesDryRun` with `(a's, b's)` and `(b's, a's)`, `InvalidateMemory`, `GetMemoriesByEntity`, `GetRelationships`, `GetMemoryEdges`, `GetMemoryEdgesByType`, `Remember` referencing b's entity ID, `PutMemory` and `PutChunk` with b's entity or memory refs, `PutRelationship` with b's entity as either end, `PutMemoryEdge` with b's memory, `Traverse` from b's entity, `Profile` of b's entity, `SoftDelete*` stubs added in Task 4 are appended to this table there.
  - Other tests: `TestIsolationEntities` (`FindEntities{Name:"Alice"}` returns 1 row, a's), `TestIsolationRecall` (a's recall contains none of b's texts, with identical embeddings in both brains), `TestIsolationSearchVector`, `TestIsolationTraverse`, `TestIsolationProfile`, `TestIsolationLintMaintain` (running `a.Maintain()`, which includes `BuildMemoryEdges` (it takes no IDs), leaves b's confidence, `expired_at` and edges unchanged). `TestIsolationImport` lives in `internal/sqliteimport/isolation_test.go` (importing the root package from there is not a cycle): the importer with `userID=a` creates no rows for b and counts only a's.
- [ ] **Step 2: Run** `go test -run 'Isolation' . ./internal/sqliteimport/`. Expected: FAIL.
- [ ] **Step 3: Implement.** Re-run the grep from Task 1 Step 3 until clean.
- [ ] **Step 4: Run** `make test`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(store): scope remaining queries, core isolation suite`.

### Task 3: Per-user vector dims, generations, indexes

**Files:** modify `vector.go`, `search.go`, `cortex.go` (embedding writes), `store.go`; test `vector_test.go`. Schema already exists from Task 1; this task adds no migration.

**Interfaces:**
- Writes use `dims = len(vec)` and the handle's generation. Queries filter `user_id`, `dims`, `ref_type`, active generation and cast `embedding::halfvec(<dims>)`.
- `func (c *Cortex) ActiveGeneration(ctx) (int, error)`, `SetActiveGeneration(ctx, g int) error`, `PutEmbeddingGen(ctx, refID, refType string, gen int, v []float32) error`.
- `func (c *Cortex) WithGeneration(g int) *Cortex`: a handle **pinned** to generation `g` for ordinary writers. Every embedding write through a pinned handle takes `pg_advisory_xact_lock_shared(hash('reembed', userID))` and fails with `ErrStaleGeneration` when `g != users.active_embedding_generation`. `PutEmbeddingGen` (the re-embed writer) is not pinned: it takes the same shared lock, never applies the stale check, and accepts only `gen` equal to `active` or `active+1`, so a stale pinned handle and the re-embed target are told apart by which entry point is used.
- `func (c *Cortex) FlipGeneration(ctx, newGen int, embedTail func(ctx context.Context, tx *sql.Tx, put func(refID, refType string, v []float32) error) error, inTx func(tx *sql.Tx) error) error`: the whole flip as one core function, in one transaction: take the exclusive lock (`pg_advisory_xact_lock`); call `embedTail` (it lists rows still lacking `newGen` through `tx` and writes through the supplied `put`, which uses the same tx and takes no second lock, so the flip cannot block on itself); set `users.active_embedding_generation = newGen`; delete all rows of other generations; call `inTx` (the accounts layer's `PromoteActiveEmbedTx` runs here); commit. Any error rolls everything back.
- `func (c *Cortex) EnsureVectorIndex(ctx, dims int, refType string) error`: creates `emb_hnsw_<t>_<dims>` (`USING hnsw ((embedding::halfvec(<dims>)) halfvec_cosine_ops) WHERE dims=<dims> AND ref_type='<t>'`) outside a transaction, holding `pg_advisory_lock(4243)`; hard-bounds `1 <= dims <= 4000`. **It is never called on a request path**: a job (Task 8) calls it before the first write for new dims. Until the index exists, search falls back to a filtered scan; writes never block on index builds.
- Keeps `hnsw.iterative_scan = strict_order` for filtered queries.

- [ ] **Step 1: Write failing tests:**
  - `TestVectorTwoUsersDifferentDims`: user a has an 8-dim embedder and user b a 16-dim one; each `SearchVector` returns only its own results; after `EnsureVectorIndex` for both, indexes `emb_hnsw_memory_8` and `emb_hnsw_memory_16` exist.
  - `TestEnsureVectorIndexRejectsBadDims`: 0, -1 and 4001 return errors and create nothing; concurrent calls for one (dims, type) create exactly one index.
  - `TestVectorGenerationIsolation`: write gen 1 and gen 2 rows. Search sees only gen 1 until `SetActiveGeneration(2)`, then only gen 2.
  - `TestStaleGenerationWriteRejected`: a handle with `WithGeneration(1)` writing after the flip to 2 returns `ErrStaleGeneration` and leaves no row.
  - `TestFlipGenerationAtomic`: a failing `inTx` leaves the active generation and both generations' rows unchanged; a successful flip with a tail row leaves exactly one generation. `TestFlipDoesNotSelfDeadlock`: `embedTail` writing through `put` completes within the timeout.
  - `TestVectorWriteTakesSharedLock`: a transaction holding the exclusive per-user lock blocks `PutEmbeddingGen` until released (context timeout asserts the block).
- [ ] **Step 2: Run** `go test -run 'Vector|EnsureVectorIndex|StaleGeneration' .`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make test`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(store): per-user embedding dims, generations and on-demand indexes`.

### Task 4: Soft delete, batches, restore, purge, reversible merge

**Files:** create `softdelete.go`, `softdelete_test.go`; modify `cortex.go` (Forget, `deleteOrphanedMemories`), `entity.go` (`MergeEntities`), `cmd/cortex/mcp/tools.go` (forget and merge go soft). Tables `delete_batches` and `delete_batch_changes` come from `002_users.sql` (Task 1); no new migration.

**Interfaces:**
- `type DeleteFilter struct{ IDs []string; WrittenBy string; OlderThan *time.Time; Query string; All bool }`
- `func (c *Cortex) SoftDeleteMemories(ctx, f DeleteFilter, by string) (batchID string, n int, err error)`
- `func (c *Cortex) SoftDeleteEntity(ctx, id, by string) (batchID string, err error)`
- `func (c *Cortex) SoftDeleteByConnection(ctx, connID string) (batchID string, n int, err error)` (memories, entities, relationships, chunks)
- `func (c *Cortex) RestoreBatch(ctx, batchID string) error` (takes this user's batches only; a foreign or unknown ID returns `ErrNotFound`)
- `func (c *Cortex) ListDeleted(ctx, limit, offset int) ([]Memory, error)`, `ListDeleteBatches(ctx) ([]DeleteBatch, error)`
- `func (c *Cortex) PurgeDeleted(ctx, olderThan time.Duration) (int, error)`: hard delete of rows with `deleted_at < now()-olderThan`, and **in the same transaction** their `embeddings` rows (all generations), `memory_entities` and `memory_edges`. Batches are purged as units: a `delete_batches` row with its `delete_batch_changes` (retarget rows are keyed by batch, not by purged row) is removed when `created_at < now()-olderThan` or the batch has been restored; so `ListDeleteBatches` lists only unrestored, unpurged batches and never an empty one.
- `by` is one of `"user"`, `"connection:<id>"` or `"compaction"`.
- **Soft `Forget` keeps links** (`memory_entities` rows) and **skips `deleteOrphanedMemories`**; a memory whose only links are to deleted entities is still shown (spec §7). Hard purge, not soft delete, removes the links.
- **Merge** (`MergeEntities`) is soft and reversible: the dropped entity gets `deleted_at` and a `delete_batch_id`; the batch records, in `delete_batch_changes`, the keeper's pre-merge row (`op='update'`), each retargeted `memory_entities`/`relationships` row with its old `entity_id` (`op='retarget'`) and each deduplicated link or relationship removed (`op='delete'`, full row). `RestoreBatch` replays these in reverse inside one transaction.
- Restore collision: if a restored entity's (name, type) is live, merge the restored entity into the live one with the merge logic, inside the restore transaction (the nested merge is not itself recorded).

- [ ] **Step 1: Write failing tests:**
  - `TestSoftDeleteHidesEverywhere` (table-driven over memories, entities, relationships, chunks): after delete the row is absent from `Search`, `Recall`, `GetMemoriesByEntity`, `as_of` recall, `Traverse`, `FindEntities`, chunk search and `SearchVector`; plus the case of a memory whose only linked entity is deleted: still returned by `Recall`.
  - `TestRestoreBatchExact`: restore brings back exactly the batch's rows. Other batches stay deleted.
  - `TestRestoreCollisionMerges` (Review Focus 3).
  - `TestMergeRestoreReversesExactly` (Review Focus 8): merge B into A where B has 3 links (1 duplicating a link of A) and 2 relationships (1 duplicate); `RestoreBatch`; assert B is live with its original 3 links and 2 relationships, A's aliases and fields equal the pre-merge snapshot, and the link and relationship row sets of both entities equal the snapshot.
  - `TestPurgeAfter30Days`: set `deleted_at` 31 days back; `PurgeDeleted(30*24h)` hard-deletes; a 29-day-old row survives.
  - `TestPurgeRemovesEmbeddingsAndLinks`: after purge there are 0 `embeddings`, `memory_entities` and `memory_edges` rows for purged IDs (all generations). `TestPurgeRemovesBatches`: after purging a merge batch (and a restored batch) its `delete_batches` and `delete_batch_changes` rows are gone and `ListDeleteBatches` does not list it; a younger batch keeps both.
  - `TestMCPForgetIsSoft` and `TestMCPMergeIsSoft`: the rows remain with `deleted_at` set; the tool text mentions 30-day recovery.
  - Append to the Task 2 `TestIsolationForeignIDs` table: `SoftDeleteEntity`, `RestoreBatch` with b's IDs return the same `ErrNotFound` as absent IDs; `SoftDeleteMemories{All:true}` as a affects 0 of b's rows; `PurgeDeleted` as a leaves b's expired rows.
- [ ] **Step 2: Run** `go test -run 'SoftDelete|Restore|Purge|MCPForget|MCPMerge|MergeRestore|Isolation' ./...`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make test`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(store): recoverable soft delete, reversible merge, purge`.

### Task 5: Provenance on writes

**Files:** modify `cortex.go` (Remember), `entity.go`, `relationship.go`, `memory.go`, `chunk.go`; test `provenance_test.go`. Columns exist from Task 1.

**Interfaces:**
- `func (c *Cortex) WithConnection(connID string) *Cortex`: a copy whose writes set `written_by_connection_id = connID`.
- `Memory.WrittenBy string` (`json:"written_by,omitempty"`), and the same field on `Entity`.
- `DeleteFilter.WrittenBy` (Task 4) matches it.

- [ ] **Step 1: Write failing tests:**
  - `TestRememberRecordsConnection`: `cx.WithConnection("c1").Remember(...)` sets every memory, entity and chunk written to `WrittenBy == "c1"`.
  - `TestPlainWriteHasNoConnection`: a write without a connection leaves `WrittenBy == ""`.
  - `TestDeleteByWrittenByScopedToUser`: `a.SoftDeleteMemories{WrittenBy:"c1"}` does not touch b's rows written with the same connection string.
- [ ] **Step 2: Run.** Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make test`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(store): record writing connection on every write`.

### Task 6: Accounts: crypto, settings, hardened provider client, connections, tokens, deletion, bootstrap

**Files:** create `migrations/003_accounts.sql`, `internal/accounts/{crypto,settings,connections,tokens,safehttp,providers,bootstrap,ratelimit}.go` with tests. Modify `llm/openai/{llm,embedder}.go` and `llm/anthropic/llm.go` to add `WithHTTPClient(*http.Client)` / `WithEmbedderHTTPClient`, with tests that a custom client is used.

**Interfaces:**
- `003_accounts.sql` is complete in this task: it holds the full DDL for every table, including the columns that Task 7 (`sessions`, `login_attempts`), Task 10 (`oauth_clients`, `authorize_requests`, `oauth_codes`, `oauth_tokens`) and Task 13 (`compactions`) specify; those tasks only reference it and never edit it. Every table with a `user_id` (`user_settings`, `connections`, `jobs`, `compactions`, `sessions`, `login_attempts` where scoped, `authorize_requests`, `oauth_codes`, `oauth_tokens`) has `REFERENCES users(id) ON DELETE CASCADE`; `agent_tokens.connection_id` and `oauth_tokens.connection_id` reference `connections ON DELETE CASCADE`. Tables: `user_settings(user_id PK, provider, base_url, api_key_sealed, chat_model, embed_model, embed_dims, embed_base_url, embed_key_sealed, active_embed_sealed, updated_at)`; `connections(id, user_id, kind, name, client_id, created_at, last_used, revoked_at)` with **partial unique index `(user_id, client_id) WHERE kind='oauth' AND revoked_at IS NULL`**; `agent_tokens(id, connection_id, token_hash UNIQUE)`; `oauth_clients`, `authorize_requests`, `oauth_codes`, `oauth_tokens` (columns in Task 10); `sessions`, `login_attempts` (Task 7); `compactions` (Task 13); `jobs(id, user_id, kind, status, progress_done, progress_total, error, created_at, updated_at)`.
- `func NewSealer(hexKey string) (*Sealer, error)`, `(*Sealer).Seal(plain string) (string, error)`, `(*Sealer).Open(sealed string) (string, error)`. AES-256-GCM, random nonce, base64.
- `type Settings struct{ Provider, BaseURL, APIKey, ChatModel, EmbedModel string; EmbedDims int; EmbedBaseURL, EmbedAPIKey string; UpdatedAt time.Time }`. `func (Settings) Masked() Settings` returns keys as `"••••" + last4`. `func (Settings) Validate(allowPrivate bool) error` enforces https (unless allowed), the dims allowlist, and a literal-IP host check.
- `type Store struct{ DB *sql.DB; Sealer *Sealer; AllowPrivate bool; Now func() time.Time /* nil = time.Now; web.Options.Now is passed here so token and refresh expiry are testable */ }`:
  - `GetSettings(ctx, userID) (*Settings, error)` (`ErrNoSettings` if none); `PutSettings(ctx, userID string, s Settings) (needsReembed bool, err error)` (validates; the active embedder (`active_embed_sealed`) is set in the same transaction when there is nothing to re-embed: the first save, a user with no embeddings rows, or a change that leaves embed model, dims and embed base URL identical (an embed-key-only change swaps the sealed key at once). Otherwise it leaves the active embedder untouched and returns `needsReembed=true`, and the caller enqueues `reembed`, whose flip promotes it in Task 8);
  - `GetActiveEmbed(ctx, userID) (*Settings, error)`, `PromoteActiveEmbedTx(ctx, tx *sql.Tx, userID string) error`;
  - `CreateConnection(ctx, userID, kind, name, clientID string) (*Connection, error)`;
  - `ListConnections(ctx, userID) ([]Connection, error)` (includes `LastUsed`, `Written`);
  - `RevokeConnection(ctx, userID, connID string) error` (foreign or absent ID: the same `ErrNotFound`);
  - `CreateAgentToken(ctx, userID, name string) (plaintext string, conn *Connection, err error)`;
  - `ResolveBearer(ctx, token string) (userID, connID string, err error)`: agent tokens and OAuth access tokens by SHA-256; rejects revoked connections, expired tokens and **users with `deleted_at` set**; updates `last_used`;
  - `DeleteAccount(ctx, userID string) error`: in one transaction revokes every connection, deletes all `oauth_tokens`, `oauth_codes` and `sessions` for the user, and sets `users.deleted_at`. `RestoreAccount(ctx, userID string) error` clears `deleted_at` (connections stay revoked).
- `type Connection struct{ ID, UserID, Kind /* "oauth"|"agent" */, Name, ClientID string; CreatedAt time.Time; LastUsed *time.Time; Written int; RevokedAt *time.Time }`
- `func SafeClient(allowPrivate bool, maxBody int64) *http.Client` (`safehttp.go`), with `const ChatBodyCap = 1<<20, EmbedBodyCap = 16<<20, CIMDBodyCap = 64<<10`; `Transport.Proxy = nil`; dialer `Control` hook blocks loopback, RFC 1918, link-local, CGNAT, ULA, unspecified and IPv4-mapped forms; `CheckRedirect` returns `http.ErrUseLastResponse`; a wrapping `RoundTripper` caps response bodies at `maxBody`; `func ProviderError(err error) error` maps failures to the fixed texts in Global Constraints. The CIMD fetcher (Task 10) uses `SafeClient(false, CIMDBodyCap)` regardless of `AllowPrivate`.
- `func Providers(chat, embed *Settings, hc *http.Client) (cortex.LLM, cortex.Embedder, cortex.Extractor, error)` builds `llm/openai` or `llm/anthropic` from `chat`, and an OpenAI-compatible embedder from `embed` (its `EmbedModel`, `EmbedDims`, embed URL and key), plus the hybrid extractor, mirroring `cmd/cortex/main.go` `openCortex`; `hc` is the chat client and the embedder gets its own `SafeClient(allowPrivate, EmbedBodyCap)`. Callers outside the resolver pass the same row for both. The embedder wrapper returns an error when `len(vec) != embed.EmbedDims`.
- `func (st *Store) ResolveUser(ctx, root *cortex.Cortex, userID, connID string) (*cortex.Cortex, error)`: the one factory for a per-user handle: `GetSettings` for chat (`ErrNoProvider` on `ErrNoSettings` or a build error), `GetActiveEmbed` for the embedder, `Providers(chat, embed, …)`, then `root.ForUser(...).WithConnection(connID).WithGeneration(active)`. The MCP resolver (Task 9) and the jobs (Tasks 8, 13, 14) all use it.
- `func TestProvider(ctx, s Settings, hc *http.Client) error` makes one chat call and one embed call and checks `len(embedding) == EmbedDims`.
- `func MigrateBootstrap(ctx, st *Store, env map[string]string) error`: idempotent and one-shot. In one transaction: if `cortex_meta` has no `bootstrap_migrated` key, seal bootstrap settings from env `OPENAI_API_KEY` / `OPENAI_BASE_URL` / `LLM_MODEL` / `EMBEDDING_MODEL` (defaults per Global Constraints, dims 512, active embedder = same), and if `CORTEX_AUTH_TOKEN` is set, store its hash as agent token "claude-code (tunnel)"; then set the marker. Later runs return immediately, so a revoked or deleted tunnel token is never recreated. Fails if `CORTEX_AUTH_TOKEN` is set but `OPENAI_API_KEY` is not. In the same transaction, if `users.owner_entity_id` for `u_bootstrap` is NULL it is set to the existing `source='owner'` entity, else a new owner person entity is created (so `context` and `Profile(owner)` work for the bootstrap user).
- `ratelimit.go`: `NewLimiter(perMin, burst int, now func() time.Time) *Limiter` with `Allow(key string) bool`; `ClientIP(r *http.Request) string` (rule in Global Constraints).

- [ ] **Step 1: Write failing tests:**
  - `TestSealRoundTrip`, `TestSealWrongKeyFails`.
  - `TestSettingsMasked`: key `sk-abc…3f9a` masks to `••••3f9a`.
  - `TestSettingsValidate`: `http://` base URL rejected; `https://10.0.0.5/v1`, `https://127.0.0.1/v1` and `https://169.254.169.254/` rejected; `EmbedDims` 100 rejected, 1536 accepted; with `allowPrivate=true` `http://localhost:11434/v1` accepted.
  - `TestSafeClientBlocksAddresses` (table over `127.0.0.1`, `::1`, `10.1.2.3`, `192.168.0.1`, `172.16.0.1`, `169.254.169.254`, `100.64.0.1`, `fc00::1`, `0.0.0.0`, `::ffff:127.0.0.1`), asserted for both the chat and the embed URL via `TestProvider`; `TestSafeClientNoRedirects` (a server redirecting to a second server: second receives 0 requests); `TestSafeClientCapsBody` (2 MB response is cut at a 1 MB cap, while a 2.4 MB embed response passes with `EmbedBodyCap`); `TestSafeClientIgnoresProxyEnv` (`HTTPS_PROXY` pointing at a local server receives 0 connections and the dial check still rejects a private target); `TestProviderErrorTextFixed` (an upstream body `secret-upstream-body` and the target address never appear in the error text); `TestAllowPrivateLiftsRestrictions` (`AllowPrivate=true` reaches an httptest loopback server).
  - `TestTestProviderChecksDims`: embedder returns 8 floats while `EmbedDims=512` returns an error and nothing is saved.
  - `TestAgentTokenResolves`, `TestRevokedTokenFails`, `TestTokensStoredHashed` (plaintext in no `agent_tokens` column), `TestRevokeForeignConnectionNotFound`.
  - `TestDeletedAccountTokensRejected` (Review Focus 6): after `DeleteAccount`, an agent token and an OAuth access token for that user both fail `ResolveBearer`; connections show `RevokedAt`; sessions and tokens rows are gone.
  - `TestRestoreAccountHasNoConnections`: after `RestoreAccount`, `ListConnections` has no live entries and the old tokens still fail.
  - `TestOAuthConnectionPartialUnique`: two live oauth connections for one (user, client_id) fail; after revoke a second succeeds; two users with one client_id both succeed.
  - `TestMigrateBootstrapIdempotent`: run twice gives one settings row and one tunnel token, and the old `CORTEX_AUTH_TOKEN` value resolves to `u_bootstrap`. `TestMigrateBootstrapNeverResurrects`: revoke the tunnel token, run again, it stays revoked. `TestMigrateBootstrapNeedsKey`. `TestMigrateBootstrapSetsOwnerEntity`.
  - `TestUserDeleteCascadesAllTables`: for every table in `information_schema` with a `user_id` column, the FK to `users` is `ON DELETE CASCADE`; deleting a fully populated user (settings, connections, tokens, jobs, compactions, sessions, oauth rows, batches and changes) leaves 0 rows with that `user_id` anywhere.
  - `TestProvidersAnthropic`: `Provider:"anthropic"` returns the anthropic LLM type; `TestProvidersUseHTTPClient`.
  - `TestClientIPUsesXFFOnlyFromLoopback`: peer `10.0.0.9` with `X-Forwarded-For: 1.2.3.4` yields `10.0.0.9`; peer `127.0.0.1` with `X-Forwarded-For: 9.9.9.9, 1.2.3.4` yields `1.2.3.4`. `TestLimiterBlocksAfterBurst` with an injected clock, refilling after a minute.
- [ ] **Step 2: Run** `go test ./internal/accounts/ ./llm/...`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make test`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(accounts): encrypted settings, hardened provider client, connections, tokens`.

### Task 7: Web server, sessions, Google sign-in, AI settings page

**Files:** create `internal/web/{server,session,google}.go`, `internal/web/pages/{layout,signin,settings}.tmpl` and `settings.go`, `internal/web/static/app.css`, tests. Does not touch `cmd/cortex/main.go` (the `serve` command is Task 9).

**Interfaces:**
- `type Options struct{ Enqueue func(ctx context.Context, userID, kind string) (jobID string, err error); MCP http.Handler; Mounts []func(mux *http.ServeMux, d Deps); Now func() time.Time }`, defined here once. Later tasks plug in through it: Task 8 supplies `Enqueue`, Task 9 `MCP`, Tasks 10-13 add `Mounts`. A nil `Enqueue` makes settings changes that need a re-embed fail the request with a clear error (tests supply a recording stub).
- `type Config struct{ PublicURL, BootstrapEmail, GoogleClientID, GoogleClientSecret string; AllowPrivate bool; Identity IdentityProvider /* nil = Google from the ID/secret */; Logger *slog.Logger }`
- `type SessionUser struct{ UserID, CSRF string }`
- `type Deps struct{ Root *cortex.Cortex; Accounts *accounts.Store; Sessions *Sessions; Cfg Config; Now func() time.Time; Auth func(next func(w http.ResponseWriter, r *http.Request, u SessionUser)) http.HandlerFunc /* requires a live session, checks CSRF on POST */; Render func(w http.ResponseWriter, r *http.Request, name string, data any) /* layout + security headers */ }`. Tasks 10-13 mount handlers using only `Deps`.
- `func NewServer(cfg Config, root *cortex.Cortex, st *accounts.Store, opts Options) http.Handler`. A middleware on every response sets `Content-Security-Policy: frame-ancestors 'none'` and `X-Frame-Options: DENY`.
- Routes: `GET /` (sign-in, or redirect to /memories), `GET /auth/google/start`, `GET /auth/google/callback`, `POST /logout`, `GET|POST /settings`, `GET /healthz`.
- Sessions: `sessions(id_hash, user_id, csrf, created_at, expires_at)` (30 d), cookie `cortex_session` (Secure, HttpOnly, SameSite=Lax). The CSRF token is per session and required on every POST. **A new session ID is issued at every sign-in** (any old one for the browser is deleted). Session lookup rejects users with `deleted_at` set.
- Google leg: `login_attempts(id_hash, state, nonce, return_to, expires_at)` (10 min). `/auth/google/start` creates an attempt, sets a `__Host-cortex_login` cookie (Secure, HttpOnly, Lax, Path=/) holding the attempt ID, and sends `state`/`nonce` to Google. The callback requires the cookie, a matching `state`, the nonce in the ID token, and consumes the attempt (single use). `return_to` is stored server-side from `?next=` and accepted only if it is a same-origin path (starts with `/`, not `//`, no `\`, no scheme); otherwise `/`.
- `type IdentityProvider interface{ AuthURL(state, nonce string) string; Exchange(ctx, code, nonce string) (Identity, error) }` with `Identity{Sub, Email, Name string; EmailVerified bool}`. Tests use a fake.
- Callback logic:
  - an existing user with that sub signs in; **if the user is soft-deleted and within 30 days, `RestoreAccount` runs and a notice "your account was restored; reconnect your assistants" is shown**;
  - otherwise, if `EmailVerified && email == CORTEX_BOOTSTRAP_EMAIL` and the bootstrap user is unbound, `BindBootstrap`;
  - otherwise `CreateUser(..., Identity.EmailVerified)`.
  - A new user goes to `/settings`.
- The sign-in page shows the notice: "The operator of this service can technically access data stored here."
- `/settings` shows masked values. "Test & save" (rate-limited 10/min per user) calls `accounts.TestProvider` with `SafeClient` (chat and embed caps), then `PutSettings`. If `PutSettings` returns `needsReembed`, it calls `opts.Enqueue(ctx, userID, "reembed")` and shows the job's progress (`GET /settings/job/{id}`, own jobs only, foreign or absent ID is 404); a failed or never-started job shows its error and a Retry button (`POST /settings/reembed`, CSRF-protected, enqueues again if none is queued or running). On the first save, and whenever the saved dims have no index yet for this user, it also enqueues an `index` job.

- [ ] **Step 1: Write failing tests** (httptest TLS server with fake IdP):
  - `TestFirstSigninCreatesUserAndRedirectsToSettings`
  - `TestBootstrapBindsOnVerifiedEmail`
  - `TestUnverifiedBootstrapEmailCreatesNewUser` (Review Focus 2)
  - `TestCallbackRejectsStateMismatch`, `TestCallbackRejectsNonceMismatch`, `TestCallbackRejectsMissingLoginCookie`, `TestLoginAttemptSingleUse`
  - `TestReturnToMustBeSameOrigin`: `next=https://evil.example`, `next=//evil.example`, `next=/\evil` all land on `/`; `next=/connections` lands on `/connections`.
  - `TestSigninRotatesSessionID`: a pre-existing `cortex_session` value is invalid after sign-in and the new cookie differs.
  - `TestDeletedUserSessionRejected`; `TestSigninRestoresDeletedAccountWithNotice`.
  - `TestPostWithoutCSRFRejected` (403).
  - `TestSecurityHeadersOnAllPages`: `/`, `/settings`, a 404 and a 403 all carry `frame-ancestors 'none'` and `X-Frame-Options: DENY`.
  - `TestPutSettingsPromotesWhenNothingToReembed`: a user with no data changing model/dims, and a user with data changing only the embed key, both get the new active embedder immediately with `needsReembed=false`; a user with data changing the model gets `needsReembed=true` and the old active embedder.: a failing stub shows the fixed error text and saves nothing. `TestSettingsRejectsPrivateURL`. `TestSettingsProviderTestRateLimited` (11th test in a minute returns 429).
  - `TestSettingsEmbedChangeEnqueuesReembed`: the stub `Enqueue` is called once with kind `reembed`; a chat-model-only change enqueues nothing. `TestFirstSaveEnqueuesIndex` and `TestDimsChangeEnqueuesIndex`: kind `index` is enqueued on the first save and on a new dims value, not on a chat-only change. `TestFailedReembedShowsRetry`: a failed job shows the error and the Retry POST enqueues once.
  - `TestSettingsShowsMaskedKey`, `TestSigninPageHasOperatorNotice`
  - `TestJobProgressOwnOnly`: b's job ID as a returns 404, same as an absent ID.
- [ ] **Step 2: Run** `go test ./internal/web/...`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make test`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(web): Google sign-in, sessions and AI provider settings`.

### Task 8: Background jobs, index builds and re-embed

**Files:** create `internal/jobs/{pool,index,reembed}.go` and tests. The `jobs` table is in `003_accounts.sql` (Task 6).

**Interfaces:**
- `type Job struct{ ID, UserID, Kind string }`; `func NewPool(n int, run func(ctx context.Context, j Job) error) *Pool` with `Enqueue(ctx, userID, kind string) (jobID string, err error)` (this is the `web.Options.Enqueue`). `Start`/`Stop`. Jobs for a user with `deleted_at` set are skipped and marked `skipped`. The pool starts 2 workers in `cortex serve` (Task 9).
- `func EnsureIndexes(ctx, cx *cortex.Cortex, dims int) error`: calls `EnsureVectorIndex` for `entity`, `memory`, `chunk` (Task 3). Job kind `index`: the pool's run function resolves the user's active embed dims (`GetActiveEmbed`) and calls `EnsureIndexes`. It is enqueued on a user's first settings save and whenever the saved dims differ from every existing index for that user (Task 7); `ReEmbed` also runs it for the target dims first.
- `func ReEmbed(ctx, cx *cortex.Cortex, st *accounts.Store, target cortex.Embedder, report func(done, total int)) error`:
  - runs `EnsureIndexes` for the target dims first;
  - writes generation `active+1` for **every** memory, chunk and entity **including soft-deleted rows**, in batches of 64, skipping rows that already have the new generation (resumable);
  - flip: outside the lock, count rows still lacking the new generation and loop while more than 64 remain; then call `cx.FlipGeneration(ctx, active+1, embedTail, inTx)` (Task 3), where `embedTail` embeds the remaining rows (at most about 64, so the embedder call under the lock is short) and `inTx` calls `st.PromoteActiveEmbedTx(ctx, tx, userID)` (the new embedder settings become the active ones). If the tail turns out larger than 64 (a burst of writes), `embedTail` returns a retry sentinel, the flip rolls back and the loop continues.
  - A failed job keeps the old generation active; `/settings` shows the failure with a Retry button (Task 7) and the nightly phase 1 re-enqueues it (Task 14).
- **The resolver's embedder (Task 9) comes from `GetActiveEmbed`, not from the desired settings, until the flip commits.** So queries always embed with the model that produced the active generation's vectors.
- Writers made through a resolver handle carry `WithGeneration(active)`; a write racing the flip gets `ErrStaleGeneration` (Task 3) and the MCP tool returns "the embedding model just changed; retry".

- [ ] **Step 1: Write failing tests:**
  - `TestReEmbedSwitchesGeneration`: with the new embedder, search finds results only after completion.
  - `TestReEmbedResumes`: the first run fails mid-way; the rerun completes with exactly one row per item per generation.
  - `TestRecallDuringReEmbedUsesOldModel` (Review Focus 4): pause `ReEmbed` after half the rows (hook); settings already hold the new model; a resolver-built handle's `Recall` queries with the old embedder (the stub counts calls per model: new-model embed calls from recall = 0) and returns the expected old-generation results; after the flip it uses the new one.
  - `TestWriteBetweenScanAndFlipSurvives`: a memory written (old generation, through the handle) after the last batch but before the flip is found by vector search after the flip with the new embedder.
  - `TestWriteAfterFlipWithStaleHandleRejected`: a handle created before the flip gets `ErrStaleGeneration`; no row written.
  - `TestReEmbedIncludesSoftDeleted`: delete a memory, re-embed, restore the batch; vector search finds it with the new model.
  - `TestReEmbedFlipExcludesWriters`: while the flip holds the lock, a writer blocks and then writes against the new generation or fails stale, never lands an old-model vector in the new generation (asserted by dims/stub-model tag).
  - `TestIndexJobBuildsForActiveDims`: an `index` job for a user with 16-dim active settings creates `emb_hnsw_*_16`.
  - `TestPoolRunsTwoConcurrent`; `TestPoolSkipsDeletedUser`; `TestEnsureIndexesCreatesForDims`.
  - `TestIsolationReEmbed`: re-embedding as a leaves b's generation, active pointer and vectors unchanged.
- [ ] **Step 2: Run.** Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make test`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(jobs): worker pool, index builds and lock-safe resumable re-embed`.

### Task 9: MCP per-user resolution, `serve` startup, legacy transport, `context` tool

**Files:** modify `cmd/cortex/mcp/{server,serve_http,auth,tools}.go` and tests, `cmd/cortex/main.go` (new `serve` command), `cmd/cortex/mcp_subcmd.go`; mount in `internal/web/server.go` via `Options.MCP`.

**Interfaces:**
- `type Resolver func(ctx context.Context, bearer string) (cx *cortex.Cortex, err error)`. The production resolver:
  1. `accounts.ResolveBearer` → (user, conn);
  2. (inside `ResolveUser`) on `ErrNoSettings` or a provider build error, return `ErrNoProvider`;
  3. `st.ResolveUser(ctx, root, user, conn)` (Task 6): chat settings from `GetSettings`, the **embedder from `GetActiveEmbed`** (Task 8), `.WithConnection(conn).WithGeneration(active)`.
- `func NewHTTPHandler(s *server.MCPServer, resolve Resolver, publicURL string, failLimiter *accounts.Limiter) http.Handler`:
  - uses `server.WithHTTPContextFunc` to put the resolved handle in ctx;
  - on a missing or unknown token returns 401 with `WWW-Authenticate: Bearer resource_metadata="<publicURL>/.well-known/oauth-protected-resource"`, and counts it against the per-IP failed-lookup limit (429 when exceeded).
- `RegisterTools(s)` handlers call `cortexFrom(ctx)`. `ErrNoProvider` becomes a tool error with text `set up your AI provider at <publicURL>/settings`.
- **`cortex serve --addr 127.0.0.1:8080`** (serves pages, OAuth and `/mcp` on one listener). Startup order, fixed: (1) require `CORTEX_SECRET_KEY` (and `DATABASE_URL`/DSN); exit non-zero with a clear message if missing; (2) `cortex.Open` (applies migrations; the explicit `cortex migrate` of Task 14 does the same with a long timeout); (3) `accounts.MigrateBootstrap(ctx, st, os.Environ map)`; (4) start the job pool; (5) listen. It reads `GOOGLE_OAUTH_CLIENT_ID`, `GOOGLE_OAUTH_CLIENT_SECRET`, `CORTEX_PUBLIC_URL` (default `https://cortex.sausheong.dev`), `CORTEX_SECRET_KEY`, `CORTEX_BOOTSTRAP_EMAIL`, `CORTEX_ALLOW_PRIVATE_PROVIDERS`. The static `CORTEX_AUTH_TOKEN` comparison is gone from `serve`; the token only seeds the bootstrap agent token.
- **Legacy `cortex mcp --transport http`** (`buildHTTPMux`/`bearerAuthMiddleware` stay, bound to the bootstrap user): `serveHTTP` returns an error and exits non-zero when `CORTEX_AUTH_TOKEN` is empty, regardless of bind address; the empty-token branch in `buildHTTPMux` is removed. stdio is unchanged.
- New tool `context`, with arg `question string`. It returns JSON `{profile: {static, dynamic}, memories: [...], entities: [...], abstain: bool}`, built from `Profile(owner)` (the user's `owner_entity_id`) plus `RecallWithStrength(question)` (top 8).
- Descriptions:
  - `context`: "Call at the start of every conversation and before answering anything that depends on the user's preferences, how they would do things, or people and things they know. Returns who the user is plus relevant memories."
  - `remember`: "Call whenever the conversation reveals something new about the user: a preference, a connection (person or organisation), a capability, background, or an identity fact. Don't store small talk or secrets."
  - Other descriptions keep their meaning. `forget` and `merge` mention 30-day recovery.

- [ ] **Step 1: Write failing tests:**
  - `TestMCPUnauthorizedHasResourceMetadata`; `TestMCPFailedLookupsRateLimited` (31st bad token from one IP gets 429; a good token from another IP is unaffected).
  - `TestMCPAgentTokenScopesToUser`: two users' tokens each `recall` only their own data.
  - `TestMCPNoSettingsNeverUsesOperatorKey` (Review Focus 1): set `OPENAI_API_KEY` in env, give a user no settings. The call returns the settings URL error, and the stub operator provider counter is 0.
  - `TestMCPDeletedUserToken401`.
  - `TestContextToolShape`; `TestRememberViaMCPRecordsConnection`.
  - **MCP isolation table** `TestMCPIsolationForeignIDs`: for each tool taking an ID or name (`get_entity`, `find_entities`, `get_relationships`, `traverse`, `search`, `forget`, `merge`, `lint`, `profile`, `remember` with a foreign entity ID, relate), call as user A with B's IDs; the result text equals the absent-ID result byte for byte, and B's rows are unchanged.
  - `TestServeStartupOrder` (serve-level, `serve_test.go` in `cmd/cortex`, using an in-process `runServe(ctx, env)` that returns the handler): with env `CORTEX_AUTH_TOKEN=tunnel-tok`, `OPENAI_API_KEY=k` and a stub provider, an empty DB, `/mcp` with `Authorization: Bearer tunnel-tok` returns 200 and a `recall` as `u_bootstrap` sees seeded data (Review Focus 9). `TestServeFailsWithoutSecretKey`: missing `CORTEX_SECRET_KEY` returns an error before any listener exists. `TestServeBootstrapRunsOnce` (second start does not recreate a revoked token).
  - `TestLegacyHTTPRequiresToken`: `cortex mcp --transport http` with an empty `CORTEX_AUTH_TOKEN` returns an error on a loopback and a non-loopback address; with a token, `/mcp` without it is 401.
- [ ] **Step 2: Run** `go test ./cmd/cortex/...`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make test`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(mcp): per-user token resolution, serve startup order, context tool`.

### Task 10: OAuth 2.1 authorization server

**Files:** create `internal/web/oauth/{metadata,register,cimd,authorize,token,store}.go`, `pages/consent.tmpl`, tests. Mounted through `web.Options.Mounts`.

**Interfaces:**
- Tables (in `003_accounts.sql`): `oauth_clients(id, kind /* 'dcr'|'cimd' */, name, redirect_uris JSONB, cimd_host, created_at, authorized_at, expires_at)`; `authorize_requests(id, user_id, client_id, redirect_uri, code_challenge, state, scope, created_at, expires_at)` (10 min); `oauth_codes(code_hash, request_fields, user_id, connection_id, expires_at, used_at)`; `oauth_tokens(token_hash, kind /* 'access'|'refresh' */, grant_id, connection_id, user_id, expires_at, last_used_at, revoked_at)`.
- Endpoints (issuer = `CORTEX_PUBLIC_URL`):
  - `GET /.well-known/oauth-protected-resource`: `{resource: <public>/mcp, authorization_servers: [<public>], scopes_supported: ["cortex","offline_access"]}`.
  - `GET /.well-known/oauth-authorization-server`, with `code_challenge_methods_supported: ["S256"]`, `client_id_metadata_document_supported: true` and the registration endpoint.
  - `POST /oauth/register` (DCR, RFC 7591): body capped at 64 KB; redirect URIs must be `https`, or `http` to `localhost`/`127.0.0.1`/`[::1]`; any other scheme returns `invalid_redirect_uri`.
  - `GET /oauth/authorize`: requires a session (else it redirects to Google sign-in with a same-origin `next`). Validates client, `redirect_uri` (exact match and the scheme rule again), `code_challenge` (S256 only), `state`, `scope ⊆ {cortex, offline_access}`; **persists the validated request in `authorize_requests`** and **always** renders consent, including on re-authorization. Consent shows the client name, the redirect host, the CIMD host for CIMD clients, an "unverified" label for DCR clients, and the three permission lines from the wireframe.
  - `POST /oauth/authorize`: takes `request_id` and the CSRF token only (all other parameters come from the stored row, which must belong to the session user, be unexpired and single-use); Allow or Deny.
  - `POST /oauth/token`: `authorization_code` (PKCE verify; one-time; 60 s TTL) and `refresh_token` (rotate; reusing an old refresh token revokes the whole grant; refresh fails for revoked connections and for deleted users).
- **CIMD:** a `client_id` that is an https URL is fetched with `accounts.SafeClient(false, CIMDBodyCap)`, 5 s timeout, 64 KB cap, cache ≤ 24 h; the document's `client_id` must equal the URL; its redirect URIs obey the same scheme rule.
- **Connection:** one live per (user, client_id) (partial unique index, Task 6). Reauthorization reuses it. Revoke sets `revoked_at` and invalidates all tokens; a later authorization creates a new connection.
- Rate limits per Global Constraints on `/oauth/register`, `/oauth/token` and `/oauth/authorize`. Clock is injected (`Options.Now`).

- [ ] **Step 1: Write failing tests:**
  - `TestMetadataDocuments`
  - `TestAuthorizeRequiresPKCES256`: `plain` is rejected.
  - `TestRedirectURIExactMatch`
  - `TestRegisterRejectsBadRedirectSchemes`: `javascript:`, `myapp://cb`, `http://evil.example/cb` rejected; `https://x.example/cb`, `http://localhost:8123/cb`, `http://127.0.0.1/cb` accepted. `TestRegisterBodyCapped` (65 KB body returns 413).
  - `TestConsentShowsHostsAndUnverified`: the consent HTML for a DCR client named "ChatGPT" with redirect `https://evil.example/cb` contains `evil.example` and "unverified"; for a CIMD client it contains the CIMD host and no "unverified".
  - `TestConsentAlwaysShownOnReauthorize`: a second authorization for an already-connected client returns the consent page, not a redirect with a code.
  - `TestConsentPostUsesStoredRequest`: a POST that tampers with `redirect_uri`/`client_id` fields is ignored (the stored values are used); a request ID created by user B and posted by A returns 404; a replayed or expired request ID fails.
  - `TestCodeExchangeAndMCPCall`: full flow with fake IdP. The access token works on `/mcp`.
  - `TestCodeSingleUse`; `TestCodeExpiresAfter60s`; `TestAccessTokenExpiresAfter1h`; `TestRefreshExpiresAfter90DaysIdle` (injected clock; a refresh at day 89 rotates and extends, at day 91 idle fails).
  - `TestRefreshRotationAndReuseRevokes`
  - `TestRefreshFailsForDeletedUser` and `TestAuthorizeFailsForDeletedUser` (Review Focus 6).
  - `TestCIMDRejectsPrivateIP`: a `client_id` resolving to 127.0.0.1 is rejected, even with `CORTEX_ALLOW_PRIVATE_PROVIDERS=1`. `TestCIMDCapsAndTimesOut`.
  - `TestReauthorizeReusesConnection`, `TestReconnectAfterRevokeNewConnection` (Review Focus 5).
  - `TestExpiredUnauthorizedDCRClientRejected`: a never-authorized DCR client past `expires_at` (24 h, injected clock) is rejected at authorize and token even if not yet purged. The physical purge runs in the nightly phase (Task 14), so the row may linger up to about 48 h but is never usable.
  - `TestOAuthEndpointsRateLimited`: the 31st request/min per IP to `/oauth/token`, `/oauth/register` and `/oauth/authorize` gets 429, and varying `X-Forwarded-For` from a non-loopback peer does not bypass it.
  - `TestTokensNotLogged`: capture the logger; no token string appears.
- [ ] **Step 2: Run** `go test ./internal/web/oauth/`. Expected: FAIL.
- [ ] **Step 3: Implement.** A hand-rolled implementation over `internal/accounts` is the expected default. A library is allowed if it meets every test.
- [ ] **Step 4: Run** `make test`, plus the MCP Inspector auth flow against a local `cortex serve` (record the result in the commit body).
- [ ] **Step 5: Commit** `feat(oauth): MCP authorization server for assistants`.

### Task 11: Connections page

**Files:** create `pages/connections.tmpl` and `connections.go`, tests. Mounted through `web.Options.Mounts`.

**Interfaces:**
- `GET /connections` lists assistants and agent tokens with name, last used, written count and a Revoke button. It shows the recommended custom-instruction text (below) with a copy button.
- `POST /connections/{id}/revoke` takes optional `remove=1`, which calls `SoftDeleteByConnection`. A foreign or absent `{id}` returns 404.
- `POST /connections/tokens` with `name` shows the plaintext once.
- The custom-instruction copy, verbatim: "You have access to my cortex memory. Before answering anything that depends on my preferences, how I'd do things, or people and things I know, call the cortex `context` tool. Whenever you learn something new about me — a preference, a person or organisation I know, a capability, my background — save it with cortex `remember`."

- [ ] **Step 1: Write failing tests:**
  - `TestConnectionsListsOwnOnly`
  - `TestRevokeAndRemoveSoftDeletes`
  - `TestRevokeForeignConnectionIs404`: as a, `POST /connections/<b's id>/revoke` returns 404 identical to an absent ID, and b's connection and tokens still work.
  - `TestNewTokenShownOnce`: a second page load does not show it.
  - `TestInstructionTextPresent`
- [ ] **Step 2: Run.** Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make test`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(web): connections page with revoke and agent tokens`.

### Task 12: Memories page, storage figures, account deletion

**Files:** create `usage.go`, `pages/memories.tmpl`, `memories.go`, `pages/account.tmpl`, tests.

**Interfaces:**
- `func (c *Cortex) Usage(ctx) (Usage, error)`, where `Usage{Bytes int64; Memories, Entities, Chunks, Deleted int}`. Bytes is the sum of `pg_column_size` over the user's rows, including embeddings (all generations). It is cached in `users.usage_json` by the nightly job, and computed live if the cache is missing.
- `GET /memories?q=&by=&from=&to=&page=` shows 50 per page.
- `POST /memories/delete` takes ids, `by=<connID>`, `older_than=<days>`, `match=<q>` or `all=1`. `all` requires the typed confirmation `delete my brain`.
- `GET /memories/deleted` lists batches with restore. `POST /memories/restore/{batch}` (a foreign or absent batch returns 404).
- `POST /account/delete` calls `accounts.DeleteAccount` (Task 6): access is cut at once (connections and tokens revoked, sessions deleted) and the browser is signed out. A sign-in within 30 days restores the account (no connections). The nightly purge phase (Task 14) removes the user row (cascade) after 30 days.

- [ ] **Step 1: Write failing tests:**
  - `TestMemoriesPageFiltersByConnection`
  - `TestBulkDeleteOlderThan`
  - `TestClearBrainNeedsConfirmation`
  - `TestRestoreBatchFromPage`
  - `TestRestoreForeignBatchIs404`: as a, `POST /memories/restore/<b's batch>` returns 404 same as an absent batch; b's rows stay deleted.
  - `TestMemoryDeleteForeignIDsIgnored`: `POST /memories/delete` with b's memory IDs as a deletes 0 rows (b's rows live).
  - `TestUsageCountsOwnRowsOnly` (also: after `PurgeDeleted`, `Usage` no longer counts the purged rows and their embeddings)
  - `TestAccountDeleteCutsAccessImmediately`: after `POST /account/delete`, the user's agent token gets 401 on `/mcp`, the session cookie gets a redirect to sign-in, and a refresh-token exchange fails (Review Focus 6).
  - `TestAccountDeleteThenSigninRestores`: data visible again, `/connections` empty, old tokens still 401.
- [ ] **Step 2: Run.** Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make test`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(web): memories page, bulk delete, restore, account deletion`.

### Task 13: Compaction with preview and undo

**Files:** create `compaction.go`, `compaction_test.go`, `internal/jobs/compact.go`, `pages/compaction.tmpl` and `compaction.go`.

**Interfaces:**
- `compactions(id, user_id, status /* proposing|ready|applied|undone|failed */, proposal_json, batch_id, created_at)` (in `003_accounts.sql`).
- `type Change struct{ Kind /* "merge"|"combine"|"rollup" */ string; SourceIDs []string; Result string; Static bool }`
- `func (c *Cortex) ProposeCompaction(ctx) ([]Change, error)` uses the user's LLM. Candidates come from memory-embedding similarity (merge/combine, cosine ≥ 0.9 for merge) and from non-static memories older than 90 days grouped by entity (rollup). Static memories are never part of a rollup. Only the active generation is read.
- `func (c *Cortex) ApplyCompaction(ctx, changes []Change) (batchID string, err error)` writes the results with a `derives` edge to each source, and soft-deletes the sources with `by="compaction"` in one batch. Every `SourceIDs` entry must belong to the user, else `ErrNotFound` and nothing is written.
- `func (c *Cortex) UndoCompaction(ctx, batchID string) error` calls `RestoreBatch` and soft-deletes the result memories.
- Pages: `POST /memories/compact` enqueues; `GET /compaction/{id}` shows the review list with checkboxes; `POST /compaction/{id}/apply` takes the selected **indexes only**; **the server builds the changes from the stored `proposal_json`, never from request fields**; `POST /compaction/{id}/undo`. A foreign or absent compaction ID returns 404.

- [ ] **Step 1: Write failing tests** with a stub LLM:
  - `TestProposeMergesDuplicates`
  - `TestRollupExcludesStatic`
  - `TestApplyReducesCountAndLinksDerives`
  - `TestUndoRestoresExactly`: the set of live memory IDs and contents equals the pre-apply snapshot.
  - `TestApplyOnlySelected`
  - `TestApplyIgnoresPostedChangeBodies`: a POST carrying extra `source_ids`/`result` fields (with b's memory IDs) applies only the stored proposal.
  - `TestIsolationCompaction`: as a, `ApplyCompaction` with b's IDs returns `ErrNotFound` and writes nothing; `GET`/`apply`/`undo` on b's compaction ID is 404 (same as absent); `UndoCompaction` with b's batch is `ErrNotFound`.
- [ ] **Step 2: Run.** Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make test`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: memory compaction with preview and undo`.

### Task 14: Nightly per-user maintenance, admin CLI, migrate command, deploy and rollback scripts

**Files:** modify `cmd/cortex/main.go` (`maintain` iterates users; `--user` on admin commands; `users list`; new `migrate` command with `--timeout`, default 30m), `cmd/cortex/import_sqlite.go`, `cmd/cortex/maintain.go`, `internal/jobs/nightly.go`, `scripts/setup-server.sh`, `scripts/deploy-app.sh`; create `scripts/rollback-app.sh`; modify `docs/deploy.md`, `README.md`.

**Interfaces:**
- `cortex maintain` with no `--user` runs two separate phases:
  1. **Maintenance** for every live user (`deleted_at IS NULL`) *with valid settings*: `Maintain` with that user's providers, the `Usage` cache. Users without settings and deleted users are skipped. Phase 1 builds each user's handle with `accounts.Store.ResolveUser` (chat settings from the current row, embedder from `GetActiveEmbed`, `WithGeneration(active)`), so a pending re-embed never makes it use the new model. If a user's desired embed settings differ from the active ones and no `reembed` job is queued or running, phase 1 enqueues one (this retries failed or never-started re-embeds). Per-user errors are stored in `users.last_maintenance_error` and shown on `/settings`; the run continues.
  2. **Purge**, independent of settings and of phase 1's failures, over **all users including settings-less and deleted ones**: `PurgeDeleted(30d)` per user, the hard delete of users with `deleted_at` older than 30 days (cascade), and expired DCR clients, `authorize_requests`, `oauth_codes`, `login_attempts`, sessions.
- `--user <id|email>` applies to `maintain`, `recall`, `profile`, `import-sqlite` and `sync`, via `ResolveUserRef` (an ID, or a *verified* email only; ambiguity refuses and lists IDs; no match is an error). `import-sqlite --user` passes the resolved ID to the importer. `cortex users list` prints ID, email (and a verified flag), created, usage.
- `cortex migrate [--timeout 30m]`: `Open` with `WithMigrationTimeout`, prints applied versions and elapsed time.
- `setup-server.sh --app-host`:
  - generates `CORTEX_SECRET_KEY` once (preserved on re-run), sets `CORTEX_PUBLIC_URL` and `CORTEX_BOOTSTRAP_EMAIL=sausheong@gmail.com`;
  - writes the service unit (`ExecStart=… serve`) and the maintain timer (plain `cortex maintain`) **without restarting the service**, after saving the previous unit as `cortex.service.prev`.
- `deploy-app.sh` also forwards `GOOGLE_OAUTH_CLIENT_ID`, `GOOGLE_OAUTH_CLIENT_SECRET` and `CORTEX_BOOTSTRAP_EMAIL`, sourcing the repo `.env` if present, never echoing them. It does the single restart: stop service → final `pg_dump -Fc` to `/root/cortex/pre-multiuser.dump` (taken after the stop so rollback loses no writes; abort if it fails) → copy the installed binary to `cortex.prev` → install the new binary → `cortex migrate --timeout 30m` → `daemon-reload` and start → poll `/healthz`; on a failed migrate or health check it exits non-zero **without** starting the new binary and prints the rollback command.
- `rollback-app.sh`: stop the service; restore `cortex.prev` and `cortex.service.prev`; `pg_restore --clean --if-exists` from the dump path given (default `/root/cortex/pre-multiuser.dump`); daemon-reload; start; poll `/healthz`.
- `docs/deploy.md`: the Google OAuth client steps (redirect URI `https://cortex.sausheong.dev/auth/google/callback`); the rollout and rollback runbook (Task 15); connecting ChatGPT (Plugins → + → Add custom MCP server → `https://cortex.sausheong.dev/mcp`, OAuth) and claude.ai (custom connector); the custom-instruction text; `CORTEX_ALLOW_PRIVATE_PROVIDERS` (operator only) and the provider URL policy; backing up `CORTEX_SECRET_KEY` off the box.

- [ ] **Step 1: Write failing tests:**
  - `TestNightlyIsolatesFailures`: user A's provider errors, user B is still maintained, and A's error is recorded.
  - `TestNightlySkipsDeletedAndSettingslessUsers`: neither calls the stub provider.
  - `TestNightlyPurgesSettingslessDeletedUser`: a user with no settings and `deleted_at` 31 days back is hard-deleted with all rows by phase 2; a 29-day one survives.
  - `TestNightlyPurgeRunsEvenIfMaintenanceFails`.
  - `TestNightlyPurges` (soft-deleted rows 31 days old, expired unauthorized DCR clients).
  - `TestAccountPurgedAfter30Days`: a user deleted via `DeleteAccount` 31 days ago is hard-deleted by phase 2 with all rows; at 29 days the account is restorable.
  - `TestNightlyMaintainUsesActiveEmbedder`: with a re-embed pending (desired model differs from active), `Maintain` embeds with the active embedder (stub counts) and writes through `WithGeneration(active)`; users built via `ResolveUser`.
  - `TestUsersListCommand` (shows IDs); `TestUserFlagByIDAndVerifiedEmail`; `TestUserFlagAmbiguousRefusesListsIDs`; `TestUserFlagUnverifiedEmailRejected`; `TestImportSqliteUserFlag` (rows land under the resolved user only).
  - `TestMigrateCommandReportsVersions`.
  - Container smoke tests (`make test-deploy`): `setup-server.sh` preserves `CORTEX_SECRET_KEY` across re-runs; the unit runs `cortex serve`; **`setup-server.sh` does not restart the service** (service start time unchanged); `deploy-app.sh` with a failing migrate leaves the old binary running and exits non-zero; `rollback-app.sh` restores `cortex.prev`, the old unit and the dump.
- [ ] **Step 2: Run** `make test && make test-deploy`. Expected: FAIL.
- [ ] **Step 3: Implement.**
- [ ] **Step 4: Run** `make test && make test-deploy && shellcheck scripts/*.sh`. Expected: PASS.
- [ ] **Step 5: Commit** `feat: per-user nightly maintenance, admin CLI, migrate, rollout and rollback scripts`.

### Task 15: Rehearsal, live rollout, benchmark and acceptance (needs the owner for steps 6-8)

A tunnel outage of about 1 minute during the restart is accepted (owner decision 7).

- [ ] **Step 1: Backup.** On the droplet: `pg_dump -Fc` to `/root/cortex/pre-multiuser.dump` and copy it to the Mac (`scp`) as an early off-box copy; verify it with `pg_restore --list`. Also copy `/etc/cortex/cortex.env` (it holds `CORTEX_SECRET_KEY`; without it every sealed provider key is unrecoverable) to the Mac, `chmod 600`, kept apart from the dump (e.g. a password manager). `deploy-app.sh` overwrites the dump with a final one after stopping the service (so rollback loses no writes) and keeps the old binary as `cortex.prev`; `scp` that final dump to the Mac after the rollout.
- [ ] **Step 2: Rehearse migration 002.** Restore the dump into a scratch database (on the droplet, or a local pgvector 17 container), run `cortex migrate --timeout 30m` against it using the new binary, and record the duration and row counts in the PR notes. Check: 512-dim vectors answer `SearchVector`, `emb_hnsw_*_512` exist. The accepted outage is about 1 minute (decision 7), because `deploy-app.sh` stops the service before migrating and the old binary cannot run on the new schema. If the rehearsed migrate takes more than about 2 minutes, stop and agree a longer outage with the owner before going live.
- [ ] **Step 3: Benchmark.** Add `bench_test.go` behind `//go:build bench`: 2 users × 100K 512-dim memories (generated vectors); report vector recall p50/p95 for user A alone and with B loaded (the spec's isolation-latency record). Run `go test -tags bench -run xxx -bench . .` locally and again on the droplet scratch DB; record results in `docs/deploy.md`.
- [ ] **Step 4: Rollout.** Run `setup-server.sh --app-host cortex.sausheong.dev` (writes units, no restart), then `deploy-app.sh root@cortex.sausheong.dev` (single restart, explicit migrate). Expected: migrations 002/003 applied; `cortex users list` shows `u_bootstrap` with its existing usage. If anything fails, run `rollback-app.sh`.
- [ ] **Step 5:** From the Mac via the tunnel, check that the Claude Code `cortex` MCP still answers `recall` with the unchanged token. This is Acceptance 2's token half.
- [ ] **Step 6 (owner):** sign in at `https://cortex.sausheong.dev` with `sausheong@gmail.com` (off the filtering network), and confirm the existing memories appear.
- [ ] **Step 7 (owner):** add cortex in ChatGPT and in claude.ai, paste the custom instructions, ask a preference question (it should call `context`), and state a new preference. It should appear on /memories as written by ChatGPT or Claude. Confirm consent showed the client name, redirect host and an "unverified" label where it applies.
- [ ] **Step 8:** Second-account check: a second Google account sees an empty brain and gets the "set up your AI provider" error before configuring a key, then **saves its own provider key** (Test & save passes against a real provider), runs a `remember` and `recall` through its own agent token, and sees nothing of the owner's data. Record `free -h` and recall p95 on the droplet (< 500 ms excluding API time).
