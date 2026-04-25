# Gmail Interaction Logger — Chrome Extension

Logs your Gmail UI interactions (clicks, keyboard shortcuts, view changes) to a local event store.
Use it to capture ground-truth usage patterns for building a JMAP client requirements spec.

## Install (Developer Mode)

1. Open Chrome → `chrome://extensions`
2. Enable **Developer mode** (top-right toggle)
3. Click **Load unpacked**
4. Select this folder (`gmail-logger/`)
5. Navigate to https://mail.google.com — logging begins immediately.

## Usage

- Click the extension icon (toolbar) to open the popup.
- The popup shows: total events, action type count, top 7 actions with frequency bars, last 15 events.
- **Export JSON** — downloads the full raw event log as JSON.
- **Analyze** — downloads a summarized report with top actions, workflow bigrams (action pairs), keyboard vs. click ratio, and views visited.
- **✕ (Clear)** — resets the event store.

## What Is Logged

Every event has this shape:

```json
{
  "ts": 1712345678901,
  "session": "lxk3f2",
  "action": "archive",
  "view": { "view": "inbox" },
  "method": "keyboard",
  "key": "e"
}
```

### Action taxonomy

| Category        | Example actions |
|-----------------|-----------------|
| Compose         | compose_new, send, discard_draft, attach_file |
| Thread reading  | open_thread, reply, reply_all, forward, star_toggle |
| List actions    | archive, delete, mark_read, mark_unread, snooze, move_to, label_apply |
| Navigation      | nav_inbox, nav_label, nav_starred, nav_snoozed, view_change |
| Search          | search_focus, search_submit |
| Session         | session_start, session_end |

### What is NOT logged

- Email content, subjects, or addresses (privacy)
- Exact search queries (only query length is recorded)
- Label names you apply (only the action is recorded)
- Anything outside mail.google.com

## Output Files

### Raw log (`gmail-log-*.json`)
Full event array. Feed this into any analysis tool (Python, jq, spreadsheet).

### Analysis report (`gmail-analysis-*.json`)
```json
{
  "summary": { "total_events": 412, "unique_actions": 23, "sessions": 7, ... },
  "top_actions": { "open_thread": 89, "archive": 72, "reply": 41, ... },
  "top_workflows": { "open_thread → reply": 38, "reply → archive": 29, ... },
  "keyboard_vs_click": { "keyboard": 187, "click": 225 },
  "views_visited": ["inbox", "thread", "label", "search", "snoozed"]
}
```

## Feeding Results into the Spec

The full mapping lives in `../docs/notes/capture-integration.md`. Short version:

1. Run the logger for 5–7 working days.
2. Export the analysis report.
3. Map `top_actions` (≥ 5 occurrences) → requirements in `../docs/requirements/02-mail-basics.md` plus feature-area docs.
4. Map `top_workflows` bigrams → workflows in `../docs/requirements/12-workflows.md`.
5. Use `keyboard_vs_click` to recalibrate priorities in `../docs/requirements/10-keyboard.md`.
6. Use `views_visited` to confirm view types in `../docs/requirements/01-data-model.md` and to decide cut-or-keep for `categorisation` and `chat`.
