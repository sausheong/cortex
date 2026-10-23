---
target: cortex web app
total_score: 29
p0_count: 0
p1_count: 1
timestamp: 2026-10-23T14-21-23Z
slug: internal-web-pages
---
# Critique v3: cortex web app (with Home, Graph, Vault)

Score 29/40 (good). Scores: status 3, real world 3, control 4, consistency 2, error prevention 3, recognition 3, efficiency 2, minimalist 3, error recovery 3, help 3.

Anti-patterns: AI-slop pass. One ban: .vault-folders a[aria-current=page] 2px sage border-left (app.css:334). Detector: cramped-padding on step-mark/empty-state/vault-files (likely false positives), DESIGN.md drift (1.5rem size, 4px radius, SFMono); overlay: possible text occlusion on connections, long lines on memories; graph detector crashed (detector bug).

Priority issues:
1. [P1] Banned side-stripe on Vault folder nav. Fix: remove border-left, keep weight + wash. /impeccable polish
2. [P2] Three page widths/layout grammars (42/64/72rem). Fix: keep 42rem heading column, breakout only map/list; Vault left rail -> segmented subnav. /impeccable layout
3. [P2] Graph map small, floats, unlabelled on mobile. Fix: fit viewBox to nodes, label <=12 nodes, panel under search on mobile. /impeccable adapt
4. [P2] Home is a feed not a glance. Fix: status strip first, one line per group, reword "Added directly", hide first-run placeholders. /impeccable distill
5. [P3] Connections rows verbose, stacked buttons, repeated status copy. /impeccable clarify
Minor: 1201 vs 1,201; danger button before Cancel on confirm; vault index opens with exported_at box; raw attribute keys in graph panel; path-first vault rows.
