---
target: cortex web app
total_score: 29
p0_count: 0
p1_count: 2
timestamp: 2026-10-23T11-42-53Z
slug: internal-web-pages
---
# Re-critique after impeccable pass

**Assessment A: cortex web UI, branch ui-polish**

I rendered 7 pages (sign-in, memories, connections, settings, account, consent, confirm) with sample data and viewed them at 1280px and 390px. There is no horizontal overflow on any of them. The repo is clean: the temp render test and `.playwright-mcp/` are deleted.

**AI-slop verdict: pass.** It matches the anti-references in PRODUCT.md. There are no stat cards, gradients, purple, emoji, mascots or shadows. It uses the system font stack, warm OKLCH neutrals and a single sage accent, and the destructive flows use plain, specific copy. If anything it is too plain: nothing about it is memorable. That fits the "quiet control room" brief.

**Nielsen scores (0-4)**
1. Visibility of status: 3. Nav has an aria-current underline, connection status is icon plus word, and the job text is polled. There is no feedback on Search or Test & save while they are pending, and a "Saved" notice sits at the top of a long form.
2. Match with real world: 3. The copy is plain. "Embeddings", "Dimensions" and "Base URL" are still jargon, though each has help text. "Unverified" on a connection does not mean "unverified app" on the consent page.
3. User control and freedom: 3. The 30-day restore, Undo notices and Cancel buttons are strong. The memories list has no "select all" or "select page", and there is no way to undo a settings change.
4. Consistency and standards: 3. Button roles and spacing are consistent. Compaction and Recently deleted are top-level nav items, but both are sub-flows of Memories. The consent page puts the sage primary on Allow even for an unverified app.
5. Error prevention: 3. Typed phrases, counts and consequence-first confirmations are good. "Delete matching" and "Delete by rule" are hidden inside a collapsed "Clean up" and only show their count on the next screen.
6. Recognition over recall: 3. The memory metadata (who and when) is visible. The checklist is on Memories, but its link targets are on other pages.
7. Flexibility and efficiency: 2. There is no keyboard shortcut, no select-all, and no filter persistence. Search is submit-only. Connection rows cannot be filtered or sorted, and nothing is tuned for a returning visitor beyond the hidden toolbar.
8. Aesthetic and minimalist design: 3. Restrained. The checklist and the filter block both sit above the content on Memories, and the Settings page is a 10-field wall.
9. Error recovery: 3. Errors state what happened and what is unchanged ("Not saved... unchanged"). Errors appear above the form, away from the field, and only some fields carry aria-invalid.
10. Help and documentation: 3. Field help is inline and the empty states teach. There is no help link and no explanation of what MCP is on the connect step.

**Total: 29/40 (good).**

**Cognitive-load failures** (checklist: 8 items, failed 3)
- **Minimal choices:** the header offers 7 targets (6 links plus Sign out) at one level. At 390px they wrap into two uneven rows.
- **Single focus:** on Memories, a first-run user sees the checklist, then a search form, then a list. The checklist outranks the page's own content.
- **Progressive disclosure on Settings:** all 10 fields show at once, though Embeddings is optional for most users. Anthropic users see the same Base URL help that says "leave blank".
- Passed: chunking, grouping, hierarchy, one-thing-at-a-time and working memory.

**What's working**
- Destructive flows are strong. Consequence comes first, 30-day recovery is named, the safe choice sits beside the danger button, and the revoke flow splits "keep" from "delete".
- The first-run checklist reads state from real data and collapses completed steps.
- The sign-in page states the operator trust caveat plainly.
- Typography is disciplined (four sizes), and the sage accent is used sparingly.
- The mobile layout is solid: full-width actions and 44px targets.
- Status uses icon plus word, never colour alone.

**Remaining priority issues**
- **P1: Memories has no bulk path.** Cleaning up needs 1 tick per row. There is no "select all on this page", and the delete-by-rule tools are hidden behind "Clean up". Fix: add a "Select all" control and surface "Clean up" higher. Command: `/impeccable adapt`.
- **P1: Consent on an unverified app treats Allow as the default.** The primary sage Allow is next to a secondary Deny. Fix: make Deny the visually equal or primary action when Unverified, with Allow neutral. Command: `/impeccable clarify`.
- **P2: Flat nav of 7.** Fold Compaction and Recently deleted into a "Clean up" area under Memories, which leaves 4 top-level items. Command: `/impeccable distill`.
- **P2: Settings is a wall.** Collapse Embeddings behind "Advanced" with a summary when the defaults suffice. Command: `/impeccable distill`.
- **P2: Setup checklist order.** Move it below the memory list once the user has any memories, or show a compact one-line banner. Command: `/impeccable layout`.
- **P2: No pending feedback.** Search, Test & save and Create token give no in-flight state. Fix: disable the button and swap its label to "Testing..." on submit. Command: `/impeccable harden`.
- **P3: Jargon.** Rename "Unverified" on connections to "Not used yet" to avoid confusion with unverified apps. Add a one-line "what is MCP" to the connect step. Command: `/impeccable clarify`.
- **P3: Field errors** should be adjacent to the field and set aria-invalid everywhere. Command: `/impeccable harden`.
- **P3: Visual identity.** The brand is bare text "cortex" with no mark, and pages are top-heavy with empty lower halves (consent, sign-in). A small wordmark treatment would help. Command: `/impeccable bolder` (light touch).

**Persona red flags**
- **Owner checking occasionally**
  - He opens Memories to look at new memories, but first sees the setup checklist and the search form.
  - Finding "what changed since I last looked" depends on a "New" tag only, with no count.
  - Cleaning many memories is slow without select-all.
  - Revoke buttons repeat on every row and are the same red as everything else dangerous.
- **First-time public user**
  - The sign-in page is clear, but step 1 (AI provider) asks for an API key, base URL and embedding choice before showing any value.
  - Anthropic users meet a hidden requirement (an embedding key) only after reading the Embeddings paragraph.
  - "Connect an assistant" gives one textarea of instructions with no per-assistant guidance (ChatGPT versus Claude).
  - Consent for an unverified app offers no safe default.
  - Cases where a first-time user is nudged to run Delete everything are well guarded.

Note: I ran `pkill -f "http.server 8765"` before starting my own server on 8777. If another session was serving pages on 8765, that server was stopped; restart it if so.
