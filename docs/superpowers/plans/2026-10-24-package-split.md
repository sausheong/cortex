# Package split plan

Goal: move the library out of the repo root (52 code files, 48 test files, one package) into `internal/` packages. The root package `cortex` keeps only the public API, as thin forwarding. No behaviour change.

Branch: `restructure`. Baseline commit for API comparison: `880d057`.

## Findings that shape the design

1. `*Cortex` is a struct of `db dbConn, cfg config, userID, pinnedGen, connID`, and about 110 exported methods plus many unexported helper methods hang off it. Go cannot define methods on a type from another package, so internal packages cannot keep `func (c *Cortex)` as is.
2. A corrected file-level call graph (calls only, not type or name mentions) shows these real cycles: `entity <-> identity <-> resolve`, `relationship <-> traverse`, `recall <-> recall_graph`. Everything else is layered. Many apparent cross-file edges are only shared helpers (`logf`, `newID`, `nullTimePtr`, `augmentForEmbedding`, `normKey`, `truncate`), which sink to a lower package.
3. Layering that falls out of the graph (low to high): pure helpers, store, graph cluster, memory cluster, query / maintain / tenancy, ingest. `recall` calls `GetEntity` and `GetRelationships` (so memory sits above graph); `profile`, `compaction`, `reconcile`, `relate` call recall, memory and entity (so maintain sits above both); `Remember` calls nearly everything (so ingest is on top).
4. Test strategy. Only `team_test.go` is `package cortex_test` today (it imports `internal/testutil`, which imports the root). The other 47 test files are `package cortex`, share helpers (`mockLLM`, `mockExtractor`, `testEmbedder`, `batchEmbedder`, `ownerCortex`, `twoUsers`, ...) and touch unexported fields (`c.db` ~94 uses, `c.cfg` ~16, `c.userID` ~28) and unexported methods that will live in internal packages (`putEmbedding`, `getMemoryByID`, `distillBucket`, `embeddingByRef`, `searchVectorRaw`, `searchMemoriesMode`, `profileEligibleIDs`, `neighbourhood`, `addAliasQuiet`, `migrate`, `migrateUpTo`, `loadMigrations`). Decision: DB tests stay at the root as `package cortex` (NOT converted to `cortex_test`), so helpers and unexported identifiers keep working. Fields are handled by sed (`c.db` to `c.h.DB`, `c.cfg` to `c.h.Cfg`, `c.userID` to `c.h.UserID`). Moved methods are exported on the owning internal `Ops` type (for example `memory.Ops.PutEmbedding`), and one root test-only file `whitebox_test.go` (package cortex) re-declares each as a thin unexported method (`func (c *Cortex) putEmbedding(...) error { return memory.New(c.h).PutEmbedding(...) }`) so test bodies are unchanged. The list is finalised by compile errors; each stage commit message lists the wrappers it added. Exceptions that move into internal packages because pure or store-only: `migrate_test`, `db_test` (to `internal/store`, using `internal/pgtest`, which does not import the root), plus the pure unit tests in the table below. `internal/testutil` keeps importing the root and is only used by `package cortex_test` files.
5. `/docs` and `/openspec` are in `.gitignore`. Tracked under docs today: `docs/deploy.md` and this plan (force-added). `docs/CORTEX.md` and `docs/cortex-improvements-from-memory-research.md` are untracked. Because `/docs` is ignored, the moved files are added with `git add -f docs/design docs/ops` so they are committed and README links resolve (leave `.gitignore` alone; `docs/superpowers` is already force-tracked). `openspec/` is deleted with `rm -rf` and `/openspec` removed from `.gitignore`. Root `testdata/` (tracked: five markdown notes and `team.csv`) is referenced by no Go test, Makefile, script or doc (`cmd/cortex/admin_test.go` reads `internal/sqliteimport/testdata`), so there is no test to move it next to. It is not deleted: `git mv testdata examples/sample-notes` so the sample fixtures survive as sample import data. Call this out in the stage 1 commit body and final report.
6. `//go:embed migrations/*.sql` needs the directory inside the package that embeds it, so `migrations/` moves to `internal/store/migrations/`. No other file references the path.
7. `cmd/cortex/CORTEX.md.template` has a comment pointing at `docs/CORTEX.md`; update it to `docs/design/CORTEX.md`.

