---
name: Mithra
description: Self-hosted family operating system for couples, in the MithraHQ paper system (Lexington Vanta derived). Mithra is the blue square.
colors:
  mithra-blue: "oklch(51.9% 0.1261 259.6)"
  link-blue: "oklch(45% 0.115 259.6)"
  blue-wash: "oklch(94.5% 0.028 259.6)"
  blue-mist: "oklch(97.5% 0.012 259.6)"
  blue-edge: "oklch(89.5% 0.05 259.6)"
  navy-ink: "oklch(22% 0.035 265)"
  slate-800: "oklch(34% 0.03 265)"
  muted-ink: "oklch(46% 0.022 265)"
  on-navy: "oklch(95% 0.01 85)"
  on-navy-muted: "oklch(78% 0.02 265)"
  paper: "oklch(99.6% 0.003 85)"
  canvas: "oklch(98.8% 0.005 85)"
  paper-surface: "oklch(96.2% 0.008 85)"
  paper-hover: "oklch(93.5% 0.012 85)"
  hairline: "oklch(88% 0.012 85)"
  mustard: "oklch(74.7% 0.1231 86.4)"
  terracotta: "oklch(52% 0.15 41.2)"
  coral: "oklch(64.2% 0.1541 41.2)"
typography:
  display:
    fontFamily: "Bricolage Grotesque, InterVariable, Inter, ui-sans-serif, system-ui, sans-serif"
    fontSize: "2.5rem"
    fontWeight: 500
    lineHeight: 1.1
    letterSpacing: "-0.025em"
  headline:
    fontFamily: "Bricolage Grotesque, InterVariable, Inter, ui-sans-serif, system-ui, sans-serif"
    fontSize: "1.625rem"
    fontWeight: 500
    lineHeight: 1.2
    letterSpacing: "-0.025em"
  panel-number:
    fontFamily: "Bricolage Grotesque, InterVariable, Inter, ui-sans-serif, system-ui, sans-serif"
    fontSize: "clamp(2.75rem, 4vw, 3.75rem)"
    fontWeight: 500
    lineHeight: 1
    letterSpacing: "-0.03em"
    fontFeature: "\"tnum\" 1"
  title:
    fontFamily: "Bricolage Grotesque, InterVariable, Inter, ui-sans-serif, system-ui, sans-serif"
    fontSize: "1.0625rem"
    fontWeight: 600
    lineHeight: 1.3
  body:
    fontFamily: "InterVariable, Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, Segoe UI, sans-serif"
    fontSize: "1rem"
    fontWeight: 400
    lineHeight: 1.55
    fontFeature: "\"liga\" 1, \"calt\" 1"
  label:
    fontFamily: "InterVariable, Inter, ui-sans-serif, system-ui, sans-serif"
    fontSize: "0.8125rem"
    fontWeight: 700
    lineHeight: 1.3
  button:
    fontFamily: "Bricolage Grotesque, InterVariable, Inter, ui-sans-serif, system-ui, sans-serif"
    fontSize: "1rem"
    fontWeight: 600
    lineHeight: 1.2
rounded:
  mark: "0.1875rem"
  field: "0.5rem"
  control: "0.75rem"
  panel-compact: "1.125rem"
  panel: "1.5rem"
  pill: "999px"
spacing:
  xs: "0.25rem"
  sm: "0.5rem"
  md: "0.75rem"
  lg: "1rem"
  xl: "1.5rem"
  2xl: "2rem"
  3xl: "3rem"
  4xl: "4rem"
  page-gutter: "clamp(1.25rem, 3vw, 2.5rem)"
