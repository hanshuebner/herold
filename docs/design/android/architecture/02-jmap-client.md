# 02 — JMAP client

The typed JMAP wrapper in `shared/commonMain/jmap`. It is the mobile counterpart
of the Suite's `docs/design/web/architecture/02-jmap-client.md`; the method-call
shape and batching model are the same, the differences are auth (bearer, not
cookie) and that the client feeds a persistent store, not an in-memory cache.

## Responsibilities

- Batch method calls into one `POST /jmap` and route responses back by call id
  (RFC 8620 3.5), with back-references between calls in a batch (e.g.
  `Email/query` -> `Email/get` in one round-trip).
- Attach the bearer token to every request; compute `using` from the pinned
  capability set plus the methods in the batch.
- Manage `Blob/upload` for attachments and inline content, and blob download for
  bodies/attachments the store caches.
- Retry transport failures with exponential back-off.

It does not own state strings, the change feed, or the outbox — that is the sync
engine (`03-sync-and-state.md`). It is a typed transport over `POST /jmap` plus
the upload/download and EventSource endpoints.

## Auth

Every request carries `Authorization: Bearer <token>`
(`../requirements/01-auth-and-token.md`). The token comes from the auth module,
which sources it from secure storage and refreshes it. A `401` surfaces to the
auth module: it attempts a silent refresh-and-retry once, and on failure raises
forced-login (Suite `REQ-AS-10` parity). A `403 step_up_required` raises the
native TOTP step-up (`REQ-AND-AUTH-20`). There is no cookie and no in-JS token
storage analogue; the token never touches the local database or logs.

## Error handling

The two JMAP error tiers (RFC 8620 3.6) are handled as in the Suite:

- Request-level (4xx/5xx, malformed) — transport failure, retried with
  back-off. A `401`/`403` is routed to auth as above rather than blindly retried.
- Method-level (`error` in a per-call result) — a typed failure scoped to that
  call; the batch's other calls still resolve. `cannotCalculateChanges` on
  `Foo/changes` triggers a full re-fetch of that type (`03-sync-and-state.md`).

## Blob upload and download

- Upload: `POST /jmap/upload/<account-id>/` streams the file body and returns a
  `blobId`, referenced from a subsequent `Email/set` (RFC 8620 6.1). Uploads run
  through the outbox when composed offline: the blob is staged locally and
  uploaded on drain (`../requirements/02-offline-and-sync.md` REQ-AND-SYNC-21).
- Download: bodies and attachments are fetched via `/jmap/download/*` and written
  to the local blob cache under the size budget (REQ-AND-SYNC-12).

## Batching and concurrency

Batches are assembled per logical action, the same as the Suite. The client is
suspend-based (coroutines); the sync engine and UI intents issue batches
concurrently within a bounded dispatcher. A batch is not cancelled mid-flight;
its results always land in the store, and the UI renders from the store, so a
navigated-away batch cannot render stale into the wrong view.

## Capability negotiation

The session descriptor's capabilities are read once at bootstrap and pinned for
the session (`../notes/server-contract.md`). Each batch's `using` is computed
from its methods plus the pinned set; a feature whose capability is unadvertised
is removed from the UI. The mobile client pins the Suite's set minus
`shortcut-coach` on phone (server-contract § Capabilities divergence).

## EventSource

`GET /jmap/eventsource?types=...` with the bearer token, consumed while
foregrounded (`03-sync-and-state.md`). Reconnect uses `Last-Event-ID`. The
connection is released on background; FCM is the wake channel there.
