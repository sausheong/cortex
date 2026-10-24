---
target: cortex web app
total_score: 30
p0_count: 0
p1_count: 1
timestamp: 2026-10-24T01-11-36Z
slug: internal-web-pages
---
# Critique v6: cortex web app (after round 5, deployed)

Score 30/40 (good). Scores: status 3, real world 3, control 4, consistency 3, error prevention 3, recognition 3, efficiency 2, minimalist 3, error recovery 3, help 3. Reviewer noise band 30-32 across v4-v6; each round's issues fixed, fresh reviewer surfaces new ones.

Anti-patterns: AI-slop pass, no bans. Detector: templates clean; rendered 10x cramped-padding (false positives: .step-mark, details.fold, .vault-files), Roboto/single-font = system-ui fallback, cream palette intentional, memories occlusion false positive (0x0 element); graph scan crashed (detector SVG bug).

Cognitive load: 2/8 (minimal choices: 7-item nav, Memories controls, Graph; working memory: delete confirm hides selection).

Priority issues:
1. [P1] Graph labels missing on rich graph (Mei, Lin, Stripe), types shape-only. Collision-aware labels or hover/focus labels. /impeccable adapt
2. [P2] Same assistant Active on Connections, Idle on Home; Home counts don't sum to rows. One status vocabulary + derivation. /impeccable clarify
3. [P2] Bulk delete confirm says "the memories you ticked" with no snippets. Show first few + "and N more". /impeccable clarify, /impeccable harden
4. [P2] First run: 7-item nav competes with checklist; step 2 "Connect an assistant" has no link (step 3 too). /impeccable onboard
5. [P3] Home copy contradicts state ("Runs overnight once your AI provider is set up" while provider configured); four sections; "Added by you or imported" awkward. /impeccable distill

Personas: Jordan (step 2 no link, MCP address unexplained); Alex (no shortcuts, full-page confirm per delete); Sam (graph shape-only meaning, mobile nav wrap).

Minor: owner node "Mentioned in 0 memories"; Compaction checkbox beside kind label not content; "Roll up" vs "rollup"; mobile Graph panel pushes list down.
