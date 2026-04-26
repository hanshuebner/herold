# 09 — UI layout

The visual structure of the client: how the screen is divided, how lists render, how a thread reads.

## Overall layout

The mail app's screen, from outermost to innermost:

```
┌─[ global bar (suite shell) ]──────────────────────────────────────────┐
├─[ rail ]──┬─[ inner sidebar ]──┬─[ pagination + 3-dot ]───────┬─[ chat ]─┤
│           │                    │ [ category tabs ]            │  panel  │
│  Mail     │ [ Compose ]        ├──────────────────────────────┤         │
│  Chat     │ Inbox        14    │                              │ (per    │
│           │ Snoozed            │                              │  arch/  │
│           │ Important          │  thread list (virtualised)   │  06-    │
│           │ Sent               │                              │  design)│
│           │ Drafts        1    │                              │         │
│           │ All Mail           │                              │         │
│           │ ▼ More             │                              │         │
│           │                    ├──────────────────────────────┤         │
│           │ Labels         +   │  reading pane (when a thread │         │
│           │ ▶ work             │  is open); else preview      │         │
│           │ ▶ family           │                              │         │
│           │ ▼ More             │                              │         │
└───────────┴────────────────────┴──────────────────────────────┴─────────┘
```

| ID | Requirement |
|----|-------------|
| REQ-UI-01 | Three-pane layout (rail + sidebar combined as the left region; thread list centre; reading pane right). The chat panel is a fourth column on the right edge, mounted by the suite shell — see `08-chat.md` and `../architecture/01-system-overview.md`. |
| REQ-UI-02 | The inner sidebar is collapsible to a thin (rail-only) state. Collapsed/expanded state persists per-account in `localStorage`. |
| REQ-UI-03 | The thread list / reading pane split ratio is user-adjustable via a drag handle and persists. |
| REQ-UI-04 | Compose opens as a modal in the bottom-right corner. Multiple compose windows stack (cap: 3). Above the cap, Send-or-discard prompts on a fourth open attempt. |
| REQ-UI-05 | Toast / snackbar notifications appear bottom-centre with Undo where applicable. Auto-dismiss at 5 seconds. |

## Global bar (suite shell)

A persistent bar at the top of every route in the suite. Owned by the suite shell, not the mail app — described here because most of its functionality drives mail.

| ID | Requirement |
|----|-------------|
| REQ-UI-06 | The global bar contains, left-to-right: search input, advanced-search button (filter icon), presence dropdown, help button, settings button. |
| REQ-UI-07 | Search input is context-aware: in mail routes it searches mail (`07-search.md`); in chat routes it searches chat (`08-chat.md` REQ-CHAT-80..82); in calendar / contacts (when those apps exist) it searches their respective stores. The placeholder text indicates the active context. |
| REQ-UI-08 | Active queries surface a clear-search affordance (×) inside the input. Clearing returns to the prior view. |
| REQ-UI-09 | Advanced-search button opens a structured filter panel inline below the bar (not a separate route). Filter shape per route: in mail, fields for From / To / Subject / Has-attachment / Date range / Label / Size; in chat, From / Conversation / Date range. Submitting builds a structured query and runs it. |

### Global bar — non-search controls

| ID | Requirement |
|----|-------------|
| REQ-UI-06a | Presence dropdown: shows current presence (`online` / `away` / `offline`) plus "show as offline" toggle (`08-chat.md` REQ-CHAT-50..54). Dropdown is hidden if the chat capability isn't advertised. |
| REQ-UI-06b | Help button: opens the keyboard-shortcut help overlay (the same one `?` opens) plus a link to the user docs. |
| REQ-UI-06c | Settings button: navigates to `/settings` (`20-settings.md`). |
| REQ-UI-06d | The global bar is hidden in the compose modal and in the call modal — both take focus and shouldn't share top-of-window real estate. |

### Coach strip

| ID | Requirement |
|----|-------------|
| REQ-UI-06e | The shortcut coach (`23-shortcut-coach.md`) renders in a 24px-tall strip docked at the bottom of the suite shell. Always present; empty when no hint is active. Hidden in compose / call modals and while the keyboard help overlay (`?`) is open. |