## Package map

Dependency direction is strictly downward; no package imports one above it.

| Package | Contents (current files) | Imports |
|---|---|---|
| `internal/model` | All exported data types, interfaces (`LLM`, `Embedder`, `Extractor`, `Reconciler`, `RelationDetector`, ...), `Option` (`func(*Config)` form) and `Config`, per-call option types plus their `With*` constructors and config structs (exported fields), constants, sentinel errors and error types (`ErrNotFound`, `NotFoundError` plus constructor `NotFound(format, args...)` moved from `user.go`, `ErrEntityNameTaken` struct with pointer-receiver `Error`, `EmbedPartialError`, `ExtractPartialError`), `TemporalMode` (exported, with `Clause`), `EmbedFailures` (exported, with `Add`, `Merge`, `Err`, `AddN`), `coerceConfidence`, and `extraction_json.go` (pure JSON parsing into model types, kept here). Everything from `types.go` plus type decls scattered in `identity`, `graph`, `queries`, `usage`, `reembed`, `compaction`, `normalize_graph`, `user`, `embedfail`, `extractchunks`. Types with methods live here so root aliases carry the methods. | stdlib only |
| `internal/ontology` | `ontology.go`, `normalize.go` (`NormKey`, `normTypeSQL`, `Canonical*`, vocabulary) | stdlib |
| `internal/vecmath` | `math.go` (cosine, float32 codec, rrf, mmr, decay math) plus `Jaccard` (moved from `profile.go`, used by `identity.go`) | stdlib |
| `internal/llmutil` | `retry.go` (`RetryLLM`, `IsTransientLLMErr`, `AddReportError`, exported var `Backoff`), `split.go` (`SplitContent`), `Truncate`, `TruncateRunes`, `EscapeLike` | stdlib |
| `internal/store` | `Handle` (below), `db.go` (`DB`, `Tx`, `Rebind`), `migrate.go` as Handle/store functions (`Migrate`, `MigrateUpTo`, `LoadMigrations`), `migrations/`, `id.go` (`NewID`), `scope.go` (`RequireEntity`, `RequireMemory`; they build errors via `model.NotFound`), `NullTimePtr` (from `memory.go`), `Logf`, `NullConn`, `Open`/`Close` internals (connect timeout, pgconn parse, migrate) | model, stdlib, pgx |
| `internal/tenancy` | `user.go` (users, accounts CRUD, `OwnerEntityID`; `ForUser`/`UserID` stay in root), `usage.go` (usage, `ListMemories`, `MemoryListFilter` usage). Both are self-contained SQL over `users`/`memories`, so tenancy sits directly above store and below graph. Created in stage 4. | model, llmutil, store |
| `internal/graph` | `entity`, `identity`, `resolve`, `softdelete`, `relationship`, `traverse`, `graph.go`, `normalize_graph`, `lint`. One package because of the SCCs in finding 2. Exports `Ops`, `ResolveContext`. `EntityMentions` calls `tenancy.New(h).ListMemories`; `identity` and `normalize_graph` call `tenancy.New(h).OwnerEntityID`. | model, ontology, llmutil, vecmath, store, tenancy |
| `internal/memory` | `memory`, `memory_edge`, `chunk`, `search`, `vector`, `reembed`, `recall`, `recall_graph` (`graphContext` stays unexported here; its only users are in this package, confirm), `bulk`, `AugmentForEmbedding`, `compaction`'s memory-only helpers if any. `recall_graph` uses `tenancy.OwnerEntityID`; `vector` and compaction use `model.NotFound`. | model, vecmath, llmutil, ontology, store, tenancy, graph |
| `internal/query` | `queries.go` (WhoIs, Describe, Path, Timeline, WhatChanged, Link). | + memory, graph, tenancy |
| `internal/maintain` | `decay`, `expire`, `reconcile`, `relate`, `compaction`, `profile` (minus `jaccard`), `maintain` | + memory, graph, tenancy |
| `internal/ingest` | `cortex.go` (`Remember`, `Forget`, helpers `ownerVoice`, `memoryEntityIDs`), `extractchunks.go` (`ExtractParts`, `MergeExtractions`) | + memory, graph, tenancy, ontology, llmutil |
| `internal/render` | every `*_render.go` (pure string builders over `model` report types) | model |

