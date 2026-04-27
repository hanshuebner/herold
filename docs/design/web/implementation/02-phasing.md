# 02 — Phasing

How the work is sequenced. Not a schedule — a dependency order.

## Phase 0 — pre-implementation (complete)

- gmail-logger captures real Gmail usage (5–7 working days).
- Capture data feeds the placeholder requirement sections per `../notes/capture-integration.md`.
- Tech-stack decision (`01-tech-stack.md`) is made.
- Auth flow (`../notes/open-questions.md` R1) is decided.
- Server contract (`../notes/server-contract.md`) is finalised; herold provides the matching capabilities (`../notes/herold-coverage.md`).

Status: implementation has begun (`apps/suite` exists, design system loaded, suite shell layout primitives in place).

## Phase 1 — skeleton

Goal: a tabard that can read mail.

- Bootstrap, session descriptor, capability check.
- Cache + in-memory store for `Mailbox`, `Email`, `Thread`, `Identity`.
- Three-pane layout (`../requirements/09-ui-layout.md` REQ-UI-01..03).
- Inbox thread list with virtualised scroll (REQ-UI-10..16, REQ-PERF-05).
- Reading pane with HTML iframe sandbox (REQ-UI-20..25, `../architecture/04-rendering.md`).
- Keyboard engine with the P0 single-key bindings (`../requirements/10-keyboard.md`).
- EventSource push + `Foo/changes` reconciliation (`../architecture/03-sync-and-state.md`).
- Optimistic actions: archive, mark-read/unread (REQ-OPT-01).

Exit gate: the user can open the client, read their inbox, and archive threads with `e`. The result `Email/changes` after a manual mutation in another client correctly fans out to tabard.

## Phase 2 — core actions

- Compose, reply, reply-all, forward (`../requirements/02-mail-basics.md` REQ-MAIL-10..33).
- Send + Undo (`../requirements/11-optimistic-ui.md` REQ-OPT-10..12).
- Star, delete, label apply/remove (REQ-MAIL-50..53, `../requirements/03-labels.md`).
- Label CRUD (REQ-LBL-01..07).
- Search — basic + fielded (`../requirements/07-search.md`).
- Two-key navigation sequences (`g i`, `g s`, …).

Exit gate: the user can run their day from tabard. WF-01 (inbox-zero pass) and WF-05 (compose and send) work end-to-end.

## Phase 3 — extended mail features

Tabard-side work that sits on top of phase 2 once core mail is fluent.

- Snooze (`../requirements/06-snooze.md`).
- Filters (`../requirements/04-filters.md`) — the user-facing UI on top of `Sieve/set`.
- Image proxy integration (`../requirements/13-nonfunctional.md` REQ-SEC-07).
- Mailbox colour persistence (`../requirements/03-labels.md` REQ-LBL-04).
- Email reactions (`../requirements/02-mail-basics.md` § Reactions).
- Drafts deep features — multi-device conflict resolution (`../requirements/19-drafts.md` REQ-DFT-30..33).
- Categorisation (`../requirements/05-categorisation.md`) — tabs UI on top of herold's classifier.

Herold provides the substrate for all of these; the work is purely tabard-side.

## Phase 4 — polish

- Settings panel (theme, density, Undo window, default From, signature, image-load defaults).
- Shortcut help overlay (`?`).
- Captured workflows beyond WF-01..05 (whatever capture turned up).
- Performance pass against `../requirements/13-nonfunctional.md` budgets.
- Accessibility pass against REQ-A11Y.

## Out-of-band: capture refresh

Re-run gmail-logger periodically (quarterly?) to catch behaviour drift. Update placeholder sections, re-prioritise keyboard bindings, re-evaluate cut features.

## Local-dev assumption

A locally running herold is the assumed substrate during tabard development and manual testing. Tabard's Vite dev server proxies JMAP / EventSource / WebSocket / login / image-proxy requests to that herold (default `http://localhost:8080`; override via `HEROLD_URL` env var). See `apps/suite/README.md`.
