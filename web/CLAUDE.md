# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository purpose

Two artifacts that feed each other:

- `gmail-logger/` — Chrome MV3 extension that records the user's Gmail UI interactions to a local event log. Runs against `https://mail.google.com/*` only.
- `docs/` — Requirements, architecture, and implementation specs for **tabard**, a JMAP web mail client. Several requirement sections are explicit placeholders awaiting capture data exported from the logger (search the tree for `⚠ PLACEHOLDER`). Start at `docs/00-scope.md` and `README.md`.

The intended workflow: run the logger for several days → export `gmail-analysis-*.json` from the popup → use those numbers to fill in the placeholder sections per `docs/notes/capture-integration.md` (top actions → `requirements/02-mail-basics.md` plus feature-area docs, workflow bigrams → `requirements/12-workflows.md`, keyboard-vs-click ratio → `requirements/10-keyboard.md`, views visited → confirms `requirements/01-data-model.md` view list).

## Working on the extension

There is no build system, package manager, or test suite. Files are loaded directly by Chrome.

To try changes:
1. `chrome://extensions` → enable Developer mode → **Load unpacked** → select `gmail-logger/`.
2. After editing `content.js` or `manifest.json`, click the extension's reload icon and refresh the Gmail tab.
3. After editing `background.js` or `popup.js`, also reload the extension (the service worker is cached).
4. Check the popup or `chrome.storage.local` (DevTools → Application → Storage) to verify events.

## Extension architecture

Three pieces communicate via `chrome.storage.local` and `chrome.runtime` messages:

- **`content.js`** — injected into Gmail. Owns all event capture. Builds events from three sources: a click handler that matches against `ACTION_MAP` (selectors keyed on `data-tooltip` / `aria-label`, since Gmail's class names are obfuscated), a keydown handler driven by `KEY_MAP` (with a `g…` two-key sequence buffer for navigation shortcuts), and a hash/`pushState` watcher emitting `view_change`. Events are buffered in memory and flushed to `chrome.storage.local` every 2s or on `visibilitychange`/`beforeunload`. The store is capped at `MAX_EVENTS = 5000` (oldest dropped). `SESSION_ID` is per page load.
- **`background.js`** — service worker. Only role is to answer `GET_EVENTS` / `CLEAR_EVENTS` messages from the popup.
- **`popup.html` + `popup.js`** — popup UI and logic. The script is a separate file (MV3 CSP forbids inline `<script>`). Reads events via the background worker, renders top-actions / recent list, and produces two downloads: raw JSON and an analysis report (top actions, action-pair bigrams, keyboard/click ratio, views visited).

### Privacy invariant

The logger is deliberately content-blind. Do not log subjects, addresses, body text, label names, or full search queries. Search input captures only `query_length`. Preserve this when adding new event types — the spec's value depends on the user being willing to run the logger on real mail.

### Adding new logged actions

- Click-driven: append to `ACTION_MAP` in `content.js`. Prefer `data-tooltip` selectors; fall back to `aria-label`. The taxonomy in `gmail-logger/README.md` is the canonical list — keep it in sync.
- Keyboard-driven: add to `KEY_MAP`. Two-key sequences are only wired for the `g…` prefix; extending to other prefixes requires touching the `keySeq` logic.
- New view types: add a branch in `parseGmailView` so `view_change` events stay meaningful.

## Working on tabard's specs

`docs/` is the source of truth for what tabard must do. Layout: `00-scope.md` (goals, non-goals, defaults) → `requirements/` (numbered REQs by area) → `architecture/` (decisions) → `implementation/` (phasing, testing, cuts) → `notes/` (capture integration, server contract, open questions).

When filling in placeholder sections from capture data, `docs/notes/capture-integration.md` describes the mapping. `docs/00-scope.md` lists hard exclusions (Calendar, Meet, Drive previews, AI features, mobile-native, offline) — proposals that touch those areas should be pushed back unless the user explicitly expands scope. The server contract (`docs/notes/server-contract.md`) is the single place where "this is herold's job" claims live; new such claims belong there, not scattered in feature requirements.
