# 10 — Keyboard

Keyboard is the primary input. Every requirement here is binding-level: the shortcut engine itself lives in `../architecture/05-keyboard-engine.md`.

Priorities (P0 / P1 / P2) are calibrated from gmail-logger capture data. They are not contracts — they tell us what to ship first and where to invest in polish.

> **⚠ PLACEHOLDER** — priorities below are starting guesses. Adjust after running gmail-logger for 5–7 days and inspecting `keyboard_vs_click` and per-action method ratios in the analysis report.

## Single-key bindings

| Shortcut | Action | Priority |
|----------|--------|----------|
| `c` | Compose new | P0 |
| `/` | Focus search | P0 |
| `e` | Archive | P0 |
| `j` | Next thread | P0 |
| `k` | Previous thread | P0 |
| `r` | Reply | P0 |
| `a` | Reply all | P0 |
| `f` | Forward | P0 |
| `l` | Open label picker | P0 |
| `b` | Snooze | P0 |
| `u` | Back to thread list | P0 |
| `x` | Select / deselect focused thread | P0 |
| `s` | Star / unstar | P1 |
| `#` | Delete | P1 |
| `I` | Mark read | P1 |
| `U` | Mark unread | P1 |
| `?` | Show shortcut help overlay | P1 |
| `Enter` | Open focused thread | P0 |
| `Escape` | Close picker / dismiss overlay / blur search | P0 |
| `Ctrl+Enter` | Send compose | P0 |
| `+` | Open emoji picker for focused/expanded message | P1 |

## Two-key (`g…`) navigation sequences

| Shortcut | Action | Priority |
|----------|--------|----------|
| `g i` | Go to Inbox | P0 |
| `g s` | Go to Starred | P1 |
| `g d` | Go to Drafts | P1 |
| `g t` | Go to Sent | P2 |
| `g a` | Go to All Mail | P2 |
| `g b` | Go to Snoozed | P1 |
| `g k` | Go to Tasks | not in scope |
| `g l` | Go to Label (then type label name) | P1 |

## Bindings

| ID | Requirement |
|----|-------------|
| REQ-KEY-01 | Bindings are discoverable: `?` opens an overlay listing every active binding grouped by area. |
| REQ-KEY-02 | Bindings respect input focus: typing inside an `<input>`, `<textarea>`, or `contenteditable` does NOT trigger single-key actions. The shortcut engine ignores keydowns when those elements are focused, except for `Escape` (always a dismiss) and `Ctrl+Enter` in compose (always a send). |
| REQ-KEY-03 | The two-key `g` prefix waits up to 1000 ms for the second key. After timeout the buffer clears. |
| REQ-KEY-04 | All P0 bindings are remappable in a settings panel (post-v1 if behind capacity). The default set is Gmail-compatible. |
| REQ-KEY-05 | The shortcut engine has a single dispatcher; pickers (label, snooze, search) register their own keymap that supersedes the global one while open. |
| REQ-KEY-06 | `*` in the mail list toggles select-all: if every visible row is already selected, pressing `*` clears the selection; otherwise it selects all visible rows. |