components:
  button-primary:
    backgroundColor: "{colors.navy-ink}"
    textColor: "{colors.paper}"
    typography: "{typography.button}"
    rounded: "{rounded.control}"
    padding: "0.65rem 1.5rem"
    height: "2.75rem"
  button-primary-hover:
    backgroundColor: "{colors.mithra-blue}"
  button-quiet:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.navy-ink}"
    typography: "{typography.button}"
    rounded: "{rounded.control}"
    padding: "0.65rem 1.5rem"
    height: "2.75rem"
  button-quiet-hover:
    backgroundColor: "{colors.paper-hover}"
  toolbar-action:
    backgroundColor: "transparent"
    textColor: "{colors.navy-ink}"
    rounded: "{rounded.control}"
    padding: "0.5rem 0.875rem"
    height: "2.5rem"
  toolbar-action-hover:
    backgroundColor: "{colors.paper-hover}"
  input:
    backgroundColor: "{colors.paper-surface}"
    textColor: "{colors.navy-ink}"
    typography: "{typography.body}"
    rounded: "{rounded.field}"
    padding: "0.6rem 0.75rem"
    height: "2.8rem"
  input-hover:
    backgroundColor: "{colors.paper-hover}"
  nav-tab:
    backgroundColor: "transparent"
    textColor: "{colors.muted-ink}"
    padding: "0 0.75rem"
    height: "4rem"
  nav-tab-active:
    textColor: "{colors.navy-ink}"
  panel-coming-up:
    backgroundColor: "{colors.mithra-blue}"
    textColor: "{colors.on-navy}"
    rounded: "{rounded.panel}"
    padding: "1.5rem"
  panel-worth-checking:
    backgroundColor: "{colors.paper-surface}"
    textColor: "{colors.navy-ink}"
    rounded: "{rounded.panel}"
    padding: "1.5rem"
  panel-only-you:
    backgroundColor: "{colors.navy-ink}"
    textColor: "{colors.on-navy}"
    rounded: "{rounded.panel}"
    padding: "1.5rem"
  panel-review-status:
    backgroundColor: "color-mix(in oklch, oklch(74.7% 0.1231 86.4) 30%, oklch(99.6% 0.003 85))"
    textColor: "{colors.navy-ink}"
    rounded: "{rounded.panel}"
    padding: "clamp(1.25rem, 3vw, 2rem)"
  card-paper:
    backgroundColor: "{colors.paper}"
    textColor: "{colors.navy-ink}"
    rounded: "{rounded.control}"
    padding: "1.5rem"
  status-message:
    backgroundColor: "{colors.blue-wash}"
    textColor: "{colors.navy-ink}"
    rounded: "{rounded.control}"
    padding: "0.875rem 1rem"
---

# Design System: Mithra

## Overview

**Creative North Star: "The Household Paper Desk"**

Mithra is a calm desk of warm paper for two adults running a household together. Neutrals are a faintly warm off-white; ink is a deep navy rather than black; the single voice of colour is Mithra blue, the blue square in the MithraHQ family (HealthTrends is the coral arch, TinyTots the mustard circle). The system is shared across MithraHQ and derives from the Lexington "Vanta" theme: display headings in Bricolage Grotesque, running text in InterVariable, big rounded colour panels for the few numbers that matter, and hairline lists for everything else.

The authenticated app wears the "paper shell" (chosen 2026-10-23). From 64rem up, one light top bar holds the brand, five underlined text tabs (Family Brief, Week in Review, Finance, Health, Planning) and the tools (Import, Add update in navy, Quick navigation, Account). Utility destinations (Import, Settings, Help) sit in the toolbar or Account menu on desktop and stay in the Menu under 64rem, where a sticky mobile header keeps every item. Density is moderate: generous heading space, readable 60 to 70ch measures, and tables only where records demand them.

Mithra is light only. There is no dark theme yet; navy surfaces are panels inside the light world, not a mode.

**Key Characteristics:**
- Warm paper canvas, navy ink, one blue accent; mustard and terracotta in supporting roles.
- Bricolage Grotesque for h1, h2, the brand, panel numbers and buttons; InterVariable for everything else.
- Big rounded colour panels (1.5rem radius) carry key counts and status; hairline rules separate list items.
- Sentence case throughout; no eyebrow labels, uppercase or monospace costume.
- Flat by default: subtle ring shadows on paper cards, lift only on hover of linked panels.
- WCAG AA contrast, visible focus rings, reduced motion respected.

## Colors

A warm paper palette with navy ink and a single confident blue, tuned in OKLCH; the full accent and base scales live as custom properties at the top of `web/static/styles.css`.

### Primary
- **Mithra Blue** (mithra-blue): the family identity colour. The brand square, the "Coming up" panel, hover state of navy buttons, `accent-color` and caret colour.
- **Link Blue** (link-blue): text links, link actions in lists, and the default focus ring. Darker than Mithra Blue so text clears AA on paper.
- **Blue Wash / Blue Mist / Blue Edge** (blue-wash, blue-mist, blue-edge): status messages, privacy notes, pending capture, quick navigation highlight, and the review observation panel with its inset blue-edge ring.

### Secondary
- **Mustard** (mustard): the Week in Review status panel (mixed 30% into paper), text selection (45% wash), and the focus ring on colour panels where a blue ring would vanish.

### Tertiary
- **Terracotta** (terracotta): attention and errors. Invalid field borders, import blockers (tinted 7 to 9% into white), warning strokes in charts.
- **Coral** (coral): defined as the HealthTrends family colour for cross-product marks; Mithra surfaces do not use it.

