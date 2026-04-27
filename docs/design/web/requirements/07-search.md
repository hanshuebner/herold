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

## Result snippets

| ID | Requirement |
|----|-------------|
| REQ-SRC-30 | Each visible search result row's snippet is fetched via `SearchSnippet/get` (RFC 8621 §7.1), passing the active filter so the server can highlight the matched terms. |
| REQ-SRC-31 | Snippet truncation length is server-controlled (whatever herold returns); tabard renders verbatim. Highlighted terms come back wrapped in `<mark>` tags or as offsets — tabard renders them with a `--support-info` background per `../architecture/06-design-system.md`. |
| REQ-SRC-32 | If `SearchSnippet` capability is not advertised, the row falls back to `Email.preview` with no highlighting. |
| REQ-SRC-33 | Snippets are not cached — they are tied to the search query; re-running a different query for the same Email yields different snippets. |

## Query parser

The user-facing query syntax is a small grammar that tabard parses into a JMAP `FilterCondition` / `FilterOperator` tree.

```
query       := term (whitespace term)*
term        := negation? (operator | quoted | bareword)
negation    := '-'
operator    := name colon value
name        := from | to | cc | subject | label | has | is | before | after
             | filename | size | newer_than | older_than | header | list
quoted      := '"' .*? '"'
bareword    := [^\s:"]+
value       := quoted | bareword
```

| ID | Requirement |
|----|-------------|
| REQ-SRC-40 | Multiple terms combine with implicit AND. `from:alice has:attachment` matches Emails matching both. |
| REQ-SRC-41 | A leading `-` negates a term. `-from:bot@example.com` excludes those messages. |
| REQ-SRC-42 | Free-text barewords map to `text: <value>` in the filter. Quoted barewords map to phrase search (`text: <phrase>` exactly). |
| REQ-SRC-43 | Date operators (`before:`, `after:`, `newer_than:`, `older_than:`) accept ISO 8601 dates and short relative forms (`1d`, `2w`, `3m`). |
| REQ-SRC-44 | Unknown operators are syntax errors surfaced inline ("Unknown operator: `xyz:`"). The user fixes the query before search runs. |
| REQ-SRC-45 | Tabard does not support OR or parenthesised expressions in v1. (Most search needs are AND-shaped; OR adds parser and UI complexity for marginal gain.) |

## Search within thread

| ID | Requirement |
|----|-------------|
| REQ-SRC-50 | When a thread is open, pressing `/` with focus in the reading pane opens an in-thread search bar (separate from the global search). |
| REQ-SRC-51 | In-thread search is purely local — it scans the in-memory `bodyValues` of the thread's Emails. No JMAP call. |
| REQ-SRC-52 | Matches are highlighted in place. `n` / `Shift+n` cycle through matches (consistent with the browser's find-in-page idiom). |
| REQ-SRC-53 | Escape closes the in-thread search and removes the highlights. |

## Saved searches

| ID | Requirement |
|----|-------------|
| REQ-SRC-60 | Verdict deferred to capture data. If gmail-logger reveals the user repeats the same search ≥ 5 times in a 5-day window, saved searches ship in v1; otherwise cut. The decision lives in `../notes/capture-integration.md`. |
| REQ-SRC-61 | If saved searches ship: a "Save this search" affordance appears on a result page. Saved searches surface as sidebar entries (under their own section). Storage: server-side via a tabard custom property on the account, OR `localStorage` per account if no server contract is available — pending the verdict. |
