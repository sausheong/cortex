# Postgres Storage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace cortex's embedded SQLite store with Postgres + pgvector, with no change in behaviour, plus a SQLite importer and a portable server setup (DigitalOcean droplet or Raspberry Pi).

**Architecture:** `cortex.Open(dsn)` connects through pgx's `database/sql` adapter. A thin wrapper rewrites `?` placeholders to `$n`, so most existing SQL strings survive. Schema comes from embedded, versioned migrations; the dimension-dependent vector DDL runs once on first `Open`. FTS5 is replaced by generated `tsvector` columns, and brute-force cosine by per-`ref_type` partial HNSW indexes on `halfvec(N)`.

**Tech Stack:** Go 1.25.1, `github.com/jackc/pgx/v5` (stdlib), Postgres 17, pgvector ≥ 0.8.0, `modernc.org/sqlite` (importer only), Docker `pgvector/pgvector:pg17` (tests only).

**Spec:** `docs/superpowers/specs/2026-10-22-postgres-storage-design.md`

## Global Constraints

- Postgres only. No SQLite backend and no storage abstraction. `modernc.org/sqlite` may be imported **only** by the importer package.
- pgvector ≥ 0.8.0; `Open` errors if older.
- Vectors are `halfvec(N)`, with one partial HNSW index (`halfvec_cosine_ops`) per `ref_type` (`entity`, `memory`, `chunk`); `SET hnsw.iterative_scan = relaxed_order` on query sessions.
- Default embedding dims: 512 for OpenAI `text-embedding-3-*` (sent via the API `dimensions` param). `EMBEDDING_DIMS` overrides it. 512 is used when no embedder is configured.
- Text-search config `'english'`. Keyword queries OR-join per-token `plainto_tsquery`. Never use `websearch_to_tsquery`.
- Env var `CORTEX_DATABASE_URL` replaces `CORTEX_DB`; `--db` takes a DSN; no default (clear error if unset).
- `Open` connect timeout: 5 s; the error names host:port.
- Timestamps are `TIMESTAMPTZ`. Naive SQLite timestamps import as UTC.
- Postgres access on the droplet is direct over the public internet (owner's decision, 2026-10-22), TLS-only with scram-sha-256 and a firewall; see Task 11. No Tailscale.
- Deploy targets: Debian/Ubuntu, amd64 or arm64. No Docker on deploy targets.
- Commits: no AI attribution (no Co-Authored-By, no "Generated with").
- The Postgres version ships as `v0.5.0`; `v0.4.0` is the last SQLite release.

## Review Focus

1. **Entity name filter case**: SQLite `LIKE` is case-insensitive for ASCII, Postgres `LIKE` is not. `FindEntities(NameLike: "ali%")` must still match "Alice". Test in Task 3.
2. **Odd keyword queries**: stopword-only (`"the"`), punctuation (`"C++"`, `"\"quoted\""`), empty. These must not raise SQL errors; stopword-only returns no keyword hits. Test in Task 4.
3. **Concurrent writers** (MCP + CLI now share one server): two concurrent `PutEntity` calls with the same name and type must not create duplicates. A unique index on `(name, type)` plus `ON CONFLICT DO UPDATE` handles this; the existing brain has 0 duplicates, so the index is safe. Test in Task 3.
4. **Huge chunks**: Postgres errors when a tsvector exceeds 1 MB, and the real brain has a 332K-char chunk. The generated column indexes `left(content, 200000)`; storing a 1M-char chunk must succeed. Test in Task 4.
5. **Non-UTC times round-trip**: a `ValidAt` in UTC+8 must come back as the same instant, and `as_of` queries must honour it. Test in Task 5.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/pgtest/pgtest.go` (new) | Per-test isolated schema DSN. Imports nothing from cortex, so package `cortex` tests can use it without an import cycle |
| `db.go` (new) | `dbConn`/`dbTx` wrappers that rebind `?`→`$n`; `rebind` func |
| `migrate.go` (new), `migrations/*.sql` (new, embedded) | Versioned migrations, `schema_migrations`, `cortex_meta` |
| `vector.go` (new) | Vector DDL on first Open, dims check, pgvector version check |
| `store.go` | `Open(dsn)`, remove SQLite schema/`ensureColumn` |
| `entity.go`, `relationship.go`, `traverse.go`, `memory.go`, `chunk.go`, `cortex.go`, `search.go`, `decay.go`, `expire.go`, `reconcile.go`, `lint.go`, `memory_edge.go`, `temporal.go` | SQL dialect port |
| `llm/openai/embedder.go` | Send `Dimensions` |
| `cmd/cortex/main.go`, `cmd/cortex-eval/setup.go`, `cmd/cortex/mcp/*` | DSN config |
| `internal/sqliteimport/` (new), `cmd/cortex/import_sqlite.go` (new) | `cortex import-sqlite` |
| `scripts/setup-server.sh`, `scripts/backup.sh` (new), `docs/deploy.md` (new) | Server setup and backups |
| `Makefile` | `test-db`, `test` |

---

### Task 1: Postgres connection, migrations, test harness

**Files:**
- Create: `internal/pgtest/pgtest.go`, `db.go`, `db_test.go`, `migrate.go`, `migrations/001_init.sql`
- Modify: `store.go` (whole file), `store_test.go` (rewrite SQLite-specific tests), `entity_test.go:11-21` (`openTestDB`), `go.mod`, `Makefile`

**Interfaces:**
- Produces:
  - `pgtest.DSN(t testing.TB) string`: reads `CORTEX_TEST_DATABASE_URL`, calls `t.Skip("set CORTEX_TEST_DATABASE_URL (make test-db) to run DB tests")` if unset, creates schema `t_<random>`, registers a cleanup `DROP SCHEMA ... CASCADE`, and returns the URL with `search_path=t_<random>,public` added as a query parameter (pgvector lives in `public`).
  - `func rebind(q string) string` turns `?` into `$1..$n`.
  - `type dbConn struct{ *sql.DB }` with `ExecContext/QueryContext/QueryRowContext/BeginTx` (BeginTx returns `*dbTx` with the same three methods), all applying `rebind`. `Cortex.db` becomes `dbConn`.
  - `func Open(dsn string, opts ...Option) (*Cortex, error)`. Options are applied **before** schema setup, so Task 2 can read the embedder.
  - Migration 001: every current table ported per the spec's schema table, **without** `embeddings.embedding` and without HNSW indexes. Plus `cortex_meta(key TEXT PRIMARY KEY, value TEXT NOT NULL)`, `CREATE EXTENSION IF NOT EXISTS vector`, a unique index `entities(name, type)`, and `tsv` generated columns `to_tsvector('english', left(content, 200000))` with GIN indexes on `memories` and `chunks`. All `ensureColumn` columns (confidence, valid_at, invalid_at, expired_at, speaker, static BOOLEAN, last_decay_at, forget_after) are inline.

- [ ] **Step 1: Add the Makefile targets.** `test-db` starts (or reuses) a container `cortex-pg` from `pgvector/pgvector:pg17` on port 55432 with a `cortex_test` database. `test` depends on `test-db` and runs `CORTEX_TEST_DATABASE_URL=postgres://postgres:postgres@localhost:55432/cortex_test?sslmode=disable go test ./... -count=1`.
- [ ] **Step 2: Write failing tests** in `db_test.go` and `store_test.go`:
  - `TestRebind`: `rebind("a = ? AND b IN (?, ?)") == "a = $1 AND b IN ($2, $3)"`; `rebind("no params") == "no params"`.
  - `TestOpenAppliesMigrations`: `Open(pgtest.DSN(t))`, then `SELECT max(version) FROM schema_migrations` = 1, and table `memories` has column `tsv`.
  - `TestOpenIdempotent`: Open, Close, Open on the same DSN succeeds and the version is still 1.
  - `TestOpenUnreachable`: `Open("postgres://u:p@127.0.0.1:1/x?sslmode=disable")` returns an error within 6 s whose message contains `127.0.0.1:1`.
  - Delete the SQLite-only tests (file creation, WAL, `ensureColumn`, FTS backfill).
- [ ] **Step 3: Run** `make test-db && CORTEX_TEST_DATABASE_URL=... go test -run 'TestRebind|TestOpen' .`. Expected: FAIL.
- [ ] **Step 4: Implement** `pgtest`, `db.go`, `migrate.go` (embed `migrations/*.sql` with `//go:embed`; apply unapplied versions in order, each in a transaction, under `pg_advisory_xact_lock(4242)` so concurrent Opens don't race) and the new `Open`. Set `connect_timeout=5` on the DSN if absent, and `Ping` with a 5 s context. Wrap ping errors as `cortex: can't reach Postgres at <host:port>: %w`. Switch `openTestDB` to `Open(pgtest.DSN(t))`.
- [ ] **Step 5: Run** the Step 3 command. Expected: PASS.
- [ ] **Step 6: Commit** `feat(store): open Postgres with versioned migrations`.

### Task 2: Vector schema and vector search

**Files:**
- Create: `vector.go`, `vector_test.go`
- Modify: `search.go:20-90` (embeddings put/get/search), `store.go` (call vector setup), `search_test.go:10` (`openTestDBWithEmbedder`)

**Interfaces:**
- Consumes: `Open`, `dbConn`, `cortex_meta` (Task 1).
- Produces:
  - `func checkPgvectorVersion(v string) error`: nil for `"0.8.0"`, `"0.8.1"`, `"1.0.0"`; error mentioning `0.8.0` for `"0.7.4"`.
  - `ensureVectorSchema(ctx)`, called by `Open`. It reads `extversion` and checks it, then, if `cortex_meta['embedding_dims']` is absent, runs `ALTER TABLE embeddings ADD COLUMN embedding halfvec(N) NOT NULL` plus three partial HNSW indexes (`WHERE ref_type = 'entity'|'memory'|'chunk'`) and records N. N = `embedder.Dimensions()`, or 512 if there is no embedder. If dims are present, an embedder is configured, and the values differ, it returns `cortex: embedder dimension %d does not match stored dimension %d`.
  - `putEmbedding` and `searchVectorRaw` keep their signatures. Search is `ORDER BY embedding <=> $1 LIMIT $k` with `ref_type` filtered and `hnsw.iterative_scan = relaxed_order` set for the transaction. Vectors are passed in pgvector text form `[x,y,...]` cast `::halfvec`. Similarity is returned as `1 - distance`.
  - `putEmbedding` returns `cortex: embedding has %d dims, want %d` if the length is wrong.

- [ ] **Step 1: Write failing tests:**
  - `TestCheckPgvectorVersion` (the values above).
  - `TestVectorDimsFromEmbedder`: Open with `MockEmbedder` (8 dims), then `cortex_meta.embedding_dims == "8"`.
  - `TestVectorDimsDefault512`: Open with no embedder gives `"512"`.
  - `TestVectorDimsMismatch`: Open with 8 dims, Close, reopen the same DSN with an embedder whose `DimensionsFn` returns 16. The error contains `does not match`.
  - `TestVectorPartialIndexes`: `pg_indexes` on `embeddings` has 3 hnsw indexes.
  - `TestPutEmbeddingWrongDims`: an error, not a panic.
  - The existing `search_test.go` vector tests pass unchanged (ordering by similarity).
- [ ] **Step 2: Run** `go test -run 'Vector|Pgvector|Embedding|SearchVector' .`. Expected: FAIL.
- [ ] **Step 3: Implement** `vector.go` and port `search.go`.
- [ ] **Step 4: Run** the Step 2 command. Expected: PASS.
- [ ] **Step 5: Commit** `feat(store): halfvec embeddings with per-type HNSW indexes`.

### Task 3: Entities, relationships, traversal, merge

**Files:**
- Modify: `entity.go`, `relationship.go`, `traverse.go` (and the merge code in `entity.go`), `entity_test.go`, `relationship_test.go`, `traverse_test.go`

**Interfaces:**
- Consumes: `dbConn` (Task 1).
- Produces: unchanged public API. `PutEntity` becomes one `INSERT ... ON CONFLICT (name, type) DO UPDATE SET attributes, source, confidence, updated_at RETURNING id, created_at`. `NameLike` uses `ILIKE`. Attributes are written as JSON text into `JSONB`.

- [ ] **Step 1: Write failing tests:**
  - `TestFindEntitiesNameLikeCaseInsensitive`: put "Alice"; `FindEntities(EntityFilter{NameLike: "ali%"})` returns 1.
  - `TestPutEntityConcurrentNoDuplicates`: 20 goroutines `PutEntity(&Entity{Type:"person", Name:"Bob"})`; afterwards `FindEntities(Type:"person")` has length 1 and all goroutines got the same ID.
- [ ] **Step 2: Run** `go test -run 'Entit|Relationship|Traverse|Merge' .`. Expected: the new tests FAIL.
- [ ] **Step 3: Port** the SQL in the three files (`ON CONFLICT`, `ILIKE`, any SQLite-only functions).
- [ ] **Step 4: Run** the Step 2 command. Expected: PASS.
- [ ] **Step 5: Commit** `feat(store): port entities and relationships to Postgres`.

### Task 4: Memories, chunks, keyword search, forget

**Files:**
- Modify: `memory.go` (incl. `ftsQuery` at :233), `chunk.go`, `cortex.go:141-344` (Forget paths), `memory_test.go`, `chunk_test.go`, `cortex_test.go`

**Interfaces:**
- Consumes: `dbConn` (Task 1).
- Produces:
  - `func orTSQuery(q string, firstArg int) (expr string, args []any)` replaces `ftsQuery`. For `"alice stripe"` with firstArg=1 it returns `(plainto_tsquery('english', $1) || plainto_tsquery('english', $2))` and `["alice","stripe"]`. For empty or whitespace input it returns `"", nil` (callers return no results). Callers filter `tsv @@ <expr>` and order by `ts_rank_cd(tsv, <expr>) DESC`.
  - All `*_fts` inserts and deletes and every `LastInsertId` use are deleted.
  - `firstChunkBySource` uses `metadata->>'source' = ?`.

- [ ] **Step 1: Write failing tests:**
  - `TestOrTSQuery`: the cases above.
  - `TestSearchMemoriesOddQueries`: with one memory "Alice joined Stripe", `SearchMemories` for each of `"the"`, `"C++"`, `"\"quoted\""`, `""` returns no error, and `"the"` returns 0 results.
  - `TestSearchMemoriesOrSemantics`: `"Stripe nonexistentword"` still finds the memory.
  - `TestPutChunkHuge`: `PutChunk` with `strings.Repeat("word ", 200000)` (1M chars) succeeds.
- [ ] **Step 2: Run** `go test -run 'Memor|Chunk|Forget|Remember|TSQuery' .`. Expected: FAIL.
- [ ] **Step 3: Port** the three files.
- [ ] **Step 4: Run** the Step 2 command. Expected: PASS.
- [ ] **Step 5: Commit** `feat(store): Postgres full-text search for memories and chunks`.

### Task 5: Maintenance passes and the rest of the root package

**Files:**
- Modify: `decay.go`, `expire.go`, `reconcile.go`, `lint.go`, `memory_edge.go`, `temporal.go`, `recall.go`/`profile.go`/`maintain.go`/`relate.go` only if their SQL needs it; `temporal_test.go`

**Interfaces:**
- Consumes: Tasks 1–4.
- Produces: no API change. The whole root package is green on Postgres.

- [ ] **Step 1: Write failing test** `TestValidAtNonUTCRoundTrip`: put a memory with `ValidAt` = 2026-03-01T08:00:00+08:00. Read it back: `ValidAt.Equal(that instant)`. A `Recall` with `as_of` 2026-03-01T00:30:00Z includes it, and one with `as_of` 2026-02-28T23:00:00Z excludes it.
- [ ] **Step 2: Run** `go test .` (whole package). Expected: failures across unported files.
- [ ] **Step 3: Port** remaining SQL: `datetime('now')` → `now()`, `INSERT OR IGNORE` → `ON CONFLICT DO NOTHING`, boolean `static` comparisons, any `julianday`/`strftime` arithmetic → interval arithmetic.
- [ ] **Step 4: Run** `go test .`. Expected: PASS, with no skips when `CORTEX_TEST_DATABASE_URL` is set.
- [ ] **Step 5: Commit** `feat(store): port maintenance passes to Postgres`.

### Task 6: Other packages, embedder dims, CLI config

**Files:**
- Modify: `connector/files/*_test.go`, `connector/conversation/conversation_test.go:23`, `cmd/cortex/mcp/tools_test.go:26`, `example_test.go`, `internal/testutil/testutil.go` (drop the temp-file DB helper; use `pgtest`), `eval/harness.go`, `cmd/cortex-eval/setup.go:104`, `cmd/cortex/main.go` (`dbPath`, help text, `configureEmbedder` default dims), `llm/openai/embedder.go`, `llm/openai/embedder_test.go`

**Interfaces:**
- Consumes: `Open(dsn)`, `pgtest.DSN`.
- Produces:
  - The CLI resolves its DSN as `--db` > `CORTEX_DATABASE_URL`. If both are empty it exits 1 with `set CORTEX_DATABASE_URL or pass --db <postgres-url>`. The same applies to `cortex mcp` and `cortex-eval`.
  - `NewEmbedder` defaults to `text-embedding-3-small` at 512 dims. `Embed` sets `EmbeddingRequestStrings.Dimensions = e.dims` when the model name starts with `text-embedding-3`. `configureEmbedder` defaults to 512 (not 1536); `EMBEDDING_DIMS` still overrides it.

- [ ] **Step 1: Write failing tests:**
  - `TestEmbedderSendsDimensions`: against an `httptest` server, the decoded request body has `"dimensions":512`.
  - `TestEmbedderNoDimensionsForOtherModels`: model `nomic-embed-text` gives no `dimensions` key.
  - `TestMainRequiresDSN` (in `cmd/cortex/main_test.go`): running with both empty gives exit 1 and the message above.
- [ ] **Step 2: Run** `make test`. Expected: FAIL.
- [ ] **Step 3: Implement** and switch all remaining test helpers to `pgtest.DSN`.
- [ ] **Step 4: Run** `make test && go vet ./...`. Expected: PASS for all packages.
- [ ] **Step 5: Commit** `feat(cli): Postgres DSN config; 512-dim OpenAI embeddings`.

### Task 7: `cortex import-sqlite`

**Files:**
- Create: `internal/sqliteimport/import.go`, `internal/sqliteimport/import_test.go`, `internal/sqliteimport/testdata/v040_schema.sql` (copy of `schemaSQL` + `ensureColumn` columns from `v0.4.0` `store.go`), `cmd/cortex/import_sqlite.go`
- Modify: `cmd/cortex/main.go` (subcommand + help)

**Interfaces:**
- Consumes: `cortex.Open`, `Cortex` put APIs or direct SQL via an exported `(*Cortex).DB() *sql.DB` (add it; documented as for tooling).
- Produces:
  - `func Import(ctx context.Context, sqlitePath string, cx *cortex.Cortex, opts Options) (Report, error)`, where `Options{Force, EmbedOnly bool; BatchSize int}` and `Report{Counts map[string][2]int /* source, target */; Embedded int}`.
  - Phase 1 is a single transaction copying entities, relationships, memories (all columns), memory_entities, memory_edges, chunks and sync_state, preserving IDs and timestamps. It parses timestamps with `time.RFC3339Nano`, `"2006-01-02 15:04:05.999999999-07:00"`, `"2006-01-02 15:04:05"` and `"2006-01-02T15:04:05Z"`; naive values are taken as UTC. An unparseable timestamp returns `table %s row %s: bad timestamp %q`. Phase 1 refuses a non-empty target (`entities` count > 0) unless `Force`.
  - Phase 2 embeds every memory, chunk and entity (entity text = name) with no row in `embeddings`, in `BatchSize` batches (default 64), committing per batch. `EmbedOnly` skips phase 1.
  - CLI: `cortex import-sqlite <path> [--force] [--embed-only]` prints `table  source  target` lines and exits 1 if any pair differs.

- [ ] **Step 1: Write failing tests** (fixture: build a temp SQLite file from `v040_schema.sql`, insert 2 entities, 1 relationship, 2 memories (one with naive `valid_at` "2026-03-01 08:00:00", one static), 1 memory_edge, 1 chunk, 1 sync_state):
  - `TestImportCopiesAll`: every `Report.Counts` pair is equal; the memory `ValidAt` equals 2026-03-01T08:00:00Z; static is preserved; IDs are equal.
  - `TestImportRefusesNonEmpty`: a second Import without Force errors.
  - `TestImportBadTimestamp`: the row is set to `created_at='garbage'`, the error names the table and row, and the target `entities` count is 0 (rolled back).
  - `TestImportEmbedResumes`: the embedder fails after the first batch (BatchSize 1), so Import returns an error. `EmbedOnly` rerun with a working embedder: every memory, chunk and entity has exactly one embedding.
- [ ] **Step 2: Run** `make test` scoped: `go test ./internal/sqliteimport/`. Expected: FAIL.
- [ ] **Step 3: Implement** the importer and CLI.
- [ ] **Step 4: Run** the Step 2 command and `go test ./cmd/cortex/`. Expected: PASS.
- [ ] **Step 5: Commit** `feat(cli): import-sqlite migrates a v0.4 brain into Postgres`.

### Task 8: Server setup, backups, docs

**Files:**
- Create: `scripts/setup-server.sh`, `scripts/backup.sh`, `docs/deploy.md`
- Modify: `README.md` (storage, setup, env vars, architecture), `docs/CORTEX.md` and `cmd/cortex/CORTEX.md.template` if they mention `brain.db`

**Interfaces:**
- Produces:
  - `setup-server.sh` is idempotent, `set -euo pipefail`, and supports Debian/Ubuntu on amd64 or arm64 (exit 1 otherwise) — including **Debian 13 (trixie)**, which is the owner's actual Pi target (Raspberry Pi OS on trixie, aarch64; PGDG publishes trixie-pgdg). Derive the PGDG suite from `VERSION_CODENAME`. It installs PGDG Postgres 17 + `postgresql-17-pgvector`, verifies `extversion >= 0.8.0`, and creates the role and database `cortex` with a generated password printed once. It computes `shared_buffers` ≈ 25% RAM, `effective_cache_size` ≈ 60%, `maintenance_work_mem` ≈ min(RAM/8, 1GB), `work_mem` 4MB if RAM < 2GB else 16MB, and `max_connections=20`. It creates a 2 GB swapfile if RAM < 2 GB. Network access flags (`--app-host`, `--cert/--key`, `--listen-ip`) are specified in Tasks 11 and 12; without them Postgres listens on localhost only. It installs a cron entry for `backup.sh` at 03:15 daily.
  - `backup.sh` runs `pg_dump -Fc cortex` and uploads to an S3-compatible bucket from env `BACKUP_S3_ENDPOINT`, `BACKUP_S3_BUCKET`, `BACKUP_S3_KEY`, `BACKUP_S3_SECRET` (via `rclone` or `aws s3`, builder's choice). It keeps 14 days and deletes older objects.
  - `docs/deploy.md` covers: droplet (smallest size, Ubuntu 24.04, droplet backups on, app host setup per Task 12); Pi section (64-bit OS, SSD not SD card, the S3 dump is the only backup); MCP-over-HTTPS client config and the ssh-tunnel DSN; restore procedure (`pg_restore` into a scratch DB); the resize trigger (indexes exceed RAM or disk > 70%, with the `pg_relation_size` query to check); and pinning downstream projects to `v0.4.0`.

- [ ] **Step 1: Lint** `shellcheck scripts/*.sh`. Expected: no findings.
- [ ] **Step 2: Smoke test in a container** (test-only use of Docker): `docker run --rm -v $PWD/scripts:/s ubuntu:24.04 bash -c "/s/setup-server.sh --no-cron && su postgres -c \"psql -d cortex -c 'select extversion from pg_extension where extname=\\'vector\\''\""`. Expected: a version ≥ 0.8.0. Add a `--no-cron` flag for this. Repeat with `debian:trixie --platform linux/arm64` (the real target) and `debian:bookworm`.
- [ ] **Step 3: Restore check:** in the container, insert a row, run `backup.sh` with a local-directory target (`BACKUP_LOCAL_DIR` fallback for tests), `pg_restore` into `cortex_restore`, and confirm the row is present.
- [ ] **Step 4: Write the docs and the README changes.** `grep -rn 'brain.db\|CORTEX_DB\b' README.md docs/CORTEX.md cmd/cortex/CORTEX.md.template` should return only import-sqlite mentions.
- [ ] **Step 5: Commit** `docs(deploy): portable Postgres server setup with off-site backups`.

### Task 9: Abstention recalibration and acceptance run (needs the owner's server + OpenAI key)

**Files:**
- Modify: `types.go` (`AbstainRelevanceThreshold` value and its calibration comment)

- [ ] **Step 1:** Record the SQLite baseline. Using the `v0.4.0` binary on `~/cortex/brain.db`, run `cortex-eval` and save the output to `eval-baseline-sqlite.txt` (not committed).
- [ ] **Step 2:** `cortex import-sqlite ~/cortex/brain.db` into a fresh database on the server. Expected: all count pairs equal.
- [ ] **Step 3:** Run the `cortex-eval` threshold sweep on the migrated brain. Pick the center of the plateau where easy-negative abstention is 100% and false abstention is 0%. Update the constant and comment (cite 512-dim halfvec, the date, the counts).
- [ ] **Step 4:** Run full `cortex-eval`. Expected: recall and abstention metrics ≥ baseline. If keyword-heavy questions regress, tune `orTSQuery` (prefix `:*`, `simple` config) per the spec; regressions block.
- [ ] **Step 5:** Performance checks. From the Mac, `cortex recall` p95 < 500 ms over 50 queries (excluding LLM and embedding API time: measure with the API calls stubbed out or timed separately). On the server, load 100K random 512-dim vectors into a scratch schema and get vector query p95 < 200 ms. A failure on 512 MB doesn't block: record the result and the droplet size that passes in `docs/deploy.md`.
- [ ] **Step 6: Commit** `fix(recall): recalibrate abstention threshold for 512-dim halfvec`. Paste both eval runs and the perf numbers into the PR description.

### Task 10: Pin downstream projects (before merging to master)

- [ ] **Step 1:** In `../goclaw/go.mod`, delete `replace github.com/sausheong/cortex => ../cortex` and set `github.com/sausheong/cortex v0.4.0`. Run `go mod tidy && go build ./... && go test ./...` in `../goclaw`. Expected: PASS.
- [ ] **Step 2:** Commit in goclaw: `chore: pin cortex v0.4.0 (last SQLite release)`.
- [ ] **Step 3:** Confirm felix (`v0.4.0`) and cloudcat (pseudo-version) build unchanged: `go build ./...` in each.

### Task 11: Public TLS access (replaces Tailscale) — SUPERSEDED by Task 12

> Public Postgres was dropped on 2026-10-22: the owner's network resets TLS on port 5432. `--public-host`, the certbot-to-Postgres cert copy and the 5432 firewall rule no longer exist; see Task 12. The text below is kept for history.

Owner's decision (2026-10-22): the droplet at `cortex.sausheong.dev` (Ubuntu 24.04, 512 MB, SGP1) is reached directly over the internet. Tailscale is dropped.

**Files:** `scripts/setup-server.sh`, `docs/deploy.md`, `README.md`.

- [ ] **Step 1: Flags.** Remove `--tailscale-ip`/`--no-tailscale`. Add `--public-host FQDN`, `--email ADDR`, `--cert FILE --key FILE`, `--listen-ip IP` (mutually exclusive with `--public-host`), keeping `--no-cron`. Without access flags: localhost only.
- [ ] **Step 2: Public mode config.** `listen_addresses='*'`, `ssl=on`, `ssl_min_protocol_version='TLSv1.2'`, `password_encryption=scram-sha-256`; pg_hba managed block (rewritten each run, legacy block removed): localhost rules, `hostssl cortex cortex 0.0.0.0/0` and `::/0` scram, `hostnossl all all 0.0.0.0/0` and `::/0` reject.
- [ ] **Step 3: Certificates.** certbot `certonly --standalone --non-interactive --agree-tos --keep-until-expiring` (`--email` or `--register-unsafely-without-email`); skip when a cert with more than 30 days left exists. Copy to `/etc/postgresql/17/main/ssl/` (owner postgres, key 0600). Deploy hook in `/etc/letsencrypt/renewal-hooks/deploy/` re-copies and reloads Postgres. `--cert/--key` bypass certbot.
- [ ] **Step 4: Firewall.** `ufw` default deny incoming; allow 22, 80, 5432; enable. If ufw cannot run (container), warn and continue.
- [ ] **Step 5: DSN.** Print once at the end; `sslmode=verify-full` for `--public-host` with certbot (`require` with a custom cert), `require`/`prefer` for `--listen-ip`, `disable` for localhost. Role is created `nosuperuser` via stdin (password not on the process command line).
- [ ] **Step 6: Docs.** `docs/deploy.md`: public TLS mode, DNS-only (grey cloud) record in Cloudflare, optional DO cloud firewall allowing 22/80/5432, Mac DSN, Pi/LAN mode, security notes, password rotation; keep backups/restore/resize.
- [ ] **Step 7: Tests.** `shellcheck scripts/setup-server.sh` clean. In `ubuntu:24.04` with `--public-host test.local --cert/--key` (openssl self-signed) and in `debian:trixie --platform linux/arm64` with `--listen-ip` and localhost-only, connecting via the container IP: `cortex` with `sslmode=require` succeeds; `sslmode=disable` is rejected by pg_hba; `postgres` over TCP is rejected; a second run leaves pg_hba and conf byte-identical. Also: legacy pg_hba block upgraded; "certbot already issued" path (fake `/etc/letsencrypt/live/<host>`) succeeds; renewal hook runs.
- [ ] **Step 8: Live rollout** (owner): run on the droplet with `--public-host cortex.sausheong.dev`; from the Mac verify `psql 'sslmode=verify-full'` and `cortex init`.

### Task 12: cortex app on the droplet (replaces public Postgres)

Owner's decision (2026-10-22): cortex and Postgres run on the same droplet (`cortex.sausheong.dev`, 168.144.141.118, Ubuntu 24.04, 512 MB + 2 GB swap). Postgres is localhost-only; clients use MCP over HTTPS.

**Files:** `cmd/cortex/mcp/{serve_http.go,serve_http_test.go}`, `scripts/setup-server.sh`, `scripts/deploy-app.sh`, `scripts/test-setup-server.sh`, `Makefile`, `docs/deploy.md`, `README.md`.

- [x] **Step 1: Auth.** When `CORTEX_AUTH_TOKEN` is set the bearer check is always applied, on any bind (loopback behind a proxy included); the non-loopback-without-token warning stays. Tests: loopback bind with token returns 401 with no or wrong token and 200 with the right one; `/healthz` stays open. Commit `fix(mcp): always enforce bearer token when set`.
- [x] **Step 2: setup-server.sh.** Remove `--public-host`, the hostssl/`*` pg_hba mode and the certbot-to-Postgres copy and hook. Add `--app-host FQDN [--email]`: Postgres localhost-only; `cortex` system user; `/etc/cortex/cortex.env` (0600 root) with `CORTEX_DATABASE_URL`, `OPENAI_API_KEY` skeleton, `CORTEX_ADDR`, `CORTEX_AUTH_TOKEN` (generated once, preserved); systemd `cortex.service` (`cortex mcp --transport http --addr 127.0.0.1:8080`, `Restart=on-failure`), `cortex-maintain.service` and `.timer` (19:15 UTC); Caddy from the official apt repo with `reverse_proxy 127.0.0.1:8080`; `ufw` limit 22, allow 80/443, delete the 5432 rule; disable certbot renewal for the host. Idempotent, and converts an existing public-TLS install. No-systemd hosts get a warning.
- [x] **Step 3: deploy-app.sh.** Detect arch over ssh, cross-compile (`GOOS=linux CGO_ENABLED=0`), atomic install to `/usr/local/bin/cortex`, push allow-listed settings from the Mac environment into the env file over ssh stdin, restart, check `https://<fqdn>/healthz` and that unauthenticated `/mcp` is 401, print the MCP URL and where the token is (`--show-token` prints it).
- [x] **Step 4: Tests.** `shellcheck scripts/*.sh` clean; `scripts/test-setup-server.sh` (`make test-deploy`) runs `--app-host` in `ubuntu:24.04` and asserts localhost-only Postgres, env file mode/contents, unit and Caddyfile contents, `caddy validate`, idempotent re-run with token/DSN/user values preserved, and legacy public-TLS conversion. `make test` passes.
- [x] **Step 5: Docs.** `docs/deploy.md` and README (MCP over HTTPS), public-Postgres instructions removed; spec updated.
- [ ] **Step 6: Live rollout** (owner, not part of the implementation): on the droplet re-run `setup-server.sh --app-host cortex.sausheong.dev` (converts the public-TLS install; stores a new DB password in the env file), then `scripts/deploy-app.sh root@cortex.sausheong.dev` from the Mac. Verify `curl https://cortex.sausheong.dev/healthz` and an MCP client with the bearer token; check `systemctl list-timers cortex-maintain.timer`.
