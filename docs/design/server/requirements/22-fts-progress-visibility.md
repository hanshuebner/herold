# 22 - FTS indexing progress visibility

*(Added 2026-05-09 alongside REQ-PERF-RESP. The trigger: the response-
time-budget work locked the search path to FTS, which means a message
that has not yet been indexed is invisible to search until the worker
catches up. On the maintainer's mailbox at landing time
herold_fts_indexed_messages_total = 340,792 of 404,620 with
herold_fts_indexing_lag_seconds = 6,629 (~110 minutes). With FTS
authoritative, "I just imported a Takeout / new mailbox / new mail
batch and search is empty" stops being a UX paper-cut and becomes a
correctness gap the user has no way to recognise. This document
specifies the contract for surfacing indexing progress in the suite
SPA so the user understands why a search returns less than expected.)*

## Scope

Suite SPA (`web/apps/suite`). The admin SPA gets the same data via
the existing `herold_fts_*` Prometheus metrics, which is sufficient
for an operator. End-user-facing surfaces are limited to: the suite
search bar, the suite settings panel, and (later) per-message detail
when a message is known to be unindexed. SMTP / IMAP / JMAP wire
clients are out of scope: no new IMAP capability, no JMAP per-method
field, no SMTP greeting tag.

Out of scope: per-message indexing state (REQ-FTS-PROGRESS-90), a
"reindex this message" button (REQ-FTS-PROGRESS-91), historical
indexing-progress charts (REQ-FTS-PROGRESS-92).

## Data model

| ID | Requirement |
|----|-------------|
| REQ-FTS-PROGRESS-01 | The server exposes a single typed `FTSStatus` value to the suite SPA via the existing JMAP session bootstrap. The shape is: principal-scoped totals and the worker's global lag. Concretely: `{ "indexed_messages": uint64, "total_messages": uint64, "lag_seconds": float, "as_of": rfc3339 }`. `total_messages` is the principal's message count (envelope rows visible to that principal); `indexed_messages` is the count for which an FTS document exists. `lag_seconds` mirrors `herold_fts_indexing_lag_seconds`. `as_of` is the server-wall-clock time the snapshot was taken. |
| REQ-FTS-PROGRESS-02 | The principal scope is enforced: a non-admin principal sees only their own counts. An admin principal sees the same view (admin tooling for cross-principal indexing health is the operator surface, not this surface). |
| REQ-FTS-PROGRESS-03 | The two counts are derived once per JMAP session bootstrap, not per Email/query. Recomputing on every search would amplify the worker's lock pressure on a large mailbox. |

## Transport

| ID | Requirement |
|----|-------------|
| REQ-FTS-PROGRESS-10 | The status is delivered as a non-RFC capability in the `/.well-known/jmap` session descriptor under `urn:netzhansa:params:jmap:fts-status`. The capability descriptor is the typed `FTSStatus` value verbatim. JMAP clients ignore unknown capability URNs (RFC 8620 §2.1.1) so this is forward-compatible. |
| REQ-FTS-PROGRESS-11 | A push update is delivered on the existing `eventSourceUrl` whenever the session's `indexed_messages` advances by at least 1% of `total_messages` OR `lag_seconds` crosses a threshold (1, 10, 60, 600 s). Sub-1% advances do not push; the worker can index hundreds of messages per second on a fresh corpus and per-message push would saturate the SSE channel. |
| REQ-FTS-PROGRESS-12 | The push event type is `state` with the existing JMAP state-change shape, plus a sibling `fts:status` field carrying the new `FTSStatus`. Clients that do not care can ignore `fts:status`; clients that do care read it without re-fetching the session descriptor. |

## SPA surface

