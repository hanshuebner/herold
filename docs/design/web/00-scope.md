# 00 — Scope

What the suite is, what it isn't, the defaults that hold unless this file is edited.

## The suite

The suite is one cohesive product name. It comprises three planned web applications sharing one JMAP backend (herold), one design system, one auth flow, and one set of integration points:

- **the suite** — the email client. v1 is in flight; this docs/ tree currently describes it.
- **the calendar app** — calendar UI over JMAP for Calendars (RFC 8984 + the JMAP-Calendars binding). Future; not started.
- **the contacts app** — contacts UI over JMAP for Contacts (RFC 9553 + the JMAP-Contacts binding). Future; not started.
- **chat (built into the suite shell)** — DMs, Spaces, 1:1 video calls. Not a separate app — a persistent panel rendered by the suite shell, plus a `/chat/*` fullscreen route. See `requirements/08-chat.md`, `requirements/21-video-calls.md`, `architecture/07-chat-protocol.md`.

The suite ships as **one SPA shell** with client-side routing (`architecture/01-system-overview.md` § Suite shape). Per-app code organises into `apps/{suite,mail,calendar,contacts,chat}` packages but builds into a single bundle. The persistent chat panel forces this shape: it must outlive route changes, so the apps cannot be separately-bundled SPAs.

This file describes scope for **the suite**. When the calendar app and the contacts app get their own scope docs, they will live alongside the suite's under each app's tree, and a small suite-level scope doc will name the cross-cutting decisions (auth, design system, JMAP server target).

## Chat scope at a glance

- **In:** DMs (1:1), Spaces (group), text + emoji + inline images, reactions, read receipts, typing indicators, presence, mute / block, search-within-conversation, 1:1 video calls.
- **Out (v1):** group video calls (require an SFU), threaded replies, federation / bridges to other networks, end-to-end encryption, screen sharing, recording, voice messages, custom emoji, browser-level push notifications when the tab is closed (NG2).

Detail: `requirements/08-chat.md`, `requirements/21-video-calls.md`.

## Cross-app integration points

The suite-mail surfaces hooks that will be wired into the sibling apps when they exist:

- **iMIP RSVP.** Calendar invites in mail (`text/calendar; method=REQUEST`) render inline with Accept / Tentative / Decline. The mail app sends the iMIP REPLY via `EmailSubmission/set` and (when the calendar app exists) updates the calendar app's view of the event. Until the calendar app exists, the RSVP path operates against herold's JMAP for Calendars directly.
- **Contact autocomplete in compose.** Recipient autocompletion sources from the contacts app's data when it exists; until then, from a client-local history of seen From/To addresses (see `requirements/02-mail-basics.md` REQ-MAIL-11 and `notes/open-questions.md` Q9).
- **"View contact" / "All emails with this person".** Sender-name in the reading pane links into the contacts app once it exists.
- **"Add to calendar" on detected event-like content.** Cut for the suite v1; revisit when the calendar app exists.

These are spec-only for now — implementation lands when the sibling app does.

## Goals

- **G1.** Replicate the Gmail subset its single user actually uses, calibrated from gmail-logger capture data — not from feature lists or assumptions.
- **G2.** Be a competent JMAP citizen. RFC 8620 / 8621 conformant; uses `Email/changes` + state strings for incremental sync; uses EventSource push (RFC 8620 §7) for live updates; never polls when push is available.
- **G3.** Keyboard-first UX. Mouse remains supported throughout, but every primary action has a shortcut and the shortcut model is internally consistent (Gmail-compatible where the user has muscle memory).
- **G4.** Optimistic UI. Archive / label / snooze / star / delete update the screen before the server confirms; failure reverts with a clear error and a Retry affordance.
- **G5.** Single-user, single-account, online-first. No accidental complexity from features the user doesn't need.
- **G6.** Plays cleanly with herold. Both ends are ours; the JMAP capability set the suite relies on is explicit (`notes/server-contract.md`), not guessed by feature-detection on every connect.
- **G7.** **LLM transparency.** Anywhere herold ran an LLM against the user's content (spam classification, automatic categorisation, and any future LLM-driven suite feature), the suite can show the user the prompt that was used minus operator guardrails. Surfaced both per-account in settings ("the prompt currently used to categorise your mail is …") and per-message in an inspect view ("the LLM was asked …" + verdict + confidence). Reads from herold's transparency contract (`../server/requirements/06-filtering.md` REQ-FILT-65..68). The user is never left wondering "why did this end up here?".
- **G8.** **Inline images and attachments are user-controlled, not auto-detected.** Compose offers two distinct drop targets — "drop here to inline in the body" and "drop here to attach alongside the body". Pasting an image in the body inlines it; the file picker attaches; the explicit drop targets cover the ambiguous drag-from-desktop case. Either choice is reversible (drag inline image out to the attachment list and vice versa). Received messages render inline images in the body where the sender placed them, and surface the same single-action download affordance per inline image that attachments already have. "Download all attachments" includes inline images by default.

