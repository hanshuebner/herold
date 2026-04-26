# 06 — Design system

Tabard's UI design language. Used by tabard-mail today, by tabard-calendar and tabard-contacts when they exist.

This doc currently lives in tabard-mail's tree because there's no other tree yet; when the repo splits into a monorepo, it moves to `packages/design-system/docs/` and the apps reference it from there. The decisions below apply suite-wide.

## Approach

We adopt **IBM Carbon Design System's design language** by reference (carbondesignsystem.com) — its principles, color tokens, type scale, spacing scale, motion curves — and build components ourselves on top of headless Svelte primitives. We do NOT use `carbon-components-svelte`'s component library: the components tabard actually needs (thread row, message accordion, label picker, compose stack, picker overlay) aren't in Carbon, and the Carbon components we'd skip make the bundle heavier than the language is worth.

This is the "shadcn approach" applied to Carbon: borrow the documented rules, own the components.

### Why not full Carbon?

Carbon's component library is enterprise-shaped (data tables, structured forms, multi-step workflows). A keyboard-first mail client is a different shape. Adopting full Carbon means either fighting it on the parts that matter most or accepting a UI that doesn't fit the user's actual workflow.

### Why not build everything from scratch?

Tokens and motion curves are work that's already been done well by Carbon. Re-doing them adds nothing; following them gives us a documented, accessibility-validated foundation we can point at when decisions need to be defended.

## Primitives layer

**Decision: Bits UI** (`bits-ui`) for headless Svelte primitives — Dialog, Popover, Combobox, Listbox, Menu, Tabs, Toggle, Tooltip, AlertDialog, Toast.

Bits UI is the Svelte port of Radix Primitives (built on top of Melt UI). It gives us accessibility (ARIA roles, focus management, keyboard interactions) and behaviour (close-on-outside-click, focus return, escape-to-close) without imposing any visual style. We attach Carbon tokens via class names or CSS custom properties.

We drop to Melt UI directly only when Bits UI lacks a primitive (rare).

## Principles

Six rules that override conventions.

1. **Density beats decoration.** This is a power-user app. Padding is functional, not decorative. Carbon's "compact" type and spacing scale are the defaults; "default" is the exception (used for primary actions and dialog headers only).
2. **Focus is always visible.** Every interactive element has a visible focus ring. The ring is a single token (`--focus`); the same ring on every element. Ring colour is high-contrast (Carbon `focus`).
3. **Motion is functional.** Animation exists only to communicate causality (this came from there) or state (this is loading). Motion does not exist to be admired. All transitions complete inside Carbon's `productive` durations: 70ms / 110ms / 150ms / 240ms.
4. **Errors are sentence-shaped.** No truncated codes. No "something went wrong." Tell the user what happened, where, and what to do — at minimum: "Action failed: <reason>. Retry."
5. **Dark first; light is parity, not afterthought.** Both themes are first-class. We test every component in both. Colour token switches; the design language doesn't bifurcate.
6. **Keyboard equivalence.** Every action that has a button has a keyboard binding (`requirements/10-keyboard.md`). Conversely, keyboard-only actions are surfaced visually somewhere — `?` lists every binding (REQ-KEY-01) so they're discoverable.

## Colour tokens

We use Carbon's role tokens (semantic, not raw colour). The dark-mode and light-mode values are taken from the Carbon Gray 100 (dark) and White (light) themes.

| Token | Role |
|-------|------|
| `--background` | Page background |
| `--layer-01` | Cards, panels, the thread list rows |
| `--layer-02` | Components on top of layer-01 (the reading pane on top of the list, the picker on top of the layout) |
| `--layer-03` | Components on top of layer-02 (a tooltip on top of a picker) |
| `--border-subtle-01` | Dividers within a single layer |
| `--border-strong-01` | Field borders, separators between layers |
| `--text-primary` | Body text, sender name, subject |
| `--text-secondary` | Snippets, dates, helper text |
| `--text-helper` | Captions, placeholder text |
| `--text-on-color` | Text on coloured backgrounds (label chips) |
| `--interactive` | Primary action background, links, accent |
| `--focus` | Focus ring |
| `--support-error` | Error banners and toasts |
| `--support-success` | Success indicators (auth-results pass) |
| `--support-warning` | Auth-results soft-fail, suspicious-attachment warning |
| `--support-info` | Informational banners (mailing list chip background) |

