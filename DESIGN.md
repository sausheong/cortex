---
name: cortex
description: A calm, warm control room for a personal memory service. Apple Settings, but warmer.
colors:
  paper: "oklch(0.975 0.008 85)"
  ink: "oklch(0.23 0.012 85)"
  muted-ink: "oklch(0.47 0.014 85)"
  hairline: "oklch(0.89 0.01 85)"
  control-border: "oklch(0.6 0.014 85)"
  sage: "oklch(0.42 0.09 165)"
  on-sage: "oklch(0.985 0.008 85)"
  sage-wash: "oklch(0.94 0.03 165)"
  info-wash: "oklch(0.945 0.025 165)"
  info-line: "oklch(0.78 0.06 165)"
  brick: "oklch(0.46 0.17 27)"
  on-brick: "oklch(0.985 0.006 27)"
  brick-wash: "oklch(0.955 0.025 27)"
  brick-line: "oklch(0.62 0.14 27)"
  paper-dark: "oklch(0.2 0.008 85)"
  ink-dark: "oklch(0.93 0.01 85)"
  muted-ink-dark: "oklch(0.73 0.012 85)"
  hairline-dark: "oklch(0.31 0.01 85)"
  control-border-dark: "oklch(0.58 0.012 85)"
  sage-dark: "oklch(0.78 0.1 165)"
  brick-dark: "oklch(0.74 0.14 25)"
typography:
  headline:
    fontFamily: "system-ui, -apple-system, 'Segoe UI', Roboto, 'Helvetica Neue', sans-serif"
    fontSize: "1.75rem"
    fontWeight: 600
    lineHeight: 1.2
    letterSpacing: "-0.02em"
  title:
    fontFamily: "system-ui, -apple-system, 'Segoe UI', Roboto, 'Helvetica Neue', sans-serif"
    fontSize: "1.25rem"
    fontWeight: 600
    lineHeight: 1.2
    letterSpacing: "-0.01em"
  body:
    fontFamily: "system-ui, -apple-system, 'Segoe UI', Roboto, 'Helvetica Neue', sans-serif"
    fontSize: "1rem"
    fontWeight: 400
    lineHeight: 1.55
  label:
    fontFamily: "system-ui, -apple-system, 'Segoe UI', Roboto, 'Helvetica Neue', sans-serif"
    fontSize: "0.875rem"
    fontWeight: 500
    lineHeight: 1.4
rounded:
  control: "6px"
  panel: "8px"
  group: "10px"
spacing:
  xs: "4px"
  sm: "8px"
  md: "12px"
  lg: "16px"
  xl: "24px"
  section: "40px"
components:
  button-primary:
    backgroundColor: "{colors.sage}"
    textColor: "{colors.on-sage}"
    rounded: "{rounded.control}"
    padding: "8px 16px"
    height: "44px"
  button-secondary:
    backgroundColor: "transparent"
    textColor: "{colors.ink}"
    rounded: "{rounded.control}"
    padding: "8px 16px"
    height: "44px"
  button-secondary-hover:
    backgroundColor: "{colors.sage-wash}"
  button-danger:
    backgroundColor: "{colors.brick}"
    textColor: "{colors.on-brick}"
    rounded: "{rounded.control}"
    padding: "8px 16px"
    height: "44px"
  button-danger-quiet:
    backgroundColor: "transparent"
    textColor: "{colors.brick}"
    rounded: "{rounded.control}"
    padding: "8px 16px"
    height: "44px"
  input:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.ink}"
    rounded: "{rounded.control}"
    padding: "8px"
  notice:
    backgroundColor: "{colors.info-wash}"
    textColor: "{colors.ink}"
    rounded: "{rounded.panel}"
    padding: "12px 16px"
  error:
    backgroundColor: "{colors.brick-wash}"
    textColor: "{colors.ink}"
    rounded: "{rounded.panel}"
    padding: "12px 16px"
  list-group:
    backgroundColor: "{colors.paper}"
    rounded: "{rounded.group}"
    padding: "12px 16px"
---

# Design System: cortex

## 1. Overview

**Creative North Star: "The Private Notebook"**

cortex holds a person's whole life of context, so the interface behaves like a well-made private notebook or an account-settings pane: warm paper, quiet ink, one calm green. The owner opens it briefly, checks what the assistants remembered, changes one thing, and leaves. Nothing performs, sells, or decorates. In PRODUCT.md's words, "Calm, trustworthy, personal. Quiet confidence, privacy-forward."

