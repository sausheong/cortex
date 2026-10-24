---
target: cortex web app
total_score: 31
p0_count: 0
p1_count: 0
timestamp: 2026-10-24T00-47-21Z
slug: internal-web-pages
---
# Critique v5: cortex web app (after ui-polish-3)

Score 31/40 (good). Scores: status 3, real world 3, control 4, consistency 3, error prevention 4, recognition 3, efficiency 2, minimalist 3, error recovery 3 (provisional, static capture), help 3. Within noise of v4 (32); v4 issues resolved, new reviewer surfaced different ones.

Anti-patterns: AI-slop pass, no bans. Detector: templates clean; rendered 6x cramped-padding (false positives: details.fold hairline, .step-mark, .vault-files), Roboto = system-ui fallback in Linux Chromium (false positive), no text-occlusion with Connections panels open; graph scan crashed (detector SVG id bug). Detector found nothing real.

Cognitive load: 2/8 failures (Home chunking with 4-5 sections; Graph hierarchy).

Priority issues:
1. [P2] Two left edges on Vault and Graph (heading x=304, list x=128); Account facts list 27px short. /impeccable layout
2. [P2] Home status contradicts itself ("You are set up. An assistant is connected" above "No assistants active"); 20px status sentence off the type scale. Derive from one source. /impeccable clarify
3. [P2] Graph weak with sparse data: overlapping nodes at 2 nodes, big empty map. Min separation, size map to node count, list first under ~5 nodes. /impeccable adapt
4. [P3] Memories Select all and Clean up far apart; Clean up at bottom on mobile. /impeccable distill
5. [P3] Disclosure toggles (Filters, More filters, Edit or delete) styled like nav links. Add chevron or secondary style. /impeccable polish

Personas: Jordan (no "use defaults" path in Settings; "Not used yet" unexplained on Home); Alex (no shortcuts or header search, multi-screen bulk delete); owner (Home needs reading 4 sections to find what changed).

Minor: Organisation vs Organization spelling; "Never used" duplicates "Not used yet"; Copy address button vs field width; first-run Home empty lower half; nav 7 links + Sign out; mobile header two rows.