Concrete values come from Carbon's published token files; we vendor the dark and light theme JSON as `packages/design-system/tokens/{dark,light}.json` (when the package exists; lives in `apps/mail` until then).

### Label colours

The 12-colour palette for user-assigned label colours (`requirements/03-labels.md` REQ-LBL-04) is a separate set tuned for legibility on both themes. Picked from Carbon's expressive palette (cyan-50, magenta-50, purple-50, teal-50, blue-50, green-50, yellow-30, orange-50, red-50, warm-gray-50, cool-gray-50, gray-50). Each carries a paired text-on-color token for contrast.

## Typography

IBM Plex Sans for UI. IBM Plex Mono for monospace contexts (Message-ID display, raw-headers view, code blocks in body).

Type scale follows Carbon's productive scale:

| Token | Size / line / weight | Use |
|-------|---------------------|-----|
| `body-compact-01` | 14px / 18px / 400 | Default UI text (list rows, body in reading pane) |
| `body-01` | 14px / 20px / 400 | Long-form text (the message body fallback when no HTML iframe) |
| `body-02` | 16px / 22px / 400 | The HTML-iframe injected base style (so plain text in messages reads at 16px) |
| `heading-compact-01` | 14px / 18px / 600 | Section titles (sidebar group, picker section) |
| `heading-01` | 16px / 22px / 600 | Modal titles (compose window header, settings panel) |
| `heading-02` | 16px / 22px / 600 | Page-level (thread subject in reading pane) |
| `heading-03` | 20px / 28px / 400 | Used sparingly — e.g. unread count on Inbox in the sidebar |
| `code-01` | 12px / 16px / 400 mono | Inline code, IDs |
| `code-02` | 14px / 20px / 400 mono | Block code in messages |

Letter spacing is per Carbon's defaults; we don't override.

## Spacing

Carbon's 8-pt grid plus half-step:

| Token | px |
|-------|-----|
| `spacing-01` | 2 |
| `spacing-02` | 4 |
| `spacing-03` | 8 |
| `spacing-04` | 12 |
| `spacing-05` | 16 |
| `spacing-06` | 24 |
| `spacing-07` | 32 |
| `spacing-08` | 40 |
| `spacing-09` | 48 |
| `spacing-10` | 64 |

Layout rules:
- Thread list row vertical padding: `spacing-03` (8px).
- Reading pane outer padding: `spacing-05` (16px).
- Sidebar item vertical padding: `spacing-03` (8px).
- Modal content padding: `spacing-06` (24px).
- Toast inset from viewport: `spacing-06` (24px) bottom, `spacing-05` (16px) horizontal.

## Motion

| Token | Duration | Curve | Use |
|-------|----------|-------|-----|
| `duration-fast-01` | 70ms | productive | Small, isolated changes (focus ring, hover) |
| `duration-fast-02` | 110ms | productive | Most UI transitions (toggle, button press) |
| `duration-moderate-01` | 150ms | productive | Medium transitions (menu open, picker open, toast slide-in) |
| `duration-moderate-02` | 240ms | productive | Slower transitions (modal open, sidebar collapse) |

Productive easing: `cubic-bezier(0.2, 0, 0.38, 0.9)` (entrance) / `cubic-bezier(0.2, 0, 1, 0.9)` (exit), per Carbon.

We do not use Carbon's "expressive" motion durations (400ms+). They communicate moments of weight that don't exist in a mail client.

## Elevation

Three layers, no more.

- **Layer 0** — page background.
- **Layer 1** — sidebar, thread list, reading pane (peer-to-peer; same layer).
- **Layer 2** — pickers (label, snooze, identity), compose window, modal dialogs.
- **Layer 3** — tooltips, toasts.

Elevation is communicated by `--layer-N` background tokens plus a single token shadow. We do NOT stack drop shadows for "depth"; we change the layer token.

## Component anatomy — bespoke

The components tabard owns and Carbon doesn't give us. Each is a Svelte component built on Bits UI primitives where one applies; pure-CSS where no behaviour is needed.

### Thread row

```
┌─[chk] ─[★] ─[▶] ─[sender summary  3] ─[subject — snippet]─ ─[chips]─ ─[📎]─ ─[date]─┐
└──────────────────────────────────────────────────────────────────────────────────────┘
```

