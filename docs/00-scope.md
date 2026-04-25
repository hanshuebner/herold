# 00 — Scope

What tabard is, what it isn't, the defaults that hold unless this file is edited.

## Tabard is a suite

**Tabard** is the suite name. It comprises three planned web applications sharing one JMAP backend (herold), one design system, one auth flow, and one set of integration points:

- **tabard-mail** — the email client. v1 is in flight; this docs/ tree currently describes it.
- **tabard-calendar** — calendar UI over JMAP for Calendars (RFC 8984 + the JMAP-Calendars binding). Future; not started.
- **tabard-contacts** — contacts UI over JMAP for Contacts (RFC 9553 + the JMAP-Contacts binding). Future; not started.

Repository layout will be a monorepo (`apps/mail`, `apps/calendar`, `apps/contacts`, plus shared `packages/` for the design system, JMAP client substrate, and generated JMAP types). The split happens when the second app starts; until then, the existing `docs/` tree at root is tabard-mail's spec.

This file describes scope for **tabard-mail**. When tabard-calendar and tabard-contacts get their own scope docs, they will live alongside tabard-mail's under each app's tree, and a small suite-level scope doc will name the cross-cutting decisions (auth, design system, JMAP server target).

## Cross-app integration points

Tabard-mail surfaces hooks that will be wired into the sibling apps when they exist:

- **iMIP RSVP.** Calendar invites in mail (`text/calendar; method=REQUEST`) render inline with Accept / Tentative / Decline. The mail app sends the iMIP REPLY via `EmailSubmission/set` and (when tabard-calendar exists) updates the calendar app's view of the event. Until tabard-calendar exists, the RSVP path operates against herold's JMAP for Calendars directly.
- **Contact autocomplete in compose.** Recipient autocompletion sources from the contacts app's data when it exists; until then, from a client-local history of seen From/To addresses (see `requirements/02-mail-basics.md` REQ-MAIL-11 and `notes/open-questions.md` Q9).
- **"View contact" / "All emails with this person".** Sender-name in the reading pane links into the contacts app once it exists.
- **"Add to calendar" on detected event-like content.** Cut for tabard-mail v1; revisit when tabard-calendar exists.

These are spec-only for now — implementation lands when the sibling app does.

## Goals

- **G1.** Replicate the Gmail subset its single user actually uses, calibrated from gmail-logger capture data — not from feature lists or assumptions.
- **G2.** Be a competent JMAP citizen. RFC 8620 / 8621 conformant; uses `Email/changes` + state strings for incremental sync; uses EventSource push (RFC 8620 §7) for live updates; never polls when push is available.
- **G3.** Keyboard-first UX. Mouse remains supported throughout, but every primary action has a shortcut and the shortcut model is internally consistent (Gmail-compatible where the user has muscle memory).
- **G4.** Optimistic UI. Archive / label / snooze / star / delete update the screen before the server confirms; failure reverts with a clear error and a Retry affordance.
- **G5.** Single-user, single-account, online-first. No accidental complexity from features the user doesn't need.
- **G6.** Plays cleanly with herold. Both ends are ours; the JMAP capability set tabard relies on is explicit (`notes/server-contract.md`), not guessed by feature-detection on every connect.

## Non-goals

- **NG1.** Mobile-native clients (iOS / Android apps). Mobile web below 1280 px is best-effort, not a target.
- **NG2.** Offline mode. No service-worker cache, no IndexedDB outbox, no operating-while-disconnected. Reconnect-and-resync is the resilience model.
- **NG3.** Multi-account UI. v1 binds to one JMAP Account; account switching is not a feature.
- **NG4.** Delegation, shared mailboxes, admin / multi-user views.
- **NG5.** S/MIME and PGP — encryption, signing, key management. Out for v1; revisit only if the user changes their mail-handling pattern.
- **NG6.** **Within tabard-mail:** calendar and contacts *management* (creating events, editing the address book) live in the sibling apps tabard-calendar and tabard-contacts, not in mail. **Always out of tabard's suite:** video conferencing, generic file storage, ad-hoc note-taking. Mail-side integration with iMIP and with the contacts data is in scope and lives under "Cross-app integration points" above.
- **NG7.** AI features. No "Help me write", no smart compose, no inbox summarisation. The user explicitly does not want these.
- **NG8.** Print-to-PDF, Confidential Mode equivalent, scheduled send, vacation responses. Some of these are server-side concerns that herold may grow; tabard does not expose them in v1.
- **NG9.** Third-party tracking, analytics, ad scripts. Ever.

## Defaults in force

- **Server:** herold. v1 does not target other JMAP servers. The capability-driven approach in `notes/server-contract.md` keeps "support another server" tractable but not free.
- **Protocol surface:** RFC 8620 (Core), RFC 8621 (Mail), RFC 9007 (Sieve scripts for filters), EventSource push from RFC 8620 §7. WebSocket subprotocol (RFC 8887) is **not** v1; revisit if SSE proves inadequate under load.
- **Browser support:** Chromium 120+, Firefox 120+, Safari 17+. Older browsers explicitly unsupported.
- **Viewport target:** ≥1280 px primary; layout below 768 px is best-effort.
- **Auth:** Bearer token over HTTPS. Token sourcing TBD (interactive form vs OIDC redirect via herold) — see `notes/open-questions.md`.
- **Capture-driven requirements:** sections covering mail basics, keyboard priorities, workflows, and performance budgets are populated from gmail-logger output per `notes/capture-integration.md`. Until that data lands, those sections carry placeholders rather than guesses.
- **Visual style:** dark default, light theme switchable. IBM Plex Sans / IBM Plex Mono so we stay visually consistent with the gmail-logger popup during prototyping.

## Open scope items

See `notes/open-questions.md`. Items there block specific requirements; resolving each typically updates one section of this tree.
