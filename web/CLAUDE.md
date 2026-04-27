# CLAUDE.md (web/)

Working agreement for Claude Code agents touching anything under `web/`.

## What this directory is

`web/` is the herold web workspace: a pnpm monorepo containing the
SPAs herold serves at runtime.

- `web/apps/suite/` — end-user mail / calendar / contacts / chat
  SPA. Mounted at `/` on herold's public listener.
- `web/apps/admin/` — operator admin SPA (Phase 2 of the merge plan;
  does not exist yet). Mounted at `/admin/` on herold's admin
  listener once Phase 2 lands.
- `web/packages/design-system/` — shared design tokens (Carbon-derived
  colour / typography / spacing / motion) and base CSS, consumed by
  both apps.

The matching design docs live at `docs/design/web/`. The folded-in
ADR is `docs/design/web/notes/adr-0001-merge-tabard-and-rewrite-admin-ui.md`.
Pre-import history is preserved in git via the subtree merge; commits
authored before 2026-04-27 still use the historical "tabard" name in
their messages.

## Build, test, run

The repo-root `Makefile` is the source of truth:

- `make build-web` runs `pnpm -C web install --frozen-lockfile` then
  `pnpm -C web build`, producing `web/apps/{admin,suite}/dist/`. A
  follow-up step (`scripts/build-web.sh`, see plan section 5) copies
  the dists into `internal/webspa/dist/{admin,suite}/`.
- `make build` runs `make build-web && make build-server`, producing
  the full single-binary release artifact with the SPAs embedded.
- `make build-server` runs `go build` with `-tags nofrontend` (no
  pnpm dependency; binary serves a placeholder at `/` and `/admin/`).
- `make test-web` runs vitest + Playwright. `make test-server` runs
  `go test -tags nofrontend ./...`. `make test` runs both.

Dev loop, web-only:

```bash
pnpm -C web dev --filter @herold/suite   # or --filter @herold/admin
```

The suite Vite config (`web/apps/suite/vite.config.ts`) proxies the
herold backend paths (`/api`, `/jmap`, `/.well-known/jmap`,
`/chat/ws`, `/login`, `/oidc`, `/proxy`) at `http://localhost:5173`
so cookies attach to JMAP / chat-WS / login requests as if served
from the same origin. Override `HEROLD_URL` to point at a herold
instance other than `http://localhost:8080`.

## House rules

- No build pipeline state checked in to `web/apps/*/dist/`. Those are
  produced by `make build-web` and copied into `internal/webspa/dist/`
  for embedding.
- The `nofrontend` build tag must keep working. Anything that imports
  `internal/webspa` must compile against either `embed_default.go` or
  `embed_stub.go` (`go test -tags nofrontend ./...` is a CI lane).
- npm package namespace is `@herold/*`. Workspace packages must use
  the `workspace:*` protocol, never floating versions.
- The suite is content-blind on the wire — it never sends or stores
  message bodies, addresses, or search queries unencrypted to anything
  other than the same-origin herold backend.
- Same-origin deployment is the production posture
  (`docs/design/web/00-scope.md` defaults,
  `docs/design/web/architecture/01-system-overview.md` § Bootstrap).
  Cross-origin deployment is not supported.

## Brand

The product is "herold". User-facing strings (HTML titles, page
headers, web manifest `name`) say "Herold". The directory
`web/apps/suite/` is named for content (the suite of consumer
apps), not a brand. The pre-import "tabard" name survives only in
git history, in the merge plan filename, and in the ADR filename;
new code does not introduce it.
