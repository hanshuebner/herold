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
- pull-to-refresh and manual retry;
- the sync loop's own schedule, below.

## The sync loop

The reconciler is prompted; the loop is what prompts it when nothing else
does (`../requirements/02-offline-and-sync.md` REQ-AND-SYNC-14). It runs
for as long as the shell holds the foreground: a pass, a wait, the next
pass.

```
   pass
    ├ reached the server → record the time; wait 60 s
    └ failed             → wait 5 s, then 10, 20, 40, 60, 60 ... ; log the decision
   any wait is cut short by a forced sync:
     the refresh action, an arriving push, the inbox's own entry
```

Starting the loop is itself a pass, so returning to the foreground
reconciles at once whatever the backoff stood at. The 60 s floor is what
covers an event stream that is connected and silent: the stream carries a
change in well under a second, and the floor bounds how long a change can
sit on the server unnoticed if it does not. Outside the foreground the
loop does not run — FCM is the wake channel there.

The time of the last pass that reached the server is on the diagnostics
screen, so a report of "the app is behind" carries the fact rather than an
impression.

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

What counts as permanent is the server having answered: a `notUpdated` /
`notCreated` entry, or a 4xx other than 408 and 429. Everything else - no
route to the host, a 5xx, a timeout - is transient: the entry stays queued
with an exponential backoff (5 s doubling to 5 min, six attempts) and the
entries behind it on that account wait, so the queue keeps its order. A
failed entry is skipped rather than blocking, and a manual retry submits it
at once.

A composed message is one entry with three steps - upload each attachment,
`Email/set` the draft, `EmailSubmission/set` - and the entry's payload is
rewritten as each step takes, so a pass interrupted between two of them
resumes at the one that did not finish rather than uploading or creating
twice. Attachment bytes live in an app-private spool directory, outside the
blob cache's budget, because the picker's grant on a chosen URI is long gone
by the time a queue built offline drains.

A send's entry carries a not-before time: the undo window
(`../requirements/02-offline-and-sync.md` REQ-AND-SYNC-26). The drain skips
an entry whose time has not come and carries on with the ones behind it,
which is what distinguishes a hold from a backoff.

Taking a message away is an outbox entry like any other write: the row goes
from the local store at once, so the screen it was on stops rendering it, and
a destroy entry carries the ids to the server with the same retries, backoff
and refusal handling. A destroy the server refuses puts the message back where
the server still has it and says so, rather than leaving a screen that
disagrees with the account.

A message the client deleted locally is held against writes for two minutes:
a reconciliation pass writes whatever its `Email/get` answered, and a response
prepared before the delete carries the message, so without the hold a fetch in
flight puts a discarded draft back and the card returns to its conversation. A
destroy the server refuses forgets the hold, which is how the message comes
back on the next pass.

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

A blob's bytes are held as a file under the app's cache directory,
`<accountId>/<blobId>`, and the `blob_cache` row keeps the content type, the
size, the last use and that file's path. Android hands a row to the client
through a 2 MiB cursor window, so a part above that size is only readable
outside the row (issue #420). Eviction deletes the files it drops, a sign-out
clears the directory, and a row whose file the system reclaimed is dropped as
it is read, so the part downloads again. A part the cache and the network both
fail to produce is drawn as unavailable and logged to the diagnostic ring; it
never ends the screen that asked for it.

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
