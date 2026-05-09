# 23 - External-image internalization off the request path

*(Added 2026-05-09 in response to the response-deadline rollout
exposing a synchronous slow path. The maintainer's freshly-imported
mailbox carries 93,481 messages with `InternalizePending = true` out
of 278,769 total. Today, opening the mailbox view in the suite fires
a JMAP batch [Email/query, Email/get(50, properties=[..., preview]),
Thread/get]; renderOne calls `maybeInternalizeOnDemand` for every
pending message, which fetches its external images, rewrites the
blob, replaces the body, and returns. Each message takes 60-900 ms
depending on image count. 50 messages multiplied by per-message
work routinely tops 3 s, well past the 1 s default deadline. The
operational fix is a `[performance.method_deadline]."Email/get" =
"30s"` override; the architectural fix specified here is to move
the rewrite off the request path entirely.)*

## Scope

Backend: `internal/extimg/`, `internal/store/`, `internal/protojmap/`.
Frontend: `web/apps/suite/`. The change is end-to-end: the worker
runs in the herold process, the JMAP wire shape gains a typed
status capability and a per-message field, and the suite SPA
surfaces both.

Out of scope: changing the existing one-shot `extimg.Internalize`
surface (REQ-EXTIMG-93..98 still apply); changing the importer's
flag-set behaviour at insert time; SMTP-time inbound
internalization (which is already non-blocking in the relay
pipeline).

## Server-side architecture

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-BG-01 | A new background worker runs in the herold process from `StartServer` to shutdown. The worker drains messages with `internalize_pending = 1` newest-first: it loads each match's blob, runs `extimg.Internalize`, replaces the body via `ReplaceMessageBody`, and clears the flag (which `ReplaceMessageBody` already does on success). On any failure the worker calls `ClearMessageInternalizePending` so the message does not retry indefinitely; the failure is recorded on the existing `extimg` audit metric. |
| REQ-EXTIMG-BG-02 | The worker processes at most `concurrency` messages in parallel (default 4) so external image origins are not hammered. Each in-flight rewrite carries a per-message timeout of `extimg.PerMessageTimeout` (already enforced by the `extimg.Internalize` call site). |
| REQ-EXTIMG-BG-03 | A new store method `ListMessagesWithInternalizePending(ctx, beforeMessageID, limit) ([]MessageID, error)` returns up to `limit` MessageIDs in DESCENDING order (newest first) with id < beforeMessageID, scanning only rows where `internalize_pending = 1`. Pass `beforeMessageID = 0` to start from the newest pending message — the implementation treats 0 as MAX_INT64. The worker maintains an in-memory cursor; on each batch it advances the cursor to the lowest id returned, so a full sweep walks the backlog once from the freshest message to the oldest. The newest-first order matters because it ensures freshly-arrived mail is searchable / image-rewritten as soon as the user opens it, even when an older import backlog is still draining. |
| REQ-EXTIMG-BG-04 | A new store method `CountInternalizePending(ctx, principalID) (uint64, error)` returns the number of pending rows scoped to a principal. Used by the JMAP capability descriptor (REQ-EXTIMG-BG-20). |
| REQ-EXTIMG-BG-08 | The worker exposes a buffered notify channel (`Notify chan struct{}`, capacity 1) that wakes the drain loop. `MarkMessageInternalizePending` and `InsertMessage` (when `InternalizePending = true`) send a non-blocking notification on this channel after the row commits. A worker that is already mid-batch finishes the in-flight messages, resets its cursor to "newest" (beforeID = 0), and begins a new sweep — so a freshly-imported batch never waits for the previous backlog to drain in full before its newest messages are internalized. The notification is non-blocking; a full channel buffer means a wake is already pending and the duplicate is dropped. |
| REQ-EXTIMG-BG-09 | When the drain loop reaches the end of the descending sweep (an empty list returned for a valid `beforeMessageID`), it parks on the notify channel until the next signal — there is no periodic poll. The single poll that matters is the wake-up at startup (REQ-EXTIMG-BG-01); subsequent activity is event-driven. As a safety net the worker also wakes every `idle_poll_interval` (default 5 minutes) so a missed notification cannot strand pending messages indefinitely. |
| REQ-EXTIMG-BG-05 | The synchronous `maybeInternalizeOnDemand` call in `internal/protojmap/mail/email/get.go:renderOne` is removed. Email/get serves the message body whatever its current state. |
| REQ-EXTIMG-BG-06 | The worker is gracefully cancellable: a context cancellation aborts the in-flight Internalize call (the existing `extimg.Internalize` honours ctx) and the worker exits cleanly. No goroutine leaks at shutdown (REQ-NFR-12). |
| REQ-EXTIMG-BG-07 | The worker emits `herold_extimg_worker_messages_total{outcome}` (counter, outcome in `{ok, error, skipped, no_change}`) and `herold_extimg_worker_inflight` (gauge, current concurrency). The existing `herold_extimg_*` audit metrics emitted by `extimg.Internalize` continue to fire on the worker's invocations. |