Cross-package symbol moves required by the layering (all verified against current definitions): `jaccard` (profile.go) to `vecmath.Jaccard`; `notFound`/`notFoundError` (user.go, not scope.go) to `model`; `OwnerEntityID` (user.go) and `ListMemories` (usage.go) to `tenancy`; `nullTimePtr` (memory.go) to `store.NullTimePtr`; `embedFailures` and `temporalMode` to exported `model` types. Method renames on exported-for-cross-package types: `add` to `Add`, `merge` to `Merge`, `err` to `Err`, `addN` to `AddN`, `clause` to `Clause`; `dbConn`/`dbTx` become `store.DB`/`store.Tx`.

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

Methods that only need these fields live on `*Handle` in store: `Logf`, `NullConn`, `Embedder`. `ForUser` and `WithConnection` must deep-copy the Handle (`h2 := *c.h`, apply options to `h2.Cfg` only, new `Cortex{h: &h2}`), never copy the pointer; today `n := *c` copies `cfg`, a struct with func fields, by value. Rules: an `Ops` is built per call from the current `*Handle` and never stored in a struct or captured by a goroutine that outlives the call (check compaction and profile job goroutines); every cross-package accessor must pass the same `*Handle` (so `WithConnection` and `pinnedGen` are read from one place). Each stage review greps for `Ops` stored in fields and for `go func` closures capturing one.

Each higher package defines `type Ops struct{ *store.Handle }` and a `New(h *store.Handle) Ops`. Existing method bodies keep their shape: `func (c *Cortex) PutEntity` becomes `func (c Ops) PutEntity`, so same-package calls `c.foo(...)` are untouched. The mechanical edits per file are limited to `c.db` to `c.DB`, `c.userID` to `c.UserID`, `c.cfg.x` to `c.Cfg.X`, `c.connID`, `c.pinnedGen`, and cross-package calls become `graph.New(c.Handle).GetEntity(...)` (a small unexported accessor per consumer package keeps this terse).

Unexported helpers needed across packages are exported with the shortest capitalised name in the lowest owning package (`store.NewID`, `store.NullTimePtr`, `ontology.NormKey`, `llmutil.Truncate`, `memory.AugmentForEmbedding`, ...). No `Export_`-style shim files.

## Root package after the split

Root keeps: `doc.go`, `cortex.go` (type `Cortex struct{ h *store.Handle }`, `Open`, `Close`, `ForUser`, `WithConnection`, `UserID`, `ConnectionID`, `DB`, `Embedder`), `aliases.go` (one alias per exported type), `consts.go` (re-declared constants and sentinel vars), `options.go` (wrapper funcs: `func WithLimit(n int) RecallOption { return model.WithLimit(n) }`), and `api_*.go` forwarders grouped by area (entities, memories, recall, maintenance, admin). Every forwarder is one line: `func (c *Cortex) PutEntity(ctx context.Context, e *Entity) error { return graph.New(c.h).PutEntity(ctx, e) }`.

