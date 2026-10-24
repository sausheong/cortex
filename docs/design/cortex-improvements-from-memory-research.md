# Improving Cortex: Lessons from supermemory, Mem0, Zep/Graphiti & the memory benchmarks

*Research synthesis, 2026-06-22. Sources: supermemory codebase + docs + research/blog; Mem0 (arxiv 2504.19413 + code); Zep/Graphiti (arxiv 2501.13956 + code); LongMemEval (2410.10813), LoCoMo (2402.17753), ConvoMem (2511.10523).*

## TL;DR

Cortex is already a competent hybrid retrieval engine (typed graph + RRF + confidence). Its **single biggest gap** is the absence of any **temporal / conflict-resolution model**: ingest is purely additive, so a fact that changes ("Alice left Google for Stripe") is stored alongside the stale one and both rank as equally true forever. Every system studied — and every benchmark — treats this as the central problem of memory.

The good news: the best-documented solution (Zep/Graphiti's two-stage conflict resolution) is ~30 lines of deterministic interval math on top of one LLM call, and it maps cleanly onto cortex's existing maintain-loop (`lint`/`merge`). Cortex also has a latent strength nobody else surfaces — **calibrated confidence at recall** — which directly enables **abstention**, a first-class benchmark capability.

There is also one near-free bug fix: **memory embeddings are written at ingest but never searched** (vector recall only queries `ref_type='chunk'`), and memory retrieval falls back to `LIKE` — so the highest-signal records get the weakest retrieval.

---

## 1. Where cortex stands today (baseline)

Confirmed by reading the source:

- **Data model** (`types.go`): `Entity` (upsert by name+type), `Relationship` (entity→entity), `Chunk` (FTS5 + embedded), `Memory` (embedded + entity links). Every record carries `Confidence` 0–1.
- **Ingest** (`Remember`, `cortex.go`): extract → store entities → relationships → chunks (embedded) → memories (embedded). **Purely additive — no reconciliation against existing knowledge.**
- **Recall** (`recall.go`): LLM decompose → parallel sub-queries (`memory_lookup`, `keyword_search`, `vector_search`, `graph_traverse`) → RRF merge (k=60) → min-confidence **hard filter** → limit.
- **Forget** (`cortex.go`): delete-by-filter (entity/source), with orphan cleanup.
- **Maintain loop**: `lint` (orphans, near-dupes, dead sources, low-confidence) + `merge` (atomic entity dedup with provenance).

### Concrete gaps found

| # | Gap | Evidence in code |
|---|-----|------------------|
| G1 | **Memory embeddings written, never read.** Vector recall only searches chunks. | `search.go:91` (`searchVectorRaw(..., "chunk", ...)`); `cortex.go:115` writes `ref_type='memory'` |
| G2 | **Memory retrieval is `LIKE`, not FTS or vector** — weakest retrieval on highest-value records. | `memory.go:55` (`content LIKE '%word%'` OR-match) |
| G3 | **No temporal model.** No `valid_at`/`invalid_at`/`isLatest`/version on memories or relationships. | `types.go:20-47` |
| G4 | **No conflict resolution on ingest.** New facts never reconciled with old. | `cortex.go:11-123` (additive) |
| G5 | **No memory→memory edges** (updates/extends/derives). Relationships connect entities only. | `types.go:20` |
| G6 | **Recall ignores recency; confidence is only a hard filter, not a ranking weight.** | `recall.go:53-73` |
| G7 | **No abstention** despite confidence being available everywhere. | — |
| G8 | **No event-vs-ingestion time distinction.** Only `CreatedAt`/`UpdatedAt` (wall-clock of ingest). | `types.go` |

### Strengths to preserve / lean into

- **Calibrated confidence at every layer** — neither Mem0 nor supermemory surfaces this at recall. It's cortex's differentiator and the key to abstention.
- **Typed knowledge graph** — richer than Mem0's *shipped* OSS graph (untyped co-occurrence, no reconciliation).
- **Maintain loop** (`lint`/`merge`) — the natural home for background reconsolidation ("dreaming").
- **Single binary, pure-Go SQLite, Obsidian projection** — keep. ConvoMem confirms brute-force is *fine* at personal scale (long/exhaustive context beats RAG up to ~150 conversations); don't over-engineer scaling.

---

## 2. What the field actually does (the mechanisms)

### Conflict resolution — the spectrum

- **Mem0**: one LLM tool-call per new fact decides **ADD / UPDATE / DELETE / NONE** against top-*s* similar memories. Contradiction → **hard DELETE**. Simple, but destroys history and trusts a single LLM call for the supersede decision. *(Note: Mem0's typed-graph "Mem0g" with conflict detection is in the paper but NOT in the shipped OSS graph, which is untyped co-occurrence with no reconciliation. Don't design from the paper.)*
- **Zep/Graphiti** (the model to copy): **two stages.** (a) LLM `resolve_edge` flags `duplicate_facts` + `contradicted_facts` (an edge can be both — "same subject, new value supersedes"). (b) **Deterministic** `resolve_edge_contradictions()` does interval-overlap math: if the old and new validity intervals overlap and the old started earlier, set `old.invalid_at = new.valid_at` and `old.expired_at = now()`. **Nothing is deleted** — old facts get invalidated, enabling "what was true as of date X."
- **supermemory**: same three outcomes cortex would want (`updates`/`extends`/`derives` + `isLatest`), resolved by an async background "Dynamic Dreaming" pass — but the *decision rule* is proprietary/black-boxed. Their genuinely-substantiated wins: atomic-memory search + source-chunk re-injection, dual-timestamp grounding, Matryoshka two-stage retrieval.

**Key design insight:** separate **detection** (LLM, fuzzy) from **resolution** (deterministic code, reproducible). Graphiti's invalidation is ~30 lines and auditable; Mem0's "trust the LLM to emit DELETE" is not.

### Bi-temporal schema (copy verbatim)

Every fact/edge carries **four** timestamps:
- **T (event time):** `valid_at` / `invalid_at` — when the fact was true in the world.
- **T' (ingestion time):** `created_at` / `expired_at` — when the system learned / retired it.

This is what supermemory calls dual-timestamp grounding (`documentDate` vs `eventDate`) and what they credit for their temporal-reasoning benchmark wins.

### Retrieval patterns worth adopting

- **Atomic-memory search + source-chunk re-injection** (supermemory's core, LoCoMo's best approach): search high-signal atomic facts, but hand the LLM the *originating chunk* for detail. Cortex already stores both and links them — it just isn't doing the two-tier handoff.
- **Fact-augmented keys** (LongMemEval, +9.4% recall / +5.4% acc): embed `value + extracted fact/entity context`, not the bare text. Index-stage merge beats retrieval-stage rank merge.
- **Reranker stage** (Graphiti): RRF → MMR / node-distance / episode-mentions. Cortex has RRF; an MMR or graph-distance rerank would cut the "more results = more noise" effect LoCoMo warns about.

### What the benchmarks collectively demand (the "why")

1. Atomic assertion-level facts (not summaries — summaries lose info in both LoCoMo & LongMemEval). ✅ cortex extracts memories.
2. Index the **assistant's own** statements, not just user input. ❌ cortex has no speaker provenance.
3. **Track updates, resolve to current value.** ❌ → G3/G4.
4. **Time-aware indexing + query-time temporal filtering** (+11% recall). ❌ → G8.
5. Multi-session / multi-hop synthesis. ◑ partial (graph traverse depth-1, decompose).
6. **Abstention** — return "not in memory" with calibrated confidence. ❌ but cortex has the raw material → G7.
7. Speaker/attribution binding. ❌
8. Fact-augmented keys. ❌
9. Right-sized granularity (don't over-compress). ✅
10. Extract-then-reason reading (Chain-of-Note/JSON, +10 pts) — agent-side, surface via `CORTEX.md` contract.
11. Control retrieval signal-to-noise. ◑ (RRF + limit; no rerank).
12. Scale-aware: don't reach for RAG prematurely. ✅ brute-force is fine here.

---

## 3. Roadmap for cortex (prioritized by impact ÷ effort)

### Tier 1 — High impact, low effort (do first)

**1.1 Fix memory retrieval (G1, G2).** Make `recallMemories` use the memory embeddings already being written (add `ref_type='memory'` vector search) and/or a `memories_fts` table, instead of `LIKE`. Nearly free; fixes the highest-signal records getting the weakest retrieval. *Backs benchmark #1.*

**1.2 Two-tier recall: atomic memory → source chunk re-injection.** When a memory ranks, attach its originating chunk (via `memory_entities`/source) so the agent gets both the crisp fact and the supporting detail. *supermemory's best-substantiated pattern; LoCoMo's winning approach.*

**1.3 Confidence-weighted ranking + abstention (G6, G7).** Fold confidence into the RRF score (weight, not just post-filter), and have `Recall` signal "low-confidence / possibly-not-known" when the top result set is weak — feed that into the `CORTEX.md` agent contract so agents abstain instead of fabricating. *Leans into cortex's unique strength; backs benchmark #6. This is a differentiator no competitor surfaces.*

### Tier 2 — High impact, medium effort (the core upgrade)

**2.1 Bi-temporal schema (G3, G8).** Add `valid_at`, `invalid_at`, `created_at`, `expired_at` to `Memory` and `Relationship`. Soft-invalidate (set timestamps) instead of delete. Migration: backfill `valid_at = created_at`, leave `invalid_at` null. Recall defaults to "currently valid" but supports point-in-time. *Copy Graphiti's schema verbatim.*

**2.2 Two-stage conflict resolution on ingest (G4).** On `Remember`, for each new memory/relationship: retrieve similar existing records → one LLM call flags `duplicate`/`contradicted` candidates → **deterministic** interval-overlap code decides supersede vs keep-both and closes the old interval. Never delete. *Graphiti's pattern; the single most important capability gap (benchmark #3).*

**2.3 Dual-timestamp extraction.** Extend the extraction prompt to emit event-time alongside ingestion-time per memory/relationship (e.g. "joined Stripe in March 2026" → `valid_at=2026-03`). Feeds 2.1.

### Tier 3 — Medium impact

**3.1 Fact-augmented embedding keys (G2).** Before embedding a memory/chunk, prepend entity + context (à la Anthropic Contextual Retrieval). +9.4% recall in LongMemEval. *Backs benchmark #8.*

**3.2 Time-aware query expansion.** Add a `temporal_filter` sub-query type: LLM extracts a time range from the query → filter on `valid_at`/`invalid_at`. +11% recall. *Backs benchmark #4.* (Guard: needs a strong LLM; degrade gracefully.)

**3.3 Speaker/source provenance (benchmark #2, #7).** Record who asserted each memory (user vs agent vs document) so attribution questions and assistant-recall work.

**3.4 Reranker stage.** Add MMR or graph-node-distance rerank after RRF to control signal-to-noise. *Backs benchmark #11.*

### Tier 4 — Reconsolidation ("dreaming") via the maintain loop

**4.1 Extend `lint`/`merge` into a background reconsolidation pass:** resolve deferred contradictions, build memory→memory edges (`derives`/`extends`, G5), reweight/prune stale low-confidence facts. Cortex already has the scaffolding and the provenance discipline; this is supermemory's "Dynamic Dreaming" done in the open, on cortex's existing operational primitives.

---

## 4. Design decisions to make explicitly

- **Where does versioning live?** Graphiti puts temporal validity on *edges*. Cortex has both typed relationships *and* free-text memories, so it needs versioning on **both**. A relationship change ("works_at Google" → "works_at Stripe") invalidates an edge; a memory change ("budget is $5k" → "$10k") invalidates a memory. Don't force everything into edges.
- **Soft-invalidate, never hard-delete** (except explicit `Forget`). Preserves auditability and point-in-time queries — the whole reason for bi-temporal. Reconcile `Forget` semantics accordingly (it stays a hard delete; invalidation is a separate, softer state).
- **Detection (LLM) vs resolution (deterministic code).** Keep the supersede decision in reproducible Go, not in an LLM's discretion.
- **Don't copy:** multi-tenant container-tags (cortex is single-user/personal), proprietary aggregation synthesis, premature ANN/scaling (brute-force cosine is fine at personal scale per ConvoMem).

## 5. Suggested first PR

Tier 1 as one cohesive change: fix memory vector/FTS retrieval (1.1), wire source-chunk re-injection (1.2), and make confidence a ranking weight with an abstention signal (1.3). All three are low-risk, need no schema migration, and immediately improve recall quality on the highest-value records — while setting up the confidence-as-signal foundation that Tier 2's temporal work builds on.