- `[chk]`: checkbox. Hidden when no rows selected; shown on hover or when any row is selected.
- `[★]`: star (toggles `$flagged`). Filled when starred; outlined otherwise.
- `[▶]`: importance chevron (toggles `$important`). Filled (yellow) when important; outlined otherwise.
- Sender summary: collapsed multi-sender ("Olaf, Ich, Olaf") with the user shown as "Ich" (or locale equivalent). Trailing small numeral indicates the message count in the thread; absent for single-message threads.
- Subject + snippet: subject in `--text-primary`; snippet in `--text-secondary` after a separator. Single line, ellipsised.
- Label chips render between the subject snippet and the right-side icons; truncate with overflow chip if more than ~2 fit.
- `[📎]`: attachment indicator when the thread has any attachment.
- Date: relative within the year; year-prefixed older. Locale-aware (`requirements/22-internationalization.md` REQ-I18N-21).
- Built on a plain `<div role="option">` inside a `role="listbox"` parent (`requirements/13-nonfunctional.md` REQ-A11Y-02).
- Unread state: `--text-primary` weight 600 on sender + subject; read state: weight 400 + `--text-secondary` on snippet.
- Selected state: `--layer-02` background, optional `--interactive` left-border accent.
- Hit target: full row click → opens thread.
- Tokens: `body-compact-01`, padding `spacing-03` vertical / `spacing-05` horizontal, divider `--border-subtle-01`.

### Global bar

```
┌─────────────────────────────────────────────────────────────────────────────┐
│         [🔍 search input ........................] [×] [⚙ filter]   [● Aktiv ▾] [?] [⚙] │
└─────────────────────────────────────────────────────────────────────────────┘
```

- Mounted by the suite shell at the top of every route.
- Search input is the dominant element, ~50% of the bar's width on desktop. Placeholder reflects the active context (mail / chat / calendar).
- Right-aligned controls: presence dropdown, help button, settings button.
- Tokens: `body-compact-01`, height `spacing-08` (40px), padding `spacing-05` horizontal, background `--layer-01`, bottom border `--border-subtle-01`.
- Hidden in compose modal and call modal.

### Inner sidebar (mail)

```
┌─[ 📝 Compose ]──────────────────────────────┐
├──────────────────────────────────────────────┤
│ 📥 Inbox                                14   │
│ 🕒 Snoozed                                   │
│ Σ Important                                  │
│ ▶ Sent                                       │
│ 📄 Drafts                                 1  │
│ 📨 All Mail                                  │
│ ▼ More                                       │
├──────────────────────────────────────────────┤
│ Labels                                    +  │
│ ▶ work                                       │
│ ▶ family                                     │
│ ▼ More                                       │
└──────────────────────────────────────────────┘
```