### Neutral
- **Navy Ink** (navy-ink): body text, headings, primary buttons, the "Only you" panel, the Add update button.
- **Slate 800** (slate-800): secondary copy on the mustard status panel.
- **Muted Ink** (muted-ink): supporting copy, inactive nav tabs, form labels, timestamps.
- **On Navy / On Navy Muted** (on-navy, on-navy-muted): text on blue and navy panels and on the Add update button.
- **Paper** (paper): cards, dialogs, mobile header and menu.
- **Canvas** (canvas): page background and the translucent desktop top bar.
- **Paper Surface / Paper Hover** (paper-surface, paper-hover): fields, the "Worth checking" panel, quiet surfaces, and their hover state.
- **Hairline** (hairline): every 1px rule: header borders, list separators, inset panel rings.

### Named Rules
**The One Blue Rule.** Mithra blue is the only saturated hue that identifies Mithra. Mustard and terracotta have jobs (status, attention); coral belongs to HealthTrends.

**The Navy Not Black Rule.** Ink is navy-ink. No pure black text or surfaces anywhere.

## Typography

**Display Font:** Bricolage Grotesque (with InterVariable, system sans fallback), self-hosted at `web/static/fonts/bricolage.woff2`
**Body Font:** InterVariable (with Inter, system sans fallback), self-hosted at `web/static/fonts/inter-variable.woff2`

**Character:** Bricolage gives headings and numbers a warm, slightly characterful grotesque voice at weight 500; Inter keeps records, forms and lists neutral and legible.

### Hierarchy
- **Display** (500, 2.5rem, 1.1, -0.025em, max 18ch, balanced wrap): page h1.
- **Headline** (500, 1.625rem, 1.2, -0.025em, balanced wrap): section h2; the review status h2 rises to clamp(1.75rem, 3vw, 2.25rem).
- **Panel Number** (500, clamp(2.75rem, 4vw, 3.75rem), 1, -0.03em, tabular figures): counts inside colour panels; 2rem under 45rem.
- **Title** (600, 1.0625rem): colour panel labels in the display face.
- **Body** (400, 1rem, 1.55): running text, capped near 60ch for introductions and status copy.
- **Label** (700, 0.8125rem to 0.875rem): form field labels in muted ink, sentence case.
- **Button** (600, display face): every button and the file picker button; toolbar tools use 500.

### Named Rules
**The Sentence Case Rule.** Every heading, label, tab and button is sentence case. No uppercase transforms, no letter-spaced small caps, no monospace labels.

**The Two Faces Rule.** Bricolage for h1, h2, brand, panel numbers and buttons; Inter for everything else. No third face; monospace is reserved for real `kbd` key caps.

## Layout

Pages sit in a centred shell: `min(100% - 3rem, 64rem)` for public pages, widening to 72rem inside the authenticated shell at 64rem and up (80rem below that breakpoint with 1rem gutters). The desktop top bar is sticky, 4rem tall, padded to align with an 80rem frame, and uses canvas at 94% over the page. Under 64rem the sidebar and toolbar give way to a sticky 4rem mobile header with a Menu disclosure that lists every destination, including utilities.

Rhythm follows the spacing scale (0.25rem to 4rem). Page headings get 4rem above and 2rem below; sections are separated by 2rem to 3rem. Breakpoints in use: 40rem, 45rem (panels stack, rows collapse), 64rem (paper shell), 90rem (Quick navigation shows its text label). Family Brief lays its three panels in an equal three-column grid with 1rem gaps; under 45rem they become compact full-width rows.

### Named Rules
**The Utility Drawer Rule.** On desktop, only the five views are tabs. Import, Settings and Help never become tabs; they live in the toolbar or Account menu, and every item stays reachable from the mobile Menu.

## Elevation & Depth

Flat by default, with tonal layering doing most of the work: canvas, paper, paper-surface and the colour panels separate by tone. Paper cards carry a near-invisible ring shadow; floating layers (mobile menu, quick navigation dialog) carry the soft stacked shadow. Linked colour panels lift 0.1875rem with a navy-tinted drop on hover. The dialog backdrop is navy at 48% with a 3px blur.

### Shadow Vocabulary
- **Subtle ring** (`box-shadow: 0 0 0 1px rgb(30 31 37 / 9%), 0 1px 2px rgb(30 31 37 / 8%)`): paper cards, capture and import panels, quiet controls.
- **Normal float** (`box-shadow: 0 0 0 1px rgb(14 63 126 / 4%), 0 1px 1px rgb(42 51 69 / 4%), 0 3px 3px -1.5px rgb(42 51 70 / 4%), 0 6px 6px -3px rgb(42 51 70 / 4%), 0 12px 12px -6px rgb(14 63 126 / 4%)`): menus and dialogs.
- **Panel lift** (`box-shadow: 0 1rem 2rem -1.25rem oklch(22% 0.035 265 / 45%)`): hover on linked colour panels only.

### Named Rules
**The Lift Means Link Rule.** A panel lifts on hover only when the whole panel is a link. Static surfaces never move.