Compatibility rules:
- Types: `type Memory = model.Memory`. Never a defined type, so method sets and `errors.As` targets are identical.
- Constants: `const AbstainThreshold = model.AbstainThreshold` (stay constants, untyped values preserved).
- Sentinel vars: `var ErrNotFound = model.ErrNotFound` (same value, so `errors.Is` still works). `ErrEntityNameTaken` is a struct type with a pointer-receiver `Error`, not a sentinel: it is `type ErrEntityNameTaken = model.ErrEntityNameTaken`. Same for `EmbedPartialError`, `ExtractPartialError`.
- Exported funcs (`With*`, `Canonical*`, `IsEmbedPartial`, `IsExtractPartial`, `Render*Markdown`, `AugmentForEmbedding`, `VocabularyPrompt`, ...) stay funcs, as wrappers, not `var f = pkg.F`.
- Methods on exported types (`MaintainReport.SkippedItems`, `NamedEdge.String`, error `Error/Unwrap/Is`) are defined in `model`.
- Error strings, SQL text, and migration file contents are moved verbatim (`git mv`, no edits to the SQL or `fmt.Errorf` strings; the `cortex:` prefix stays).

## Tests

Rule: a test file whose tests only exercise pure functions (or only store/migrations) moves with the code. A file that opens a database through the Cortex API stays at the root as `package cortex`. Mixed files are split by function: pure tests move, DB tests stay in a root file of the same stem; shared helpers defined in a split file (notably `ownerCortex` in `identity_test.go`) stay in the root file. Root test mechanics: sed `c.db` to `c.h.DB`, `c.cfg` to `c.h.Cfg`, `c.userID` to `c.h.UserID`; unexported method calls go through `whitebox_test.go` wrappers (finding 4); the `llmBackoff` swap in `retry_test` imports `internal/llmutil` and sets `llmutil.Backoff` directly (no shim).

| Test file(s) | Destination |
|---|---|
| `types_test` | `internal/model` (stub reconciler local to it) |
| `math_test` | `internal/vecmath` (plus a `Jaccard` test) |
| `split_test`, pure part of `retry_test` (`TestIsTransientLLMErr`, backoff) | `internal/llmutil`; `Reconcile_*` retry tests stay root |
| `db_test` (`TestRebind`), `migrate_test` (3+2 `migrate`/`migrateUpTo` cases, `loadMigrations`) | `internal/store` (uses `internal/pgtest`) |
| `temporal_test` clause tests | `internal/model`; DB part stays root |
| `ontology_test` pure parts | `internal/ontology`; DB parts stay root |
| `*_render_test` (decay, expire, lint, maintain, profile, reconcile, relate) | `internal/render` |
| pure parts of `identity_test` (`TestNameSimilarity`, alias normalisation), `graph_quality_test` (`TestNormalizeType`, `ParseExtractionJSON`), `recall_graph_test` (`TestQueryNgrams`), `profile_select_test` (`selectStaticCandidates`), `profile_test` (`isFresh`, partition) | the owning internal package |
| everything else (`cortex_test`, `entity_test`, `memory_test`, `recall_test`, `softdelete_test`, `store_test`, `vector_test`, `search_test`, `reconcile_test`, `relate_test`, `maintain_test`, `decay_test`, `expire_test`, `compaction_test`, `isolation_test`, `bench_test`, `chunked_test`, `embedfail_test`, `provenance_test`, `queries_test`, `traverse_test`, `relationship_test`, `lint_test`, `bulk_test`, `chunk_test`, `memory_edge_test`, `temporal_edges_test`, remainder of mixed files) | stay at root, `package cortex` |
| `team_test`, `example_test` | stay root, unchanged package (`cortex_test`) |

`internal/accounts`, `internal/jobs`, `internal/web`, `internal/sqliteimport`, `internal/testutil`, `internal/pgtest`, `cmd/*`, `connector/`, `extractor/`, `llm/`, `vault/`, `eval/` import only the root package; aliases keep them compiling. `internal/pgtest` must not import the root (it does not today).

## Verification used at every stage

