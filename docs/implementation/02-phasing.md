# 02 — Phasing

How the work is sequenced. Not a schedule — a dependency order.

## Phase 0 — pre-implementation

Already in flight.

- gmail-logger captures real Gmail usage (5–7 working days).
- Capture data feeds the placeholder requirement sections per `../notes/capture-integration.md`.
- Tech-stack decision (`01-tech-stack.md`) is made.
- Auth flow (`../notes/open-questions.md` Q1) is decided with herold.
- Server contract (`../notes/server-contract.md`) is reconciled with herold's roadmap — anything tabard requires that herold doesn't yet ship is filed as a herold requirement.

Exit gate: every requirement doc has zero `⚠ PLACEHOLDER` markers, or the remaining placeholders are explicitly marked as v2.

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

## Phase 3 — server-gated features

- Snooze, contingent on herold shipping the server contract (`../requirements/06-snooze.md`).
- Filters, contingent on herold's Sieve (`../requirements/04-filters.md`).
- Image proxy integration (`../requirements/13-nonfunctional.md` REQ-SEC-07).
- Mailbox colour persistence.

Each feature in this phase has an independent gate: it ships when herold ships its server-side counterpart, not before and not bundled.

## Phase 4 — polish

- Settings panel (theme, density, Undo window, default From, signature, image-load defaults).
- Shortcut help overlay (`?`).
- Captured workflows beyond WF-01..05 (whatever capture turned up).
- Performance pass against `../requirements/13-nonfunctional.md` budgets.
- Accessibility pass against REQ-A11Y.

## Out-of-band: capture refresh

Re-run gmail-logger periodically (quarterly?) to catch behaviour drift. Update placeholder sections, re-prioritise keyboard bindings, re-evaluate cut features.
