# 02 — JMAP client

The single object responsible for talking to herold. Lives between view code and the network.

## Responsibilities

- Batch method calls into a single HTTP request and route responses back to the right call sites (RFC 8620 §3.5).
- Hold the auth token, attach it to every request.
- Build back-references between method calls in a batch (so e.g. `Email/query` → `Email/get` for the same IDs is one round-trip).
- Retry transport failures with exponential back-off (`../requirements/13-nonfunctional.md` REQ-REL-03).
- Manage `Blob/upload` for attachments and inline content.

It does NOT manage state strings or the change feed — that's `03-sync-and-state.md`. It does NOT touch the cache directly — view code reads/writes the cache; the JMAP client is just a typed wrapper around `POST /jmap`.

## Method-call shape

The suite calls JMAP through a typed wrapper:

```
const [emails] = await jmap.batch(b => {
  const q = b.call('Email/query', { filter: { inMailbox: inboxId }, sort: [{ property: 'receivedAt', isAscending: false }], limit: 50 });
  const e = b.call('Email/get', { '#ids': { resultOf: q.id, name: 'Email/query', path: '/ids' } });
  return [e];
});
```

The `batch` function:
- Collects all `b.call(...)` invocations into one `methodCalls` array.
- Issues a single `POST /jmap` with `using` populated from `../notes/server-contract.md`.
- Returns the awaited responses by call ID, in declaration order.
- Surfaces method-level errors (per RFC 8620 §3.6.1) as typed exceptions per call.

Back-references are first-class: any subsequent call in the same batch can reference an earlier call's result via the `#name` syntax.

## Auth

All JMAP requests use `credentials: 'include'` so suite-origin session cookie attaches automatically. There is no `Authorization` header and no token in JS-accessible storage. A 401 response invalidates the session; the suite redirects to herold's `/login?return=<current-url>` (resolved Q1). See `01-system-overview.md` § Bootstrap.

## Error handling

JMAP defines two error tiers (RFC 8620 §3.6):

- **Request-level** (4xx, 5xx, malformed JSON) — transport or framing failure. Retried with exponential back-off, max 3 attempts.
- **Method-level** (`error` object inside the per-call result) — semantic failure for that call only. Surfaced as a typed exception scoped to that call. The batch's other calls still resolve normally.

A `cannotCalculateChanges` method-level error on `Foo/changes` triggers a full type re-fetch (`Foo/get` for everything visible) — see `03-sync-and-state.md`.

## Blob upload

`POST /jmap/upload/<account-id>/` per RFC 8620 §6.1. Streams the file body, returns a `blobId`, which is then referenced from a subsequent `Email/set` in the same logical action (separate HTTP request because uploads are not batched into JMAP method-call frames).

## Concurrency

Method-call batches do not cancel mid-flight. If the user navigates away from a view that issued a batch, the batch completes and its result populates the cache; the view code is responsible for not rendering stale data into a now-irrelevant view (a request ID or a `Promise.race`-against-navigation pattern).

## Capability negotiation

On bootstrap, the session descriptor's `capabilities` is read once and pinned for the session. Each batched call's `using` is computed from the methods it contains plus the pinned capability set. Capabilities the server doesn't advertise (e.g. `urn:ietf:params:jmap:sieve` if filters aren't supported) cause the corresponding feature paths to be removed from the UI; the suite never issues a method whose capability isn't advertised.
