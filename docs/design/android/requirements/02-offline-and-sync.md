# 02 — Offline and sync

The mobile client is fully usable with no connectivity: read synced mail,
compose, and act; reconcile on reconnect. This is the deliberate divergence from
the Suite, which is online-first with no offline (`docs/design/web/00-scope.md`
NG2). The JMAP sync primitives are the Suite's
`docs/design/web/architecture/03-sync-and-state.md` (state strings +
`Foo/changes`); this file records the offline behaviour built on top of them.

## Local store

| ID | Requirement |
|----|-------------|
| REQ-AND-SYNC-01 | A persistent local database (SQLDelight, in the shared core) is the source of truth the UI renders from. Views read the local store, never the network directly; the sync engine is the only writer of server-derived rows. |
| REQ-AND-SYNC-02 | The local store persists, per account: mailboxes, threads, emails (metadata + rendered body + inline-image blobs for synced messages), identities, submissions, filters, and the per-type JMAP state string. Blob bodies are cached to a size budget (REQ-AND-SYNC-12). |
| REQ-AND-SYNC-03 | On cold start the UI renders immediately from the local store, then the sync engine reconciles in the background. There is no blank-until-fetched state when a prior sync exists. |

## Reconciliation

| ID | Requirement |
|----|-------------|
| REQ-AND-SYNC-10 | On connectivity, the sync engine issues `Foo/changes` per type against the persisted state string and folds created/updated/destroyed IDs into the local store, replaying `Foo/get` for created and updated IDs (Suite sync loop). A `cannotCalculateChanges` error drops that type's cached rows and re-fetches, as does an answer that reports more changes to come without advancing the state. |
| REQ-AND-SYNC-11 | While the app is foregrounded and connected, EventSource `StateChange` events drive the same reconciliation in real time (`../notes/server-contract.md` § EventSource). When backgrounded, FCM is the wake channel (`03-notifications.md`); on next foreground the engine reopens EventSource and reconciles. |
| REQ-AND-SYNC-12 | Body/blob caching is bounded by a configurable size budget with LRU eviction; metadata for all synced threads is retained, bodies for the most-recently-viewed within budget. Eviction never drops unsynced outbox content (REQ-AND-SYNC-22). A cached blob's bytes are held as a file under the app's cache directory and its database row keeps the content type, the size, the last use and the file's path, so a part larger than Android's 2 MiB cursor window reads back; eviction deletes the files with the rows, and a row whose file the system reclaimed is dropped as it is read so the part downloads again. A part neither the cache nor the server produces is rendered as unavailable and logged to the diagnostic ring; a failed load never terminates the app. |
| REQ-AND-SYNC-13 | Search offline returns results from the locally-synced set only, clearly scoped as such; full-corpus search (`docs/design/web/requirements/07-search.md`, server FTS) requires connectivity and is indicated when offline. |
| REQ-AND-SYNC-14 | A reconciliation pass that failed is retried on a bounded backoff: the first retry after 5 s, doubling to a ceiling of 60 s while failures continue, and the wait returns to a 60 s floor once a pass reaches the server. The floor runs while the shell holds the foreground, so a change reaches the list within a minute even where the event stream is connected and silent. A refresh, an arriving push and a return to the foreground each run a pass at once, whatever the backoff stands at. The diagnostics screen states when the last pass reached the server (REQ-AND-SYS-54). |

## Outbox and optimistic actions

The Suite's optimistic-write model (`docs/design/web/requirements/11-optimistic-ui.md`,
architecture § Optimistic writes) applies, extended to persist across
disconnection and app restart.

