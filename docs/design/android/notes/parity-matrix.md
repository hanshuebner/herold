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
| `07-search` — search UI + suggestions | presentation | todo | — |
| `11-optimistic-ui` — optimistic action semantics | presentation | done (milestone 1a, online only) | #327 |
| `14-unsubscribe` — List-Unsubscribe handling | presentation | todo | — |
| `17-attachments` — inline-vs-attach (suite G8) | presentation | todo | — |
| `19-drafts` — draft model | protocol | n/a | — |
| `19-drafts` — compose UI | presentation | todo | — |
| `20-settings` — settings model | protocol | n/a | — |
| `20-settings` — settings UI | presentation | todo | — |
| `25-push-notifications` — enriched push payload | protocol | n/a | — |
| `25-push-notifications` — notification presentation (FCM) | presentation | done (milestone 1b) | #328 |
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
| Local store as UI source of truth (cache-first) | REQ-AND-SYNC-01..13 | done (milestone 1a); offline search is milestone 1c |
| Durable offline outbox | REQ-AND-SYNC-20..25 | deferred (milestone 2) |
| FCM registration, channels, thread notifications, Archive / Mark Read, tap-through | REQ-AND-PUSH-01..03, 10..13, 20 | done (milestone 1b) |
| Inline direct-reply and conversation shortcuts / Bubbles | REQ-AND-PUSH-21/22 | todo (reply needs compose, milestone 1c) |
| System integration (share, widgets, tiles, SAF) | REQ-AND-04x | todo |
| Native navigation shell + predictive back | REQ-AND-05x | done (milestone 1a, phone single-pane) |

## Server gaps the client hit

| Gap | Effect on the client | Owner |
|---|---|---|
| `/proxy/image` authenticates by session cookie only (`internal/admin/server.go` wires it to `authsession.ResolveSession`); a bearer token gets a `401`. | Remote images in the reading pane cannot be proxied, so they stay blocked. Inline `cid:` images are unaffected: they come from `/jmap/download`, which accepts the bearer token. | server (`http-api-implementor`) |
| No disposition property on a category. `CategorySettings` exposes `derivedCategories` names only, so pinned-vs-bundled (suite `REQ-CAT-04/05/11`) has no wire surface. | The client splits lanes itself: the first five names are tabs, the rest bundles. It reads a server disposition as soon as one exists. | server + suite |

## Behaviour the two clients share by copying, not by protocol

| Behaviour | Suite | Mobile |
|---|---|---|
| Push subscription registration | `web/apps/suite/src/lib/push/push-subscription.svelte.ts` sends `deviceClientId`, the endpoint, and `types: ["Email", "Message", "Conversation"]`; it sends no `notificationRules` and no `quietHours`, so herold's REQ-PUSH-81 defaults apply. | `PushRegistrar` sends the same `types` with `kind: "fcm"` and an `fcmToken`. Rules and quiet hours are typed and optional; a settings surface on either client fills them in and the other follows. |
| Notification content | The service worker renders sender as title, subject as body, thread as the tag, with Archive / Mark Read / Reply. | The same payload fields, with the server's 80-byte preview appended to the body and Archive / Mark Read as actions; Reply waits on compose (milestone 1c). |