1. `go build ./... && go vet ./...`
2. `CORTEX_TEST_DATABASE_URL="postgres://postgres:postgres@localhost:55432/cortex_test?sslmode=disable" go test ./... -count=1`
3. API check against the baseline. Create a baseline worktree once (`git worktree add /tmp/cortex-base 880d057`, remove it at the end) and compare with `golang.org/x/exp/cmd/apidiff` (exports data for the root, `extractor`, `llm`, `connector`, `vault` at the baseline and at HEAD; any "incompatible" entry fails the stage). If apidiff cannot be fetched, fall back to a small go/types checker in `/tmp` (not committed) that resolves aliases and compares exported objects, method sets and signatures, and also diff `go doc -all` against `/tmp/cortex-api-before-full.txt` allowing alias lines to differ.
4. `go list -deps ./... ` shows no cycle (the compiler enforces it) and `go list -f '{{.ImportPath}}: {{join .Imports " "}}' ./internal/...` matches the layer table.
5. Migration guard: `git diff -M --stat 880d057 -- internal/store/migrations` shows renames only; embedded file list equals the nine originals.

## Stages (one commit each, build and tests green after each)

**Stage 1. `chore: tidy repository`**
- First copy untracked `docs/CORTEX.md` and `docs/cortex-improvements-from-memory-research.md` to `/tmp`.
- `rm -f cortex cortex-mcp`, `rm -rf bin/*` (ignored).
- `rm -rf openspec/`; drop `/openspec` from `.gitignore`.
- `mkdir -p examples && git mv testdata examples/sample-notes` (unreferenced fixtures kept as sample data; note in commit body).
- `mkdir docs/design docs/ops`; `git mv docs/deploy.md docs/ops/deploy.md`; `mv` the two untracked docs into `docs/design/`; `git add -f docs/design docs/ops`. `docs/superpowers` unchanged.
- Fix references: `README.md` (links to `docs/deploy.md` and `docs/CORTEX.md`), `cmd/cortex/CORTEX.md.template` comment to `docs/design/CORTEX.md`, any script comment found by `grep -rn "docs/"`.
- Keep `PRODUCT.md`, `DESIGN.md`, `.impeccable/` untouched.

**Stage 2. `refactor: extract model package`**
Create `internal/model` with types, interfaces, constants, errors (`NotFound`, `NotFoundError`, `ErrEntityNameTaken`, partial errors), `Option`/`Config`, option constructors and config structs (exported fields), `TemporalMode`, `EmbedFailures`, `extraction_json.go`. Root gets `aliases.go`, `consts.go`, `options.go`; remaining root code uses model names through aliases; unexported `notFound` becomes a thin call to `model.NotFound`, `embedFailures`/`temporalMode` references switch to the exported model types with renamed methods. Move `types_test`, `temporal_test` clause tests. Run apidiff.

**Stage 3. `refactor: extract pure helper packages`**
Create `internal/ontology`, `internal/vecmath` (include `Jaccard` moved from `profile.go`), `internal/llmutil` (`Backoff` exported var; update `retry_test` to set `llmutil.Backoff`). Move files and pure tests. Root keeps wrappers for exported functions (`Canonical*`, `VocabularyPrompt`, ...).

**Stage 4. `refactor: extract store and tenancy packages`**
Create `internal/store`: `Handle`, `DB`/`Tx`/`Rebind`, `NewID`, `NullTimePtr`, scope checks, `Logf`/`NullConn`, `Open` internals, and `migrate` as `Handle.Migrate`/`MigrateUpTo`/`LoadMigrations`. The `Option` type, `Config` and `With*` connection options already live in model (stage 2) so `Open` can move whole in this commit. `git mv migrations internal/store/migrations`, update `go:embed`. Root `Cortex` becomes `struct{ h *store.Handle }`; sed root files for `c.db`, `c.cfg`, `c.userID`, `c.pinnedGen`, `c.connID`. Move `db_test` and `migrate_test` to `internal/store`. Create `internal/tenancy` (`user.go` minus `ForUser`/`UserID`, `usage.go`, with `OwnerEntityID` and `ListMemories`); root `api_*` forwarders call `tenancy.New(c.h)`. Add `whitebox_test.go` as needed. This changes every root file, so it is deliberately before the graph carve-out. Implement `ForUser`/`WithConnection` as deep copies of the Handle and keep `isolation_test` green.