| ID | Requirement |
|----|-------------|
| REQ-AND-SYNC-20 | Optimistic actions (archive / label / snooze / star / mark-read / delete) apply to the local store immediately and enqueue a durable outbox entry. The UI reflects the optimistic state; the entry carries the intended `Foo/set` patch. |
| REQ-AND-SYNC-21 | Composed messages and `EmailSubmission`s created offline are durable outbox entries. Drafts persist to the local store and sync to the server draft mailbox when connectivity returns. |
| REQ-AND-SYNC-22 | Outbox entries survive app restart and process death. They are never evicted by cache pressure. The outbox is drained in order on reconnect. |
| REQ-AND-SYNC-23 | On drain, each entry is submitted; on server success the optimistic local state is replaced with the server-returned state for the affected IDs. On a permanent server rejection the local optimistic state reverts and the user is notified with the entry retained for inspection (Suite `REQ-OPT-02` failure semantics). A transient failure (no connection, 5xx, timeout) leaves the entry queued with a bounded exponential backoff and holds the entries behind it on that account, so the queue keeps its order; a rejected entry is skipped so it blocks nothing. |
| REQ-AND-SYNC-24 | If a reconciliation delivers a server state for an entity ahead of a pending optimistic write on the same entity, the server truth wins and the optimistic version is discarded (Suite architecture § Optimistic writes and reconciliation). |
| REQ-AND-SYNC-25 | A visible outbox surface lists pending entries (queued sends, pending actions) with their state (queued / sending / waiting for the server / failed), the reason a failed or waiting one gives, and a manual retry, which submits at once and keeps the entry's attempt count. |
| REQ-AND-SYNC-26 | A send is held in the outbox for a configurable undo window (off / 5 / 10 / 20 / 30 seconds, default 5) before the drain submits it, and the message list offers to take it back for that long; an undo drops the entry and reopens the composer with the message intact. The window is the entry's not-before time, so it applies offline and with the app closed. The Suite realises undo-send server-side with `EmailSubmission.sendAt` (`../../web/requirements/11-optimistic-ui.md` REQ-OPT-11); on the phone the message has not left the device, so an undo costs no round trip and needs no connectivity. |
| REQ-AND-SYNC-27 | A server that answers that it does not understand the request as sent - a 400, a route it does not serve, a method or a media type it does not take - is a server behind the build that queued the entry, not a decision about the entry. The entry is deferred rather than failed: it keeps its payload and its optimistic rows, is offered again on a widening schedule (from a quarter of an hour, capped at half a day), and lets the entries behind it through while it waits, so a write queued against yesterday's server leaves when the server catches up. The waiting is bounded: after 24 offers, about a working week, the drain reverts the entry's optimistic rows, marks it failed with what became of it, and tells the user; the entry itself is kept for a manual retry. A refusal the server did make - a forbidden request, a payload over a documented cap, a JMAP method error - stops the entry as before (REQ-AND-SYNC-23). |
| REQ-AND-SYNC-28 | An outbox entry is only removed when the server has taken it or the user discards it. The clear that drops an account's rows on sign-out, and when another principal signs in on the same install (REQ-AND-AUTH-21), hands back the entries it takes and the app names each of them in the diagnostic log, so unsent work never goes quietly; a schema migration carries the queue across an app update untouched. |

## Status indication

| ID | Requirement |
|----|-------------|
| REQ-AND-SYNC-30 | The client's dealings with the server are surfaced by one small indicator in the shell's chrome, in a slot it holds in every state - on the inbox at the end of the row that carries the mailbox's name and the lanes (`05-navigation-shell.md` REQ-AND-NAV-02), on a conversation in its app bar, so no status change moves the content column. It has four states: idle (a faint dot), an interaction in flight (a pulsing dot while a sync or an outbox drain runs), offline (a struck muted ring, described "Offline"), and failed (an accented dot after a refused sync or a refused queue entry). Transient drops under a few seconds do not reach it. Tapping it opens the diagnostics screen (`04-system-integration.md` REQ-AND-SYS-54), which is where the detail lives: no chip, no progress bar and no transient error banner occupies layout space on the message list or the conversation. The Suite's own treatment is a bar under the app bar (`REQ-MOB-80`); the phone diverges because a bar that comes and goes reflows the list under the reader. Undo offers stay snackbars: they are actions, not status. |
| REQ-AND-SYNC-31 | Background sync (WorkManager) refreshes on a bounded schedule and on FCM wake so the local store is warm when the user opens the app, and drains the outbox under a network constraint so queued mail leaves with the app closed; it respects the platform's battery/doze constraints. A user who force-stops the app suspends this until they open it again, which is the platform's rule for a stopped package, not a client choice. |

## Out of scope

- Full-corpus offline search (server FTS is connectivity-gated, REQ-AND-SYNC-13).
- Conflict-resolution UI beyond last-writer/server-wins (REQ-AND-SYNC-24); JMAP's state model makes silent server-wins the correct default.
