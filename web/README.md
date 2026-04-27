# Herold web workspace

This is the web/ workspace inside the herold monorepo: a pnpm
workspace that builds the SPAs herold serves at runtime.

## Apps

- **`apps/suite/`** — end-user mail / calendar / contacts / chat
  client. Mounted at `/` on herold's public listener.
- **`apps/admin/`** — operator admin SPA. Phase 2 of the merge plan;
  does not exist yet. Mounted at `/admin/` once it lands. Until
  then the working operator UI is `internal/protoui` at `/ui/`.

## Packages

- **`packages/design-system/`** — shared design tokens (Carbon-derived
  colour / typography / spacing / motion tokens) and base CSS,
  consumed by both apps.

## Defaults in force

- **Single user, single account.** No delegation, no shared
  mailboxes, no multi-account UI.
- **Online-only at v1.** Graceful degradation if the connection
  drops; no service-worker cache, no IndexedDB outbox.
- **Server: same-origin herold.** Both ends ship in one binary. The
  JMAP capability set the suite expects herold to advertise lives
  in [docs/design/web/notes/server-contract.md](../docs/design/web/notes/server-contract.md).
- **Protocol:** JMAP — RFC 8620 (Core), RFC 8621 (Mail), RFC 9007
  (Sieve for filters), EventSource push (RFC 8620 §7). WebSocket
  subprotocol (RFC 8887) deferred.
- **Browser support:** Chromium 120+, Firefox 120+, Safari 17+.
- **Viewport target:** ≥1280 px primary; below 768 px is
  best-effort.
- **Keyboard-heavy.** Shortcut priorities calibrated against gmail
  capture data (see `docs/design/web/notes/capture-integration.md`).

## Layout

```
web/
├── README.md              this file
├── CLAUDE.md              working agreement for Claude Code agents
├── package.json           workspace root
├── pnpm-workspace.yaml
├── pnpm-lock.yaml
├── tsconfig.base.json
├── apps/
│   ├── suite/             end-user JMAP suite SPA
│   └── admin/             operator admin SPA (Phase 2; placeholder today)
└── packages/
    └── design-system/     suite-wide design tokens and base styles
```

Design docs (requirements, architecture, implementation, ADRs) live
at `docs/design/web/` at the repo root, mirroring the layout of
`docs/design/server/` for the Go backend.

## Build, test, run

The repo-root `Makefile` is the source of truth. From the herold
root:

- `make build-web` — `pnpm -C web install --frozen-lockfile` then
  `pnpm -C web build`. Output: `web/apps/{admin,suite}/dist/`.
  `scripts/build-web.sh` then copies the dists into
  `internal/webspa/dist/{admin,suite}/`.
- `make build` — `make build-web && make build-server`. Full
  release artifact with the SPAs embedded.
- `make build-server` — `go build -tags nofrontend ./cmd/herold`.
  No pnpm dependency; binary serves a tiny placeholder at `/` and
  `/admin/`.
- `make test-web` — vitest + Playwright.
- `make test` — Go + web tests + integration smoke.

Web-only dev loop:

```bash
pnpm -C web dev --filter @herold/suite      # or --filter @herold/admin
```

The suite Vite config proxies herold backend paths so the browser
sees everything as same-origin at `http://localhost:5173`. Override
`HEROLD_URL` to point at a herold instance other than
`http://localhost:8080`.

## Status

Pre-launch. The design docs at `docs/design/web/` are the
authoritative spec. Capture-driven requirements are still being
filled in from gmail-logger output. Not feature-frozen.