**Stage 5. `refactor: extract graph package`**
Move `entity`, `identity`, `resolve`, `softdelete`, `relationship`, `traverse`, `graph.go`, `normalize_graph`, `lint` into `internal/graph` with `Ops`. Calls to `jaccard`, `notFound`, `nullTimePtr`, `OwnerEntityID`, `ListMemories` are already in lower packages (stages 2 to 4). Pure test fragments move; add `whitebox_test.go` wrappers (`addAliasQuiet` etc.).

**Stage 6. `refactor: extract memory package`**
Move `memory`, `memory_edge`, `chunk`, `search`, `vector`, `reembed`, `recall`, `recall_graph`, `bulk`. Export `AugmentForEmbedding`; `graphContext` stays unexported. Export needed methods (`PutEmbedding`, `GetMemoryByID`, `DistillBucket` if in memory, `EmbeddingByRef`, `SearchVectorRaw`, `SearchMemoriesMode`, `Neighbourhood`, ...) and add wrappers in `whitebox_test.go`. Pure tests move.

**Stage 7. `refactor: extract query and maintain packages`**
Move `queries.go` to `internal/query`; `decay`, `expire`, `reconcile`, `relate`, `compaction`, `profile`, `maintain` to `internal/maintain` (export `DistillBucket`, `ProfileEligibleIDs` for the white-box wrappers). They are siblings with no edges between them (confirm with the build); split into two commits at maintain if the diff is too large.

**Stage 8. `refactor: extract ingest and render packages`**
Move `cortex.go` (`Remember`, `Forget`) and `extractchunks.go` to `internal/ingest`; all `*_render.go` plus tests to `internal/render`. Check no goroutine or struct keeps an `Ops` beyond a call.

**Stage 9. `refactor: root package is public API only`**
Root now holds only `doc.go`, `cortex.go`, `tooling.go` (DB/Embedder accessors; `AugmentForEmbedding` wrapper), `aliases.go`, `consts.go`, `options.go`, `api_*.go`, and tests (`package cortex` plus the existing `cortex_test` files, `whitebox_test.go`). Remove leftovers, `gofmt -l .`, `go vet`, `staticcheck` if installed, final apidiff for the root, `extractor`, `llm`, `connector`, `vault` against the baseline, and a check against `/tmp/cortex-api-before-full.txt` and `/tmp/cortex-api-before.txt`. Add `doc.go` (what cortex is, `Open`, `ForUser`, `Remember`/`Recall`, pointer to README).

**Stage 10. `docs: update README for new layout`**
README architecture/layout section (root API, `internal/*` layer table, `cmd/`, `extractor/`, `llm/`, `connector/`, `vault/`, `examples/`, `docs/design`, `docs/ops`, `docs/superpowers`), fix links, mention `make test`. No em dashes. Remove the baseline worktree.

## Risks and how they are handled

- Config option structs gaining exported fields: only inside `internal/`, so the public API is unchanged. Options stay `func(*model.X)`; user-written options are impossible today (unexported param type), so this is not a compatibility break.
- Aliased type printing in `go doc` differs textually; hence apidiff or a go/types check rather than text diff alone.
- `Handle` copying: `ForUser` must apply `opts` to the copy's `Cfg` only, as now. A root test (`isolation_test`) already covers cross-user handles; keep it green at every stage.
- Hot paths gain one pointer indirection through `Ops`; no measurable cost expected, `bench_test` is the check.
- Lock-step hazards: `llmBackoff` is a package var swapped by tests; it becomes the exported var `llmutil.Backoff`, and root tests import `internal/llmutil` and set it directly.
- Untracked docs cannot be recovered from git; take a copy of `docs/CORTEX.md` and the research note to `/tmp` before Stage 1.
- Layering was cross-checked against definitions, not only call sites; if the step 4 import listing disagrees with the package map, fix the map or move the symbol down, never import upward.

