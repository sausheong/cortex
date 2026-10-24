# Package split plan

Goal: move the library out of the repo root (52 code files, 48 test files, one package) into `internal/` packages. The root package `cortex` keeps only the public API, as thin forwarding. No behaviour change.

Branch: `restructure`. Baseline commit for API comparison: `880d057`.

## Findings that shape the design

1. `*Cortex` is a struct of `db dbConn, cfg config, userID, pinnedGen, connID`, and about 110 exported methods plus many unexported helper methods hang off it. Go cannot define methods on a type from another package, so internal packages cannot keep `func (c *Cortex)` as is.
2. A corrected file-level call graph (calls only, not type or name mentions) shows these real cycles: `entity <-> identity <-> resolve`, `relationship <-> traverse`, `recall <-> recall_graph`. Everything else is layered. Many apparent cross-file edges are only shared helpers (`logf`, `newID`, `nullTimePtr`, `augmentForEmbedding`, `normKey`, `truncate`), which sink to a lower package.
3. Layering that falls out of the graph (low to high): pure helpers, store, graph cluster, memory cluster, query / maintain / tenancy, ingest. `recall` calls `GetEntity` and `GetRelationships` (so memory sits above graph); `profile`, `compaction`, `reconcile`, `relate` call recall, memory and entity (so maintain sits above both); `Remember` calls nearly everything (so ingest is on top).
4. Test helpers (`mockLLM`, `mockExtractor`, `testEmbedder`, `batchEmbedder`, `ownerCortex`, `twoUsers`, ...) are shared across the root test files, and `internal/testutil` imports the root package. DB-backed tests therefore cannot move into internal packages without a cycle or duplicated mocks. They stay at the root. Only pure unit tests move.
5. `/docs` and `/openspec` are in `.gitignore`. Only `docs/deploy.md` is tracked there (plus this plan, force-added). `docs/CORTEX.md` and the research note are untracked, so they move with plain `mv`, and `openspec/` is deleted with `rm -rf`. Root `testdata/` is tracked but no test or doc references it (the live fixtures are `vault/testdata` and `internal/sqliteimport/testdata`), so it is `git rm`ed rather than moved.
6. `//go:embed migrations/*.sql` needs the directory inside the package that embeds it, so `migrations/` moves to `internal/store/migrations/`. No other file references the path.
7. `cmd/cortex/CORTEX.md.template` has a comment pointing at `docs/CORTEX.md`; update it to `docs/design/CORTEX.md`.

## Package map

Dependency direction is strictly downward; no package imports one above it.

| Package | Contents (current files) | Imports |
|---|---|---|
| `internal/model` | All exported data types, interfaces (`LLM`, `Embedder`, `Extractor`, `Reconciler`, `RelationDetector`, ...), option types plus their `With*` constructors, option config structs (fields exported, e.g. `RecallConfig`), `Config` (llm, embedder, extractor, logf, migration timeout), constants, sentinel errors and error types (`ErrNotFound`, `notFoundError`, `ErrEntityNameTaken`, `EmbedPartialError`, `ExtractPartialError`), `TemporalMode` and clause helpers (`temporal.go`), `coerceConfidence`, `extractionJSON`. Everything from `types.go`, plus the type decls scattered in `identity`, `graph`, `queries`, `usage`, `reembed`, `compaction`, `normalize_graph`, `user`, `embedfail`, `extractchunks`. Types that have methods live here so root aliases carry the methods. | stdlib only |
| `internal/ontology` | `ontology.go`, `normalize.go` (`NormKey`, `normTypeSQL`, `Canonical*`, vocabulary) | stdlib |
| `internal/vecmath` | `math.go` (cosine, float32 codec, rrf, mmr, decay math) | stdlib |
| `internal/llmutil` | `retry.go` (`RetryLLM`, `IsTransientLLMErr`, `AddReportError`, `Backoff` var), `split.go` (`SplitContent`), `Truncate`, `TruncateRunes`, `EscapeLike` | stdlib |
| `internal/store` | `Handle` (below), `db.go` (`DB`, `Tx`, `Rebind`), `migrate.go`, `migrations/`, `id.go` (`NewID`), `scope.go` (`RequireEntity`, `RequireMemory`, not-found constructors), `NullTimePtr`, `Logf`, `NullConn`, `Open`/`Close` internals (connect timeout, pgconn parse, migrate) | model, stdlib, pgx |
| `internal/graph` | `entity`, `identity`, `resolve`, `softdelete`, `relationship`, `traverse`, `graph.go`, `normalize_graph`, `lint`, `scope`-dependent graph ops. One package because of the SCCs in finding 2. Exports `Ops`, `ResolveContext`. | model, ontology, llmutil, store |
| `internal/memory` | `memory`, `memory_edge`, `chunk`, `search`, `vector`, `embedfail` (`EmbedFailures`, `EmbedTarget`), `reembed`, `recall`, `recall_graph`, `bulk`, `augmentForEmbedding` (exported `AugmentForEmbedding`). | model, vecmath, llmutil, ontology, store, graph |
| `internal/query` | `queries.go` (WhoIs, Describe, Path, Timeline, WhatChanged, Link). Needs graph and memory, so it sits above both. | + memory, graph |
| `internal/maintain` | `decay`, `expire`, `reconcile`, `relate`, `compaction`, `profile`, `maintain` | + memory, graph |
| `internal/tenancy` | `user.go` (users, accounts CRUD), `usage.go` | model, store |
| `internal/ingest` | `cortex.go` (`Remember`, `Forget`, helpers `ownerVoice`, `memoryEntityIDs`), `extractchunks.go` (`ExtractParts`, `MergeExtractions`) | + memory, graph, ontology, llmutil |
| `internal/render` | every `*_render.go` (pure string builders over `model` report types) | model |

