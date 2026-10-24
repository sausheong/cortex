# Postgres Storage — Design Spec

**Date:** 2026-10-22
**Status:** Approved (design); pending implementation plan
**Author:** brainstormed with Claude

## Summary

Move cortex's storage from embedded SQLite to **Postgres + pgvector**, running
on a single **DigitalOcean droplet** the owner controls. Postgres becomes the only backend.
The existing graph data model (entities, relationships, memories, chunks, edges)
is kept as-is, ported to Postgres. Vector search moves from brute-force Go
cosine to pgvector HNSW indexes, and keyword search moves from FTS5 to Postgres
full-text search.

This is project 1 of 3:

1. **Postgres storage** (this doc)
2. Capture + self-model: ingest ChatGPT and Claude Code conversations, build a
   "how I work / who I am" profile (separate spec)
3. Reach: hosted MCP with OAuth so ChatGPT and claude.ai can use cortex
   (separate spec)

## Why

The owner wants cortex to hold *everything*: ChatGPT and Claude Code history
now, contacts, iMessage and WhatsApp later. That pushes vector counts from
about 1.4K today to possibly millions. Brute-force cosine in Go stops scaling
past roughly 100K. Project 3 also needs several tools writing to one shared
store concurrently, which suits a server database better than a SQLite file.

