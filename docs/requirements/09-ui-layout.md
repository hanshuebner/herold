# 09 — UI layout

The visual structure of the client: how the screen is divided, how lists render, how a thread reads.

## Overall layout

| ID | Requirement |
|----|-------------|
| REQ-UI-01 | Three-panel layout: sidebar (labels / nav) ｜ thread list ｜ reading pane. |
| REQ-UI-02 | The sidebar is collapsible. The collapsed/expanded state persists across sessions (per-account `localStorage`). |
| REQ-UI-03 | The thread list / reading pane split ratio is user-adjustable via a drag handle and persists. |
| REQ-UI-04 | Compose opens as a modal in the bottom-right corner. Multiple compose windows stack (cap: 3). Above the cap, Send-or-discard prompts on a fourth open attempt. |
| REQ-UI-05 | Toast / snackbar notifications appear bottom-centre with Undo where applicable. Auto-dismiss at 5 seconds. |

## Thread list

| ID | Requirement |
|----|-------------|
| REQ-UI-10 | Each row shows: sender name, subject, snippet, date, label chips, star, attachment indicator. |
| REQ-UI-11 | Unread threads are visually distinct (bold sender + subject). |
| REQ-UI-12 | Clicking a row (or pressing Enter on the focused row) opens the thread in the reading pane. |
| REQ-UI-13 | A checkbox appears on hover for multi-select. Pressing `x` toggles selection on the focused row without requiring hover. |
| REQ-UI-14 | "Select all" selects all threads in the current view. For result sets > 50, a confirmation banner is shown ("This selects all N threads"). |
| REQ-UI-15 | A bulk-actions toolbar appears when ≥ 1 thread is selected. |
| REQ-UI-16 | The list virtualises rendering: only rows in (and near) the viewport are in the DOM. Scrolling fetches additional pages via `Email/query` with the next position cursor. |

## Reading pane

| ID | Requirement |
|----|-------------|
| REQ-UI-20 | The reading pane shows all messages in the thread. All are collapsed except the most recent unread message — or the most recent message if all are read. |
| REQ-UI-21 | Each message shows: sender, To/Cc, date, body. Collapsed messages show sender plus snippet. |
| REQ-UI-22 | Clicking a collapsed message expands it. |
| REQ-UI-23 | Inline images load on demand by default — not automatically — to prevent tracking-pixel execution. The user can opt-in per sender or per thread. See `13-nonfunctional.md` REQ-SEC. |
| REQ-UI-24 | HTML message bodies render inside a sandboxed iframe. See `../architecture/04-rendering.md`. |
| REQ-UI-25 | A "Show quoted text" toggle collapses / expands quoted reply chains. |

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

### Bulk actions

| ID | Requirement |
|----|-------------|
| REQ-UI-50 | When ≥ 1 row is selected, the per-row toolbar above the thread list switches to a bulk-actions toolbar. The list keeps rendering. |
| REQ-UI-51 | The bulk-actions toolbar exposes: archive, delete, mark-read / mark-unread, label, snooze, "more" (mute thread, report spam, etc. — capture-driven additions). |
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
