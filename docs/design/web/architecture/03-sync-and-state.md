# 03 — Sync and state

How tabard keeps its cache in sync with herold.

## Primitives

JMAP gives us two things (RFC 8620 §5.2):

- An opaque `state` string per type per account, returned with every `Foo/get` and `Foo/set` response.
- A `Foo/changes` method that, given a `since` state, returns the IDs created / updated / destroyed in that interval, plus the new state.

That's the whole sync contract. Every type tabard cares about (`Email`, `Mailbox`, `Thread`, `Identity`, `EmailSubmission`, plus `Sieve` if `:sieve` is advertised, plus the snooze keyword/property which rides on `Email`) has a state string; every type has a `changes` method.

## Sync loop

```
   bootstrap
       │
       ▼
   batched Foo/get for initial views
   capture state strings
       │
       ▼
   open EventSource (RFC 8620 §7)
       │
       ▼ events
   on StateChange:
       for each changed type:
         Foo/changes(since: cached state)
           ├ ok          → fold ids into cache; replay Foo/get for created+updated
           └ cannotCalc  → drop cached entries of that type; re-fetch from scratch
   loop
```

The EventSource carries `StateChange` events (RFC 8620 §7.3): a JSON object mapping changed type names to their new states. Tabard compares against its cached states and issues `Foo/changes` only for types whose state actually advanced. That deduplicates fanout when many small changes commit close in time.

## Optimistic writes and reconciliation

Before sending an `Email/set`, tabard:

1. Computes the post-change cache state.
2. Applies it locally.
3. Issues the `Email/set`.
4. On response: replaces the optimistic state with the server-returned state (for the affected IDs).
5. On failure: reverts to pre-change cache state and surfaces a toast (`../requirements/11-optimistic-ui.md` REQ-OPT-02).

Between steps 2 and 4, if a push event arrives carrying a state for the same type that's *ahead* of where the optimistic write would land, tabard discards its own optimistic version and folds in the server's truth. This is the rare case where an action visibly "vanishes" — covered in REQ-OPT-02's failure-mode table.

## Push fallback

The push channel can disconnect (network blip, herold restart, proxy idle-out). When it does:

1. Tabard records the disconnect timestamp and starts polling: `Foo/changes` against every cached type at 30 s intervals (`../requirements/13-nonfunctional.md` REQ-REL-02).
2. In parallel, tabard reconnects EventSource with exponential back-off.
3. On successful reconnect, polling stops; the EventSource takes over, using `Last-Event-ID` to resume the change stream.
4. A subtle "connecting…" indicator is shown only while polling; transient disconnects (under ~3 s) do not flash UI.

Polling and EventSource never both feed the cache — the polling loop suspends as soon as EventSource confirms connection.

## State strings as cache keys

Each cached list view is keyed by `(view-shape, state-of-driving-type)`. When `Email`'s state advances, list views over `Email` invalidate their "complete" status; they're still rendered from the cache, but the next time they're opened they trigger a `Email/changes` and (if any IDs are new) extend their materialised range.

State strings are opaque to the client; tabard never tries to parse or compare them. Equality and "advanced past" are the only operations needed: `cached === server` means in sync; otherwise issue `changes`.

## Crash and reload

On reload, tabard's in-memory cache is gone. The bootstrap flow (`01-system-overview.md`) re-fetches the inbox and other initial views from scratch with fresh state strings; sync resumes from there. There is no client-side persistent cache to make stale (`../00-scope.md` NG2).

## Cross-reference to herold

Herold owns the producer side: every mutation appends a row to its per-principal state-change feed (see `../../herold/docs/design/architecture/05-sync-and-state.md`). The EventSource events and the `Foo/changes` results both come from that feed. Tabard's only job is the consumer pattern documented above.