## Sidebar

Two regions: an outer **rail** with suite-app navigation, and an **inner sidebar** with the active app's navigation tree. Mail's inner sidebar is what most of this section describes.

### Rail

| ID | Requirement |
|----|-------------|
| REQ-UI-12a | The rail shows app icons for the top-level suite apps that are deployed: Mail, Chat (the panel toggle, also addressable as the `/chat` fullscreen route), eventually Calendar and Contacts. |
| REQ-UI-12b | Each rail entry shows an unread / activity badge: Mail's badge is the global unread count across mail; Chat's badge is the count of conversations with unread messages (excluding muted). |
| REQ-UI-12c | The rail is always visible; not collapsible. |

### Inner sidebar (mail)

| ID | Requirement |
|----|-------------|
| REQ-UI-13a | Top of the inner sidebar: a prominent "Compose" button (also `c` shortcut). |
| REQ-UI-13b | Below Compose: the system-mailbox list in this fixed order — Inbox, Snoozed, Important, Sent, Drafts, All Mail. Spam, Trash, and any further system folders live under a "More" expander (default collapsed; state persists). |
| REQ-UI-13c | Each system-mailbox row shows the unread count when non-zero. The Inbox count is also reflected on the rail's Mail badge. |
| REQ-UI-13d | Below the system mailboxes: a "Labels" section with a "+" affordance to create a label. Labels render as a tree (REQ-MODEL-05); top-level labels at the root, children indented. Each label shows its colour swatch and unread count. |
| REQ-UI-13e | The Labels section also has a "More" expander when the count exceeds a threshold (default: more than 7 visible; persisted threshold is per-account configurable in settings). |
| REQ-UI-13f | The currently-active mailbox/label is highlighted (`--layer-02` background). |

## Thread list

### List header (above the thread rows)

| ID | Requirement |
|----|-------------|
| REQ-UI-09a | Above the thread list: a header bar with — left-to-right — the bulk-select control (checkbox + dropdown, REQ-UI-44a), refresh button, three-dot more-menu, and on the right: a page-range counter ("1–50 von 1,247") plus prev/next page buttons. |
| REQ-UI-09b | Refresh: triggers `Email/changes` for the active view; if state hasn't advanced, the call is a no-op (idempotent). The button briefly shows a spinner while in flight. |
| REQ-UI-09c | Three-dot menu offers view-scope actions (apply to the entire view, not just selection): "Mark all as read", "Mark all as not important", and a "View settings" entry (page size, conversation grouping toggle). |
| REQ-UI-09d | Page navigation uses `Email/query` cursors. Prev/next move by the configured page size (default 50, configurable in settings). The counter format is locale-aware (see `22-internationalization.md`). |
| REQ-UI-09e | Below the list header (in mailboxes that have categorisation): the category-tab strip per `05-categorisation.md` REQ-CAT-10..14. Tabs are visible only in views where categorisation applies (Inbox); other views hide the strip. |

### Row layout

