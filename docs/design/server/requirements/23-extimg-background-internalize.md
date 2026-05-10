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
| REQ-EXTIMG-BG-09 | When the drain loop reaches the end of the descending sweep (an empty list returned for a valid cursor), it parks on the notify channel until the next signal — there is no periodic poll. The single poll that matters is the wake-up at startup (REQ-EXTIMG-BG-01); subsequent activity is event-driven via REQ-EXTIMG-BG-08's notify channel. *(Earlier wording specified a 5-minute safety-net `idle_poll_interval` as a backstop against missed notifications. Removed 2026-05-10 after observation showed the buffered notify channel is reliable: a missed notification would require both that the channel buffer is already-set when a fresh `MarkMessageInternalizePending` fires AND that the worker reaches the park branch without consulting the buffer first. The select in the !empty branch drains the buffer before each batch, so the buffer is always empty when the worker parks; a fresh poke always wakes it. The safety-net wake added log noise on idle systems without observable benefit.)* |
| REQ-EXTIMG-BG-05 | The synchronous `maybeInternalizeOnDemand` call in `internal/protojmap/mail/email/get.go:renderOne` is removed. Email/get serves the message body whatever its current state. |
| REQ-EXTIMG-BG-06 | The worker is gracefully cancellable: a context cancellation aborts the in-flight Internalize call (the existing `extimg.Internalize` honours ctx) and the worker exits cleanly. No goroutine leaks at shutdown (REQ-NFR-12). |
| REQ-EXTIMG-BG-07 | The worker emits `herold_extimg_worker_messages_total{outcome}` (counter, outcome in `{ok, error, skipped, no_change}`) and `herold_extimg_worker_inflight` (gauge, current concurrency). The existing `herold_extimg_*` audit metrics emitted by `extimg.Internalize` continue to fire on the worker's invocations. |

## Render-time placeholder

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-BG-10 | When Email/get renders the body of a message with `InternalizePending = true`, every external `<img>` `src` (and `<source srcset>` and CSS `url(...)`) in the rendered HTML is rewritten to a herold-local placeholder before the body is returned to the client. The placeholder is a 1x1 transparent GIF emitted from the server-side constant `extimg.PlaceholderDataURI` (`internal/extimg/placeholder.go`); identical bytes on every emission. The user sees no external image until the worker rewrites the blob; first-render does not leak the recipient's IP or open-rate to the image origin. |
| REQ-EXTIMG-BG-11 | The placeholder rewrite reuses `extimg.RewriteForPlaceholder` (a new helper in `internal/extimg/`) that walks the HTML the same way `extimg.Internalize` walks it, but emits the placeholder data URI in place of every external src instead of fetching. The signature is `RewriteForPlaceholder(raw []byte) ([]byte, AuditSummary, error)`; same shape as `Internalize` so the rendering pipeline can swap them by mode. |
| REQ-EXTIMG-BG-12 | Inline images (CID-referenced, data: URIs, blob: URIs) are NOT rewritten -- they were never an external-tracking risk. Plain text, multipart/alternative text parts, and DKIM-verified passthrough images are also not rewritten (the latter because the importer would not have flagged them). |
| REQ-EXTIMG-BG-13 | The placeholder rewrite is cheap (a single HTML walk, no I/O) and is included in Email/get's normal time budget. There is no separate deadline for the rewrite. |
| REQ-EXTIMG-BG-14 | When `extimg.Internalize` produces a partial-success outcome (`Internalized > 0` AND `Failed > 0`, or `Internalized == 0` but the fetch pass attempted candidates), every URL that survived the cid: rewrite is replaced with the placeholder data URI before `rebuildMessage` is called. The stored body therefore contains zero externally-fetchable references regardless of which call site (`worker.processOne`, SMTP-time `protosmtp.IngestBytes`, the on-demand path) drove `extimg.Internalize`. The `AuditSummary.Placeholdered` counter records how many candidates were placeholdered. Without this rule, an origin that 403s the operator's IP (e.g. Cloudflare Bot-Fight gating spacer.png on `forums.atariage.com`) leaves the failed URLs intact in the stored body — relying entirely on the SPA's privacy gate as the sole protection on render. Any path that bypasses the SPA gate (raw blob download, IMAP FETCH, future "always load images" toggle) would then leak the open rate to the original origin. The placeholder rewrite eliminates that risk permanently; the user opt-in to view those images is necessarily forfeited (the original URL is no longer in the body) — which matches reality, since an origin that refuses our IP refuses our IP indefinitely. |

