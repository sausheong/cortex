---
target: cortex web app
total_score: 32
p0_count: 0
p1_count: 2
timestamp: 2026-10-23T16-29-50Z
slug: internal-web-pages
---
# Critique v4: cortex web app (after ui-polish-2)

Score 32/40 (good). Scores: status 3, real world 3, control 4, consistency 3, error prevention 4, recognition 3, efficiency 2, minimalist 3, error recovery 4, help 3.

Anti-patterns: AI-slop pass, no bans (side stripe gone). Sameness of single column is the only templated feel. Detector: templates clean; rendered: cramped-padding x6 (false positives: .fold, .step-mark, .vault-files), overused-font Roboto (system-ui fallback in Linux Chromium, note only), cream-palette (intended), line-length ~86ch on memories/connections (mild, real), text-occlusion on connections closed details panels (likely false positive), graph scan crashed (detector SVG id bug).

Cognitive load: 2/8 failures (minimal choices, progressive disclosure on Memories).

Priority issues:
1. [P1] Connections buries "Connect an assistant" under the agent-token form; first-run step 2 lands here. Fix: lead with assistant instructions, fold agent tokens into "For your own agents" disclosure, plainer name. /impeccable onboard, /impeccable layout
2. [P1] Memories front-loads ~9 filter controls; rows undifferentiated. Fix: Search + Written by visible, fold dates/sort/per-page/quick ranges; group by day. /impeccable distill
3. [P2] Mobile Graph puts entity panel before the map. Fix: map first at all widths, then panel, then list. /impeccable adapt
4. [P2] Vault and Compaction leak internals: cortex_id, ISO timestamps, snake_case relations (works_at), loud mint props wash; Compaction 13px checkboxes, bare Replaces/With preview. /impeccable clarify, /impeccable polish
5. [P3] Rename and Revoke equal weight on every connection row (four red buttons). Fix: Rename as text link or per-row Manage disclosure. /impeccable quieter

Personas: Jordan (Connections token-first, Settings jargon: Base URL/Dimensions/Embeddings, no "done" moment after checklist); Sam (muted 0.875rem near 4.5:1, small compaction checkboxes, focus order after details expand); Alex (no shortcuts, no cross-page search, Compaction/Deleted only via Memories subnav, Graph vs Vault overlap).

Minor: mobile nav wraps with Sign out among links; Storage line shows no number on Home; "Has not run yet" reads as warning; Compaction h1 glued to subnav; Consent buttons tight under trust panel; Vault footer ISO timestamp.