The system is "Apple Settings, but warmer". Content sits in a single 42rem column on a tinted-warm neutral ground. Structure comes from hairline rules and grouped lists, not cards or shadows. Color is restrained: sage green for the primary action, links and current navigation, brick red only where something is destructive, revoked or unverified. Light is warm paper and dark is warm charcoal; both follow the OS setting. The one distinctive detail is the sage check mark in the first-run checklist, which turns a ring into a done state.

It explicitly rejects the anti-references in PRODUCT.md: the generic SaaS dashboard (stat cards, gradient accents, hero metrics, AI-startup purple), the admin or database console, the cute chatbot app (mascots, emoji), and the dark hacker terminal.

**Key Characteristics:**
- One column, 42rem wide, 68ch reading measure, generous vertical rhythm.
- Hairline-divided groups instead of cards; flat at rest.
- System font only; hierarchy from weight and size, not family.
- One accent (sage) at low coverage; brick is reserved for danger and state.
- Every state is double-coded: text or icon plus color, never color alone.
- 44px touch targets and visible 2px focus rings throughout.

## 2. Colors

A warm-neutral ground with one sage accent and one brick danger role, all in OKLCH. Neutrals are tinted toward hue 85 and never pure black or white.

### Primary
- **Sage** (oklch(0.42 0.09 165); dark mode oklch(0.78 0.1 165)): The primary button fill, links, the current-nav underline, the active status and completed setup steps. Sage Wash (oklch(0.94 0.03 165)) is its hover tint on secondary buttons and selections.

### Secondary
- **Brick** (oklch(0.46 0.17 27); dark mode oklch(0.74 0.14 25)): Destructive buttons, the revoked and unverified states, field errors. Brick Wash (oklch(0.955 0.025 27)) fills error messages and the unverified trust panel; Brick Line (oklch(0.62 0.14 27)) is their border.

### Neutral
- **Paper** (oklch(0.975 0.008 85); dark oklch(0.2 0.008 85)): Page and input background.
- **Ink** (oklch(0.23 0.012 85); dark oklch(0.93 0.01 85)): Body text and headings.
- **Muted Ink** (oklch(0.47 0.014 85); dark oklch(0.73 0.012 85)): Labels, metadata, help text, inactive nav. Meets 4.5:1 on Paper.
- **Hairline** (oklch(0.89 0.01 85); dark oklch(0.31 0.01 85)): Dividers and group borders.
- **Control Border** (oklch(0.6 0.014 85); dark oklch(0.58 0.012 85)): Borders of inputs and secondary buttons; held at 3:1 so controls are findable.
- **Info Wash** (oklch(0.945 0.025 165)) with **Info Line** (oklch(0.78 0.06 165)): Neutral notices and the one-time token panel.

### Named Rules
**The One Voice Rule.** Sage appears on a small share of any screen: one primary button, links, the current nav item. Its rarity is the point.

**The Danger Is Earned Rule.** Brick marks only destructive steps and states of loss or risk. Never use it for decoration or emphasis.

**The Tinted Neutral Rule.** No `#000` or `#fff`. Every neutral carries hue 85 at low chroma.

## 3. Typography

**Display Font:** none; the system UI stack (system-ui, -apple-system, Segoe UI, Roboto, Helvetica Neue, sans-serif)
**Body Font:** the same stack
**Label/Mono Font:** ui-monospace, SFMono-Regular, Menlo, only for tokens, URLs and code, at 0.9em

**Character:** Native and unbranded, so cortex feels like part of the user's own device. Hierarchy is carried by weight (400, 500, 600) and a four-step fixed rem scale.

### Hierarchy
- **Headline** (600, 1.75rem, 1.2, -0.02em; 1.5rem under 560px): The single page title.
- **Title** (600, 1.25rem, 1.2, -0.01em): Section headings and fieldset legends.
- **Body** (400, 1rem, 1.55): Prose, list content, memory text; capped at 68ch. Sub-headings (h3) share this size and are set apart by weight 600 and 24px above.
- **Label** (500, 0.875rem, 1.4): Form labels, nav links, metadata, status text and help text in Muted Ink.

### Named Rules
**The Four-Step Rule.** Only 0.875, 1, 1.25 and 1.75rem exist. No fluid type in the app UI.

**The Tabular Rule.** Dates, counts and sizes use tabular numerals so columns of facts align.

## 4. Elevation

Flat by default. Depth comes from hairline borders, tonal washes and grouping, not shadows. There are no box-shadows anywhere. The only layering is the sticky selection toolbar on Memories, which sits on Paper with a bottom hairline, and the skip link, which appears on focus with a border.