## Stage 1 notes

Root testdata/ was referenced by no Go test, script or doc, so it was moved to examples/sample-notes instead of next to a test. docs/ is gitignored, so docs/design and docs/ops were added with `git add -f`.

## Stage 2 notes

- Renames were done with `gopls rename` before the move (config structs and their fields, `TemporalMode`, `EmbedFailures` and methods, `CoerceConfidence`, `DefaultProfileConfig`), so references in root and tests stayed consistent.
- Also moved to model: `mergeRecord` as `MergeRecord`, the temporal SQL clause helpers (`Qualify`, `CurrentlyValidClause`, `ValidAsOfClause`, `ValidInRangeClause`; `TemporalMode.Clause` needs them), `ParseEventTime` and `ParseExtractionJSON` (root keeps wrapper funcs in `options.go`), `ErrAliasTaken`, `ErrStaleGeneration`, `NotFoundError`, the `Compact*`, `Alias*`, `Resolved*`, `BootstrapUserID`, `NoConnection`, `DefaultMaxChunkChars` constants. `IsEmbedPartial` and `IsExtractPartial` stay in root. `DisambiguatePrompt` stays in root (moves with identity in stage 5).
- `aliases.go`, `consts.go`, `options.go` in root were generated by a throwaway Go program (not committed) from the model package.
- apidiff cannot see through aliases to an internal package: it reports each of the 81 aliased types as "changed from X to internal/model.X" while the 129 other entries (all methods) are textually identical. A go/types checker (resolves aliases, compares all exported objects, method sets, signatures, constant values against the baseline worktree) found 336 symbols with 14 differences, all of the form `Option`/`*Option`/`RelFilter` whose unexported parameter type `*config` became `*model.Config`. These types cannot be implemented outside the package (the parameter type was unnameable), so this is the accepted risk from the plan.

## Stage 3 notes

- Renames were done with `gopls rename` using temporary `pkg_Name` identifiers, then the files were moved and the prefix rewritten to `pkg.Name`. Exported names: `vecmath.{Cosine,EncodeF32,DecodeF32,RRF,MMR,DecayConf,Jaccard,Ranked,MMRCand}`, `llmutil.{Backoff,RetryLLM,IsTransientLLMErr,AddReportError,SplitContent,Truncate,TruncateRunes,EscapeLike}`, `ontology.{NormKey,NormTypeSQL,SingleValuedVerbs,RelSpec}` plus the existing exported vocabulary.
- Root `ontology.go` re-declares the `Type*`/`Rel*` constants, `EntityTypes`, `RelationshipVerbs` and forwards `Canonical*`, `VocabularyPrompt`, `NormalizeType`.
- Only new apidiff difference: `RelationshipVerbs` element type was the unexported `relSpec`, now `ontology.RelSpec` (same fields, plus exported `Single`). The old type was unnameable outside the package.
- The three new packages import only the standard library.

## Stage 4 notes

- `store.Open` is split: `store.Connect` (parse, open, ping, apply options) plus `Handle.MigrationContext`/`Handle.Migrate`; root `Open` then runs `ensureVectorSchema` (still in root until stage 6) with the same migration-timeout context. Behaviour and error texts are unchanged.
- Only `TestRebind`, `TestMigrationNamesAreIntegers` and `TestMigration007FoldsDuplicateRelationships` moved to `internal/store`. The other migration tests (`TestMigration002*`) need the root API (`Open`, `SearchMemoryVector`) or share a fixture with such a test, so they stay in root and use whitebox wrappers `migrate`/`migrateUpTo` in `whitebox_test.go`.
- `WithGeneration` also copied the handle by value; it now deep-copies the `Handle` like `ForUser` and `WithConnection`.
- `Cortex.AppliedMigrations` forwards to `Handle.AppliedMigrations`; the 13 tenancy methods are forwarded from `api_tenancy.go`.

## Stage 5 notes