| ID | Requirement |
|----|-------------|
| REQ-UI-10 | Each row shows, left-to-right: checkbox (visible on hover or when any row is selected), star, importance chevron (`$important` keyword), sender summary (multi-sender), subject + snippet (single line, ellipsised), label chips, attachment indicator (paperclip), date. |
| REQ-UI-10a | Sender summary collapses multi-message threads: "Volker..Volker 3" when one external sender has sent 3 messages; "Olaf, Ich, Olaf 3" when the user ("Ich") replied between two messages from Olaf. The summary lists the most recent up to ~3 distinct senders, with "Ich" highlighting the user's own messages. The thread message count appears as a small numeral after the sender summary. |
| REQ-UI-10b | Importance chevron renders as a small arrow glyph in the row, before the sender. Filled when `$important` is set; outline when not. Clicking toggles the keyword. |
| REQ-UI-10c | Star is filled when `$flagged` is set; outline when not. Clicking toggles. |
| REQ-UI-10d | Snippet text follows a separator (e.g. " — ") after the subject and renders in `--text-secondary`. |
| REQ-UI-10e | Date renders as relative when within the current year ("20. Apr.", "Mo."), absolute year-prefixed when older ("13. Jan. 2024"). Format is locale-aware. |
| REQ-UI-11 | Unread threads render the sender summary and subject in weight 600 (`--text-primary`); read threads in weight 400 with snippet in `--text-secondary`. |
| REQ-UI-12 | Clicking a row (or pressing Enter on the focused row) opens the thread in the reading pane. |
| REQ-UI-13 | A checkbox appears on hover for multi-select. Pressing `x` toggles selection on the focused row without requiring hover. |
| REQ-UI-14 | "Select all" selects all threads in the current view. For result sets > 50, a confirmation banner is shown ("This selects all N threads"). |
| REQ-UI-15 | A bulk-actions toolbar replaces the per-row toolbar in the list header when ≥ 1 thread is selected (REQ-UI-50). |
| REQ-UI-16 | The list virtualises rendering: only rows in (and near) the viewport are in the DOM. Scrolling fetches additional pages via `Email/query` with the next position cursor. |

## Reading pane

### Thread header

When a thread is open, the top of the reading pane shows the thread-level affordances.

