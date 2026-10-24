# Product

## Register

product

## Users

Primarily the owner, Sau Sheong. He opens the cortex web app occasionally to:
- check what his assistants have remembered,
- connect or revoke assistants and access keys for scripts and agents,
- adjust AI provider settings,
- clean up memories (bulk delete, restore, compaction).

Most of cortex's use happens elsewhere, inside ChatGPT, Claude and his own agents via MCP. The web app is the control room, not the daily driver.

Sign-up is open to anyone with a Google account. Other users are a secondary audience, and they need a clear first run: sign in, then set up an AI provider, then connect an assistant.

## Product Purpose

cortex is a personal memory service that every assistant and agent shares. The web app lets a user see and control what cortex knows about them and who can access it:
- **Memories & storage:** what's stored, who wrote it, how much space it takes.
- **Connections:** which assistants and agents are connected, with revoke.
- **AI provider:** the user's own key and models.
- **Consent:** approving a new assistant.
- **Compaction and recovery:** previewed, undoable clean-up.

Success means the user trusts what they see: it is obvious what is stored and who can touch it, and every destructive action is understood and recoverable.

## Brand Personality

Calm, trustworthy, personal. Quiet confidence, privacy-forward. It holds someone's whole life, so it should feel like a well-made private notebook or account settings, never like a dashboard selling itself. Plain, direct copy; no hype.

References:
- **Apple Settings / iCloud:** plain, trustworthy account management.
- **Linear:** precision and restraint.

## Anti-references

- **Generic SaaS dashboard:** stat cards, gradient accents, hero metrics, AI-startup purple.
- **Admin/database console:** raw phpMyAdmin-style tables and forms.
- **Cute chatbot app:** mascots, bubbly illustrations, emoji.
- **Dark hacker terminal:** neon on black, monospace everywhere.

## Design Principles

1. **Trust through clarity.** Always show what is stored, who wrote it, and who has access, in plain words.
2. **Safe by default.** Every destructive action states its consequence and its 30-day recovery. Undo is closer than confirm.
3. **Quiet control room.** The user is here briefly to check or change something. Make the current state legible at a glance and the next action obvious; no decoration.
4. **Respect the private.** Secrets stay masked, and the interface never feels like it's surveilling or upselling the user.
5. **One path for first run.** For new users: sign in, then AI provider, then connect an assistant, with nothing else competing.

## Accessibility & Inclusion

- WCAG 2.2 AA: contrast, full keyboard operation, visible focus, labelled controls.
- Respect `prefers-reduced-motion`.
- Never use colour alone to signal state (revoked, deleted, unverified).