| ID | Requirement |
|----|-------------|
| REQ-FTS-PROGRESS-20 | The suite displays an inline notice in the search bar's results panel whenever `indexed_messages < total_messages`. The text is locale-aware ("Indizierung laeuft: 340.792 von 404.620 indiziert (84%)" / "Indexing in progress: 340,792 of 404,620 indexed (84%)"). The notice is non-modal and dismissable; dismissal persists for the session, not across reloads. |
| REQ-FTS-PROGRESS-21 | The suite settings panel has a permanent "Search index" section showing `indexed_messages / total_messages`, `lag_seconds`, and `as_of`. Updates live via the push channel (REQ-FTS-PROGRESS-11). The section is visible to every user, not gated by admin scope. |
| REQ-FTS-PROGRESS-22 | When `indexed_messages == total_messages` AND `lag_seconds < 1`, neither the inline notice (REQ-FTS-PROGRESS-20) nor any percentage display is shown; the settings panel reads "Search index up to date." This is the steady state for an established mailbox; users see no clutter unless something is actually catching up. |
| REQ-FTS-PROGRESS-23 | The notice does NOT block the search action. A user can still issue a search; the result set is whatever FTS knows about. The point of the notice is to explain why the result set might be smaller than the user expects, not to gate the operation. |

## Wire example

The JMAP session descriptor when indexing is in flight:

```json
{
  "capabilities": {
    "urn:ietf:params:jmap:core": { ... },
    "urn:ietf:params:jmap:mail": { ... },
    "urn:netzhansa:params:jmap:fts-status": {
      "indexed_messages": 340792,
      "total_messages":   404620,
      "lag_seconds":      6629.077,
      "as_of":            "2026-05-09T05:30:00Z"
    }
  },
  ...
}
```

When indexing is complete:

```json
"urn:netzhansa:params:jmap:fts-status": {
  "indexed_messages": 404620,
  "total_messages":   404620,
  "lag_seconds":      0.4,
  "as_of":            "2026-05-09T07:42:00Z"
}
```

## Implementation order

| ID | Requirement |
|----|-------------|
| REQ-FTS-PROGRESS-50 | First commit: server-side. Add the `urn:netzhansa:params:jmap:fts-status` capability descriptor populated from a new store helper `Meta().PrincipalMessageCount(ctx, pid)` and a new FTS helper `FTS().IndexedMessageCount(ctx, pid)`. Wire into `protojmap.handleSession`. No SPA work yet; the descriptor is observable via `curl /.well-known/jmap`. |
| REQ-FTS-PROGRESS-51 | Second commit: SPA bootstrap. The suite reads the capability on session bootstrap and exposes it via a Svelte store. Render the settings-panel section (REQ-FTS-PROGRESS-21). No push channel yet; the section refreshes on page reload. |
| REQ-FTS-PROGRESS-52 | Third commit: search-bar inline notice (REQ-FTS-PROGRESS-20). Dismissal state in sessionStorage. |
| REQ-FTS-PROGRESS-53 | Fourth commit: push channel updates (REQ-FTS-PROGRESS-11..12). The eventsource path adds the `fts:status` sibling on threshold crossings. |

## Test strategy

| ID | Requirement |
|----|-------------|
| REQ-FTS-PROGRESS-60 | Server unit test: TestSession_FTSStatus_PrincipalScoped seeds two principals, indexes a subset of one's messages, asserts the session descriptor reports the per-principal counts (not the global). |
| REQ-FTS-PROGRESS-61 | SPA test: TestSearchBar_NoticeShownWhenIndexing renders the search panel with a stub session that reports `indexed_messages < total_messages`, asserts the notice appears with the right percentage. |
| REQ-FTS-PROGRESS-62 | SPA test: TestSettings_SearchIndexSection_LiveUpdates dispatches a push event with an updated `fts:status` and asserts the settings-panel numbers re-render without a page reload. |

## Out of scope

| ID | Requirement |
|----|-------------|
| REQ-FTS-PROGRESS-90 | Per-message "this message is not yet searchable" indicator. The cost is one DB query per rendered message; the value is marginal once the global notice is visible. |
| REQ-FTS-PROGRESS-91 | Manual reindex of a single message via the SPA. Operators reindex via the existing `herold admin diag fts reindex` CLI; the SPA does not need to expose this. |
| REQ-FTS-PROGRESS-92 | Historical indexing-progress charts in the SPA. Operators read the Prometheus history. |
| REQ-FTS-PROGRESS-93 | An IMAP capability advertising indexing state. IMAP clients are not the audience for this signal. |

## Open questions

None at landing; the principal-scoped data model and the JMAP-capability transport were settled in the 2026-05-09 design discussion.
