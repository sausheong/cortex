# Vault Export and CORTEX.md Schema — Design

**Date:** 2026-05-26
**Status:** Draft (pending user review)
**Author:** sausheong + Claude

## Background

Cortex stores knowledge as a typed graph (entities, relationships, memories, chunks) in SQLite. It exposes three interfaces (CLI, MCP, HTTP) for `remember` / `recall` / `forget` / `sync`. The [llm-wiki pattern](../../../llm-wiki.md) describes an LLM-maintained interlinked markdown wiki with an explicit schema file that turns a generic LLM into a disciplined maintainer.

Cortex and llm-wiki are solving the same problem from opposite ends. The graph **is** a wiki — it's just not rendered as one, and the workflow discipline isn't codified. This design adds two pieces:

1. **Vault export** — a one-way projection of the graph onto a browsable Obsidian-compatible markdown vault.
2. **CORTEX.md schema** — a static, generic agent contract describing how to use cortex as a knowledge layer.

## Goals

- Make the graph human-browsable using existing tooling (Obsidian, `grep`, file managers).
- Give agents a single document they can read at session start to understand cortex conventions.
- Both features are additive — no changes to existing commands or data model.

## Non-goals

- Bidirectional sync from vault edits back into the graph.
- A `cortex lint` command (covered in a separate spec).
- Filing recall results back as derived memories (covered in a separate spec).
- Per-brain customization of CORTEX.md at generation time.

## Scope and commands

Two new CLI commands and one new template file:

| Command | Purpose |
|---|---|
| `cortex export [--vault <dir>] [--full] [--dry-run]` | Regenerate the vault from the graph. Default vault dir: `./vault`. Incremental by default. |
| `cortex init-schema [<dir>] [--force]` | Copy the canonical `CORTEX.md` template into `<dir>` (default: cwd). Refuses to overwrite without `--force`. |

The static template lives at `docs/CORTEX.md` in the cortex repo and is embedded into the `cortex` binary via `//go:embed`.