## Render-time placeholder

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-BG-10 | When Email/get renders the body of a message with `InternalizePending = true`, every external `<img>` `src` (and `<source srcset>` and CSS `url(...)`) in the rendered HTML is rewritten to a herold-local placeholder before the body is returned to the client. The placeholder is a 1x1 transparent PNG served from a known data URI, embedded in the suite asset bundle. The user sees no external image until the worker rewrites the blob; first-render does not leak the recipient's IP or open-rate to the image origin. |
| REQ-EXTIMG-BG-11 | The placeholder rewrite reuses `extimg.RewriteForPlaceholder` (a new helper in `internal/extimg/`) that walks the HTML the same way `extimg.Internalize` walks it, but emits the placeholder data URI in place of every external src instead of fetching. The signature is `RewriteForPlaceholder(raw []byte) ([]byte, AuditSummary, error)`; same shape as `Internalize` so the rendering pipeline can swap them by mode. |
| REQ-EXTIMG-BG-12 | Inline images (CID-referenced, data: URIs, blob: URIs) are NOT rewritten -- they were never an external-tracking risk. Plain text, multipart/alternative text parts, and DKIM-verified passthrough images are also not rewritten (the latter because the importer would not have flagged them). |
| REQ-EXTIMG-BG-13 | The placeholder rewrite is cheap (a single HTML walk, no I/O) and is included in Email/get's normal time budget. There is no separate deadline for the rewrite. |

## JMAP wire shape

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-BG-20 | The Email JMAP object gains a non-RFC field `internalizePending` (boolean). `true` whenever the underlying message row has `internalize_pending = 1`. The field is exposed only when the requested properties include `internalizePending` or `*`. |
| REQ-EXTIMG-BG-21 | The session descriptor at `/.well-known/jmap` grows a non-RFC capability `urn:netzhansa:params:jmap:internalize-status` shaped as `{ pending_messages: uint64, total_messages: uint64, as_of: rfc3339 }`. Principal-scoped: `pending_messages` and `total_messages` are computed per the requesting principal (REQ-EXTIMG-BG-04 above). The capability is forward-compatible with REQ-FTS-PROGRESS-10's transport pattern. |
| REQ-EXTIMG-BG-22 | A push event of the existing JMAP state-change shape fires whenever the worker rewrites a message; the affected `Email` state advances so any open SPA mailbox view re-fetches and the badge clears. The push channel is the same `eventSourceUrl` already used for Email state changes; no new channel. |

## Suite SPA surface

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-BG-30 | The mailbox-list row for a message with `internalizePending = true` carries a small badge (icon + tooltip "Images are being processed in the background. Refresh in a moment to see them.") to the right of the subject. The badge is locale-aware. |
| REQ-EXTIMG-BG-31 | The thread reader for a message with `internalizePending = true` shows the same notice as a non-modal banner above the body. The body itself renders with the placeholders from REQ-EXTIMG-BG-10 in place of external images. |
| REQ-EXTIMG-BG-32 | The settings panel adds an "Image processing" section showing `pending_messages / total_messages` from the JMAP capability. Hidden when `pending_messages == 0` (the steady state). Mirrors REQ-FTS-PROGRESS-21. |
| REQ-EXTIMG-BG-33 | The badge and banner clear automatically when the push event fires and the SPA re-fetches the affected Email object. No explicit "refresh" button is needed; the user sees the images appear once the worker catches up. |

