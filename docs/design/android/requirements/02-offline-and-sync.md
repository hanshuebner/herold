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
| REQ-AND-SYNC-10 | On connectivity, the sync engine issues `Foo/changes` per type against the persisted state string and folds created/updated/destroyed IDs into the local store, replaying `Foo/get` for created and updated IDs (Suite sync loop). A `cannotCalculateChanges` error drops that type's cached rows and re-fetches. |
| REQ-AND-SYNC-11 | While the app is foregrounded and connected, EventSource `StateChange` events drive the same reconciliation in real time (`../notes/server-contract.md` § EventSource). When backgrounded, FCM is the wake channel (`03-notifications.md`); on next foreground the engine reopens EventSource and reconciles. |
| REQ-AND-SYNC-12 | Body/blob caching is bounded by a configurable size budget with LRU eviction; metadata for all synced threads is retained, bodies for the most-recently-viewed within budget. Eviction never drops unsynced outbox content (REQ-AND-SYNC-22). |
| REQ-AND-SYNC-13 | Search offline returns results from the locally-synced set only, clearly scoped as such; full-corpus search (`docs/design/web/requirements/07-search.md`, server FTS) requires connectivity and is indicated when offline. |

## Outbox and optimistic actions

The Suite's optimistic-write model (`docs/design/web/requirements/11-optimistic-ui.md`,
architecture § Optimistic writes) applies, extended to persist across
disconnection and app restart.

| ID | Requirement |
|----|-------------|
| REQ-AND-SYNC-20 | Optimistic actions (archive / label / snooze / star / mark-read / delete) apply to the local store immediately and enqueue a durable outbox entry. The UI reflects the optimistic state; the entry carries the intended `Foo/set` patch. |
| REQ-AND-SYNC-21 | Composed messages and `EmailSubmission`s created offline are durable outbox entries. Drafts persist to the local store and sync to the server draft mailbox when connectivity returns. |
| REQ-AND-SYNC-22 | Outbox entries survive app restart and process death. They are never evicted by cache pressure. The outbox is drained in order on reconnect. |
| REQ-AND-SYNC-23 | On drain, each entry is submitted; on server success the optimistic local state is replaced with the server-returned state for the affected IDs. On a permanent server rejection the local optimistic state reverts and the user is notified with the entry retained for inspection (Suite `REQ-OPT-02` failure semantics). |
| REQ-AND-SYNC-24 | If a reconciliation delivers a server state for an entity ahead of a pending optimistic write on the same entity, the server truth wins and the optimistic version is discarded (Suite architecture § Optimistic writes and reconciliation). |
| REQ-AND-SYNC-25 | A visible outbox surface lists pending entries (queued sends, pending actions) with their state (queued / sending / failed) and a manual retry for failed entries. |

## Connectivity indication

| ID | Requirement |
|----|-------------|
| REQ-AND-SYNC-30 | Connectivity state is surfaced non-intrusively (a bar or chip) when offline or reconnecting, matching the Suite's more-prominent-on-mobile treatment (`REQ-MOB-80`). Transient drops under a few seconds do not flash UI. |
| REQ-AND-SYNC-31 | Background sync (WorkManager) refreshes on a bounded schedule and on FCM wake so the local store is warm when the user opens the app; it respects the platform's battery/doze constraints. |

## Out of scope

- Full-corpus offline search (server FTS is connectivity-gated, REQ-AND-SYNC-13).
- Conflict-resolution UI beyond last-writer/server-wins (REQ-AND-SYNC-24); JMAP's state model makes silent server-wins the correct default.
