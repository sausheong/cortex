# Cortex

A personal knowledge graph and memory system in Go. Cortex keeps what you know — people, organizations, projects, events, documents, and the facts about them — as one graph with typed relationships. It extracts entities and facts from notes, conversations and documents, resolves them to the things you already know about, and lets any AI agent read and write the graph over MCP, the CLI or the Go API.

Agents recall from cortex before answering and remember into it afterwards, so knowledge compounds: the next time a person comes up, the agent already has context.

Inspired by [GBrain](https://github.com/garrytan/gbrain) (operational model), [Cognee](https://github.com/topoteretes/cognee) (graph structure), [mem0](https://github.com/mem0ai/mem0) (memory pipeline) and Atlassian's Teamwork Graph (curated vocabulary, stable identity, graph signals in ranking).

## How it works

```
Remember  "Had coffee with Carol Seah from WYNNES; she's moving to Singapore"
  -> extract (deterministic + LLM, long text in chunks)
       entities       Carol Seah (person), WYNNES (organization), Singapore (place)
       relationships  Carol Seah works_at WYNNES, Carol Seah lives_in Singapore
       memories       "Carol Seah is moving to Singapore"  about [Carol Seah]
  -> resolve each entity to one you already know (alias, owner, name, fuzzy match, LLM tie-break)
  -> map types and verbs onto the curated vocabulary
  -> store; lives_in Singapore closes Carol's previous lives_in edge
  -> embed chunks and memories for semantic search

Recall  "what's Carol up to?"
  -> seed from the entities the query names (Carol Seah)
  -> search in parallel: memories (keyword + vector), chunks (keyword + vector),
     the seeds' own memories, optional graph traversal
  -> reciprocal rank fusion, weighted by confidence, graph proximity and past use
  -> ranked results with provenance, plus a strength score and an abstain hint

Maintain (nightly)
  reconcile -> relate -> decay -> expire -> refresh profiles, then purge old deletions
```

## Contents

- [Features](#features)
- [Quick start](#quick-start)
- [Hosted multi-user service](#hosted-multi-user-service)
- [Knowledge model](#knowledge-model)
- [Ingestion](#ingestion)
- [Recall](#recall)
- [Keeping the graph healthy](#keeping-the-graph-healthy)
- [CLI reference](#cli-reference)
- [MCP server](#mcp-server)
- [Go library](#go-library)
- [Providers and configuration](#providers-and-configuration)
- [Storage](#storage)
- [Development](#development)

## Features

- **One graph** of entities, relationships, memories (distilled facts) and chunks (raw text), in Postgres 17 + pgvector.
- **Curated vocabulary** — ten entity types and 21 directional relationship verbs. Whatever an extractor invents is mapped onto them, keeping the original label, so the graph stays queryable.
- **Stable identity** — aliases (other names, emails, phones, handles) and a resolution cascade that reuses known entities instead of creating near-duplicates, anchored on the owner.
- **Per-memory links** — each memory is linked to the entities it is about, not to everything mentioned alongside it.
- **Graph-aware recall** — entities named in a query seed the search; results near them rank higher. Multi-strategy retrieval merged by reciprocal rank fusion, with calibrated abstention.
- **Time** — memories and relationships carry validity windows; recall can read the graph as it was (`as_of`) or include what is no longer true.
- **Self-maintaining** — reconcile contradictions, relate memories, decay what nobody uses, expire time-bound facts, refresh profiles, all reversible.
- **Curated graph questions** — `who_is`, `connections`, `timeline`, `what_changed` over MCP.
- **Undoable** — merges, forgets, compactions and the vocabulary normalisation are recorded as batches you can restore for 30 days.
- **Interfaces** — CLI, MCP (stdio or HTTP), a multi-user web app with Google sign-in and OAuth, and the Go library.
- **Pluggable providers** — OpenAI and any OpenAI-compatible API (Ollama, vLLM, …), Anthropic, or your own.

---

## Quick start

Single-user, local. Requires **Go 1.25+**, a **Postgres 17 + pgvector ≥ 0.8** database, and optionally an OpenAI (or compatible) API key.

```bash
git clone https://github.com/sausheong/cortex.git && cd cortex
make build                       # bin/cortex; `sudo make install` copies it to /usr/local/bin

export CORTEX_DATABASE_URL='postgres://cortex:PASSWORD@localhost:5432/cortex'
export OPENAI_API_KEY=sk-...     # optional: enables LLM extraction and semantic search

cortex init                      # apply migrations, create you as the owner entity
cortex remember "Alice works at Stripe as a staff engineer"
cortex sync ~/notes              # incremental; .md .csv .tsv .yaml .json .txt .xml .toml .log
cortex recall "who works at Stripe"
```

Without an API key cortex still runs, limited to deterministic extraction (YAML frontmatter and `[[wikilinks]]`) and keyword search. To migrate a v0.4 SQLite brain: `cortex import-sqlite <brain.db>`.

Hook it up to an AI client with `cortex mcp` ([MCP server](#mcp-server)), and drop the agent contract into a project with `cortex init-schema`.

---

## Hosted multi-user service

`cortex serve` runs the web app, Google sign-in, OAuth 2.1 and `/mcp` for several users on one server, each with an isolated graph and their own AI provider settings (set on the Settings page or with `cortex users set-provider`; the operator's key is never used for users).

The web app has pages for:

- **Home** — set-up checklist and assistant status.
- **Memories** — search, filter, bulk delete, compaction (merge near-duplicate memories, undoable), and **Deleted** to restore any batch.
- **Graph** — interactive map; rename, merge and delete entities.
- **Vault** — the graph as browsable pages, downloadable as an Obsidian vault.
- **Connections** — MCP address, per-assistant tokens, revoke and rename.
- **Settings / Account** — provider, re-embedding, storage, account deletion.

Deployment to a small server (Postgres on localhost, Caddy for HTTPS, a nightly maintenance timer) is scripted: `scripts/setup-server.sh --app-host <fqdn>` once, then `scripts/deploy-app.sh root@<fqdn>` for each release. Every deploy takes a database dump before migrating; `scripts/rollback-app.sh` restores it. See [docs/ops/deploy.md](docs/ops/deploy.md).

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

The owner — you — is a person entity created by `cortex init` (or at sign-up). "The user" always resolves to the owner; "I" and "me" do when the text is in the owner's voice (`speaker` `user`).

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
- **static** — durable identity facts and preferences, exempt from decay and always in a profile's static half;
- **forget_after** — an expiry for time-bound facts ("dentist tomorrow"), resolved to a date at ingest;
- **validity** — `valid_at` / `invalid_at` (when it was true) and `expired_at` (when the system retired it); retired memories drop out of recall but stay reachable with `as_of` / `include_invalid`;
- **access** — how often recall surfaced it to an agent, which slows its decay and lifts it slightly in ranking.

Memories can also be linked to each other: `supersedes` (from reconcile), `derives` and `extends` (from relate).

### Chunks

The raw text, split at about 6,000 characters, indexed for keyword and vector search, with metadata such as source and part numbers.

---

## Ingestion

`Remember` (CLI `remember`, MCP `remember`, connectors) runs one pipeline:

1. **Extract.** The hybrid extractor runs deterministic parsing (frontmatter, wikilinks) and the LLM. Long text is split and extracted in chunks concurrently, then merged; a failing chunk does not discard the rest. The LLM is given the curated vocabulary and returns entities (with attributes such as email or phone), relationships, and memories each naming the entities they are `about`.
2. **Resolve entities.** Each extracted entity goes through a cascade before anything is created:
   1. an alias (identifiers match any type);
   2. the owner, by "the user", by first person in the owner's voice, or by the owner's name or email;
   3. the same name ignoring case, same kind of type;
   4. fuzzy name similarity, nudged towards entities one hop from the owner or from entities already resolved in the same text — a clear winner links;
   5. if still unclear and the provider supports it, an LLM picks among the top candidates using the source text.

   A match absorbs the extracted attributes without overwriting stored ones. Uncertain near-misses are created as new entities and show up in `cortex lint` for review.
3. **Store relationships**, mapped onto the vocabulary, upserted with evidence counts.
4. **Store chunks and memories**, link each memory to its `about` entities, and embed in batches. If embedding fails the save still succeeds; nightly maintenance backfills missing vectors.

Over MCP, content larger than about 12,000 characters is saved and processed in the background; facts appear within minutes.

For facts you already know structurally — an email's sender and recipients, a calendar invite — skip extraction: `link_entities` / `Link` records the relationship directly, resolving or creating each end, and `add_alias` / `AddAlias` records identifiers.

### Connectors

- **Files** — `cortex sync <dir>` ingests supported text files recursively, re-processing only files changed since the last sync. Markdown frontmatter (`type:`, `name:`, other keys as attributes) and `[[wikilinks]]` are parsed deterministically.
- **Conversation** — `conversation.New().Ingest(ctx, c, messages)` remembers chat messages with role prefixes; use it in an agent's post-response hook.

Custom extractors implement `Extractor` (`Extract(ctx, content, contentType) (*Extraction, error)`); custom connectors implement `Connector` (`Sync(ctx, c) error`).

---

## Recall

1. **Decompose.** The LLM splits the question into sub-queries (`memory_lookup`, `memory_vector`, `keyword_search`, `vector_search`, `graph_traverse`, `temporal_filter`). Without an LLM, recall runs memory lookup, memory vector, keyword and vector search on the raw query.
2. **Seed from the graph.** Word n-grams of the query that equal an entity name or alias become seeds (up to five). Their own memories join the fusion as an extra ranked list, so a memory about a named entity is found even with no keyword or vector overlap. Turn this off with `WithGraph(false)`.
3. **Fuse.** Ranked lists are merged with reciprocal rank fusion (`Σ 1/(60 + rank)`), then each result's score is multiplied by its confidence, a graph proximity boost (×1.5 / 1.25 / 1.1 for a link to an entity 0 / 1 / 2 hops from a seed, never walking through the owner), and a small, capped lift for memories recalled before (at most +15%). Optional MMR diversity reranking (`rerank`) spreads out near-duplicates for browsing; it is off by default because it hurts fact recall.
4. **Filter** by `min_confidence`, `source`, and time: by default only currently valid memories and edges; `as_of` reads the graph as it was; `include_invalid` adds retired ones.
5. **Strength and abstention.** `RecallWithStrength` returns the best query↔result cosine as `Strength` and sets `Abstain` below 0.40, calibrated on a real graph so out-of-domain questions abstain instead of returning the nearest irrelevant match. Counterfactual questions close to real facts still need an answer-level check.

The MCP `recall` and `context` tools record which memories they returned (`WithTrackAccess`); library and benchmark callers don't unless asked.

---

## Keeping the graph healthy

`cortex maintain` runs the nightly pass. On a server (`--user` omitted) it covers every user with provider settings, then purges deletions older than 30 days.

| Pass | What it does | Standalone |
|------|--------------|-----------|
| reconcile | finds newer memories that contradict older ones about the same entity; soft-invalidates the stale ones (needs an LLM) | `cortex reconcile [--apply]` (dry run by default; `--json` / `--apply --from` for reviewed apply) |
| relate | records `derives` / `extends` edges between related memories (needs an LLM) | `cortex relate` |
| decay | exponential confidence decay (90-day half-life) from creation or last use; soft-retires memories below the floor; static memories exempt | `cortex decay [--dry-run]` |
| expire | soft-retires memories past their `forget_after` | `cortex expire [--dry-run]` |
| profile | refreshes the cached static/dynamic digest of the owner and tracked entities | `cortex profile [--track <id>]` |

Other tools:

- **`cortex lint`** — read-only report: orphan entities, entities without memories, possible duplicates (same kind, same or similar name, with a similarity score), dead sources, unlinked memories, optionally low-confidence memories.
- **`cortex merge <keep> <drop> [--dry-run]`** — folds one entity into another: relationships, memory links, chunks and aliases move, duplicates collapse, attributes union (keep wins), the dropped name becomes an alias, provenance goes in `merged_from`. Undoable.
- **`cortex normalize [--dry-run]`** — one-off migration of an existing graph onto the curated vocabulary: retypes entities and relationships, merges entities that then share a name and kind (and namesakes of the owner into the owner), folds edges that become identical. One undoable batch; run the dry run first.
- **`cortex export [--vault <dir>]`** — writes the graph as an Obsidian-compatible markdown vault (frontmatter, wikilinks, backlinks), incrementally.
- **`cortex init-schema [<dir>]`** — writes `CORTEX.md`, a contract telling agents when to recall, when to remember and how to use the graph tools.

Forgets, merges, compactions and normalisations are soft and batched: restore them from the web app's Deleted page or `RestoreBatch` within 30 days.

To schedule maintenance yourself (single-user installs), run `cortex maintain --user <id>` from cron or launchd, e.g. nightly at 03:17; preview with `--dry-run`. Reconcile and relate are skipped without an LLM provider; decay and expire still run.

---

## CLI reference

```
Usage: cortex [--db <postgres-url>] [--user <id|verified-email>] <command> [arguments]

  --db      Postgres URL (or CORTEX_DATABASE_URL)
  --user    act as that user with their provider settings (needs CORTEX_SECRET_KEY);
            accepted by maintain, recall, profile, import-sqlite, sync and normalize

Service
  serve [--addr host:port]           multi-user web, OAuth and MCP service
  mcp [--transport stdio|http] [--addr host:port]
                                     MCP server
  migrate [--timeout 30m]            apply database migrations
  users list                         list users
  users set-provider --user <ref> [--provider openai-compatible|anthropic] [--base-url URL]
                     [--chat-model M] [--embed-model M] [--embed-dims N] [--embed-base-url URL]
                                     set a user's AI provider (key from stdin or env, never argv)

Graph
  init                               apply the schema and set the owner
  config [--name N] [--nickname N] [--email E]
                                     show or update the owner identity
  remember <text>                    remember text
  recall <query> [--min-confidence <0-1>]
  sync <dir>                         sync a directory of text files
  entity list [--type T] | entity get <id>
  forget --source <src> | --entity <id>
  profile [<entity-id>] [--track <id>] [--untrack <id>] [--json]
  export [--vault <dir>] [--full] [--dry-run]
  import-sqlite <path> [--force] [--embed-only]
  init-schema [<dir>] [--force]

Maintenance
  maintain [--user <id> [--dry-run] [--no-reconcile] [--no-relate] [--no-decay]
           [--half-life <dur>] [--floor <0-1>] [--out <file>]]
  reconcile [--apply] [--out <file>] [--json <file>] [--from <file>]
  relate [--out <file>]
  decay [--half-life <dur>] [--floor <0-1>] [--dry-run] [--out <file>]
  expire [--dry-run] [--out <file>]
  lint [--low-confidence] [--low-confidence-threshold <0-1>] [--out <file>]
  merge <keep-id> <drop-id> [--dry-run]
  normalize [--dry-run] [--out <file>]
```

`cortex <command>` with bad arguments prints its own usage.

---

## MCP server

### Connecting

Local, over stdio (Claude Code, Cursor, Windsurf):

```json
{
  "mcpServers": {
    "cortex": {
      "command": "/path/to/cortex",
      "args": ["mcp"],
      "env": {
        "CORTEX_DATABASE_URL": "postgres://cortex:PASSWORD@host:5432/cortex",
        "OPENAI_API_KEY": "sk-..."
      }
    }
  }
}
```

Over HTTP: `cortex mcp --transport http [--addr 127.0.0.1:8080]` serves `/mcp` and `/healthz`; set `CORTEX_AUTH_TOKEN` to require `Authorization: Bearer <token>` (always set it beyond localhost). Against a hosted service:

```bash
claude mcp add --transport http cortex https://cortex.example.com/mcp \
  --header "Authorization: Bearer <token>"
```

### Tools

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

## Go library

```go
import (
    "github.com/sausheong/cortex"
    "github.com/sausheong/cortex/extractor/deterministic"
    "github.com/sausheong/cortex/extractor/hybrid"
    "github.com/sausheong/cortex/extractor/llmext"
    oaillm "github.com/sausheong/cortex/llm/openai"
)

llm := oaillm.NewLLM("sk-...")
c, err := cortex.Open("postgres://cortex:PASSWORD@host:5432/cortex",
    cortex.WithLLM(llm),
    cortex.WithEmbedder(oaillm.NewEmbedder("sk-...")),
    cortex.WithExtractor(hybrid.New(deterministic.New(), llmext.New(llm))),
)
if err != nil { ... }
defer c.Close()

c.Remember(ctx, "Alice works at Stripe as a staff engineer", cortex.WithSource("notes"))

rr, _ := c.RecallWithStrength(ctx, "who works at Stripe", cortex.WithLimit(10))
if !rr.Abstain {
    for _, r := range rr.Results { fmt.Println(r.Type, r.Content, r.Score) }
}

card, _ := c.WhoIs(ctx, "alice@stripe.com")               // resolve + describe
path, _ := c.Path(ctx, aliceID, bobID, 4)                  // how two entities connect
events, _ := c.Timeline(ctx, aliceID, time.Time{}, 50)     // history, oldest first
```

`Open` applies migrations. A handle is scoped to one user; `ForUser(id, ...opts)` returns another user's handle on the same database.

### API

| Area | Methods |
|------|---------|
| Memory | `Remember`, `Recall`, `RecallWithStrength`, `Forget` |
| Entities | `PutEntity`, `GetEntity`, `FindEntities`, `MergeEntities`, `MergeEntitiesDryRun`, `Describe` |
| Identity | `ResolveEntity`, `WhoIs`, `AddAlias`, `Aliases`, `RemoveAlias` |
| Relationships | `PutRelationship`, `GetRelationships`, `InvalidateRelationship`, `Link`, `Traverse`, `Path` |
| History | `Timeline`, `WhatChanged` |
| Memories | `PutMemory`, `GetMemoriesByEntity`, `InvalidateMemory`, `PutMemoryEdge`, `GetMemoryEdges` |
| Search | `SearchKeyword`, `SearchVector`, `SearchMemories` |
| Maintenance | `Reconcile`, `BuildMemoryEdges`, `DecayConfidence`, `Profile`, `RefreshProfiles`, `Lint`, `NormalizeGraph` |
| Undo | `ListDeleteBatches`, `RestoreBatch`, `PurgeDeleted` |

Common options:

```go
// Remember
cortex.WithSource("notes"); cortex.WithSpeaker("user"); cortex.WithContentType("markdown"); cortex.WithMaxChunkChars(6000)
// Recall
cortex.WithLimit(10); cortex.WithMinConfidence(0.7); cortex.WithSourceFilter("notes")
cortex.WithAsOf(t); cortex.WithIncludeInvalid(); cortex.WithRerank(true); cortex.WithGraph(false); cortex.WithTrackAccess()
// Relationships and traversal
cortex.RelTypeFilter("works_at"); cortex.RelAsOf(t); cortex.RelIncludeInvalid()
cortex.WithDepth(2); cortex.WithEdgeTypes("works_at", "knows"); cortex.WithTraverseAsOf(t)
// Open
cortex.WithLLM(l); cortex.WithEmbedder(e); cortex.WithExtractor(x); cortex.WithLogger(logf)
```

Without an LLM, `cortex.Open(dsn, cortex.WithExtractor(deterministic.New()))` works on frontmatter and wikilinks alone.

---

## Providers and configuration

| Interface | Methods | Shipped |
|-----------|---------|---------|
| `LLM` | `Extract`, `Decompose`, `Summarize` | OpenAI / OpenAI-compatible (default `gpt-5.4-mini`), Anthropic (default Claude Sonnet 4.5) |
| `Embedder` | `Embed`, `Dimensions` | OpenAI / compatible (default `text-embedding-3-small`, 512 dims) |
| `Extractor` | `Extract` | deterministic, LLM, hybrid |

Optional LLM capabilities, detected by type assertion: `Reconciler` (reconcile), `RelationDetector` (relate) and `EntityDisambiguator` (the resolution tie-break). Both shipped LLMs implement all three. Anthropic has no embeddings API, so pair it with another embedder.

```go
oaillm.NewLLM("ollama", oaillm.WithBaseURL("http://localhost:11434/v1"), oaillm.WithModel("llama3"))
anthropicllm.NewLLM("sk-ant-...", anthropicllm.WithModel("claude-haiku-4-5"))
```

### Environment variables

| Variable | Default | Purpose |
|----------|---------|---------|
| `CORTEX_DATABASE_URL` | *(required)* | Postgres URL |
| `LLM_PROVIDER` | `openai` | `openai` or `anthropic` |
| `LLM_MODEL` | provider default | chat model |
| `OPENAI_API_KEY`, `OPENAI_BASE_URL` | | OpenAI or compatible API |
| `ANTHROPIC_API_KEY`, `ANTHROPIC_BASE_URL` | | Anthropic or compatible API |
| `EMBEDDING_API_KEY`, `EMBEDDING_BASE_URL` | fall back to the OpenAI values | embedding API |
| `EMBEDDING_MODEL` | `text-embedding-3-small` | embedding model |
| `EMBEDDING_DIMS` | `512` | vector size, fixed once the database is initialised |
| `CORTEX_TRANSPORT`, `CORTEX_ADDR` | `stdio`, `127.0.0.1:8080` | `cortex mcp` transport and address |
| `CORTEX_AUTH_TOKEN` | | bearer token for HTTP MCP |
| `CORTEX_SECRET_KEY` | | encrypts per-user provider keys (`serve`, `--user`) |

The provider variables configure single-user CLI and MCP use. On the multi-user service each user's settings hold their provider; see [docs/ops/deploy.md](docs/ops/deploy.md) for the service's variables.

---

## Storage

Postgres 17 with pgvector ≥ 0.8. The schema is created by embedded, numbered migrations on `Open` (or `cortex migrate`). Every graph row carries a `user_id`.

| Table | Holds |
|-------|-------|
| `entities` | nodes: type, name, JSON attributes, source, confidence; unique per user on (name, type) among live rows |
| `entity_aliases` | other names and identifiers per entity |
| `relationships` | edges: verb, attributes, confidence, evidence count, validity window; unique per (source, target, verb) among live rows |
| `memories` | facts with confidence, static, forget_after, validity, access counts; `tsvector` for keyword search |
| `memory_entities`, `memory_edges` | memory→entity links; memory→memory edges |
| `chunks` | raw text with metadata; `tsvector` |
| `embeddings` | `halfvec(N)` vectors, one HNSW index per kind |
| `delete_batches`, `delete_batch_changes` | soft deletes and merges, for restore |
| `users`, `user_settings`, `connections`, `agent_tokens`, `oauth_*`, `sessions`, `login_attempts` | multi-user service and auth |
| `jobs`, `compactions`, `sync_state` | background work, compaction previews, connector state |
| `schema_migrations`, `cortex_meta` | applied migrations, embedding dimension |

IDs are ULIDs. Keyword search uses Postgres full text (`english`), vector search uses HNSW cosine indexes with iterative scans for filtered queries.

---

## Development

```bash
make build        # bin/cortex
make test         # starts a throwaway pgvector container on :55432, runs go test ./...
make test-v       # verbose
make test-cover   # coverage.html
make vet
make run-mcp      # MCP over stdio;  make run-mcp-http for HTTP
make dist         # cross-compiled release binaries
```

Database tests need `CORTEX_TEST_DATABASE_URL` (set by `make test`; `make test-db` just starts the container) and are skipped without it; each test gets its own schema. No API keys are needed — providers are mocked. Live provider tests: `OPENAI_API_KEY=... go test ./llm/openai/` and `ANTHROPIC_API_KEY=... go test ./llm/anthropic/`.

Retrieval quality is measured with `cortex-eval` (`generate` an eval set from a graph, then `run` it across configurations, including `hybrid+graph`).