Both commands are read-only against `brain.db` (export reads; init-schema doesn't touch it). No changes to `remember`, `recall`, `sync`, `forget`, `entity`, or `config`.

## Vault layout

Output of `cortex export --vault ./vault`:

```
vault/
├── index.md                     # catalog: entities grouped by type
├── log.md                       # chronological: ingests, exports (newest last)
├── people/
│   └── alice-chen.md
├── organizations/
│   └── stripe.md
├── concepts/
│   └── distributed-systems.md
├── events/
│   └── 2026-04-02-board-meeting.md
├── documents/
│   └── api-spec.md
├── sources/
│   └── 2026-04-02--article-title.md   # one page per distinct `source` value
├── _archive/
│   └── 2026-05-26T14-32-10/
│       └── people/bob-was-merged.md   # pages whose entities no longer exist
└── .cortex-export.json          # manifest: entity ID → file path + content hash
```

### Filename strategy

`slug(entity.Name) + ".md"`. Slugging:

1. Lowercase.
2. Replace each run of non-alphanumeric characters with a single `-`.
3. Trim leading and trailing `-`.
4. If the result is empty (e.g. a name of only symbols), fall back to the first 8 chars of the entity ID.

Collisions within a type folder are disambiguated by appending `-<short-id>` (first 6 chars of the entity ID) to **every** colliding page — including the first one. This keeps filenames stable across export runs regardless of iteration order. Example: two `concept` entities both named "Java" become `java-a3f9c2.md` and `java-b71e08.md` (never `java.md` + `java-b71e08.md`, because that would be order-dependent).

The frontmatter always carries the full `cortex_id`, so wikilinks remain unambiguous regardless of filename.

### Type → folder map

Built-in pluralization:

| Entity type | Folder |
|---|---|
| `person` | `people/` |
| `organization` | `organizations/` |
| `concept` | `concepts/` |
| `event` | `events/` |
| `document` | `documents/` |

Unknown types fall through to lowercased type + `s` (e.g. `project` → `projects/`). The map is defined in one place (`vault/layout.go`) and trivially extensible.

### Archive

`_archive/<timestamp>/` preserves the original type-folder structure so an archived page is recognizable. Each export run that finds stale pages creates one timestamped subfolder. No automatic cleanup of `_archive/` — that's the user's call (or a future flag).

### log.md ordering

Append-only at the bottom (newest last). Each entry one line, prefixed with `## [YYYY-MM-DDTHH:MM:SSZ] <op> | <summary>` so `grep "^## \["` works as in the llm-wiki tip.

## Page format

Each entity page is markdown with YAML frontmatter. Example for a `person` entity:

```markdown
---
cortex_id: ent_01H9X7K2M3N4P5Q6R7S8T9V0W1
type: person
name: Alice Chen
source: notes/2026-04-02-coffee.md
created_at: 2026-04-01T10:23:11Z
updated_at: 2026-05-20T14:55:02Z
attributes:
  role: engineer
  team: payments
exported_at: 2026-05-26T14:32:10Z
---

# Alice Chen

## Memories

- Alice is joining Stripe next month — `notes/2026-04-02-coffee.md`
- Worked at Square 2019–2024 — `linkedin-import`

## Relationships

- works_at → [[organizations/stripe|Stripe]]
- knows → [[people/bob-singh|Bob Singh]]
- interested_in → [[concepts/distributed-systems|distributed systems]]

## Backlinks

- [[organizations/stripe|Stripe]] — employs
- [[events/2026-04-02-board-meeting|2026-04-02 board meeting]] — attended

## Sources

- [[sources/2026-04-02--coffee-notes]]
- [[sources/linkedin-import]]
```

### Frontmatter

Carries everything Dataview-style plugins need: ID, type, name, source, timestamps, all entity attributes flattened under `attributes:`, plus `exported_at`. The `cortex_id` is the round-trip key if export ever becomes bidirectional.

### Body sections

Fixed and predictable: `# <Name>`, then `## Memories`, `## Relationships`, `## Backlinks`, `## Sources`. Sections with no content are omitted. Every entity reference is a wikilink with explicit path: `[[organizations/stripe|Stripe]]`. The path matches the vault layout; the alias is the human name.

**Backlinks computation:** while iterating the graph, build an in-memory inverted index `targetID → []Relationship` once per export. Each entity's `## Backlinks` section is rendered from that index. The relationship's `Type` is used as the label (e.g. "employs", "attended"). This is what makes the content hash for entity A change when entity B gains a relationship pointing to A — the page's content genuinely depends on inbound edges, so the hash must reflect them.

### Memory lines

Bullet list items: memory content, em-dash, then backtick-wrapped source string. Memory IDs are not surfaced in the body.

### Source pages

`sources/...md` follow a similar template inverted: frontmatter has the source identifier; body lists entities and memories that came from that source.

### index.md

Generated last. Sections per entity type, each listing `[[path|name]] — <one-line summary>` (summary derived from attributes or first memory). Bottom of file links to `log.md`.

### log.md entry format

```
## [2026-05-26T14:32:10Z] export | 47 pages written, 3 archived
## [2026-05-26T14:18:22Z] ingest | sync ./notes (12 files, 38 entities, 91 memories)
## [2026-05-26T11:02:05Z] ingest | remember "Alice is joining Stripe..."
```

## Architecture

### Package layout

New package `vault/` at the repo root:

```
vault/
├── vault.go        # Exporter type, top-level Export() entry point
├── layout.go       # type → folder map, slug(), filename collision handling
├── render.go       # page templates: entity, source, index, log
├── manifest.go     # .cortex-export.json read/write, change detection
└── vault_test.go
```

Depends on `cortex` (reads entities, relationships, memories, chunks via the existing public API — no SQL of its own). The CLI wires it up:

```go
// cmd/cortex/main.go
case "export":
    cmdExport()  // opens cortex.Cortex, calls vault.Export(ctx, cx, opts)
```

`cmd/cortex/export.go` parses flags and prints progress; all logic lives in `vault/`. This mirrors how `cmdSync` delegates to `connector/files`. Keeping export out of the `cortex` package keeps the core library focused on graph operations.

`init-schema` lives entirely in the CLI (`cmd/cortex/init_schema.go`) — it's a file copy from an embedded `docs/CORTEX.md` (using `//go:embed`). No new package needed.

### Public API of `vault`

```go
package vault

type Options struct {
    VaultDir string
    Full     bool   // false = incremental
    DryRun   bool   // print what would change, don't write
}

type Stats struct {
    Written, Skipped, Archived int
    Errors []error
}

func Export(ctx context.Context, c *cortex.Cortex, opts Options) (Stats, error)
```

One entry point. Easy to test, easy to call from anywhere (HTTP server, future MCP tool).

### Incremental change detection — content-hash manifest

`.cortex-export.json` at the vault root:

```json
{
  "version": 1,
  "exported_at": "2026-05-26T14:32:10Z",
  "pages": {
    "ent_01H9X7K2M3...": {
      "path": "people/alice-chen.md",
      "content_hash": "sha256:9a4f...",
      "exported_at": "2026-05-26T14:32:10Z"
    }
  }
}
```

Algorithm per export run:

1. Load manifest (or create empty one).
2. For each entity in the graph: render the page body in memory, compute sha256.
3. If hash matches manifest entry → skip write, increment `Skipped`.
4. If hash differs or no entry → write file, update manifest entry, increment `Written`.
5. After processing all entities: any manifest entry whose `cortex_id` no longer exists in the graph → move the on-disk file to `_archive/<timestamp>/<original-path>`, remove from manifest, increment `Archived`.
6. Regenerate `index.md` (always rewritten — cheap).
7. Append one line to `log.md`.
8. Save manifest.

`--full` skips the hash comparison and rewrites everything but otherwise follows the same flow.

`--dry-run` runs the full algorithm — load manifest, render every page, compute hashes, compare, compute archive set — but suppresses all filesystem writes (no page files written, no archives moved, no `index.md`/`log.md` updates, no manifest save). `Stats` is populated exactly as it would be for a real run, so the user sees what *would* change without anything actually changing.

**Why hash-on-render rather than timestamps on entities:** memories and relationships can change without the entity row's `updated_at` moving (different tables, no cascading triggers today). A page's content is the union of entity + its memories + its relationships + backlinks (which depend on *other* entities' relationships). Hashing the rendered output is the only correct invariant. Costs one render per entity per export (cheap; no LLM calls).

**Manifest in the vault dir, not in `brain.db`:** the vault is self-describing, the user can delete `.cortex-export.json` to force a full export, and the manifest can be committed alongside the vault if the user wants export history in git.

### Error handling

Per-entity errors accumulate into `Stats.Errors` and don't abort the run. The function returns an error only on unrecoverable conditions: can't open `brain.db`, can't create vault dir, manifest corrupt (and not `--full`). Partial export is better than no export.

## CORTEX.md template

Single markdown file, ~150-250 lines, generic-agent voice (no "Claude" or "Codex" specifics). Sections:

1. **What this file is** — one paragraph: cortex is the knowledge layer for this project; read before answering, write after meaningful exchanges; this file describes the conventions.
2. **The three operations** — `remember` / `recall` / `forget`, one-line semantics each, pointers to CLI / HTTP / MCP surfaces. Contract, not reference manual.
3. **When to recall** — explicit triggers: start of a conversation about a recognized entity; any "what do I know about X" question; before making a recommendation that might have prior history.
4. **When to remember** — explicit triggers: new fact about a person, project, or entity; decisions made; events that happened; corrections to prior beliefs (`forget` then `remember`).
5. **What not to remember** — bullets mirroring the auto-memory exclusion list: code patterns, git history, ephemeral task state, anything already in source-of-truth files.
6. **The vault** — short section: if `cortex export --vault ./vault` has been run, browsable pages exist at `<path>`; read those before falling back to `cortex recall` for broad entity context.
7. **Workflow loop** — llm-wiki ingest/query/lint rhythm rephrased for cortex. `lint` is forward-referenced as future work.
8. **Customization** — final paragraph: this file is yours to edit. Add project-specific entity types, conventions, escalation rules.

The file lives at `docs/CORTEX.md` and is embedded into the `cortex` binary via `//go:embed`. `cortex init-schema` writes it verbatim. No templating, no parameter substitution — keeping it static means updates can be diffed against user customizations.

## Testing strategy

**Unit tests in `vault/`:**

- `layout_test.go` — slug edge cases (unicode, punctuation, empty after slugging), collision suffix is deterministic, type → folder map covers built-ins and unknown fallback.
- `render_test.go` — golden-file tests for each page kind (entity with full sections, entity with no relationships, source page, index, log entry). Goldens live in `vault/testdata/`. Run with `-update` flag to regenerate.
- `manifest_test.go` — round-trip JSON, missing file treated as empty, corrupt JSON returns a typed error.

**Integration tests in `vault_test.go`:**

- Seed an in-memory cortex with a small fixed graph (3 entities, 2 relationships, 4 memories, 2 sources). Run `Export` to a `t.TempDir()`. Assert: file tree matches expectation, manifest is well-formed, every page parses as valid markdown with frontmatter.
- Idempotency: run `Export` twice with no changes; second run reports `Skipped == N, Written == 0`.
- Rename: change an entity's name between runs; old file removed, new file present, manifest updated.
- Delete: delete an entity; page moves to `_archive/<ts>/...` and manifest entry is gone.
- `--full`: rewrites everything regardless of hash.

**CLI tests in `cmd/cortex/`:**

- `init-schema` writes the embedded template to a tempdir, refuses to overwrite, accepts `--force`.
- `export` parses flags correctly and prints stats. Rendering correctness is tested in `vault/`, not duplicated here.

**No LLM or embedder in any test.** All tests run under `go test ./vault/... ./cmd/cortex/...` in under a second.

## Migration path for future work

- **Per-brain CORTEX.md customization:** if we later want it tailored, the CLI surface doesn't change — `init-schema` switches from `os.WriteFile(embedded)` to a template renderer.
- **`cortex lint`:** the vault gives lint a concrete artifact to operate on (orphan pages, missing backlinks, contradictions across memory bullets). Plumbing already in place.
- **Bidirectional sync:** `cortex_id` in every frontmatter is the round-trip key. A future `cortex import-vault` could parse vault pages, match by `cortex_id`, and reconcile edits — but that's a separate spec.
- **MCP tool surface:** `Export()` is a single function call with a small Options struct — trivial to expose as an MCP tool later.

## Open questions

None blocking. Spec is complete enough to plan from.