- Compose button at top, full-width, prominent (`--interactive` background, light variant `--layer-02`).
- System mailboxes in fixed order (`requirements/03-labels.md` REQ-LBL-06a).
- "More" expander reveals Spam, Trash, etc.
- Labels section: a heading row with "+" affordance (creates label) followed by the label tree. Top-level labels with children render an expand triangle.
- Built on plain Svelte components; no Bits UI primitive needed (the picker pattern doesn't apply here — these are persistent navigation entries).
- Tokens: `body-compact-01` on rows, `heading-compact-01` on section headings ("Labels"), padding `spacing-03` vertical per row.

### List header

```
┌─[☑ ▾] ─[⟳ refresh] ─[⋮ more]─────────────────────[ 1–50 von 1247  < > ]─┐
├──────────────────────────────────────────────────────────────────────────┤
│ [Allgemein]   [Werbung]   [Soziale Netzwerke]   [Benachrichtigungen]    │  ← category tabs
└──────────────────────────────────────────────────────────────────────────┘
```

- Bulk-select control: checkbox + dropdown (Bits UI Combobox). Subset filters per `requirements/09-ui-layout.md` REQ-UI-44a.
- Refresh button: spinner while in flight.
- Three-dot more-menu: view-scope actions per REQ-UI-09c.
- Right side: page-range counter + prev/next page navigation (both arrow buttons disable when at first/last page).
- Below the header: category-tab strip in views that show categories. Active tab has an accent underline (`--interactive`).
- Tokens: `body-compact-01`, height `spacing-08` for the header row + `spacing-08` for the tab strip when present, sticky to the top of the list.

### Reading pane toolbar

```
┌─[←] ─[📥 archive] ─[! spam] ─[🗑 delete] ─[✉ mark unread] ─[🕒 snooze] ─[✓ tasks] ─[📂 move] ─[🏷 label] ─[⋮]─┐
└────────────────────────────────────────────────────────────────────────────────────────────────────────────────┘
```

- Sits at the top of the reading pane when a thread is open.
- All buttons are icon-only with tooltips; the reading-pane toolbar is dense by default. A user setting toggles to a labelled "simple toolbar" mode (REQ-UI-19c).
- Add-to-tasks is hidden in v1 (no tasks app).
- Three-dot menu opens via Bits UI Menu (Popover with Listbox semantics).
- Tokens: icon size 20px, `spacing-04` between groups (back arrow, mail-action group, classification group, organisation group, more), `spacing-08` height.

### Message header (per-message in the accordion)

```
┌─[🟣 avatar]   Sender Name <sender@example.com>      📎  Fr., 20. Okt. 2023, 21:45  ⭐ 😀 ↩ ⋮─┐
│            🔓 to me ▼                                        [rscsupdt.zip]                  │
└──────────────────────────────────────────────────────────────────────────────────────────────┘
```

- Avatar: initial-circle when no avatar image; coloured-by-hash when no avatar source.
- Sender display name + email; encryption / authentication indicator below (`requirements/18-authentication-results.md` REQ-AR-30..33 surfaced visibility).
- Recipient summary ("an mich" / "to me", "an Hans und 4 weitere") clickable to expand the To/Cc/Bcc list.
- Right-side controls (in row order): attachment indicator (paperclip), date+time (locale formatted), star, react (emoji picker), reply (when expanded), per-message context menu (three-dot).
- Attachment chips render under the date when collapsed; under the body when expanded (handled by `17-attachments.md`).
- Tokens: `body-compact-01` on header text; avatar 32px circle; right-side icons 18px; padding `spacing-04`.

### Category tabs

```
┌─[📥 Allgemein] [🏷 Werbung] [👥 Soziale Netzwerke] [ⓘ Benachrichtigungen]─┐
└──[─────────]──────────────────────────────────────────────────────────────┘
   ↑ accent underline on active tab
```

- Above the thread list when categorisation is active (`requirements/05-categorisation.md` REQ-CAT-10).
- Each tab: icon + label + (optional) unread count badge when the category has unreads.
- Active tab: `--interactive` accent underline, `--text-primary` weight 600.
- Inactive tabs: `--text-secondary`, no underline.
- Built on Bits UI Tabs.

### Message accordion

```
┌─ Sender ───────────────────────── Date ─┐
│ snippet preview when collapsed          │
├─────────────────────────────────────────┤  ← when expanded
│ to / cc                                 │
│                                         │
│ [HTML iframe or plain-text body]        │
│                                         │
└─[reply]─[reply-all]─[forward]──────────┘
```

- Collapsed: shows sender + date + snippet.
- Expanded: shows full headers, body iframe (`architecture/04-rendering.md`), action toolbar.
- Built on Bits UI Accordion. One message can be expanded independently of others.
- Tokens: header at `body-compact-01`, body inherits the iframe's typography.

### Label picker

```
┌─[search input ............................]─┐
│ ┌─ Recent ─────────────────────────────────┐ │
│ │ ☑ ProjectX                              │ │
│ │ ☐ Personal                              │ │
│ ├─ All ────────────────────────────────────┤ │
│ │ ☐ Work / ProjectY                       │ │
│ │ ☐ Volunteer                             │ │
│ └──────────────────────────────────────────┘ │
└─[Apply]──[Cancel]────────────────────────────┘
```

- Built on Bits UI Combobox.
- Filter input narrows the list by substring.
- Multi-select: each row toggles a checkmark; Apply commits, Cancel discards.
- Keyboard: `↑/↓` navigates, `Enter` toggles, `Apply` is `Enter` again or `Esc` cancels. The picker registers its own keymap (`architecture/05-keyboard-engine.md`).

### Compose window

```
┌─ New message ─────────────────────────── _ ⤢ ✕ ┐
│ From: [identity dropdown]                       │
│ To:   [recipient autocomplete]                  │
│ Subject: [...........................]          │
├─────────────────────────────────────────────────┤
│                                                 │
│ [ProseMirror editor]                            │
│                                                 │
├─[B I U …]─────────────────[Send] ▼──────[🗑]──┤
└─────────────────────────────────────────────────┘
```

- Anchored bottom-right of viewport.
- Three states: minimised (header bar only), default, expanded (full-screen-ish modal).
- Stack of up to 3 (`requirements/09-ui-layout.md` REQ-UI-04). Stacked windows offset 24px right and below.
- Built on Bits UI Dialog (with `modal={false}` so it doesn't trap the screen).
- The body uses the ProseMirror editor (`implementation/01-tech-stack.md`), styled with our compose-schema tokens.

### Picker overlay (generic)

The shape used by label, snooze, From-identity, and search-suggestions pickers. A floating panel anchored to a trigger element, with:

- A filter input (optional).
- A keyboard-navigable list of options.
- Confirm-on-Enter, dismiss-on-Escape.
- Built on Bits UI Popover + Listbox.

### Toast / snackbar

```
┌─ Action message ─────────────────[ Undo ]─[ ✕ ]─┐
└──────────────────────────────────────────────────┘
```

- Bottom-centre of viewport, `spacing-06` from edge.
- One at a time (`requirements/11-optimistic-ui.md` REQ-OPT-14).
- Auto-dismiss at 5s; pauses on hover; clears on action click.
- Built on Bits UI Toast.
- Slide-in from below at `duration-moderate-01`.

### Virtualised list shell

A pure component (no Bits UI primitive needed):

- Accepts a height, an item-count, an item-height (fixed in v1), and a row renderer.
- Owns the scroll container, an IntersectionObserver to trigger the next-page fetch when within ~10 rows of the end (`requirements/13-nonfunctional.md` REQ-PERF-05).
- DOM stays bounded at ~200 rows regardless of total count.

### Chat panel

```
┌─[chat] ──────── [search] ─[+ DM] ─[+ Space] ─[─ collapse]┐
│ ┌─ Pinned ────────────────────────────────────────────┐ │
│ │ 🟢 Charlotte                       12:04   • • •    │ │
│ │ Hans, Alice (Space "Project X")    11:48   2        │ │
│ ├─ Direct messages ──────────────────────────────────┤ │
│ │ 🟢 Bob                              09:30           │ │
│ │ ⚪ Eve                              yesterday       │ │
│ ├─ Spaces ────────────────────────────────────────────┤ │
│ │ Engineering                         Tue             │ │
│ │ Volunteers                          Mon             │ │
│ └────────────────────────────────────────────────────┘ │
│ ──── (active conversation when one is open) ────       │
└──────────────────────────────────────────────────────── ┘
```

- Mounted by the suite shell, anchored to the right edge of the viewport. Collapsible to a 48px-wide notification rail (just unread badges + presence dots); expanded width ~340px.
- Two stacked regions: the conversation list (top, scrollable), and the active conversation (bottom, when one is open). The list collapses to make room for the active conversation; both regions scroll independently.
- Persists across the shell's route changes (`requirements/08-chat.md`). The user navigates from `/mail/inbox` to `/calendar/today` and the panel keeps its state and connection.
- States: collapsed / expanded-no-conversation / expanded-with-conversation / fullscreen (`/chat/conversation/<id>`, used when the user wants chat to dominate). Transitions at `duration-moderate-01`.
- Built on Bits UI Dialog (for fullscreen), plain Svelte components otherwise. The active-conversation region embeds a ProseMirror editor with the chat schema (`requirements/08-chat.md` REQ-CHAT-21).
- Tokens: list at `body-compact-01`, conversation messages at `body-01`, padding `spacing-03` per row.

### Conversation message

```
┌─[avatar] Charlotte ─────────── 12:04 ─[edited]──┐
│ Hey, can you take a look at the proposal?       │
│                                                  │
│ [inline image thumbnail]                         │
│                                                  │
│ 🎉 3   👀 1                  └─ replied via 📞 ─┘│
└──────────────────────────────────────────────────┘
                     [Charlotte read at 12:05]
```

- Avatar + sender name + timestamp + (optional) edited indicator on header.
- Body is the rendered ProseMirror output of the chat schema. Inline images render lazily with click-to-expand.
- Reaction chips below the body, each clickable to toggle the user's reaction.
- Read-receipt indicator (DMs only; in Spaces, available via "Read by" affordance).
- Hover reveals the per-message action menu: react, reply, edit (within window), delete (own messages only).

### Call modal

Full-screen modal triggered by REQ-CALL-20:

```
┌──────────────────────────────────────────────────────────┐
│                                                          │
│   ┌─────────────────────────────────────────┐            │
│   │                                         │            │
│   │                                         │  ┌──────┐  │
│   │           remote video                  │  │local │  │
│   │           (Charlotte)                   │  │video │  │
│   │                                         │  └──────┘  │
│   │                                         │            │
│   └─────────────────────────────────────────┘            │
│                                                          │
│       [🎤 mute]  [📷 camera]  [⛶ fullscreen]  [📞 hang]  │
└──────────────────────────────────────────────────────────┘
```

- Full-window modal, focus trapped, Escape does NOT dismiss (REQ-CALL-20).
- Local video tile is a draggable PIP overlay (default bottom-right of remote video). Mutable / movable; resets on next call.
- Controls dock at the bottom; auto-hide after 3s of no mouse movement, reveal on movement.
- Bits UI: this one wraps Dialog with custom focus-trap configuration (no escape-to-dismiss).

### Coach hint chip

The single hint chip rendered in the coach strip (`requirements/09-ui-layout.md` REQ-UI-06e, `requirements/23-shortcut-coach.md` REQ-COACH-50..56).

```
┌─[ Try ⌘⏎ to send       ×]─┐  ← when state = learning
└─────────────────────────────┘

┌─[ ↩ Welcome back — try e to archive       ×]─┐  ← when state = forgotten
└────────────────────────────────────────────────┘
```

- One chip at a time, anchored centre-bottom of the suite-shell strip.
- Chip background `--layer-02`; subtle border `--border-subtle-01`; radius 9999 (pill).
- Action label in `body-compact-01`; key glyphs in `code-01` mono with `--layer-03` background, padding `spacing-01` horizontal, `spacing-02` minimum width per glyph.
- Optional "Welcome back" prefix in `--text-secondary`, weight 400, when the action's state is `forgotten` (REQ-COACH-35).
- Dismiss × button on the right; 12px icon; tap target padded to `spacing-04`.
- Slide-up + fade-in `duration-moderate-01`; auto-dismiss fade-out `duration-fast-02` after 6 s (paused on hover); replace cross-fade `duration-fast-02` when a new hint pre-empts.
- `role="status"` `aria-live="polite"` on the strip so screen readers announce as hints appear.

### Sidebar entry

```
[icon] Label name              42
```

- Variant for system mailbox (Inbox, Sent, …): no colour swatch, system icon.
- Variant for user label: 8×8 colour swatch from `requirements/03-labels.md` REQ-LBL-04 palette.
- Variant for category: bold weight, no count badge.
- Active state: `--layer-02` background, `--text-primary` text.
- Hover: `--layer-02` background.

## Cross-suite consistency

When tabard-calendar and tabard-contacts arrive, the constraints they inherit:

- All tokens (color, type, spacing, motion) are shared verbatim. No app re-defines a token.
- Bits UI is the primitives layer for all three apps.
- Bespoke components are app-specific. The mail-only components (thread row, message accordion, label picker, compose stack) live in `apps/mail/src/components/`. Calendar / contacts will have their own (event card, day-view grid, contact card, etc.) in their own `src/components/`. Shared *generic* components (Toast, picker overlay shell, virtualised list shell) graduate to `packages/design-system/`.
- Suite shell — if/when there's a single chrome with app switcher — lives in `packages/design-system/` too. Provisional decision: each app is its own URL with no shared shell, and cross-app navigation is plain links. Confirm in `notes/open-questions.md` Q16.

## What's deliberately not specified here

- Specific component code. This doc is the constraint set; components are written in the app(s) and reviewed against it.
- Marketing-grade illustration, brand identity, logo work. Tabard is a tool, not a brand experience.
- Density variants (comfortable / cosy / compact). Single density in v1 (`implementation/04-simplifications-and-cuts.md`).