## Non-goals

- **NG1.** Native iOS / Android applications. **Mobile and tablet web is in scope as a first-class experience** (`requirements/24-mobile-and-touch.md`) — installable as a PWA, full responsive layout, full touch interaction model, browser-level push notifications (`requirements/25-push-notifications.md`). Native apps remain out: the PWA + Web Push delivers app-icon, standalone-window, and notification UX without a native build pipeline.
- **NG2.** Offline mode. No service-worker cache, no IndexedDB outbox, no operating-while-disconnected. Reconnect-and-resync is the resilience model.
- **NG3.** Multi-account UI. v1 binds to one JMAP Account; account switching is not a feature.
- **NG4.** Delegation, shared mailboxes, admin / multi-user views.
- **NG5.** S/MIME and PGP — encryption, signing, key management. Out for v1; revisit only if the user changes their mail-handling pattern.
- **NG6.** **Within the suite:** calendar and contacts *management* (creating events, editing the address book) live in the sibling apps the calendar app and the contacts app, not in mail. **Always out of the Suite scope:** video conferencing, generic file storage, ad-hoc note-taking. Mail-side integration with iMIP and with the contacts data is in scope and lives under "Cross-app integration points" above.
- **NG7.** AI features. No "Help me write", no smart compose, no inbox summarisation. The user explicitly does not want these.
- **NG8.** Print-to-PDF, Confidential Mode equivalent, scheduled send, vacation responses. Some of these are server-side concerns that herold may grow; the suite does not expose them in v1.
- **NG9.** Third-party tracking, analytics, ad scripts. Ever.

## Defaults in force

- **Server:** herold. v1 does not target other JMAP servers. The capability-driven approach in `notes/server-contract.md` keeps "support another server" tractable but not free.
- **Protocol surface:** RFC 8620 (Core), RFC 8621 (Mail), RFC 9007 (Sieve scripts for filters), EventSource push from RFC 8620 §7. WebSocket subprotocol (RFC 8887) is **not** v1; revisit if SSE proves inadequate under load.
- **Browser support:** Chromium 120+ (desktop and Android), Firefox 120+ (desktop), Safari 17+ (desktop and iOS). Older browsers explicitly unsupported.
- **Viewport target:** Three first-class breakpoints — phone (< 768 px), tablet (768–1279 px), desktop (≥ 1280 px). All three are designed and tested. See `requirements/24-mobile-and-touch.md`.
- **Auth:** Bearer token over HTTPS. Token sourcing TBD (interactive form vs OIDC redirect via herold) — see `notes/open-questions.md`.
- **Capture-driven requirements:** sections covering mail basics, keyboard priorities, workflows, and performance budgets are populated from gmail-logger output per `notes/capture-integration.md`. Until that data lands, those sections carry placeholders rather than guesses.
- **Visual style:** light and dark are equal peers. **Default theme follows the system's `prefers-color-scheme`** (`requirements/20-settings.md` REQ-SET-01); the user can override to a fixed theme in settings. IBM Plex Sans / IBM Plex Mono so we stay visually consistent with the gmail-logger popup during prototyping.
- **Localisation:** English (US/GB), German (DE/AT/CH), French (FR/BE/CA/CH) at v1 (`requirements/22-internationalization.md`). ICU MessageFormat resource bundles; `Intl` APIs for date/time/number formatting. RTL and CJK out for v1.

## Open scope items

See `notes/open-questions.md`. Items there block specific requirements; resolving each typically updates one section of this tree.