| ID | Requirement |
|----|-------------|
| REQ-UI-19a | The thread header has two rows. First row: thread-action toolbar (REQ-UI-19b). Second row: the thread subject with search-term highlighting (when the thread was opened from a search) plus the importance chevron and trailing thread-level actions (print, open-in-new-window). |
| REQ-UI-19b | Thread-action toolbar — left-to-right — back arrow (returns to the list), archive, report-spam, delete, mark-unread (returns to list and marks the thread `$seen=null`), snooze, add-to-tasks (cut for v1; hidden), move-to (opens a single-mailbox picker), label (opens the label picker), three-dot more-menu (REQ-UI-19c). |
| REQ-UI-19c | Three-dot more-menu items (thread-level): mark-as-important / not-important, mute-thread, filter-similar, forward-as-attachment, "Switch to simple toolbar" (toggles a compact toolbar variant). The "simple toolbar" mode is a user preference persisted in settings. |
| REQ-UI-19d | Subject row: the rendered thread subject; if opened from a search, matched terms are highlighted (`<mark>` background `--support-info`). Trailing icons: print (opens the browser print dialog scoped to the thread) and open-in-new-window (opens the thread in a new browser tab via the suite shell's routing). |

### Message accordion

Each message in the thread renders as an accordion entry (`02-mail-basics.md` REQ-MAIL-02). The header is per-message; the body is the rendered HTML inside the sandboxed iframe.

| ID | Requirement |
|----|-------------|
| REQ-UI-20 | The reading pane shows all messages in the thread. All are collapsed except the most recent unread message — or the most recent message if all are read. |
| REQ-UI-21 | Each message header shows: sender avatar (initial-circle when no avatar), sender display name + email address, recipient summary ("an mich" / "to me", "an Hans und 4 weitere" / "to Hans and 4 others"), date+time. Collapsed messages additionally show the snippet. |
| REQ-UI-21a | The recipient-summary affordance is clickable: expands inline to show the full To / Cc / Bcc list with avatars. |
| REQ-UI-21b | Per-message right-side controls (collapsed and expanded): attachment indicator (paperclip when present), star (toggles `$flagged`), react button (emoji picker; reactions stored as a server-side `Email.reactions` extension per `02-mail-basics.md` § Reactions). When expanded, also: reply button (the most-common single-action shortcut), three-dot more-menu (per-message context menu, see `02-mail-basics.md` REQ-MAIL-130..142). |
| REQ-UI-21c | Encryption / authentication indicator: when authentication results indicate a problem (`18-authentication-results.md` REQ-AR-30..33), an indicator appears in the per-message header. Clicking expands the verbose tooltip (REQ-AR-40..42). |
| REQ-UI-22 | Clicking a collapsed message expands it. Clicking an expanded message's header collapses it. |
| REQ-UI-23 | Inline images load on demand by default — not automatically — to prevent tracking-pixel execution. The user can opt-in per sender or per thread. See `13-nonfunctional.md` REQ-SEC. |
| REQ-UI-24 | HTML message bodies render inside a sandboxed iframe. See `../architecture/04-rendering.md`. |
| REQ-UI-25 | A "Show quoted text" toggle collapses / expands quoted reply chains. |
| REQ-UI-25a | Below the message body (when expanded), three pill-shaped action buttons appear: Reply, Forward, React (emoji). These mirror REQ-UI-21b but at the bottom for at-a-glance access after reading. They follow the same actions as the toolbar buttons. |
| REQ-UI-25b | Attachments render as chips below the body (`17-attachments.md` REQ-ATT-20..25). Inline (CID) images are referenced from within the iframe body, not the chip list. |

## Selection model

Multi-selection in the thread list — gestures, range-select, bulk actions, and what selection survives.

### Gestures

| ID | Requirement |
|----|-------------|
| REQ-UI-40 | Click a row's checkbox: toggles selection on that row. |
| REQ-UI-41 | `Cmd/Ctrl+click` a row's body: toggles selection on the row without opening it. (Mouse-only alternative to clicking the checkbox.) |
| REQ-UI-42 | `Shift+click` a row's body: range-selects from the row most-recently selected (or the focused row if no row is selected) to the clicked row, inclusive. |
| REQ-UI-43 | `x`: toggles selection on the focused row (`../requirements/10-keyboard.md`). |
| REQ-UI-44 | `Cmd/Ctrl+A`: selects all rows in the current view, capped at the loaded range. For result sets larger than 50 loaded rows, a banner appears: "Selected 50 in view — [Select all 1,247 matching this query]"; the second click extends the selection to the full result set, which is held as a query reference rather than a list of IDs. |
| REQ-UI-44a | The bulk-select control in the list header is a checkbox plus dropdown. The dropdown offers subset filters: All, None, Read, Unread, Starred, Not starred. Picking a subset selects every row in the current view matching the subset (e.g. "Read" selects all read rows). Picking "None" clears selection. |

### Bulk actions

| ID | Requirement |
|----|-------------|
| REQ-UI-50 | When ≥ 1 row is selected, the per-row toolbar above the thread list switches to a bulk-actions toolbar. The list keeps rendering. |
| REQ-UI-51 | The bulk-actions toolbar exposes — left-to-right — the bulk-select control (REQ-UI-44a), archive, report-spam, delete, mark-as-unread, snooze, add-to-tasks (cut for v1; hidden), move-to, label, three-dot more-menu. |
| REQ-UI-51a | The bulk three-dot more-menu offers: star/unstar, mark-as-read, mark-as-unread, mark-as-important, mark-as-not-important, filter-similar, mute-thread, forward-as-attachment, "Switch to simple toolbar" (matches the per-thread toolbar's compact-mode toggle in REQ-UI-19c). |
| REQ-UI-52 | A bulk action issues a single batched JMAP call where possible (`Email/set` with multiple `update` entries, not one call per email). |
| REQ-UI-53 | If a "select all matching this query" selection is in force, bulk actions issue against the query rather than enumerated IDs — the underlying mechanism is `Email/query` for the IDs as needed, then `Email/set` in pages. |
| REQ-UI-54 | A bulk action affecting > 100 emails shows a confirmation: "This will affect N emails. Continue?". Avoids "I clicked Archive on 1,247 emails by accident". |

### Persistence

| ID | Requirement |
|----|-------------|
| REQ-UI-60 | Selection clears when the view changes (navigating to another label, opening a thread). |
| REQ-UI-61 | Selection clears when the thread list re-fetches due to a state-string change (`../architecture/03-sync-and-state.md`). |
| REQ-UI-62 | Selection survives scroll within the same view, including out-of-viewport rows being virtualised away and back. |

### Failure modes

| ID | Requirement |
|----|-------------|
| REQ-UI-70 | A bulk action that partially fails (some Emails succeeded, some didn't) reports both: "12 archived, 3 failed — Retry the 3" with a button that retries only the failures. The cache reflects the partial outcome; the failed entries revert to pre-action state. |
