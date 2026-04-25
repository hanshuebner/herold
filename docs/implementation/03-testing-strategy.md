# 03 — Testing strategy

Three layers, each with a clear scope.

## Unit tests

For pure functions:

- JMAP query/filter builders (per `../requirements/01-data-model.md` method shapes).
- HTML sanitiser (`../architecture/04-rendering.md`) — golden inputs from the OWASP XSS cheat sheet plus real adversarial mails caught in capture.
- Search-operator parser (`../requirements/07-search.md`).
- Keyboard engine — sequence buffer, keymap stack, focus carve-outs (`../architecture/05-keyboard-engine.md`).
- Reply / forward body composers (REQ-MAIL-30..32).
- Snooze time-preset calculator (REQ-SNZ-01..05) — clock-injectable.

Unit tests do not touch the network, the cache, or the DOM beyond JSDOM where unavoidable.

## Contract tests against JMAP

A test JMAP server (herold's `internal/testharness` once exposed, or a stubbed RFC 8621 server) drives end-to-end JMAP shape tests:

- Bootstrap → session descriptor parse → capability degrade if `:sieve` is absent.
- Inbox load → push event → `Email/changes` round-trip.
- Optimistic write → server response → cache reconcile.
- Push channel disconnect → polling fallback → reconnect → polling stops.
- `cannotCalculateChanges` → full re-fetch.

These tests run against a fresh server fixture per test; they're slower than unit but the only place we catch protocol-shape regressions.

## BDD acceptance scenarios

End-to-end, browser-driven (Playwright). One scenario per workflow in `../requirements/12-workflows.md`. Examples:

```gherkin
Scenario: Apply label to thread via keyboard (REQ-WF-02)
  Given the user is authenticated and the inbox is visible
  When the user presses "l", types "Pro", sees "ProjectX" highlighted, and presses Enter
  Then the thread shows the "ProjectX" label chip
  And exactly one Email/set request was sent with the ProjectX mailbox added to mailboxIds

Scenario: Snooze a thread to tomorrow morning (REQ-WF-03)
  Given the inbox contains a thread
  When the user selects the thread, presses "b", and chooses "Tomorrow morning"
  Then the thread disappears from the Inbox
  And a toast appears: "Snoozed until tomorrow, 8:00 AM"
  And the thread is in the Snoozed view with the wake time displayed

Scenario: Send with undo (REQ-WF-05)
  Given the compose window is open with a valid recipient and subject
  When the user presses Ctrl+Enter
  Then the compose closes and a toast "Message sent — Undo" appears for 5 seconds
  When the user clicks "Undo" within 5 seconds
  Then the compose re-opens with the message content intact
  And no EmailSubmission was committed at the server

Scenario: Full-text search (REQ-WF-04)
  Given the user presses "/"
  When the user types "invoice" and presses Enter
  Then the result list shows threads matching "invoice"
  And the URL reflects the search query

Scenario: Fielded search
  Given the user presses "/"
  When the user types "from:alice@example.com has:attachment" and presses Enter
  Then only threads from alice@example.com with attachments are shown
```

Acceptance scenarios MUST cover every `REQ-WF-nn`. New workflows added from capture data come with their scenario in the same PR as the requirement.

## Failure-mode tests

Per `../requirements/11-optimistic-ui.md`:

- `Email/set` fails with 500 → optimistic state reverts, toast appears, Retry replays the request.
- Network timeout → revert + "No server response" toast.
- Push event arrives ahead of an in-flight optimistic write → optimistic state is replaced, no toast.

These are not edge cases; they are first-class.

## What we don't test

- Browser UI rendering pixel-by-pixel. Visual-regression suites are out for v1.
- Performance budgets in CI; we measure them on demand against a representative dataset (`../requirements/13-nonfunctional.md` REQ-PERF-01..05).
- Accessibility automated audits beyond axe-core in the Playwright runner (manual screen-reader review covers the remainder).