If `go build` reports a cycle that this table did not predict, the rule is: move the offending symbol to the lowest package both sides already import, never add an upward import. Record the move in the commit message.

## Shared handle

`store.Handle` replaces the `Cortex` struct body:

```
type Handle struct {
    DB        DB          // placeholder-rewriting wrapper over *sql.DB (was dbConn)
    Cfg       model.Config
    UserID    string
    PinnedGen int
    ConnID    string
}
```

Methods that only need these fields live on `*Handle` in store: `Logf`, `NullConn`, `Embedder`. `Handle` is copied by value for `ForUser` and `WithConnection` (same semantics as `n := *c` today).

Each higher package defines `type Ops struct{ *store.Handle }` and a `New(h *store.Handle) Ops`. Existing method bodies keep their shape: `func (c *Cortex) PutEntity` becomes `func (c Ops) PutEntity`, so same-package calls `c.foo(...)` are untouched. The mechanical edits per file are limited to `c.db` to `c.DB`, `c.userID` to `c.UserID`, `c.cfg.x` to `c.Cfg.X`, `c.connID`, `c.pinnedGen`, and cross-package calls become `graph.New(c.Handle).GetEntity(...)` (a small unexported accessor per consumer package keeps this terse).

Unexported helpers needed across packages are exported with the shortest capitalised name in the lowest owning package (`store.NewID`, `store.NullTimePtr`, `ontology.NormKey`, `llmutil.Truncate`, `memory.AugmentForEmbedding`, ...). No `Export_`-style shim files.

## Root package after the split

Root keeps: `doc.go`, `cortex.go` (type `Cortex struct{ h *store.Handle }`, `Open`, `Close`, `ForUser`, `WithConnection`, `UserID`, `ConnectionID`, `DB`, `Embedder`), `aliases.go` (one alias per exported type), `consts.go` (re-declared constants and sentinel vars), `options.go` (wrapper funcs: `func WithLimit(n int) RecallOption { return model.WithLimit(n) }`), and `api_*.go` forwarders grouped by area (entities, memories, recall, maintenance, admin). Every forwarder is one line: `func (c *Cortex) PutEntity(ctx context.Context, e *Entity) error { return graph.New(c.h).PutEntity(ctx, e) }`.

Compatibility rules:
- Types: `type Memory = model.Memory`. Never a defined type, so method sets and `errors.As` targets are identical.
- Constants: `const AbstainThreshold = model.AbstainThreshold` (stay constants, untyped values preserved).
- Sentinel vars: `var ErrNotFound = model.ErrNotFound` (same value, so `errors.Is` still works).
- Exported funcs (`With*`, `Canonical*`, `IsEmbedPartial`, `IsExtractPartial`, `Render*Markdown`, `AugmentForEmbedding`, `VocabularyPrompt`, ...) stay funcs, as wrappers, not `var f = pkg.F`.
- Methods on exported types (`MaintainReport.SkippedItems`, `NamedEdge.String`, error `Error/Unwrap/Is`) are defined in `model`.
- Error strings, SQL text, and migration file contents are moved verbatim (`git mv`, no edits to the SQL or `fmt.Errorf` strings; the `cortex:` prefix stays).

## Tests