### Named Rules
**The Flat-By-Default Rule.** Surfaces never carry shadows. If a surface needs separation, give it a hairline or a wash.

**The No Nested Cards Rule.** A bordered group never contains another bordered group; use rules between rows instead.

## 5. Components

### Buttons
- **Shape:** gently curved (6px), 44px minimum height, 8px 16px padding.
- **Primary:** Sage fill, On-Sage text, weight 500. One per page.
- **Secondary:** transparent, Ink text, Control Border; hover fills Sage Wash. The quiet default.
- **Danger:** Brick fill, weight 600, used only on the final step of a destructive flow. **Danger Quiet** is Brick text with a Brick Line border for the first, reversible-looking trigger (for example Revoke).
- **States:** hover shifts fill 12% toward Ink; active nudges 1px down; disabled drops to 55% opacity with a not-allowed cursor; focus is a 2px Sage ring offset 2px. Color transitions run 150ms ease-out and collapse under reduced motion.
- **Link button:** text-only, Sage, for low-weight actions such as Sign out (Muted Ink in the header).

### Inputs / Fields
- **Style:** Paper fill, 1px Control Border, 6px radius, 8px padding, label above in Label type.
- **Focus:** the shared 2px Sage outline.
- **Error:** `aria-invalid` thickens the border to 2px Brick; the message sits below in Brick at 0.875rem, never color alone.
- **Checkboxes** in memory rows are 20px.

### Lists and grouped containers
- **Connection list:** one 10px-radius hairline group, rows separated by hairlines, 12px 16px padding, status as icon plus word.
- **Memory list:** borderless rows divided by hairlines; the whole row is the label for its checkbox. Selection toolbar is sticky and appears only when something is selected (with JS).
- **Facts list (Account):** definition rows with muted term left, medium-weight value right, hairline top and bottom.
- **Setup checklist:** a 10px group with numbered rings that become sage check marks when done; completed steps collapse to one line.

### Messages
- **Notice:** Info Wash fill, Info Line border, 8px radius. **Error:** Brick Wash fill and Brick Line border, with the lead word in bold Brick. Both are capped at the 68ch measure.
- **Trust panel (consent):** hairline group; the unverified variant switches to Brick Wash with a Brick heading and an explicit warning.

### Navigation
- **Style:** header row with the brand at left and links at right in Label type, 44px tall, wrapping on narrow screens.
- **States:** Muted Ink at rest, Ink on hover, current page in weight 600 with a 2px Sage underline and `aria-current="page"`. A skip link is first in tab order.

### Destructive confirmation
Every delete, revoke or compaction has a dedicated confirm step (or an inline two-choice panel) that states the consequence in bold, restates what will change, names the 30-day recovery, and puts the safe choice beside the Danger button. It works without JavaScript.

## 6. Do's and Don'ts

### Do:
- **Do** use Sage on the one primary action, links and the current nav item only.
- **Do** state the consequence and the 30-day recovery before any destructive button, in plain words with no em dashes.
- **Do** pair every state color with text or an icon (revoked, deleted, unverified, done).
- **Do** keep hit areas at 44px and focus rings at 2px Sage with 2px offset.
- **Do** keep controls at 3:1 border contrast and text at WCAG 2.2 AA or better, in light and dark.
- **Do** mask secrets and show a token only once, in a focusable panel.
- **Do** use hairlines and spacing (4, 8, 12, 16, 24, 40) to group; 40px between sections.
- **Do** respect `prefers-reduced-motion` and keep every safety-critical flow working without JavaScript.

### Don't:
- **Don't** build a generic SaaS dashboard: no stat cards, gradient accents, hero metrics or AI-startup purple.
- **Don't** build an admin or database console: no raw tables of rows or phpMyAdmin-style forms.
- **Don't** add cute chatbot touches: no mascots, bubbly illustrations or emoji.
- **Don't** drift toward a dark hacker terminal: no neon on black, no monospace everywhere.
- **Don't** use box-shadows, glassmorphism, gradient text or side-stripe borders.
- **Don't** nest bordered groups inside bordered groups.
- **Don't** use `#000`, `#fff` or untinted grays.
- **Don't** use Brick for anything that is not destructive, revoked, unverified or an error.
- **Don't** signal state by color alone, or hide the safe option behind the destructive one.
- **Don't** introduce a second typeface, fluid type, or a type size outside the four-step scale.
