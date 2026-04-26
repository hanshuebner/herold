# Tabard — design baseline

A planned web suite over JMAP, comprising three sibling apps sharing one backend (herold), one design system, and one auth flow:

- **tabard-mail** — email client. v1 in flight; this docs/ tree currently describes it.
- **tabard-calendar** — calendar (future; over JMAP for Calendars, RFC 8984 + binding draft).
- **tabard-contacts** — contacts (future; over JMAP for Contacts, RFC 9553 + binding draft).

Repository layout will be a monorepo (`apps/`, `packages/`); the split lands when the second app starts. Until then, the existing `docs/` tree at root is tabard-mail's spec.

## How to read this

1. **[docs/00-scope.md](docs/00-scope.md)** — goals, non-goals, defaults. Read this first.
2. **docs/requirements/** — what the client must do, grouped by area. Numbered requirements (`REQ-XXX-nn`) so we can reference them in discussion.
3. **docs/architecture/** — how the client is shaped. Decisions, not code.
4. **docs/implementation/** — language/runtime choices, phasing, testing, deliberate cuts.
5. **docs/notes/** — reference material, capture-integration guide, server contract, open questions.

## Defaults in force

Working assumptions. Override by editing `docs/00-scope.md`; affected docs will be revised.

- **Single user, single account.** No delegation, no shared mailboxes, no multi-account UI.
- **Online-only at v1.** Graceful degradation if the connection drops; no service-worker cache, no IndexedDB outbox.
- **Server: herold.** Both ends are ours. The JMAP capability set tabard expects herold to advertise lives in [docs/notes/server-contract.md](docs/notes/server-contract.md).
- **Protocol:** JMAP — RFC 8620 (Core), RFC 8621 (Mail), RFC 9007 (Sieve for filters), EventSource push (RFC 8620 §7). WebSocket subprotocol (RFC 8887) deferred.
- **Browser support:** Chromium 120+, Firefox 120+, Safari 17+.
- **Viewport target:** ≥1280 px primary; below 768 px is best-effort.
- **Keyboard-heavy.** Shortcut priorities calibrated from gmail-logger capture data.
- **Capture-driven requirements.** Several requirement sections are placeholders that grow from `gmail-analysis-*.json` exports. See [docs/notes/capture-integration.md](docs/notes/capture-integration.md).

## Directory

```
tabard/
├── README.md                       this file
├── CLAUDE.md                       working agreement for Claude Code agents
├── gmail-logger/                   Chrome MV3 extension capturing real Gmail usage
└── docs/
    ├── 00-scope.md                 goals, non-goals, defaults
    ├── requirements/
    │   ├── 01-data-model.md        Gmail ↔ JMAP mapping + method shapes
    │   ├── 02-mail-basics.md       read/compose/reply/forward/star/archive/delete
    │   ├── 03-labels.md            label CRUD, apply/remove, label views
    │   ├── 04-filters.md           Sieve-backed filters (RFC 9007)
    │   ├── 05-categorisation.md    user-defined category groupings
    │   ├── 06-snooze.md            defer-and-resurface
    │   ├── 07-search.md            full-text and fielded search
    │   ├── 08-chat.md              scope TBD
    │   ├── 09-ui-layout.md         three-panel layout, list, reading pane
    │   ├── 10-keyboard.md          shortcut bindings and priorities
    │   ├── 11-optimistic-ui.md     optimistic actions, undo, error revert
    │   ├── 12-workflows.md         end-to-end user workflows
    │   ├── 13-nonfunctional.md     perf, security, reliability, a11y, browsers
    │   ├── 14-unsubscribe.md       List-Unsubscribe (RFC 2369) + one-click (RFC 8058)
    │   ├── 15-calendar-invites.md  iMIP rendering and RSVP (RFC 5545 / 6047)
    │   ├── 16-mailing-lists.md     RFC 2369 List-* metadata and per-list affordances
    │   ├── 17-attachments.md       upload, render, suspicious-extension warnings
    │   ├── 18-authentication-results.md  SPF/DKIM/DMARC/ARC display (RFC 8601)
    │   ├── 19-drafts.md            auto-save, recovery, multi-device conflict
    │   ├── 20-settings.md          settings panel scope and storage
    │   ├── 21-video-calls.md       1:1 video calls (WebRTC, signaling, TURN)
    │   └── 22-internationalization.md  en/de/fr + regional variants; ICU; Intl
    ├── architecture/
    │   ├── 01-system-overview.md
    │   ├── 02-jmap-client.md
    │   ├── 03-sync-and-state.md
    │   ├── 04-rendering.md
    │   ├── 05-keyboard-engine.md
    │   ├── 06-design-system.md     suite-wide design language and components
    │   └── 07-chat-protocol.md     chat data model, ephemeral WebSocket, TURN
    ├── implementation/
    │   ├── 01-tech-stack.md
    │   ├── 02-phasing.md
    │   ├── 03-testing-strategy.md
    │   └── 04-simplifications-and-cuts.md
    └── notes/
        ├── gmail-feature-map.md    inventory of Gmail features (in/out of scope)
        ├── capture-integration.md  feeding logger output into requirements
        ├── server-contract.md      what tabard expects herold to deliver
        ├── herold-coverage.md      herold's commitment status against the contract
        └── open-questions.md
```

## Requirement ID convention

- `REQ-MODEL-nn` — data model (Gmail ↔ JMAP mapping)
- `REQ-MAIL-nn`  — basic mail actions (read/compose/reply/forward/star/archive/delete)
- `REQ-LBL-nn`   — labels
- `REQ-FLT-nn`   — filters
- `REQ-CAT-nn`   — categorisation
- `REQ-SNZ-nn`   — snooze
- `REQ-SRC-nn`   — search
- `REQ-CHAT-nn`  — chat
- `REQ-UI-nn`    — layout / list / reading pane
- `REQ-KEY-nn`   — keyboard
- `REQ-OPT-nn`   — optimistic UI / undo
- `REQ-WF-nn`    — workflows
- `REQ-PERF-nn`  — performance
- `REQ-SEC-nn`   — security
- `REQ-REL-nn`   — reliability and sync
- `REQ-A11Y-nn`  — accessibility
- `REQ-BR-nn`    — browser support
- `REQ-UNS-nn`   — unsubscribe (RFC 2369 / 8058)
- `REQ-CAL-nn`   — calendar invites in mail (iMIP)
- `REQ-LIST-nn`  — mailing lists
- `REQ-ATT-nn`   — attachments
- `REQ-AR-nn`    — authentication results (RFC 8601 display)
- `REQ-DFT-nn`   — drafts
- `REQ-SET-nn`   — settings
- `REQ-CALL-nn`  — video calls (1:1)
- `REQ-I18N-nn`  — internationalization

When cutting or adding, reference by ID.

## Status

Skeleton. Awaits enrichment from gmail-logger capture data. Not reviewed. Not frozen.