The owner decided: Postgres only (no SQLite mode retained), host it on one DigitalOcean
droplet (Postgres and, later, cortex's server on the same box), keep it
lightweight, and don't build a custom database or split data across several
databases.

## Goals

- All cortex features (remember, recall, search, forget, merge, lint,
  reconcile, relate, decay, expire, profile, export, sync, MCP) work unchanged
  in behaviour on Postgres.
- Vector search uses pgvector HNSW, so it stays fast at millions of vectors.
- It runs on the **smallest DigitalOcean droplet** (512 MB RAM, 1 vCPU,
  10 GB disk, ≈ US$4/mo) at today's data size and through early growth, and
  scales by resizing the droplet, with no code changes.
- Existing data in `~/cortex/brain.db` (1,274 memories, 2,107 entities, 202
  chunks) is migrated over without loss.
- Recall quality does not regress, measured with the existing `cortex-eval`
  harness.

## Non-goals

- Keeping a SQLite backend or a storage abstraction for multiple backends.
- A graph query language (Apache AGE, Cypher). Traversal stays as SQL over the
  relationships table, using recursive CTEs where useful.
- Multi-tenant or multi-user support.
- Hosting, OAuth, or public exposure (project 3).
- New ingestion sources (project 2).

## Anti-goals

- A heavyweight stack (Kubernetes, multiple database servers,
  separate vector DB).
- Silently changing recall results: any ranking change from FTS5 → Postgres FTS
  must be measured, not assumed.
- Breaking felix, goclaw and cloudcat without a clear pin path.

## Decisions (owner's)

- **Postgres only.** SQLite support is removed.
- **Hosted on one DigitalOcean droplet** running self-managed Postgres. Not
  DO Managed Postgres and not Neon. The same droplet becomes the always-on
  cortex server in project 3.
- **Downstream projects are pinned** to the last SQLite release of cortex.
  - felix already pins `v0.4.0`; no change needed.
  - cloudcat pins a pseudo-version; leave it, or move it to `v0.4.0`.
  - goclaw uses `replace => ../cortex`, so it would pick up the Postgres code.
    Its replace directive must be dropped and it must pin `v0.4.0`. Do this
    before merging the Postgres work.
  - `v0.4.0` is current master (no commits after it), so it is the last SQLite
    release. The Postgres version ships as `v0.5.0`. Cortex is pre-1.0, so a
    breaking change in a minor version is acceptable and no `/v2` module path
    is needed.

## Design

### Connection and configuration

- `cortex.Open(path string, ...)` becomes `cortex.Open(dsn string, ...)`, taking
  a Postgres connection URL.
- Driver: `github.com/jackc/pgx/v5` via its `database/sql` adapter
  (`pgx/v5/stdlib`). This keeps `*sql.DB` and the existing query code shape, so
  the port is mostly SQL dialect changes. *(Claude's call: least churn. Native
  pgx is an option for the builder if a specific query needs it.)*
- CLI config: `--db` takes a DSN; env var `CORTEX_DATABASE_URL` replaces
  `CORTEX_DB`. No default: if neither is set, the CLI errors with a clear
  message. MCP and eval binaries follow the same rule.
- **Owner's decision, 2026-10-22 (supersedes both the Tailscale design and
  the short-lived public-Postgres design):** cortex and its Postgres run on the
  same droplet, `cortex.sausheong.dev`. Postgres listens on localhost only and
  port 5432 is closed (the owner's network resets TLS on 5432, and exposing the
  database was never necessary). Clients reach cortex over HTTPS: Caddy
  terminates TLS (its own Let's Encrypt certificate) on 443 and reverse-proxies
  to `cortex mcp --transport http` on `127.0.0.1:8080`, a systemd service
  running as a dedicated `cortex` user with secrets in `/etc/cortex/cortex.env`
  (0600, root-owned). `/mcp` always requires the bearer token
  `CORTEX_AUTH_TOKEN` when set, regardless of bind address. `ufw` allows only
  22 (rate-limited), 80 and 443. A systemd timer runs `cortex maintain`
  nightly at 03:15 SGT. The Mac keeps the stdio CLI/MCP only for local use;
  for one-off CLI work against the server DB it uses an ssh tunnel.
  Private-network hosts (a Pi on a LAN) use `--listen-ip` instead.

### Schema

A straight port of the current tables, with these changes:

| SQLite | Postgres |
|---|---|
| `TEXT` JSON `attributes`/`metadata` | `JSONB` |
| `DATETIME` | `TIMESTAMPTZ`, default `now()` |
| `static INTEGER` | `BOOLEAN` |
| `confidence REAL` | `DOUBLE PRECISION` |
| `chunks_fts`, `memories_fts` virtual tables | `tsv tsvector GENERATED ALWAYS AS (to_tsvector('english', content)) STORED` column + GIN index on `chunks` and `memories` |
| `embeddings.vector BLOB` | `embedding halfvec(N)` + HNSW index (`halfvec_cosine_ops`) |
| `rowid` joins to FTS tables | gone; FTS lives on the row |
| `INSERT OR ...` | `INSERT ... ON CONFLICT` |

Foreign keys and cascade rules stay the same. The manual FTS maintenance code
(insert and delete on `*_fts`) is deleted, since the generated column takes
care of it.

**Embeddings table.** Keep the single `embeddings` table keyed by
`(ref_id, ref_type)`, which today holds `entity`, `memory` and `chunk` vectors.
Use **one partial HNSW index per `ref_type`**
(`... WHERE ref_type = 'memory'`, and so on). Vector queries always filter on
`ref_type`, so each query hits a small, dedicated index, and one type
dominating the table can't starve recall for another. Also set
`hnsw.iterative_scan = relaxed_order` as a safety net. *(Claude's call.)*

**pgvector version.** Require **pgvector ≥ 0.8.0**, which is needed for
`halfvec` and iterative scan. `Open` checks `extversion` and errors clearly if
it is older. PGDG's `postgresql-17-pgvector` package for Ubuntu is current, so
that is the install path.

**Embedding dimensions.** The default is 512-dimension `halfvec` *for OpenAI*:
`text-embedding-3-small` accepts a `dimensions` parameter, so the OpenAI
embedder must send it, and `EMBEDDING_DIMS` still overrides it. Other providers
(Ollama etc.) use their model's native size. At 512 dims × 2 bytes, 1M vectors
≈ 1 GB, far more than the 512 MB droplet can hold in memory. At today's size
(about 1.5K vectors, a few MB) it's trivial. **Resize trigger:** when the HNSW
indexes no longer fit in `shared_buffers` + OS cache (watch
`pg_relation_size` of the indexes, roughly past 100K vectors) or the disk
passes 70%, resize the droplet. This is an ops step, not a code change.

How the dimension gets into the schema *(Claude's call)*:
- The versioned migrations create everything **except** the `embedding` column
  and its HNSW indexes, since those need N at DDL time.
- On `Open`, if `cortex_meta` has no `embedding_dims`, cortex runs the vector
  DDL using the configured embedder's `Dimensions()`, or 512 if no embedder is
  configured, and records N. So `Open` alone fully initialises a database:
  library users and tests don't need a separate `init` step. `cortex init`
  stays for the owner-identity prompt.
- On later `Open`s, if an embedder is configured and its `Dimensions()` differs
  from the stored N, it is a hard error. With no embedder configured there is
  no check (vector search is simply off, as today).

**Migrations.** Replace `ensureColumn` with versioned migrations: embedded
`.sql` files applied in order, tracked in a `schema_migrations` table, run
inside a transaction on `Open`. Migration 1 is the full schema above, minus the
vector DDL described above.

**Unreachable database.** `Open` uses a 5-second connect timeout and fails with
an error that names the host, for example "can't reach Postgres at cortex-db:5432".
No retry loop and no offline mode.

### Search

- **Keyword:** keep today's semantics. The current `ftsQuery` **OR-joins**
  quoted tokens, so the Postgres version builds an OR query too: each
  whitespace token becomes a `plainto_tsquery('english', token)`, and these are
  combined with `||`. Results are ordered by `ts_rank_cd`. Do not use
  `websearch_to_tsquery`, because it ANDs terms and would quietly narrow
  results. The text-search config is `'english'`. Non-English content
  (e.g. Chinese in future WhatsApp messages) won't tokenise well in Postgres
  FTS, but vector search covers it. *(Claude's call.)*
- **If eval regresses** on keyword-heavy questions, it blocks acceptance. The
  builder tunes within Postgres FTS (prefix matching via `:*`, weights, the
  `simple` vs `english` config) until it reaches parity. Adding a BM25
  extension is not in scope.
- **Vector:** `ORDER BY embedding <=> $1 LIMIT k` using the HNSW index. The
  brute-force scan in `searchVectorRaw` is removed. `embeddingByRef` (reused by
  the reranker) stays as a lookup.
- **Graph traversal and RRF fusion:** unchanged logic; only the SQL dialect
  changes.
- Abstention thresholds (`AbstainRelevanceThreshold`) were calibrated on
  1536-dim vectors. The change to 512 dims may shift cosine distributions, so
  rerun the threshold sweep and update the constant and its comment.

### Data migration from SQLite

A one-off command, `cortex import-sqlite <path-to-brain.db>`, reads an existing
SQLite brain and writes it into the configured Postgres database:

- It copies entities, relationships, memories (all columns, including temporal,
  static and confidence fields), memory_entities, memory_edges, chunks and
  sync_state, preserving IDs and timestamps.
- It **re-embeds** memories and chunks at the new dimension. Old 1536-dim
  vectors can't be reused. For the current brain that is about 1.5K items, which
  is cheap.
- It runs in two phases. **Phase 1** copies rows in one transaction
  (all-or-nothing). **Phase 2** embeds every memory, chunk and entity that has no
  embedding yet, in batches. If an embedding API call fails, rerunning
  `import-sqlite --embed-only` picks up where it stopped, so phase 2 is
  resumable.
- SQLite timestamps are parsed with the same formats the current code reads.
  Timestamps without a timezone are treated as **UTC**, matching how SQLite's
  `datetime('now')` wrote them. A timestamp that can't be parsed aborts phase 1
  and names the row.
- It uses whatever embedder is configured (normally OpenAI at 512 dims).
- It refuses to run against a non-empty target unless `--force` is given.
- It prints counts per table, and the counts must match the source.

This command needs to read SQLite, so `modernc.org/sqlite` stays as a
dependency for that command only. *(Claude's call: the alternative of exporting
from a v0.4.0 binary to JSON and then importing is more steps for the owner.)*

### Droplet deployment

Documented in a new `docs/deploy.md`. No application code beyond
docs and small scripts:

- Ubuntu 24.04 LTS on the **smallest droplet: 512 MB RAM / 1 vCPU / 10 GB
  disk (≈ US$4/mo)**, in the DO region nearest the owner. This is the owner's
  decision. Add a **2 GB swapfile**, because Postgres and the
  OS are tight in 512 MB, and HNSW builds spike memory.
- Postgres 17 and `postgresql-17-pgvector` from the PGDG apt repository.
  *(Claude's call: native apt over Docker, which is simpler to tune and back up
  on one box.)*
- App host mode (owner's decision, 2026-10-22), `setup-server.sh --app-host FQDN`:
  Postgres `listen_addresses='localhost'`, `password_encryption=scram-sha-256`,
  `pg_hba.conf` with localhost rules only; system user `cortex`;
  `/etc/cortex/cortex.env` (database URL, API keys, auth token generated once
  and preserved on re-runs); systemd `cortex.service`, `cortex-maintain.service`
  and `cortex-maintain.timer`; Caddy from the official apt repo; `ufw` limit 22,
  allow 80 and 443, 5432 closed. Re-running converts an older public-TLS install
  (removes the certbot deploy hook and the Postgres key copy, disables certbot
  renewal for the host, returns Postgres to localhost). `scripts/deploy-app.sh`
  cross-compiles and ships the binary and secrets from the Mac. `--listen-ip`
  covers private-network hosts; with no flags Postgres listens on localhost only.
  The public-Postgres mode (`--public-host`) is removed.
- A suggested `postgresql.conf` for 512 MB: `shared_buffers=128MB`,
  `effective_cache_size=256MB`, `maintenance_work_mem=64MB`, `work_mem=4MB`,
  `max_connections=20`. The doc lists the values to change when resizing up.
- **Backups, two layers:**
  - DO automated droplet backups enabled (daily).
  - A nightly `pg_dump -Fc` via cron, uploaded to a private DigitalOcean Spaces
    bucket, keeping 14 days.

  This brain is meant to hold the owner's whole life, so backups are part of
  the deliverable, not optional. The doc includes a tested restore procedure.
- The owner creates the droplet. The builder supplies the docs and a setup
  script (`scripts/setup-server.sh`) that installs and configures everything
  above on a fresh host.

**Portability: the same setup also targets a Raspberry Pi.** The owner wants
the option. The setup script and docs must work on **any Debian or Ubuntu host,
amd64 or arm64**: the droplet is primary, and Raspberry Pi OS 64-bit (Debian
Bookworm) is also supported. Concretely:
- No DO-specific steps in the script. Droplet backups and the cloud firewall
  are documented as DO-only extras.
- `postgresql.conf` values are computed from detected RAM (about 25%
  `shared_buffers`, 50–75% `effective_cache_size`, and so on), not hard-coded
  for 512 MB.
- The swapfile is created only when RAM is under 2 GB.
- Backup uploads target any S3-compatible bucket (Spaces by default) via
  env-configured endpoint and credentials, so the Pi uses the same off-site
  dump. On a Pi, that dump is the only backup layer.
- The docs add a Pi section: 64-bit OS, run from an SSD rather than an SD card,
  and `--listen-ip` (LAN) for access, or `--app-host` to expose cortex itself; never port-forward 5432.
- Docker is not used on any deploy target (it is only used for local tests).

### Testing

- About 170 tests open a database. Each test gets an isolated, freshly migrated
  Postgres **schema** (created per test, dropped on cleanup) in a test database
  given by `CORTEX_TEST_DATABASE_URL`.
- `make test` starts a `pgvector/pgvector:pg17` Docker container if one isn't
  running, sets the env var, and runs `go test ./...`.
- With `CORTEX_TEST_DATABASE_URL` unset, DB-backed tests call `t.Skip` with a
  message naming the env var. Pure logic tests still run. *(Claude's call: keeps
  `go test ./...` usable without Docker, while `make test` is the real gate.)*
- `internal/testutil` gets the shared helper; the per-package `openTestDB`
  helpers use it. The helper creates the schema, then opens cortex with a DSN
  that carries `search_path=<schema>` as a connection parameter. That way every
  pooled connection lands in the right schema, without `SET` and without
  limiting the pool to one connection. *(Claude's call.)*

## Acceptance

1. `make test` passes, with all DB-backed tests running against Postgres.
2. `cortex import-sqlite ~/cortex/brain.db` into a fresh database reproduces
   matching per-table counts, and spot-checked recalls return the same top
   results.
3. `cortex-eval` on the migrated brain shows recall and abstention metrics at
   least as good as the SQLite baseline, after the abstention threshold is
   re-calibrated for 512 dims. Record both runs in the PR.
4. Performance, with Postgres on the droplet:
   - `cortex recall` run from the Mac through an ssh tunnel to the droplet's Postgres: p95 under 500 ms at the
     current data size, excluding LLM and embedding API time. This includes
     the network round trips to the droplet.
   - A synthetic 100K random 512-dim vector load, with the SQL query timed on
     the droplet itself: vector search p95 under 200 ms. If it fails on 512 MB,
     record the result and the droplet size at which it passes. That number
     becomes the documented resize point; a failure here does not block the
     work.
   - A restore from the latest Spaces dump into a scratch database succeeds.
5. goclaw is pinned to `v0.4.0` (replace removed) before the merge.
6. README, CORTEX.md and the CLI help no longer mention `brain.db` as the
   store, and they document `CORTEX_DATABASE_URL`.

## Left to the builder

- Exact HNSW parameters (`m`, `ef_construction`, `ef_search`).
- Whether a hot query needs native pgx over `database/sql`.
- Connection pool sizing.
- How to structure the migration files.
- Whether `cortex init` creates the database or expects it to exist (it must
  at least run migrations and record the embedding dimension).
