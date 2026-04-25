# 07 — Search

Find threads by free text, by structured criteria, or by both.

## Basic search

| ID | Requirement |
|----|-------------|
| REQ-SRC-01 | The search bar is always accessible. Keyboard shortcut `/`, mouse click on the bar. |
| REQ-SRC-02 | Free-text search matches against subject, body, from, to, cc — implemented as `Email/query` with `filter: { text: <query> }`. |
| REQ-SRC-03 | Search results display as a thread list, sorted by `receivedAt` descending. |
| REQ-SRC-04 | The result count is shown ("about N results"); for large result sets the count may be approximate (`Email/query` `calculateTotal` may not return exact). |
| REQ-SRC-05 | Clicking a result opens the thread; the back action returns to the same result list, scrolled to the same row. |

## Fielded search

| ID | Requirement |
|----|-------------|
| REQ-SRC-10 | Tabard parses Gmail-compatible operators: `from:`, `to:`, `subject:`, `label:`, `has:attachment`, `is:unread`, `is:starred`, `is:snoozed`, `before:`, `after:`. |
| REQ-SRC-11 | Operator suggestions appear as the user types (autocomplete). |
| REQ-SRC-12 | Tabard translates the parsed query into a structured `Email/query` filter. The user-visible syntax stays Gmail-compatible to preserve muscle memory; the wire-level filter is JMAP. |

## Search state

| ID | Requirement |
|----|-------------|
| REQ-SRC-20 | The active search query appears in the URL so the search is bookmarkable and shareable. |
| REQ-SRC-21 | Pressing Escape clears the search and returns to the previous view. |
| REQ-SRC-22 | The most-recent N searches are remembered and surfaced as suggestions on focus (N: TBD; default 10). |