- `internal/graph` imports only `llmutil`, `model`, `ontology`, `store`, `tenancy` and `vecmath`. Identifiers were qualified with a throwaway go/ast program, and the root forwarders in `api_entities.go`, `api_relationships.go`, `api_softdelete.go` and `api_graphquality.go` were generated from the method signatures (docs preserved). Root also forwards `DetectAliasKind`, `NameSimilarity`, `FormatDisambiguation`, `ParseEntityChoice`, `RenderNormalizeMarkdown` and `DisambiguatePrompt`.
- Exported for later root code: `Resolve`, `IngestEntity`, `SoftDeleteEntityTx`, `AddAliasQuiet`, `NewBatch`, `EdgeValidityClause`, `ResolveContext` (fields `Content`, `OwnerSpeaks`). The internal 4-argument `softDeleteEntity(ctx, id, by, kind)` became `SoftDeleteEntityKind` because the public 3-argument `SoftDeleteEntity` already owns the plain name.
- Root `contains` calls became `slices.Contains`.
- Tests moved: `TestNameSimilarity`, `TestDetectAliasKindAndNormalize` and `TestNormalizeType` (now using `ontology.NormalizeType`). No new whitebox wrappers were needed since no root test calls the moved unexported methods.
- `Ops` is only ever a per-call value; no struct field or goroutine holds one.
- apidiff: only the known `RelationshipVerbs` element type difference plus the alias-to-internal reports.

## Stage 6 notes

- `internal/memory` imports only `graph`, `model`, `store`, `tenancy` and `vecmath`. Identifiers were qualified with a throwaway go/types program; root forwarders in `api_memories.go` and `api_recall.go` were generated from the method signatures.
- `WithGeneration` returns `*Cortex`, so it stays in root (`api_memories.go`). `EnsureVectorSchema` moved with `vector.go`; root `Open` calls it through `memory.New(c.h)`.
- Exported inside the internal package: `PutEmbedding`, `GetMemoryByID`, `GetMemoriesByEntityMode`, `EmbeddingByRef`, `SearchVectorRaw` (returns `VectorResult` with exported `RefID`, `Similarity`), `SearchMemoriesMode`, `Neighbourhood`, `EnsureVectorSchema`, `ParseVectorLiteral`, `VectorLiteral`, `TruncateEmbedText`, `AugmentForEmbedding`, `ReembedLockSQL`. `graphContext` is used only inside the package and stays unexported.
- Pure tests moved: `TestOrTSQuery`, `TestQueryNgrams`, `TestPopularityIsCapped`, `TestCheckPgvectorVersion`, `TestAugmentForEmbedding`. The one root test edit is `search_test.go` using `RefID` instead of `refID`.
- The only goroutine in the package (`Recall` sub-queries) is joined before the call returns; no `Ops` is stored.

## Stage 7 notes

- `internal/query` (queries.go) imports `graph`, `memory`, `model`, `ontology`, `store`, `tenancy`; `internal/maintain` (decay, expire, reconcile, relate, compaction, profile, maintain) imports `graph`, `llmutil`, `memory`, `model`, `ontology`, `store`, `tenancy`, `vecmath`. They are siblings with no edge between them; one commit.
- Qualification was done by a throwaway script driven by `go build` errors, then `goimports`. Root forwarders in `api_queries.go` and `api_maintenance.go` were generated from the method signatures (docs preserved).
- Exported for root white-box tests: `ProfileEligibleIDs`, `DistillBucket`, `AttrProfile`, `AttrProfiled`, `ProfileStaticPrompt`; `whitebox_test.go` re-exposes them under the old unexported names. Pure tests `TestPartitionMemories_*` and `TestSelectStaticCandidates_PerEntityCap` moved to `internal/maintain` (there is no isFresh test). DB tests stay in root.
- Neither package spawns goroutines or stores an `Ops`; every `Ops` is built per call from the same `*Handle`.
- apidiff: only the known alias-to-internal reports and the `RelationshipVerbs` element type; no removed or changed method.