## Implementation order

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-BG-50 | First commit: store-side. Add `ListMessagesWithInternalizePending` and `CountInternalizePending` to the metadata interface; implement on storesqlite and storepg. Register tests via the shared storetest harness. |
| REQ-EXTIMG-BG-51 | Second commit: worker. New `internal/extimg/internalizeworker/` package; wired up in `internal/admin/server.go` after the FTS worker. Include unit tests against fakestore + integration tests against the real SQLite store. |
| REQ-EXTIMG-BG-52 | Third commit: render-time. Add `extimg.RewriteForPlaceholder`; call it from `renderFullWithProperties` whenever the message row has `InternalizePending = true`. Drop the synchronous `maybeInternalizeOnDemand` call from `renderOne` and the now-dead `internalize_ondemand.go` file. Add the `internalizePending` Email field. |
| REQ-EXTIMG-BG-53 | Fourth commit: JMAP capability + push. Add the `internalize-status` capability descriptor and the per-message-rewrite push fan-out. The capability is computed per session bootstrap; not per request. |
| REQ-EXTIMG-BG-54 | Fifth commit: SPA. Suite reads the capability, renders the badge / banner / settings section, and re-fetches affected emails on the push event. Verified via puppeteer. |
| REQ-EXTIMG-BG-55 | Sixth commit (cleanup): remove the operator's `[performance.method_deadline]."Email/get"` override from the docs (and from the maintainer's running system.toml). The default 1 s deadline is now sufficient because the synchronous internalize is gone. |

## Test strategy

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-BG-60 | TestInternalizeWorker_DrainsBacklog: seed 50 messages with InternalizePending=true and a mock image origin; assert the worker processes all 50 within `concurrency * per-message-budget * 50/concurrency` wall time and clears every flag. |
| REQ-EXTIMG-BG-61 | TestInternalizeWorker_GracefulShutdown: start the worker, queue 1000 pending messages, cancel the parent ctx, assert all in-flight goroutines exit within 5 s and no goroutine leak per `goleak.VerifyNone`. |
| REQ-EXTIMG-BG-62 | TestEmailGet_NoSyncInternalize: flag a message as InternalizePending, call Email/get, assert the call returns in <50 ms (no synchronous network I/O), the response carries `internalizePending: true`, and the body's external image URLs have been replaced with the placeholder data URI. |
| REQ-EXTIMG-BG-63 | TestSession_InternalizeStatus_PrincipalScoped: seed two principals with different pending counts; assert each session descriptor reports the per-principal counts. |
| REQ-EXTIMG-BG-64 | SPA TestSearchBar_BadgesAppearForPendingMessages: render the mailbox list with a stub session reporting pending messages, assert the per-row badge appears with the right tooltip text. |
| REQ-EXTIMG-BG-65 | SPA TestThreadReader_PlaceholdersForPendingBody: render a thread with a body whose external images are placeholder-rewritten, assert no `<img src>` references an off-origin host. |

## Out of scope

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-BG-90 | Operator-level CLI for "drain the backlog now". The worker drains automatically on startup and on every poll; no manual trigger is required. |
| REQ-EXTIMG-BG-91 | Per-image retry: a failed Internalize attempt on a single image does not requeue the message. The user sees the placeholder; the operator sees the failure on the existing extimg-fetch-outcome metric. Re-flagging the message via a future maintenance command is left to a separate REQ. |
| REQ-EXTIMG-BG-92 | Worker prioritisation by recency / mailbox / principal. The first cut walks message-id order; the corpus drains uniformly. Recency-prioritised processing is a phase-3 nice-to-have. |
| REQ-EXTIMG-BG-93 | Real-time progress percentages on the SPA (e.g. "47% complete"). The settings-panel section reports the raw counts; the user can compute the percentage themselves. |

## Open questions

None at landing time; the worker shape, placeholder strategy, and SPA surface were settled in the 2026-05-09 design discussion that triggered this REQ.