## JMAP wire shape

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-BG-20 | The Email JMAP object gains a non-RFC field `internalizePending` (boolean). `true` whenever the underlying message row has `internalize_pending = 1`. The field is exposed only when the requested properties include `internalizePending` or `*`. |
| REQ-EXTIMG-BG-21 | The session descriptor at `/.well-known/jmap` grows a non-RFC capability `urn:netzhansa:params:jmap:internalize-status` shaped as `{ pending_messages: uint64, total_messages: uint64, as_of: rfc3339 }`. Principal-scoped: `pending_messages` and `total_messages` are computed per the requesting principal (REQ-EXTIMG-BG-04 above). The capability is forward-compatible with REQ-FTS-PROGRESS-10's transport pattern. |
| REQ-EXTIMG-BG-22 | *Superseded by REQ-EXTIMG-BG-INTERNAL-22 (2026-05-10): the original wording advanced Email state per worker rewrite, which produced a sustained ~5 Hz `Email/changes` storm during backlog drains. The replacement classifies worker writes as `cause = 'background'` and pushes the pending-count signal over a separate `InternalizeStatus` channel.* |

## Suite SPA surface

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-BG-30 | The mailbox-list row for a message with `internalizePending = true` carries a small badge (icon + tooltip "Images are being processed in the background. Refresh in a moment to see them.") to the right of the subject. The badge is locale-aware. |
| REQ-EXTIMG-BG-31 | The thread reader for a message with `internalizePending = true` shows the same notice as a non-modal banner above the body. The body itself renders with the placeholders from REQ-EXTIMG-BG-10 in place of external images. |
| REQ-EXTIMG-BG-32 | The settings panel adds an "Image processing" section showing `pending_messages / total_messages` from the JMAP capability. Hidden when `pending_messages == 0` (the steady state). Mirrors REQ-FTS-PROGRESS-21. |
| REQ-EXTIMG-BG-33 | *Superseded by REQ-EXTIMG-BG-INTERNAL-32 (2026-05-10): the badge no longer clears via a server-driven Email-state push; it clears the next time the user-driven `Email/get` re-fetches the row (and the row's `internalizePending` flag has been cleared). The settings-panel pending count refreshes promptly via the new `InternalizeStatus` push.* |

## Internal change-feed classification

*(Added 2026-05-10. The change-feed is the single source of truth for
both user-facing JMAP `/changes` and internal consumers — IMAP CONDSTORE,
FTS, seen-address. The two have different needs: JMAP / EventSource
should only react to mutations the user can observe; the internal
consumers must see every mutation that affected the data they index.
Until now the two were conflated and worker rewrites drove both. This
section splits them.)*

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-BG-INTERNAL-01 | `state_changes` grows a `cause TEXT NOT NULL DEFAULT 'user'` column. Defined values: `'user'` (any user-driven mutation: JMAP `/set`, IMAP `STORE` / `EXPUNGE` / `APPEND` / `COPY` / `MOVE`, delivery, admin), `'background'` (in-process worker that rewrites stored data without changing what the user can observe). Open enum (TEXT) for forward-compatibility with future causes. |
| REQ-EXTIMG-BG-INTERNAL-02 | Migrations add the column and a partial index `(principal_id, entity_kind, seq) WHERE cause = 'user'`. The pre-existing full index is retained for IMAP / FTS scans. Existing rows back-fill to `'user'`. |
| REQ-EXTIMG-BG-INTERNAL-03 | The `Metadata` writer surface gains a `appendStateChangeBackground` variant. The default `appendStateChange` continues to write `cause = 'user'`. Each writer site declares its cause explicitly — no ambient inference, no caller writes `'background'` without a code-review reason. |
| REQ-EXTIMG-BG-INTERNAL-10 | `GetMaxChangeSeqForKind` filters `cause = 'user'`. All current callers (JMAP `Foo/state`, `Foo/changes`, `Foo/queryChanges`, EventSource `collectStateMap`) get the user-visible state with no per-caller change. |
| REQ-EXTIMG-BG-INTERNAL-11 | `ReadChangeFeed` defaults to `cause = 'user'`. A sibling `ReadChangeFeedAll` (or an explicit cause-set option on the same primitive) is used by IMAP IDLE in `internal/protoimap/session_store_search.go`, the FTS feed reader (`storesqlite/fts.go`, `storepg/fts.go`), and the seen-address indexer (`internal/protojmap/mail/seenaddress/seenaddress.go`). The default fails closed: a missed call site sees less data, never more. |
| REQ-EXTIMG-BG-INTERNAL-12 | The EventSource push loop arms its flush timer only when at least one polled change has `cause = 'user'`. A polling cycle that sees only `'background'` advances the cursor silently — no SSE event, no `Foo/changes` round-trip provoked on the SPA. |
| REQ-EXTIMG-BG-INTERNAL-13 | IMAP IDLE / CONDSTORE behaviour is unchanged: every change-feed entry for the selected mailbox emits its untagged response, including worker rewrites. `mailboxes.highest_modseq` and `message_mailboxes.modseq` continue to advance because `ReplaceMessageBody` bumps them on the message-side tables, independent of `cause`. |
| REQ-EXTIMG-BG-INTERNAL-14 | The FTS-indexer feed reader and seen-address indexer opt in to both causes so a body rewrite triggers re-indexing. |
| REQ-EXTIMG-BG-INTERNAL-15 | The `extimg` internalize-worker is the only v1 producer of `cause = 'background'`. Adding a new internal writer requires a follow-up REQ that justifies the classification. |

### Pending-count push channel

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-BG-INTERNAL-20 | A new `JMAPStateKindInternalizeStatus` is added to `internal/store/types_phase2.go`; `jmap_states` grows an `internalize_status_state INTEGER NOT NULL DEFAULT 0` column. Bumped via the existing `IncrementJMAPState` API. |
| REQ-EXTIMG-BG-INTERNAL-21 | The internalize-worker calls `IncrementJMAPState(JMAPStateKindInternalizeStatus)` once per non-empty processed batch (after `wg.Wait()`, before the cursor advances), aggregated per affected principal. The bump signals "the worker did some work" — not "every rewrite committed". |
| REQ-EXTIMG-BG-INTERNAL-22 | EventSource `collectStateMap` registers `InternalizeStatus` alongside `Identity` / `EmailSubmission` / `VacationResponse` in the row-based section. Subscribed clients see one `InternalizeStatus` push per worker batch. *Replaces REQ-EXTIMG-BG-22's "advance Email state" mechanism end-to-end.* |
| REQ-EXTIMG-BG-INTERNAL-23 | `buildInternalizeStatusCapability` (`internal/protojmap/session.go`) keeps its current shape and remains the data source. The push only signals "the count may have moved"; the SPA re-fetches the capability via `auth.refreshSession()` to read the new value. |
| REQ-EXTIMG-BG-INTERNAL-30 | Suite `App.svelte` adds `'InternalizeStatus'` to `sync.start(types)`. A `sync.on('InternalizeStatus', () => auth.refreshSession())` handler lives next to the existing capability-refresh handlers. |
| REQ-EXTIMG-BG-INTERNAL-31 | The mail-store's `#onEmailStateChange` (`web/apps/suite/src/lib/mail/store.svelte.ts`) drops its `void auth.refreshSession();` line and the comment block referencing REQ-EXTIMG-BG-33 is rewritten to point at REQ-EXTIMG-BG-INTERNAL-32. |
| REQ-EXTIMG-BG-INTERNAL-32 | When the worker finishes processing a message it clears the row's `InternalizePending` flag. The badge / banner clears the next time the user-driven `Email/get` re-fetches that row — there is no longer a server-driven push to force per-Email re-fetch. The settings-panel "Image processing" section updates promptly via the `InternalizeStatus` push. *Replaces REQ-EXTIMG-BG-33.* |

### SPA placeholder rendering

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-BG-INTERNAL-40 | The suite SPA's sanitiser (`web/apps/suite/src/lib/mail/sanitize.ts:rewriteImage`) MUST allow the server-emitted placeholder data URI through unchanged. Without this, the existing scheme allowlist (http(s) only) strips the `src` and the browser renders a broken-image icon with the original `alt` text in its place — the failure mode reported on 2026-05-10 against pending Google-receipt mail. The narrowed allowlist for inbound `data:` is `^data:image/gif;base64,R0lGODlh` (the literal prefix of `extimg.PlaceholderDataURI`); any other inbound `data:image/...` is still stripped, preserving the constraint that user-supplied bodies cannot smuggle inline images past the external-fetch gate. |
| REQ-EXTIMG-BG-INTERNAL-41 | When the SPA renders an `<img>` whose `src` is the placeholder data URI, the iframe stylesheet sizes the placeholder to a visible gray box (`min-height: 6em`, `width: 100%` of the parent's content box, `background: var(--bx-color-layer-02)` or equivalent muted token, optional centred icon glyph). Rationale: the user opening an image-heavy email should see *where* the images will land while the worker drains; a 1x1 transparent placeholder collapses the layout and makes the "Bilder werden verarbeitet" banner visually orphaned. The CSS rule MUST match the literal placeholder prefix only (`img[src^="data:image/gif;base64,R0lGODlhAQABAIAAAP"]`), never a generic `img[src^="data:"]`, so styling never applies to user-supplied data URIs. |

### Worker observability

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-BG-INTERNAL-50 | The internalize-worker emits one INFO line per processed batch with attrs `{ batch_size, principals, ok, no_change, failed, elapsed_ms, cursor_before, cursor_after }`. This replaces the current state where a healthy worker logs only the one-shot "started" line and the operator has no signal that progress is occurring. DEBUG-level per-message lines are retained for fine-grained traces. |
| REQ-EXTIMG-BG-INTERNAL-51 | The worker emits one INFO line on each idle-park transition (batch returned empty, parking until the notify channel fires) and one on each wake-up. Without these, the operator cannot distinguish "worker is idle and waiting" from "worker has stalled". *(The original wording had a `wake_source` attribute discriminating `notify` vs `idle_poll`; the safety-net `idle_poll` source was removed 2026-05-10 — see REQ-EXTIMG-BG-09 — so the attribute was retired.)* |
| REQ-EXTIMG-BG-INTERNAL-52 | The `extimg-worker: started` line carries the initial `pending_count` (a single `CountInternalizePending` call summed across principals on startup) so the operator sees the backlog magnitude at boot. A subsequent INFO line every `progress_log_interval` (default 60 s) re-emits the current `pending_count` and the worker's processed-since-start total — a sustained-progress beacon when individual batch lines are too noisy or have rolled out of the log window. The beacon is suppressed when the worker is fully idle (`pending_count == 0` AND `processed_total` has not advanced since the prior tick) so a quiescent system collapses to a single log line per quiet stretch instead of one per minute (added 2026-05-10). The first tick after startup always logs so the operator sees the worker is alive and the interval matches expectations. |

### Worker prioritisation

*(Added 2026-05-10. The original REQ-EXTIMG-BG-03 specified
"DESCENDING order (newest first)" by message id, with the rationale
that "freshly-arrived mail is searchable / image-rewritten as soon as
the user opens it". For live SMTP delivery message-id and received-at
are correlated and the rule does what its rationale says. For a one-
shot bulk import they are anti-correlated: the maintainer's freshly-
imported mailbox put a 2026-05-08 receipt at message id 2156 and an
8-year-old archive entry at message id 126,324, so the worker — doing
exactly what BG-03 says — was processing 2018 mail first and queued
the user's freshest mail last behind 60k+ years-old messages.)*

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-BG-INTERNAL-80 | The worker processes pending messages in DESCENDING `received_at_us` order (most-recent mail by header date first), tie-broken on DESCENDING `message_id`. Supersedes REQ-EXTIMG-BG-03's "DESCENDING order by id" — the underlying intent ("newest first") is preserved with a definition of "newest" that matches what the user sees in the inbox. |
| REQ-EXTIMG-BG-INTERNAL-81 | A new metadata method `ListMessagesWithInternalizePendingByReceivedAt(ctx, beforeReceivedAtUs int64, beforeMessageID MessageID, limit int) ([]MessageRef, error)` returns up to `limit` `(MessageID, ReceivedAtUs)` pairs where `internalize_pending = 1` AND `(received_at_us, id) < (beforeReceivedAtUs, beforeMessageID)` lexicographically, ordered DESC. Pass `beforeReceivedAtUs = 0` (or sentinel max-int64) and `beforeMessageID = 0` to start from the most-recent pending message. The worker maintains an in-memory cursor as the `(receivedAtUs, id)` of the lowest item in the prior batch. The existing `ListMessagesWithInternalizePending(ctx, beforeMessageID, limit)` is retained for tests that need id-ordered iteration; production callers move to the new method. |
| REQ-EXTIMG-BG-INTERNAL-82 | Migrations on both backends add a partial covering index over `(principal_id, received_at_us DESC, id DESC)` restricted to `internalize_pending = 1`. The existing partial index over `(internalize_pending = 1)` may stay for the legacy id-ordered method; pick whichever the storage-implementor judges cheaper to maintain. The cost: one index-row per pending message, dropped as the flag clears. At the maintainer's 62k-pending peak the index is ~few-MB; at a hypothetical future 10M-row backlog the index is ~hundreds-of-MB and worth re-evaluating then. |
| REQ-EXTIMG-BG-INTERNAL-83 | The worker's batch-summary log line (REQ-INTERNAL-50) replaces `cursor_before` / `cursor_after` with `received_at_before` / `received_at_after` (RFC3339 timestamps) so the operator can read the worker's progress as "drained from 2026-05-09 down to 2026-05-08" rather than as opaque message ids. The cursor's tie-breaker `message_id` is logged as a secondary attr only. |

### Test strategy (additions)

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-BG-INTERNAL-60 | TestStateChange_BackgroundInvisibleToJMAP: writing a row with `ChangeCauseBackground` does not advance `GetMaxChangeSeqForKind` and is invisible to a default `ReadChangeFeed`. |
| REQ-EXTIMG-BG-INTERNAL-61 | TestStateChange_BackgroundVisibleToIMAP: the same row IS visible to `ReadChangeFeedAll` and `mailboxes.highest_modseq` has advanced. |
| REQ-EXTIMG-BG-INTERNAL-62 | TestEventSource_BackgroundChurnNoPush: drive the push loop with synthetic background-only changes; assert no SSE event is emitted and the cursor advances silently. |
| REQ-EXTIMG-BG-INTERNAL-63 | TestInternalizeWorker_BumpsInternalizeStatusOncePerBatch: queue 50 pending messages, run one drain pass, assert `JMAPStateKindInternalizeStatus` advanced exactly once. |
| REQ-EXTIMG-BG-INTERNAL-64 | TestSpaSync_InternalizeStatusTriggersSessionRefresh: drive the suite SPA's sync layer with a synthetic `InternalizeStatus` push; assert `auth.refreshSession()` was called and the Email-state path was *not* triggered. |
| REQ-EXTIMG-BG-INTERNAL-65 | TestSanitize_PlaceholderDataUriPassesThrough (vitest): feed `<img src="data:image/gif;base64,R0lGODlhAQABAIAAAP/...">` to `sanitizeHtml`; assert the resulting `<img>` retains its `data:` `src` and does not gain the `data-herold-blocked` attribute. Companion test: a non-placeholder `data:image/png;base64,...` is still stripped (the allowlist is the literal placeholder prefix only). |
| REQ-EXTIMG-BG-INTERNAL-66 | TestSanitize_PlaceholderRendersVisibleBox (vitest): assert the iframe stylesheet contains the literal selector `img[src^="data:image/gif;base64,R0lGODlhAQABAIAAAP"]` and that it sets a non-zero `min-height`. The selector test is a string match against the served stylesheet — guarding against a future refactor that breaks the link between server constant and SPA selector. |
| REQ-EXTIMG-BG-INTERNAL-67 | TestInternalizeWorker_BatchSummaryLogged (Go): seed 5 pending messages, run one batch, capture the worker's slog output via a buffered handler; assert exactly one INFO line with the `batch.summary` message and the expected attrs (`batch_size=5`, `ok>=0`, `failed>=0`, `elapsed_ms>=0`). |
| REQ-EXTIMG-BG-INTERNAL-68 | TestListMessagesWithInternalizePendingByReceivedAt_OrdersByReceivedAt (storetest): seed three pending messages — id=1 received 2018, id=2 received 2026, id=3 received 2020. Call the new list method with `beforeReceivedAtUs=0, beforeMessageID=0, limit=10`; assert the order is `[id=2, id=3, id=1]`. Companion: pagination test with `limit=1` and the cursor advancing through the same three messages in the same order. |
| REQ-EXTIMG-BG-INTERNAL-69 | TestInternalizeWorker_NewestByReceivedAtFirst (Go): seed two pending messages where the message-id ordering and the received-at ordering disagree (id=10 received 2020, id=20 received 2018). Run one batch with `BatchSize=1`. Assert the worker processed `id=10` first (received 2020 wins on received_at_us DESC) — guarding the prioritisation rule against a future regression that re-introduces id-DESC iteration. |

### Migration ordering

| ID | Requirement |
|----|-------------|
| REQ-EXTIMG-BG-INTERNAL-70 | The two migrations land in one commit so the schema-version invariant test never sees a half-migrated state. Existing data back-fills to `cause = 'user'` and `internalize_status_state = 0`. |
| REQ-EXTIMG-BG-INTERNAL-71 | Go-side change set lands in dependency order: (1) schema + writer plumbing; (2) reader filters; (3) push-loop guard + InternalizeStatus state column wiring; (4) extimg worker writes `'background'` and bumps InternalizeStatus per batch; (5) SPA sync handler + Email handler drop refreshSession. Steps 1-4 ride a single backend commit; step 5 is one frontend commit. |

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
