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
| `02-mail-basics` — reading-pane HTML render + inline images | presentation | todo | — |
| `02-mail-basics` — emoji reactions (`Email.reactions`) | protocol | n/a | — |
| `03-labels` — label CRUD, apply/remove | protocol | n/a | — |
| `03-labels` — sidebar label tree UI | presentation | todo | — |
| `04-filters` — Sieve filter model | protocol | n/a | — |
| `04-filters` — filter editor UI | presentation | todo | — |
| `05-categorisation` — `$category-*` keywords | protocol | n/a | — |
| `05-categorisation` — category chips / display | presentation | todo | — |
| `06-snooze` — snooze data model | protocol | n/a | — |
| `06-snooze` — snooze picker UI | presentation | todo | — |
| `07-search` — JMAP `Email/query` + FTS | protocol | n/a | — |
| `07-search` — search UI + suggestions | presentation | todo | — |
| `11-optimistic-ui` — optimistic action semantics | presentation | todo | — |
| `14-unsubscribe` — List-Unsubscribe handling | presentation | todo | — |
| `17-attachments` — inline-vs-attach (suite G8) | presentation | todo | — |
| `19-drafts` — draft model | protocol | n/a | — |
| `19-drafts` — compose UI | presentation | todo | — |
| `20-settings` — settings model | protocol | n/a | — |
| `20-settings` — settings UI | presentation | todo | — |
| `25-push-notifications` — enriched push payload | protocol | n/a | — |
| `25-push-notifications` — notification presentation (FCM) | presentation | todo | — |
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
| Bearer-token auth + biometric unlock | REQ-AND-01x | todo |
| Full offline local store + outbox | REQ-AND-02x | todo |
| FCM notifications (direct-reply, shortcuts, Bubbles) | REQ-AND-03x | todo |
| System integration (share, widgets, tiles, SAF) | REQ-AND-04x | todo |
| Native navigation shell + predictive back | REQ-AND-05x | todo |
