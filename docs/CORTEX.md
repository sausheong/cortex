# CORTEX.md

This file tells AI agents how to use [cortex](https://github.com/sausheong/cortex) as the knowledge layer for this project. Read it at the start of every session.

## What cortex is

Cortex is a personal knowledge graph stored in SQLite. It holds typed entities (people, organizations, concepts, events, documents), typed relationships between them, and short memory statements about them. Everything you, the agent, have learned about this project's domain is in there.

Treat cortex as your long-term memory. Read from it before answering questions. Write to it when something worth keeping is said.

## The three operations

- **`recall <query>`** — search the graph. Returns ranked results from memories, keyword matches, vector similarity, and graph traversal, merged via reciprocal rank fusion. Use before answering any non-trivial question.
- **`remember <text>`** — extract entities, relationships, and memories from the text and add them to the graph. Use after the user shares a new fact.
- **`forget --source <s>` / `--entity <id>`** — remove knowledge. Use when the user corrects a prior belief or asks you to delete something.

These are available via:

- **CLI** — `cortex recall "..."`, `cortex remember "..."`, etc.
- **MCP** — the `cortex-mcp` server exposes them as MCP tools.
- **HTTP** — `cortex-http` exposes them as REST endpoints.

Use whichever surface is configured in this project.

## When to recall

Recall before:

- Answering any "what do I know about X" question, where X is a person, organization, project, or concept.
- Making a recommendation that might have history (e.g. "should we use library Y?" — check if there's a prior decision).
- Starting a conversation about an entity the user has mentioned before, even in passing.

The cost of an extra recall is low; the cost of contradicting a prior decision because you didn't check is high.

## When to remember

Remember when:

- The user shares a new fact about themselves, a person, an organization, or a project.
- A decision is made (architectural, product, personnel).
- An event happens (a meeting, a launch, a milestone).
- The user corrects a prior belief — first `forget`, then `remember` the new fact.

Use the user's own words when possible. The extraction pipeline will pull out entities and relationships.

## What NOT to remember

Cortex is for knowledge that compounds, not for ephemeral state. Do **not** store:

- Code patterns, conventions, or architecture — these are in the codebase; read the source.
- Git history or who changed what — `git log` is authoritative.
- Step-by-step debugging recipes — the fix is in the code; the commit message has the context.
- Anything documented in this project's `CLAUDE.md`, `README.md`, or similar.
- In-progress task state, current conversation context, "I'm about to do X" notes — these don't survive a session and shouldn't try.

If you're tempted to remember something but it fails these tests, don't.

## The vault

If `cortex export --vault ./vault` has been run for this project, a browsable markdown projection of the graph lives at `./vault/`. It's Obsidian-compatible: each entity is its own page with frontmatter, wikilinks to related entities, backlinks, and source attribution.

Read the vault when you want broad context on an entity — it's easier to skim than running multiple `recall` calls. Use `recall` when you need ranked search results or you don't know the entity's name.

The vault is generated; don't edit it by hand. Changes to the graph are made via `remember` / `forget`.

## Workflow loop

A healthy knowledge cycle looks like:

1. **Ingest** — when the user shares something worth keeping, `remember` it. After meaningful sessions, run `cortex export` to refresh the vault.
2. **Query** — before answering, `recall` (or read the vault). Cite what you find. If your answer relied on graph knowledge, say so.
3. **Lint** — periodically (or when the user asks), look for contradictions in memories, orphan entities, stale claims, and missing relationships. Suggest cleanups.

The user is responsible for what gets stored. You are responsible for keeping the graph useful — well-summarized, well-linked, and free of cruft.

## Customization

This file is yours to edit. Add project-specific guidance: which entity types matter most here, which sources are authoritative, when to escalate to the user before remembering, custom relationship types you want extracted. The static template ships generic on purpose — make it specific.
