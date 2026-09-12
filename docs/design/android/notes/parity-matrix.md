# Parity matrix — mobile client vs the suite

The continuous-follow instrument (`../00-scope.md` § Relationship to the suite).
One row per user-facing suite feature.

## How to use it

- **Kind = protocol** — the behaviour is realised by the JMAP server surface
  (a JMAP type, keyword, filter capability, push payload). Available to the
  mobile client **by construction** the moment the mobile client speaks the
  capability; no per-feature Android work. These rows exist to record *that*
  the coverage is automatic, so drift audits don't mistake them for gaps.
- **Kind = presentation** — the behaviour is client-side UI/interaction. It
  needs deliberate native re-implementation. These rows are the mobile backlog.
- **Status** — `n/a` (protocol, automatic), `todo`, `in-progress`, `done`,
  or `deferred`.
- **Ticket** — the `android-parity` Forgejo issue, when one exists.

When the suite adds or changes a user-facing feature, add or update its row
here in the same change, and file an `android-parity` ticket if it is
presentation-level and not yet built.

## Mail

| Suite REQ / feature | Kind | Status | Ticket |
|---|---|---|---|
| `02-mail-basics` — thread model, read/unread, star | protocol | n/a | — |
| `02-mail-basics` — reading-pane HTML render + inline images | presentation | done (milestone 1a) | #327 |
| `02-mail-basics` — sub-account combined inbox + scope switcher (`REQ-MAIL-SUB-01..09`) | presentation | done (milestone 1a) | #327 |
| `02-mail-basics` — emoji reactions (`Email.reactions`) | protocol | n/a | — |
| `02-mail-basics` — undo-send window (`REQ-MAIL-14`, `REQ-SET-06`) | presentation | todo (the client sends with `sendAt: null`) | — |
| `02-mail-basics` — signature on compose (`REQ-MAIL-100/101`) | presentation | todo | — |
| `02-mail-basics` — Bcc field and the From-picker verification gating (`REQ-IDENT-60`) | presentation | todo (the model carries Bcc; no field yet) | — |
| `03-labels` — label CRUD, apply/remove | protocol | n/a | — |
| `03-labels` — label chips and apply/remove picker | presentation | done (milestone 1a) | #327 |
| `03-labels` — sidebar label tree UI | presentation | todo | — |
| `04-filters` — Sieve filter model | protocol | n/a | — |
| `04-filters` — filter editor UI | presentation | todo | — |
| `05-categorisation` — `$category-*` keywords | protocol | n/a | — |
| `05-categorisation` — pinned tabs and bundled rows | presentation | done (milestone 1a) | #327 |
| `05-categorisation` — category editing and disposition settings | presentation | todo | — |
| `06-snooze` — snooze data model | protocol | n/a | — |
| `06-snooze` — snooze picker UI (presets) | presentation | done (milestone 1a) | #327 |
| `06-snooze` — custom date-and-time preset (`REQ-SNZ-05`) | presentation | todo | — |
| `07-search` — JMAP `Email/query` + FTS | protocol | n/a | — |
| `07-search` — search field, thread results, `SearchSnippet` highlights (`REQ-SRC-01..06`, `30..32`) | presentation | done (milestone 1c); a result outside the synced set is fetched on open and the search screen is restored on back (#339, #340) | #329 |
| `07-search` — fielded operators, autocomplete, recent searches (`REQ-SRC-10/11/22`) | presentation | todo | — |
| `07-search` — in-thread find (`REQ-SRC-50..53`) | presentation | todo | — |
| `11-optimistic-ui` — optimistic action semantics | presentation | done (milestone 1a, online only); the archive undo is offered with the local write rather than after the round trip (#338) | #327 |
| `14-unsubscribe` — List-Unsubscribe handling | presentation | todo | — |
| `17-attachments` — inline-vs-attach (suite G8), upload progress, `maxSizeUpload` (`REQ-ATT-01..06`) | presentation | done (milestone 1c) | #329 |
| `17-attachments` — attachment chips with image thumbnails in the reading pane (`REQ-ATT-20/21`) | presentation | done (#341); the mobile client decodes at display size and adds the Gmail-style size choice on attach (`REQ-AND-SYS-33/34`), which the suite has no counterpart for | #341 |
| `17-attachments` — drag-and-drop targets, disposition flip after adding (`REQ-ATT-07`) | presentation | todo (no drag surface on phone; the flip needs a chip affordance) | — |
| `17-attachments` — share-link offload (`REQ-ATT-60..73`) | presentation | todo | — |
| `19-drafts` — draft model | protocol | n/a | — |
| `19-drafts` — compose UI: new / reply / reply-all / forward, rich text, From picker, drafts (`REQ-MAIL-05/12/12a/30/33`, `REQ-DFT-02`) | presentation | done (milestone 1c) | #329 |
| `19-drafts` — drafts list and re-opening a draft (`REQ-DFT-20..22`) | presentation | todo | — |
| `19-drafts` — multi-device conflict banner (`REQ-DFT-30..33`) | presentation | todo | — |
| `20-settings` — settings model | protocol | n/a | — |
| `20-settings` — settings UI | presentation | todo | — |
| `25-push-notifications` — enriched push payload | protocol | n/a | — |
| `25-push-notifications` — notification presentation (FCM) | presentation | done (milestone 1b); Gmail-grade presentation - app icon, decoded sender, avatar, subject and preview, attachment chips, Reply (#348) | #328 |
| `02-mail-basics` / `REQ-MAIL-44` — sender avatar (hosted principal's picture, initials fallback) | presentation | done in notifications (#348); the message list and reading pane still show no avatar | #348 |
| G7 — LLM transparency contract | protocol | n/a | — |
| G7 — per-message "the LLM was asked ..." inspect view | presentation | todo | — |

## Sibling apps (Phase 4+)

| Suite feature | Kind | Status | Ticket |
|---|---|---|---|
| `27-contacts` — JMAP for Contacts | protocol | n/a | — |
| contacts UI | presentation | deferred | — |
| calendar (JMAP for Calendars) | protocol | n/a | — |
| calendar UI | presentation | deferred | — |
| `08-chat` — chat WS protocol | protocol | n/a | — |
| chat UI | presentation | deferred | — |
| `21-video-calls` — 1:1 WebRTC | protocol | n/a | — |
| call UI | presentation | deferred | — |

## Mobile-only (no suite counterpart)

These are `REQ-AND-*` divergences, not parity rows — recorded here so the
matrix is a complete picture of mobile scope.

| Feature | REQ | Status |
|---|---|---|
| Bearer-token auth (device-token grant, Keystore storage) | REQ-AND-AUTH-03/04/10 | done (milestone 1a) |
| OAuth2 Custom Tab sign-in + biometric unlock | REQ-AND-AUTH-01/02/11 | deferred (milestone 2) |
| Local store as UI source of truth (cache-first) | REQ-AND-SYNC-01..13 | done (milestone 1a); offline search under a "cached results only" banner done (milestone 1c) |
| Durable offline outbox | REQ-AND-SYNC-20..25 | deferred (milestone 2) |
| FCM registration, channels, thread notifications, Archive / Mark Read, tap-through | REQ-AND-PUSH-01..03, 10..13, 20 | done (milestone 1b) |
| Inline direct-reply and conversation shortcuts / Bubbles | REQ-AND-PUSH-21/22 | the deep-link Reply action is done (#348): it opens the composer on the message with the quote prepared. The shade's inline `RemoteInput` reply and conversation shortcuts / Bubbles stay milestone 3 |
| System integration (share, widgets, tiles, SAF) | REQ-AND-04x | todo |
| Native navigation shell + predictive back | REQ-AND-05x | done (milestone 1a, phone single-pane) |

## Server gaps the client hit

| Gap | Effect on the client | Owner |
|---|---|---|
| `/proxy/image` authenticates by session cookie only (`internal/admin/server.go` wires it to `authsession.ResolveSession`); a bearer token gets a `401`. | Remote images in the reading pane cannot be proxied, so they stay blocked. Inline `cid:` images are unaffected: they come from `/jmap/download`, which accepts the bearer token. | server (`http-api-implementor`) |
| `EmailSubmission/set` refuses an envelope `mailFrom` that is not the principal's own local address: sending as a verified foreign identity answers `forbiddenFrom` ("from address ... is not owned by the authenticated principal"), and the separated sub-account's identity answers "identity domain is not authoritative on this server; configure external submission to use this identity". | A composed message can only leave through a local identity, so the dev instance's fake SMTP sink never sees one. Sends are verified by reading the message back from the recipient's account. | server (`http-api-implementor`) |
| No disposition property on a category. `CategorySettings` exposes `derivedCategories` names only, so pinned-vs-bundled (suite `REQ-CAT-04/05/11`) has no wire surface. | The client splits lanes itself: the first five names are tabs, the rest bundles. It reads a server disposition as soon as one exists. | server + suite |

## Behaviour the two clients share by copying, not by protocol

| Behaviour | Suite | Mobile |
|---|---|---|
| Push subscription registration | `web/apps/suite/src/lib/push/push-subscription.svelte.ts` sends `deviceClientId`, the endpoint, and `types: ["Email", "Message", "Conversation"]`; it sends no `notificationRules` and no `quietHours`, so herold's REQ-PUSH-81 defaults apply. | `PushRegistrar` sends the same `types` with `kind: "fcm"` and an `fcmToken`. Rules and quiet hours are typed and optional; a settings surface on either client fills them in and the other follows. |
| Reply recipients, subject markers, quoting | `web/apps/suite/src/lib/compose/compose.svelte.ts` derives To from the parent (its recipients for an own-sent message), Cc from To-then-Cc minus the user's own addresses, collapses `Re:`/`Fwd:` marker chains over the same localized vocabulary, and lays the quote out as two empty paragraphs, an attribution line and a `<blockquote>`. | `ReplyBuilder` in `mobile/shared`, same rules and same vocabulary, unit-tested against the suite's cases. A divergence in either must move both. |
| Search filter shape | `applyTrashJunkExclusion` splices `inMailboxOtherThan` into a flat `FilterCondition` so the server's fast-query gate recognises it (`REQ-SRC-06`). | `MailSearch.filter` builds the same flat object with `text` and `inMailboxOtherThan` as sibling keys. |
| Draft and submission shape | `Email/set` writes the draft into Drafts with `$draft`, then `EmailSubmission/set` with `onSuccessUpdateEmail` clears `$draft` and moves it to Sent. | `Composer.send` issues the same two-call batch; the mobile client sends with `sendAt: null` because it has no undo-send window yet. |
| Notification content | The service worker renders sender as title, subject as body, thread as the tag, with Archive / Mark Read / Reply. | The same payload fields: sender as title, subject as the body line, the server's 80-byte preview on the expanded line, thread as the tag, Archive / Mark read / Reply as actions. The phone adds what a shade shows and a browser notification cannot: the sender's avatar as the large icon, chips for the message's attachments with a thumbnail for an image part, and the account's address as the bundle's header (#348). |
| Sender avatar | `avatar-resolver.svelte.ts` resolves own identity, then the hosted principal's `avatarBlobId` through the blob download URL, then Face / Gravatar, then a letter on the one interactive colour. | The notification's large icon resolves the hosted principal's `avatarBlobId` through the same `Principal/query` + `Principal/get` pair, downloaded with the bearer token and kept in the blob cache. Face and Gravatar are not used on the phone. The fallback initials sit on a colour derived from the address, because a shade full of identical circles distinguishes nothing; the suite's single interactive colour works there because the name is beside it. |
| Sender display name | The service worker prints `payload.from` as it arrives. | `SenderLine` splits the header form and decodes RFC 2047 encoded words, so a payload from a server without the decoded-sender change (#347) still renders a name rather than `=?UTF-8?B?...?=`. |