Rule: a test file whose tests only exercise pure functions moves with the code. A file that opens a database stays at the root (finding 4). Mixed files are split by function: pure tests move, DB tests stay in a root file of the same stem.

Root DB tests become `package cortex_test` where they use only the exported API. Root `export_test.go` (package `cortex`, test-only) exposes what white-box DB tests still touch: raw `store.DB` (`c.h.DB`), `Cfg`, `UserID`, and any unexported function called directly (`migrateUpTo`, `loadMigrations`, `addAliasQuiet`, ...). These are found by compile errors, not guessed; the stage commit message lists them.

| Test file(s) | Destination |
|---|---|
| `types_test` | `internal/model` (stub reconciler is local to it) |
| `math_test` | `internal/vecmath` |
| `split_test`, pure part of `retry_test` (`TestIsTransientLLMErr`, backoff) | `internal/llmutil`; the `Reconcile_*` retry tests stay root |
| `db_test` (`TestRebind`) | `internal/store` |
| `temporal_test` clause tests | `internal/model`; DB part stays root |
| `ontology_test` pure parts | `internal/ontology`; DB parts stay root |
| `*_render_test` (decay, expire, lint, maintain, profile, reconcile, relate) | `internal/render` |
| pure parts of `identity_test` (`TestNameSimilarity`, alias normalisation), `graph_quality_test` (`TestNormalizeType`, `ParseExtractionJSON`), `recall_graph_test` (`TestQueryNgrams`), `profile_select_test` (`selectStaticCandidates`), `profile_test` (`isFresh`, partition) | the owning internal package |
| everything else (`cortex_test`, `entity_test`, `memory_test`, `recall_test`, `softdelete_test`, `migrate_test`, `store_test`, `vector_test`, `search_test`, `reconcile_test`, `relate_test`, `maintain_test`, `decay_test`, `expire_test`, `compaction_test`, `isolation_test`, `team_test`, `bench_test`, `chunked_test`, `embedfail_test`, `provenance_test`, `queries_test`, `traverse_test`, `relationship_test`, `lint_test`, `bulk_test`, `chunk_test`, `memory_edge_test`, `temporal_edges_test`, `example_test`, remainder of the mixed files) | stay at root as `package cortex_test` |

`internal/accounts`, `internal/jobs`, `internal/web`, `internal/sqliteimport`, `internal/testutil`, `internal/pgtest`, `cmd/*`, `connector/`, `extractor/`, `llm/`, `vault/`, `eval/` import only the root package; aliases keep them compiling. Only `internal/testutil` and `internal/pgtest` must remain free of imports of the new internal packages' parents (they already are).

## Verification used at every stage

1. `go build ./... && go vet ./...`
2. `CORTEX_TEST_DATABASE_URL="postgres://postgres:postgres@localhost:55432/cortex_test?sslmode=disable" go test ./... -count=1`
3. API check against the baseline. Create a baseline worktree once (`git worktree add /tmp/cortex-base 880d057`, remove it at the end) and compare with `golang.org/x/exp/cmd/apidiff` (exports data for the root, `extractor`, `llm`, `connector`, `vault` at the baseline and at HEAD; any "incompatible" entry fails the stage). If apidiff cannot be fetched, fall back to a small go/types checker in `/tmp` (not committed) that resolves aliases and compares exported objects, method sets and signatures, and also diff `go doc -all` against `/tmp/cortex-api-before-full.txt` allowing alias lines to differ.
4. `go list -deps ./... ` shows no cycle (the compiler enforces it) and `go list -f '{{.ImportPath}}: {{join .Imports " "}}' ./internal/...` matches the layer table.
5. Migration guard: `git diff -M --stat 880d057 -- internal/store/migrations` shows renames only; embedded file list equals the nine originals.

## Stages (one commit each, build and tests green after each)

**Stage 1. `chore: tidy repository`**
- `rm -f cortex cortex-mcp`, `rm -rf bin/*` (ignored, no commit effect).
- `rm -rf openspec/`; drop `/openspec` from `.gitignore`.
- `git rm -r testdata/` (unreferenced).
- `mkdir docs/design docs/ops`; `git mv docs/deploy.md docs/ops/deploy.md`; plain `mv` of untracked `docs/CORTEX.md` and `docs/cortex-improvements-from-memory-research.md` into `docs/design/`. `docs/superpowers` unchanged.
- Fix references: `README.md` (two links to `docs/deploy.md`), `cmd/cortex/CORTEX.md.template` comment, any script comment found by `grep -rn "docs/"`.
- Keep `PRODUCT.md`, `DESIGN.md`, `.impeccable/` untouched.

