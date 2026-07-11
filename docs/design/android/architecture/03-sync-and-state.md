# 03 — Sync and state

How the local store stays in sync with herold, and how offline actions
reconcile. Built on the Suite's sync primitives
(`docs/design/web/architecture/03-sync-and-state.md`: opaque per-type `state`
strings + `Foo/changes`), with a persistent store and a durable outbox where the
Suite has an in-memory cache and no offline.

## Primitives

Unchanged from the Suite: every type the client cares about (`Email`,
`Mailbox`, `Thread`, `Identity`, `EmailSubmission`, `Sieve` when advertised, plus
the snooze property on `Email`) has an opaque `state` string and a `Foo/changes`
method. State strings are persisted in the local store alongside the rows they
version. The client treats them as opaque: only equality and "advanced past"
matter.

## Reconciliation loop

```
   bootstrap: render from local store (persisted state strings)
       │
       ▼
   for each type: Foo/changes(since: persisted state)
       ├ ok         → fold created/updated/destroyed into store;
       │              replay Foo/get for created+updated; persist new state
       └ cannotCalc → drop that type's rows; Foo/get from scratch; persist state
       │
       ▼
   foreground: open EventSource
       on StateChange: for each advanced type → Foo/changes as above
   background: release EventSource; wake on FCM (04-push.md) → run one pass
```

The reconciler is the only writer of server-derived rows. It runs on:
- cold start (against persisted state — usually a small delta, not a full fetch);
- each EventSource `StateChange` while foregrounded;
- an FCM wake (`04-push.md`) via a bounded WorkManager pass;
- pull-to-refresh and manual retry.

Unlike the Suite, a cold start does not re-fetch from scratch — the persisted
state strings let `Foo/changes` deliver only the delta since last sync. A full
re-fetch happens only on `cannotCalculateChanges` or first run.

## Outbox and optimistic reconciliation

The outbox is a durable table of pending mutations (`Foo/set` patches, drafts,
submissions) with a status (`queued` / `sending` / `failed`) and the pre-change
snapshot needed to revert. It survives process death and is never evicted by
cache pressure (`../requirements/02-offline-and-sync.md` REQ-AND-SYNC-22).

Optimistic write path (Suite architecture § Optimistic writes, extended):

1. Apply the patch to the local store; the UI renders the optimistic state.
2. Enqueue a durable outbox entry carrying the patch and the pre-change snapshot.
3. On connectivity, drain in order: submit each entry's `Foo/set`
   (or upload+`Email/set` for composed mail).
4. On success: replace the optimistic rows with the server-returned state for the
   affected ids; persist the new type state; remove the entry.
5. On permanent rejection: revert to the pre-change snapshot; mark the entry
   `failed`; notify (Suite `REQ-OPT-02`).

Cross-update precedence: if reconciliation delivers a server state for an entity
that is ahead of a pending optimistic write on that entity, the server truth
wins and the optimistic version is discarded (Suite architecture § Optimistic
writes and reconciliation; `../requirements/02-offline-and-sync.md`
REQ-AND-SYNC-24). Because the store is persistent, this resolves correctly even
across an app restart that happened between steps 2 and 3.

## Blob and body cache

Bodies and inline-image blobs are cached to the store under an LRU size budget
(`../requirements/02-offline-and-sync.md` REQ-AND-SYNC-12). Metadata for all
synced threads is retained; bodies within budget. Eviction never touches outbox
content. A body absent from cache while offline renders a "not downloaded"
placeholder; online, it fetches on open.

## Connectivity and background

Connectivity transitions drive the reconciler and the outbox drain. A platform
connectivity monitor (`androidMain` actual) signals online/offline into the
shared sync engine. WorkManager schedules bounded background reconciliation and
outbox-drain passes, and an FCM wake enqueues an expedited pass, all within the
platform's doze/battery constraints (`../requirements/02-offline-and-sync.md`
REQ-AND-SYNC-31).

## Cross-reference to herold

herold owns the producer side: every mutation appends to its per-principal
state-change feed (`docs/design/server/architecture/05-sync-and-state.md`); the
EventSource events and `Foo/changes` results both derive from that feed, and FCM
wakes derive from the same push dispatch. The client is purely the consumer, with
the persistence and outbox the Suite consumer does not carry.
