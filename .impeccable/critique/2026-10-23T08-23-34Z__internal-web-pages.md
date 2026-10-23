---
target: cortex web app
total_score: 23
p0_count: 0
p1_count: 2
timestamp: 2026-10-23T08-23-34Z
slug: internal-web-pages
---
# Critique: cortex web app (internal/web/pages)

## Design Health Score
| # | Heuristic | Score | Key Issue |
|---|---|---|---|
| 1 | Visibility of System Status | 2 | One-line notices; raw job status; meta-refresh polling |
| 2 | Match System / Real World | 3 | Leaks: "entities, chunks", raw compaction Kind, "Embedding dimensions" |
| 3 | User Control and Freedom | 3 | Strong 30-day restore/undo; revoke has no undo/confirm |
| 4 | Consistency and Standards | 2 | Every button same green; three different delete guards |
| 5 | Error Prevention | 2 | Delete selected/matching: no count, no confirm; compaction pre-checked |
| 6 | Recognition Rather Than Recall | 3 | No active nav; compaction not in nav |
| 7 | Flexibility and Efficiency | 2 | No selection toolbar, no keyboard affordances |
| 8 | Aesthetic and Minimalist Design | 2 | Undifferentiated; memories stacks everything at equal weight |
| 9 | Error Recovery | 2 | Red text only, no next step or link |
| 10 | Help and Documentation | 2 | No first-run path |
| Total | | 23/40 | Acceptable, significant gaps |

## Anti-patterns verdict
Not AI slop; reads as an unstyled default page (admin-console anti-reference). Detector: flat-type-hierarchy (h1=h2=body, 4 pages), side-tab .notice border-left 3px (app.css:12, 3 pages), cramped-padding (connections), line-length ~91ch (consent).

## Priority issues
1. [P1] Destructive actions look and are guarded inconsistently (same green .btn; none/confirm()/typed phrase). Fix: .btn-danger + .btn-secondary, inline count+consequence step, persistent Undo after delete. /impeccable harden
2. [P1] Memories page lacks hierarchy; delete-everything next to reading. Fix: compact search row, list as focus with metadata, selection toolbar, move bulk/clear to collapsed Clean up. /impeccable distill
3. [P2] Visual system is a default stylesheet: flat type scale, no focus-visible, input borders ~1.3:1, side-stripe notice, inline styles, raw timestamps. Fix: OKLCH tokens, 1.25 scale, spacing set, 3:1 borders, focus rings, tinted notice, relative times. /impeccable typeset, /impeccable colorize
4. [P2] Connections: state not legible, revoke+data-delete checkbox look routine. Fix: status badge (text+icon), Revoke... reveal with explicit keep/delete choice, group Add a connection. /impeccable layout
5. [P2] No first-run path; consent "unverified" buried. Fix: 3-step checklist, prominent unverified line, group settings into Chat / Embeddings. /impeccable onboard