**Stage 2. `refactor: extract model package`**
Create `internal/model` with all types, interfaces, constants, errors, option constructors and config structs (exported fields), `TemporalMode`. Root gets `aliases.go`, `consts.go`, `options.go`; remaining root code switches to the `model` names through the aliases (no churn needed in bodies except config field renames `recallConfig.limit` to `RecallConfig.Limit`). Move `types_test`, `temporal_test` clause tests. Run apidiff (this is the stage most likely to expose a signature drift).

**Stage 3. `refactor: extract pure helper packages`**
Create `internal/ontology`, `internal/vecmath`, `internal/llmutil`. Move `ontology.go`, `normalize.go`, `math.go`, `retry.go`, `split.go` and truncate/escape helpers with their pure tests. Root keeps wrappers for exported functions (`Canonical*`, `VocabularyPrompt`, ...).

**Stage 4. `refactor: extract store package`**
Create `internal/store`: `Handle`, `DB`/`Tx`/`Rebind`, `NewID`, `NullTimePtr`, scope checks, `Logf`/`NullConn`, `Open`/migrate. `git mv migrations internal/store/migrations` and update the `go:embed` line. Root `Cortex` becomes `struct{ h *store.Handle }`; all remaining root files read `c.h.DB`, `c.h.UserID` and so on (mechanical sed). Move `db_test`. Add root `export_test.go`; convert DB tests that touch internals to `package cortex_test` as needed. This is the stage that changes every root file, so it is deliberately before any package carve-out.

**Stage 5. `refactor: extract graph package`**
Move `entity`, `identity`, `resolve`, `softdelete`, `relationship`, `traverse`, `graph.go`, `normalize_graph`, `lint` into `internal/graph` with `Ops`. Remaining root code calls `graph.New(c.h)`. Pure test fragments move.

**Stage 6. `refactor: extract memory package`**
Move `memory`, `memory_edge`, `chunk`, `search`, `vector`, `embedfail`, `reembed`, `recall`, `recall_graph`, `bulk`. Export `AugmentForEmbedding`, `EmbedFailures`, `EmbedTarget`. Pure tests move.

**Stage 7. `refactor: extract query, maintain and tenancy packages`**
Move `queries.go` to `internal/query`; `decay`, `expire`, `reconcile`, `relate`, `compaction`, `profile`, `maintain` to `internal/maintain`; `user.go`, `usage.go` to `internal/tenancy`. Three packages in one commit because they are siblings with no edges between them (confirm with the build); if the diff is too large, split into two commits at maintain.

**Stage 8. `refactor: extract ingest and render packages`**
Move `cortex.go` (`Remember`, `Forget`) and `extractchunks.go` to `internal/ingest`; all `*_render.go` plus their tests to `internal/render`.

**Stage 9. `refactor: root package is public API only`**
Root now contains only `doc.go`, `cortex.go`, `aliases.go`, `consts.go`, `options.go`, `api_*.go`, `export_test.go`, `*_test.go` (package `cortex_test`). Remove leftover files, run `gofmt -l .`, `go vet`, `staticcheck` if installed, final apidiff against the baseline and a diff of `go doc -all` for each of the five public packages. Add `doc.go` with the package comment (what cortex is, `Open`, `ForUser`, `Remember`/`Recall`, pointer to README).

**Stage 10. `docs: update README for new layout`**
README architecture/layout section (root API, `internal/*` layer table, `cmd/`, `extractor/`, `llm/`, `connector/`, `vault/`, `docs/design`, `docs/ops`, `docs/superpowers`), fix links, mention `make test`. No em dashes. Remove the baseline worktree.

## Risks and how they are handled

- Config option structs gaining exported fields: only inside `internal/`, so the public API is unchanged. Options stay `func(*model.X)`; user-written options are impossible today (unexported param type), so this is not a compatibility break.
- Aliased type printing in `go doc` differs textually; hence apidiff or a go/types check rather than text diff alone.
- `Handle` copying: `ForUser` must apply `opts` to the copy's `Cfg` only, as now. A root test (`isolation_test`) already covers cross-user handles; keep it green at every stage.
- Hot paths gain one pointer indirection through `Ops`; no measurable cost expected, `bench_test` is the check.
- Lock-step hazards: `llmBackoff` is a package var swapped by tests; it becomes `llmutil.Backoff`, and the root DB retry tests reach it through `export_test.go`.
- Untracked docs cannot be recovered from git; take a copy of `docs/CORTEX.md` and the research note to `/tmp` before Stage 1.
