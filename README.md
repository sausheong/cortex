# Cortex

Cortex is a private memory service shared by all your AI assistants. ChatGPT, Claude, Claude Code and your own agents connect to one cortex server over MCP. They recall from it before answering and remember into it afterwards. Knowledge compounds: the next time a person, project or preference comes up, every assistant already has the context.

Cortex keeps what you know as one graph: people, organizations, projects, events, documents and the facts about them, joined by typed relationships. It extracts entities and facts from what assistants send it and resolves them to things you already know about. It keeps itself tidy while you use it.

Each user's memory is encrypted with their own key, which is unlocked by their passkey. Nothing on the server can read a user's memory unless that user, or one of their connected assistants, is making a request. See [Privacy and encryption](#privacy-and-encryption).

Cortex runs as a hosted, multi-user server. One instance serves many people, each with an isolated, encrypted graph and their own AI provider. It is written in Go on Postgres 17 + pgvector.

Inspired by:
- [GBrain](https://github.com/garrytan/gbrain) for the operational model;
- [Cognee](https://github.com/topoteretes/cognee) for the graph structure;
- [mem0](https://github.com/mem0ai/mem0) for the memory pipeline;
- Atlassian's Teamwork Graph for the curated vocabulary, stable identity and graph signals in ranking.

## How it works

```
Remember  "Had coffee with Carol Seah from WYNNES; she's moving to Singapore"
  -> extract with the user's own LLM (long text in chunks, large saves in the background)
       entities       Carol Seah (person), WYNNES (organization), Singapore (place)
       relationships  Carol Seah works_at WYNNES, Carol Seah lives_in Singapore
       memories       "Carol Seah is moving to Singapore"  about [Carol Seah]
  -> resolve each entity to one you already know (alias, owner, name, fuzzy match, LLM tie-break)
  -> map types and verbs onto the curated vocabulary
  -> encrypt and store; lives_in Singapore closes Carol's previous lives_in edge
  -> embed chunks and memories (in batches) for semantic search

Recall  "what's Carol up to?"
  -> seed from the entities the query names (Carol Seah)
  -> search the user's decrypted working set in memory: memories and chunks
     by keyword and by meaning, the seeds' own memories, graph traversal
  -> reciprocal rank fusion, weighted by confidence, graph proximity and past use
  -> ranked results with provenance, plus a strength score and an abstain hint

Maintain (while you use it)
  reconcile -> relate -> decay -> expire -> refresh profiles -> backfill embeddings
```

## Contents

- [Features](#features)
- [Using cortex](#using-cortex)
- [Connecting assistants](#connecting-assistants)
- [Privacy and encryption](#privacy-and-encryption)
- [Knowledge model](#knowledge-model)
- [Ingestion](#ingestion)
- [Recall](#recall)
- [Keeping the graph healthy](#keeping-the-graph-healthy)
- [MCP tools](#mcp-tools)
- [Running your own server](#running-your-own-server)
- [Storage](#storage)
- [Development](#development)

## Features

- **One memory for every assistant.** Any MCP client (ChatGPT, Claude, Claude Code, Cursor, your own agents) reads and writes the same graph. Clients use OAuth 2.1 or a per-assistant token.
- **Private by design.** Content is encrypted per user. The key is unlocked by a passkey and wrapped for each session and assistant. There is no operator key.
- **Multi-user.** Anyone can sign up with Google. Each person gets an isolated graph and brings their own AI provider (key, base URL, chat and embedding models).
- **A graph, not a pile of notes.** It holds entities, relationships, memories (distilled facts) and chunks (raw text).
- **Curated vocabulary.** There are ten entity types and 21 directional relationship verbs. Whatever the extractor invents is mapped onto them, so the graph stays queryable.
- **Stable identity.** Aliases (other names, emails, phones, handles) and a resolution cascade reuse known entities instead of creating near-duplicates.
- **Graph-aware recall.** Entities named in a query seed the search, and results near them rank higher. Several retrieval strategies are merged by reciprocal rank fusion, with calibrated abstention.
- **Time.** Memories and relationships carry validity windows. Recall can read the graph as it was, or include what is no longer true.
- **Self-maintaining.** It reconciles contradictions, relates memories, decays what nobody uses, expires time-bound facts and refreshes profiles.
- **Large documents.** A save of any size is chunked, extracted in parallel and processed in the background.
- **Undoable.** Forgets, merges and compactions are recorded as batches you can restore for 30 days.
- **A calm web app.** It has Home, Memories, Graph, Vault (an Obsidian-style view and download), Connections, Settings and Account.

---

## Using cortex

1. **Sign in** at your cortex server (for example `https://cortex.sausheong.dev`) with Google.
2. **Protect your memory with a passkey** (Touch ID, Face ID or a security key). Cortex shows a recovery key once; print it or keep it in a password manager. If you lose every passkey and the recovery key, your memory cannot be recovered by anyone.
3. **Set up your AI provider** in Settings. Cortex uses your key and models (any OpenAI-compatible API, or Anthropic plus an embedding provider) to extract facts and embed text. The operator's key is never used.
4. **Connect your assistants** in Connections (below).

The web app's pages:

- **Home:** what is connected, what is new, and anything that needs your attention.
- **Memories:** search, filter by date or assistant, and bulk delete. Compaction merges near-duplicate memories (previewed and undoable). **Deleted** restores any batch for 30 days.
- **Graph:** an interactive map of your entities. Rename, merge and delete from it.
- **Vault:** your graph as browsable pages, downloadable as an Obsidian vault.
- **Connections:** your MCP address, connected assistants and tokens. Rename, renew and revoke them here.
- **Settings and Account:** provider and models, re-embedding, storage, passkeys, recovery key, and account deletion.

You sign in with Google and then unlock with your passkey. On a device without passkey support you unlock with your recovery key. An unlocked browser session relocks after 12 hours idle.

## Connecting assistants

Your MCP address is shown in Connections, for example `https://cortex.sausheong.dev/mcp`.

**ChatGPT, claude.ai and other OAuth clients.** Add cortex as a custom connector with that address. The client opens cortex in your browser. You approve it there while unlocked, and it then connects with its own OAuth tokens. Revoke it in Connections at any time.

**Claude Code, Cursor and your own agents.** Create a token in Connections. Tokens last 90 days, and you renew them from Connections. Then send it as a bearer header:

```bash
claude mcp add --transport http cortex https://cortex.example.com/mcp \
  --header "Authorization: Bearer <token>"
```

Generic MCP client configuration:

```json
{
  "mcpServers": {
    "cortex": {
      "type": "http",
      "url": "https://cortex.example.com/mcp",
      "headers": { "Authorization": "Bearer <token>" }
    }
  }
}
```

Assistants should call `context` at the start of a conversation and `remember` when they learn something durable about you. `cortex init-schema [dir]` writes `CORTEX.md`, a contract you can drop into a project telling agents when to recall, when to remember and how to use the graph tools.

## Privacy and encryption

- **Your key.** Each user has a random 256-bit key that is never stored in usable form. It is stored only wrapped: once by each of your passkeys (through the WebAuthn PRF extension), by your recovery key, by your current browser session, and by each assistant's token or OAuth grant.
- **What is encrypted.** Every piece of content is encrypted with AES-256-GCM:
  - memories, chunks, entity names and attributes, relationship types and attributes, sources;
  - embeddings, compaction previews, undo snapshots and background job payloads;
  - your provider settings.

  Each value is bound to its user, table, row and column. Exact-match lookups (an entity's name, an alias) use keyed blind indexes.
- **Search** runs in memory over your decrypted working set while you are using cortex. The working set is evicted when idle, and no plaintext index is kept on disk.
- **Background work** runs only while you or an assistant is connected. Maintenance runs at most daily on use, and large saves finish in the background or wait for your next visit. The server holds no standing key for you.

| Threat | Protected? |
|---|---|
| Database dumps, backups, a stolen disk | Yes |
| The operator running SQL against the database | Yes, for content. Counts, sizes, timestamps and graph shape stay visible. |
| Other users on the same server | Yes |
| The server compromised while you are not using cortex | Yes |
| Root on the server while you are using it | No: keys and plaintext are in memory during your requests |
| Your own AI provider | No: it receives the text it extracts and embeds |

Lose every passkey and your recovery key and your memory is gone, including from backups. That is the point.

---

## Knowledge model

### Entities

Every entity has a type from the curated set. Extractors are asked to use these; anything else is mapped through a synonym table (`company` → organization, `whatsapp group` → group) or falls back to `concept` with the original label kept in the `type_label` attribute.

| Type | Covers |
|------|--------|
| `person` | an individual human |
| `organization` | company, institution, agency, school, bank, merchant |
| `place` | country, city, region, address, venue, building |
| `project` | a named piece of work, initiative or programme |
| `product` | software, app, platform, model, service, device, card, plan |
| `event` | meeting, conference, incident, holiday, appointment, dated occurrence |
| `group` | team, community, chat group, channel, committee |
| `document` | document, page, email, article, publication, podcast, video, URL |
| `account` | account, email address, phone number, username, identifier |
| `concept` | topic, idea, skill, role, field, anything else |

The owner (you) is a person entity created at sign-up. "The user" always resolves to the owner; "I" and "me" do when the text is in the owner's voice (`speaker` `user`).

### Aliases

An entity can have any number of aliases: other names, email addresses, phone numbers and @handles. Identifiers match regardless of type, so `carol@wynnes.com`, "Carol Seah" and "C. Seah" can all land on one person. Aliases are learned automatically (a name that fuzzy-resolved to a stored entity, identifiers in extracted attributes, the dropped name in a merge) and can be added explicitly with `add_alias` / `AddAlias`.

### Relationships

Directed, typed edges read source-verb-target. Each curated verb allows certain entity types at each end:

| Verb | Meaning |
|------|---------|
| `works_at` | is employed by or works for |
| `member_of` | belongs to a group, team, organisation or project |
| `leads` | heads, manages or runs |
| `founded` | founded or co-founded |
| `reports_to` | has as their manager *(one current target)* |
| `knows` | knows, is a friend, colleague or contact of |
| `family_of` | is a relative or partner of (label says which) |
| `communicated_with` | messaged, emailed, called, met or invited |
| `lives_in` | lives in *(one current target)* |
| `located_in` | is based or situated in |
| `part_of` | is a part, subsidiary or component of |
| `owns` | owns or holds |
| `uses` | uses, runs on, subscribes to |
| `created` | created, wrote, built or published |
| `participated_in` | took part in, attended, works on |
| `partnered_with` | partners or collaborates with |
| `acquired` | bought or acquired |
| `invested_in` | invested in or funded |
| `paid` | paid, sent money to or spent at |
| `about` | is about, discusses or mentions |
| `related_to` | related in another way (label says how) |

Synonyms map onto these, swapping the ends where the synonym reads backwards (`employs` → `works_at` reversed). An unknown verb, or one whose ends don't fit, is stored as `related_to` with the original as the `label` attribute.

Edges are unique per (source, target, verb): asserting one again increments its `evidence_count` and refreshes `last_seen_at`. They have validity windows (`valid_at`, `invalid_at`): reads return what holds now unless asked for history, and for single-valued verbs a new target closes the previous edge rather than contradicting it.

### Memories

Distilled facts ("Carol prefers async communication"), linked to the entities they are about and searched by keyword and by meaning. Each carries:

- **confidence** (0–1), set by the extractor: 1.0 for stated facts, lower for inferences;
- **static:** durable identity facts and preferences, exempt from decay and always in a profile's static half;
- **forget_after:** an expiry for time-bound facts ("dentist tomorrow"), resolved to a date at ingest;
- **validity:** `valid_at` / `invalid_at` (when it was true) and `expired_at` (when the system retired it); retired memories drop out of recall but stay reachable with `as_of` / `include_invalid`;
- **access:** how often recall surfaced it to an agent, which slows its decay and lifts it slightly in ranking.

Memories can also be linked to each other: `supersedes` (from reconcile), `derives` and `extends` (from relate).

### Chunks

The raw text, split at about 6,000 characters and searchable by keyword and meaning, with metadata such as source and part numbers.

---

---

## Ingestion

The MCP `remember` tool runs one pipeline:

1. **Extract.**
   - The hybrid extractor runs deterministic parsing (frontmatter, wikilinks) and the user's LLM.
   - Long text is split and extracted in chunks concurrently, then merged. A failing chunk does not discard the rest.
   - The LLM is given the curated vocabulary. It returns entities (with attributes such as email or phone), relationships, and memories that each name the entities they are `about`.
2. **Resolve entities.** Each extracted entity goes through a cascade before anything is created:
   1. an alias (identifiers match any type);
   2. the owner, matched by "the user", by first person in the owner's voice, or by the owner's name or email;
   3. the same name ignoring case, with the same kind of type;
   4. fuzzy name similarity, nudged towards entities one hop from the owner or from entities already resolved in the same text; a clear winner links;
   5. if still unclear, an LLM picks among the top candidates using the source text.

   A match absorbs the extracted attributes without overwriting stored ones. Uncertain near-misses are created as new entities and appear in `lint` for review.
3. **Store relationships**, mapped onto the vocabulary and upserted with evidence counts.
4. **Store chunks and memories.** Each memory is linked to its `about` entities, and everything is encrypted and embedded in batches. If embedding fails, the save still succeeds; maintenance backfills the missing vectors.

Content larger than about 12,000 characters is saved straight away and processed in the background. Facts appear within minutes, and the Memories page shows "Processing" until they do.

For facts an assistant already knows structurally, such as an email's sender and recipients or a calendar invite, it can skip extraction:
- `link_entities` records the relationship directly, resolving or creating each end;
- `add_alias` records identifiers.

---

## Recall

1. **Decompose.** The user's LLM splits the question into sub-queries: memory lookup, memory vector, keyword, vector, graph traversal and temporal filter.
2. **Seed from the graph.** Word n-grams of the query that equal an entity name or alias become seeds (up to five). Their memories join the fusion, so a memory about a named entity is found even with no keyword or vector overlap.
3. **Search** the user's decrypted working set in memory:
   - memories by keyword (an IDF score normalised by length) and by cosine similarity;
   - chunks by BM25 and by cosine similarity.
4. **Fuse.** Ranked lists are merged with reciprocal rank fusion (`sum of 1/(60 + rank)`). Each result's score is then multiplied by:
   - its confidence;
   - a graph proximity boost of x1.5, x1.25 or x1.1 for a link to an entity 0, 1 or 2 hops from a seed (never walking through the owner);
   - a small, capped lift for memories recalled before.

   Optional MMR reranking spreads out near-duplicates.
5. **Filter** by `min_confidence` and by time. By default only currently valid memories and edges are returned. `as_of` reads the graph as it was, and `include_invalid` adds retired ones.
6. **Strength and abstention.** Recall returns the best query-to-result cosine as `strength`, and sets `abstain` when it is too weak to rely on. Out-of-domain questions then get "I don't know" instead of the nearest irrelevant match.

`recall` and `context` record which memories they returned, which slows those memories' decay.

---

## Keeping the graph healthy

When you or an assistant uses cortex and maintenance has not run for a day, it runs in the background for that user:

| Pass | What it does |
|------|--------------|
| reconcile | finds newer memories that contradict older ones about the same entity, and soft-invalidates the stale ones |
| relate | records `derives` and `extends` edges between related memories |
| decay | exponential confidence decay (90-day half-life) from creation or last use; retires memories below a floor; static memories are exempt |
| expire | retires memories past their `forget_after` date |
| profile | refreshes the cached digest of you and tracked entities |
| backfill | embeds anything saved while the provider was unavailable |

Other tools, over MCP or the web app:
- **`lint`** is a read-only report: orphan entities, possible duplicates, unlinked memories, low-confidence memories.
- **`merge`** folds one entity into another. Relationships, memory links, chunks and aliases move across; attributes are unioned with the kept entity winning; and the dropped name becomes an alias.
- **Compaction** proposes merging near-duplicate memories, and you approve it from the preview.

Forgets, merges and compactions are soft and batched. Restore them from the Deleted page within 30 days. A key-free nightly job on the server purges deletions older than 30 days and expired sessions and tokens. It never reads content.

---

## MCP tools

| Tool | Description | Required | Optional |
|------|-------------|----------|----------|
| `context` | Start-of-conversation context: the owner's profile plus memories relevant to a question | `question` | |
| `remember` | Store text; large text is processed in the background | `content` | `source`, `speaker` |
| `recall` | Multi-strategy search with `strength` and `abstain` | `query` | `limit`, `min_confidence`, `as_of`, `include_invalid`, `rerank` |
| `forget` | Remove knowledge (recoverable for 30 days) | one of `entity_id`, `source`, `memory_id`, `chunk_id` | |
| `who_is` | Resolve a name, alias, email, phone or handle; aliases, current relationships, memories | `name` | |
| `connections` | Shortest chain of current relationships between two entities | `from`, `to` | `max_hops` |
| `timeline` | An entity's history, including ended relationships and retired memories | `entity` | `since`, `limit` |
| `what_changed` | Entities, memories and relationships added or retired since a time | `since` | `limit` |
| `add_alias` | Record another identifier for an entity | `entity`, `value` | `kind` |
| `link_entities` | Record a known relationship directly, creating ends that match nothing | `source`, `type`, `target` | `source_type`, `target_type`, `provenance` |
| `profile` | Cached static + dynamic digest; owner by default | | `entity_id` |
| `get_entity` | Entity by ID | `id` | |
| `find_entities` | Entities by type, name pattern or source | | `type`, `name`, `source` |
| `get_relationships` | An entity's current relationships | `entity_id` | `type`, `include_invalid` |
| `traverse` | Breadth-first walk from an entity | `start_id` | `depth`, `edge_types` |
| `search` | Direct keyword, vector or memory search, no decomposition | `query`, `mode` | `limit` |
| `merge` | Merge two entities | `keep_id`, `drop_id` | `dry_run` |
| `lint` | Cleanup report | | `format` |

Entity arguments of the graph tools (`connections`, `timeline`, `add_alias`, `link_entities`) take an ID or a name. When remembering text in the owner's own voice, pass `speaker: "user"`; text written by someone else should not carry it.

---

## Running your own server

Cortex is designed for a small Linux server with Postgres on the same host. It runs comfortably on the smallest DigitalOcean droplet (512 MB). The setup is scripted:

```bash
# once, on the server
sudo ./scripts/setup-server.sh --app-host cortex.example.com --email you@example.com

# each release, from your workstation (deploy as root)
./scripts/deploy-app.sh root@cortex.example.com
```

`setup-server.sh --app-host` sets up the server:
- installs Postgres 17 + pgvector, with Postgres listening on localhost only;
- installs Caddy for HTTPS and a `ufw` firewall allowing 22, 80 and 443;
- adds a `cortex.service` systemd unit and a nightly housekeeping timer;
- writes `/etc/cortex/cortex.env`.

`deploy-app.sh` deploys a release:
- builds the binary and takes a database dump;
- migrates and restarts the service;
- checks `/healthz` and that `/mcp` rejects requests without a token.

`scripts/rollback-app.sh` restores the previous binary and dump.

The full runbook is [docs/ops/deploy.md](docs/ops/deploy.md). It covers Google OAuth setup, connecting assistants, backups, resizing and an SSH-tunnel option for clients on filtering networks.

Service settings in `/etc/cortex/cortex.env`:

| Variable | Purpose |
|----------|---------|
| `CORTEX_DATABASE_URL` | Postgres URL |
| `CORTEX_PUBLIC_URL` | public base URL, e.g. `https://cortex.example.com` (OAuth issuer, passkey origin) |
| `CORTEX_ADDR` | listen address behind Caddy (the setup script sets `127.0.0.1:8443`; default `127.0.0.1:8080`) |
| `GOOGLE_OAUTH_CLIENT_ID`, `GOOGLE_OAUTH_CLIENT_SECRET` | Google sign-in |
| `CORTEX_BOOTSTRAP_EMAIL` | the Google account that becomes the first user (the owner) |
| `CORTEX_ALLOW_PRIVATE_PROVIDERS` | `1` lets users point at private-network provider URLs (off by default; see the provider URL policy in the runbook) |
| `CORTEX_ENCRYPTION_ENROLL` | passkey enrollment; leave on (`1`) |
| `CORTEX_SECRET_KEY` | checked by the deploy preflight; no longer protects user data |

Passkeys are bound to the server's domain, so pick the host name before users enroll.

The `cortex` binary also has a few admin commands for the operator. None of them can read an encrypted user's content:

```
cortex serve [--addr host:port]   web app, OAuth and /mcp on one listener
cortex migrate [--timeout 30m]    apply database migrations
cortex users list                 users and metadata (no content)
cortex maintain                   key-free housekeeping: purge old deletions, expired sessions and tokens
cortex init-schema [dir]          write CORTEX.md, the agent contract
```

---

## Storage

Postgres 17 with pgvector 0.8 or later. The schema is created by embedded, numbered migrations (`internal/store/migrations`) on start or with `cortex migrate`. Every row carries a `user_id`, and content columns hold only ciphertext.

| Table | Holds |
|-------|-------|
| `entities`, `entity_aliases` | nodes and their other names and identifiers (encrypted, with blind indexes for exact match) |
| `relationships` | edges with evidence count and validity window (type and attributes encrypted) |
| `memories`, `memory_entities`, `memory_edges` | facts with confidence, static, forget_after, validity and access counts; links to entities and to each other |
| `chunks` | raw text and metadata (encrypted) |
| `embeddings` | vectors (encrypted) |
| `user_key_wraps`, `webauthn_credentials` | the user key, wrapped per passkey, recovery key, session, token and OAuth grant |
| `delete_batches`, `delete_batch_changes` | soft deletes and merges, for restore (snapshots encrypted) |
| `users`, `user_settings`, `connections`, `agent_tokens`, `oauth_*`, `sessions` | accounts, provider settings (encrypted) and auth |
| `jobs`, `compactions`, `sync_state` | background work, compaction previews, state (payloads encrypted) |
| `schema_migrations`, `cortex_meta` | applied migrations and bookkeeping |

IDs are ULIDs.

---

## Development

Requires Go 1.25 or later and Docker (for the test database).

```bash
make build        # bin/cortex
make test         # starts a throwaway pgvector container on :55432, runs go test ./...
make test-v       # verbose
make test-cover   # coverage.html
make vet
```

Database tests need `CORTEX_TEST_DATABASE_URL`, which `make test` sets (`make test-db` just starts the container). Without it they are skipped. Each test gets its own schema. No API keys are needed, because providers are mocked. Live provider tests: `OPENAI_API_KEY=... go test ./llm/openai/` and `ANTHROPIC_API_KEY=... go test ./llm/anthropic/`. Retrieval quality is measured with `cortex-eval`.

The Go package `github.com/sausheong/cortex` is the server's core, not a standalone embedded library. Every read and write needs an enrolled user's key, and only the server's request path supplies one. Integrate with cortex over MCP instead.

### Repository layout

The root package `cortex` is the public API only. It holds `doc.go`, `cortex.go` (the `Cortex` type, `Open` and handle methods), `options.go`, `types.go` (type aliases, constants, sentinel errors), three grouped method files (`api_memory.go`, `api_graph.go`, `api_maintenance.go`) and `example_test.go` (godoc examples). Methods are thin forwarders; domain types are type aliases (for example `type Memory = model.Memory`), so existing imports keep compiling. All implementation sits under `internal/`.

Tests live in two places. `test/integration` holds the black-box suite (package `integration_test`, shared helpers in `helpers_test.go`); it reaches the store handle through `internal/testutil.H`. White-box tests sit next to the code in the `internal/*` packages.

| Path | Role |
|------|------|
| `internal/model` | Shared domain types |
| `internal/ontology`, `internal/vecmath`, `internal/llmutil` | Ontology rules, vector math, LLM helpers |
| `internal/store` | Database access, migrations, ids, soft delete, usage |
| `internal/tenancy` | Per-user scoping |
| `internal/graph` | Entities, aliases, relationships, traversal |
| `internal/memory` | Memories, chunks, memory edges |
| `internal/query` | Recall and search |
| `internal/maintain` | Reconcile, relate, decay, expire, profiles, lint |
| `internal/ingest` | Ingestion pipeline |
| `internal/render` | Rendering of entity cards and profiles |
| `internal/testutil`, `internal/testhook`, `internal/pgtest` | Test-only helpers (handle access, per-test schemas) |
| `test/integration` | Black-box integration tests |
| `internal/accounts`, `internal/jobs`, `internal/web` | Hosted multi-user service |
| `cmd/cortex`, `cmd/cortex-eval` | CLI, MCP server and eval tool |
| `extractor/`, `llm/`, `connector/`, `vault/` | Public extension packages |
| `eval/` | Retrieval evaluation harness |
| `examples/sample-notes` | Sample vault |
| `docs/design` | Design notes: `CORTEX.md`, improvement research |
| `docs/ops` | Operations: `deploy.md` |
| `docs/superpowers` | Specs and plans |

`PRODUCT.md`, `DESIGN.md` and `.impeccable/` stay at the root for the design tooling. Run `make test` before sending changes.
