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