## Shapes

Soft, generous corners scaled to the element: 0.75rem for buttons, cards and menus; 0.5rem for fields; 1.5rem for colour panels (1.125rem in their compact mobile form); full pills for small status chips and the mic button. The brand mark is a 1.125rem blue square with 0.1875rem corners that rotates 90 degrees on brand hover. Borders are 1px hairlines or inset rings; there are no thick or coloured side stripes.

## Components

### Buttons
Confident and quiet at once: navy slabs in the display face.
- **Shape:** gently rounded (0.75rem), at least 2.75rem tall.
- **Primary:** navy-ink with paper text, 0.65rem by 1.5rem padding, Bricolage 600.
- **Hover / Focus:** background eases to Mithra blue over 220ms; presses translate 1px; focus shows a 0.1875rem link-blue outline offset 0.25rem.
- **Quiet / Secondary:** paper or paper-surface with navy text and the subtle ring; hover to paper-hover.
- **Toolbar tools:** transparent, display face 500, paper-hover on hover. Add update is the one navy toolbar button and hovers to blue; its `kbd` hint sits on a darker navy key cap.

### Cards / Containers
- **Corner Style:** 0.75rem.
- **Background:** paper on the canvas, or blue-mist/blue-wash for notes and pending states.
- **Shadow Strategy:** subtle ring at rest (see Elevation & Depth).
- **Border:** none; edges come from the ring shadow or a hairline.
- **Internal Padding:** 1.5rem, or clamp(1rem, 3vw, 2rem) on large forms.

### Inputs / Fields
- **Style:** paper-surface fill, 0.5rem corners, inset 1px paper-hover ring, about 2.8rem tall.
- **Focus:** the ring becomes a 2px Mithra blue ring; no outline doubling.
- **Error / Disabled:** invalid fields take a terracotta border with a soft terracotta outline and a short message below; disabled controls drop to 50 to 55% opacity with a not-allowed cursor.

### Navigation
- **Desktop (64rem and up):** text tabs in Inter 500 at 0.9375rem, muted ink, filling the 4rem bar height. Hover turns them navy; the current tab goes 600 navy with a 2px navy underline that scales in from the centre over 260ms.
- **Mobile:** sticky paper header with the brand and a Menu summary drawn as an outlined pill (inset hairline ring, display face). The menu panel is paper with the normal float shadow and full-width rows that tint paper-hover when current.
- **Quick navigation:** a paper dialog (max 38rem) with a filled search field; the active result tints blue-wash.

### Colour Panels (signature)
Big rounded panels that carry the numbers a couple acts on.
- **Family Brief glance:** three panels, minimum 11rem tall, 1.5rem radius and padding. Coming up is Mithra blue, Worth checking is paper-surface with an inset hairline, Only you is navy. Each holds a display-face label, a panel number and a short note, and the whole panel is a link to its list with a mustard focus ring. Under 45rem they collapse to single rows with the number on the right and the note hidden.
- **Week in Review status:** a mustard panel (30% mustard in paper) with navy text and a larger h2; no shadow.
- **Review observation:** blue-mist with an inset blue-edge ring at the same 1.5rem radius.

### Hairline Lists
Records, recent captures, imports and coaching items are plain lists separated by 1px hairlines with 1rem vertical padding, bold item titles, muted supporting text and link-blue actions in weight 700.

## Do's and Don'ts

### Do:
- **Do** put the few key counts and statuses in big rounded colour panels (1.5rem radius) and keep everything else on paper with hairline lists.
- **Do** use Mithra blue for identity and the "Coming up" panel, navy for primary actions and the "Only you" panel, mustard for weekly status.
- **Do** set h1, h2, brand, panel numbers and buttons in Bricolage Grotesque 500 to 600, and everything else in InterVariable.
- **Do** write every label, tab and button in sentence case.
- **Do** keep a visible focus ring on every interactive element: link-blue by default, mustard on colour panels.
- **Do** disable transforms and transitions under `prefers-reduced-motion: reduce` for the brand mark, panels and tab underline.
- **Do** keep Import, Settings and Help out of the desktop tabs and inside the toolbar or Account menu, while the mobile Menu keeps every item.
- **Do** meet WCAG AA contrast for text on every panel colour.

### Don't:
- **Don't** add eyebrow labels above headings.
- **Don't** use uppercase transforms, letter-spaced caps or monospace as decoration.
- **Don't** use coloured side-stripe borders on cards, alerts or list items.
- **Don't** use pure black, or a second saturated accent beyond the assigned roles; coral is HealthTrends' colour.
- **Don't** add a dark theme ad hoc; Mithra is light only until a dark theme is designed for the whole family.
- **Don't** lift or animate surfaces that are not links.
