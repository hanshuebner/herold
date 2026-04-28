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
  follow-up step (`scripts/build-web.sh`) copies the dists into
  `internal/webspa/dist/{admin,suite}/`.
- `make build` runs `make build-web && make build-server`, producing
  the full single-binary release artifact with the SPAs embedded.
- `make build-server` runs `go build ./cmd/herold` (no `-tags
  nofrontend`). It depends on `prep-web`, which copies the tracked
  placeholders from `internal/webspa/placeholder/` into
  `internal/webspa/dist/` if and only if `dist/` is empty -- so a
  fresh checkout that has not run pnpm yet still compiles, and the
  resulting binary serves placeholder index.html shells.
- `make build-server -tags nofrontend` (or directly,
  `go build -tags nofrontend ./cmd/herold`) compiles the backend-
  only variant with no embedded web assets at all.
- `make test-web` runs vitest + Playwright. `make test-server` runs
  `go test -race ./...` (depends on `prep-web` for the same reason
  as `build-server`). `make test` runs both.

Dev loop, web-only:

```bash
pnpm -C web dev --filter @herold/suite   # or --filter @herold/admin
```

## Admin SPA e2e tests

The admin SPA has a Playwright e2e suite under
`web/apps/admin/tests/e2e/`. It uses `page.route()` for browser-level
request interception (no stub server needed; no Vite proxy restarts
between tests).

Run the full suite (chromium, the default CI lane):

```bash
pnpm --filter @herold/admin test:e2e
```

Install browsers if not present:

```bash
pnpm --filter @herold/admin test:e2e:install
```

Run a single spec file during development:

```bash
pnpm --filter @herold/admin exec playwright test tests/e2e/auth.spec.ts
```

Firefox and WebKit are on-demand only (they are not installed by
`test:e2e:install`). To run them:

```bash
pnpm --filter @herold/admin exec playwright install firefox webkit
pnpm --filter @herold/admin test:e2e:all
```

The suite Vite config (`web/apps/suite/vite.config.ts`) proxies the
herold backend paths (`/api`, `/jmap`, `/.well-known/jmap`,
`/chat/ws`, `/login`, `/oidc`, `/proxy`) at `http://localhost:5173`
so cookies attach to JMAP / chat-WS / login requests as if served
from the same origin. Override `HEROLD_URL` to point at a herold
instance other than `http://localhost:8080`.

## House rules

- No build pipeline state checked in to `web/apps/*/dist/` or
  `internal/webspa/dist/{admin,suite}/`. Both trees are gitignored
  build output. The Vite-produced artefacts live in
  `web/apps/*/dist/`; `scripts/build-web.sh` mirrors them to
  `internal/webspa/dist/` for the `//go:embed` directive. Placeholder
  source (used by `make prep-web` when no real build has run) lives
  under `internal/webspa/placeholder/`.
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

## Patterns to avoid

- **`$effect` reading and writing the same `$state` cell loops.** A
  Svelte 5 `$effect` registers everything it reads as a dependency.
  If the body then writes one of those reads — directly or transitively
  via a store action that mutates `$state` — the effect re-runs, writes
  again, re-runs, ad infinitum. We hit this three times this session
  (`MailView` inbox load, `ThreadReader` thread load, `App.svelte`
  mailbox prime). The fix is `untrack(() => ...)` around the side-effect:

  ```ts
  $effect(() => {
    if (auth.status === 'ready') {
      untrack(() => {
        if (mail.mailboxes.size === 0) void mail.loadMailboxes();
      });
    }
  });
  ```

  Whenever a route or auth-state effect kicks off async work that
  eventually writes back into the store, wrap the write in `untrack`.
  The effect's intended deps (`auth.status`, the route prop) stay
  tracked; the side-effect's reads do not.

- **Idempotent `loadFoo` cells must serve fresh state when stale.**
  Caching by status (`'ready'`) is fine for the original load, but
  pair it with a refresh path that bypasses the cache (e.g.
  `refreshThread`). Sync handlers should call the refresh, not the
  load, so cached views update without a route remount.

## Suite test stack

`web/apps/suite/` ships vitest + happy-dom + `@testing-library/svelte`
+ `@testing-library/jest-dom`. New code must land with tests:

- Pure helpers (formatters, parsers, validators): plain vitest.
- State stores (`*.svelte.ts`): test their public surface, mock
  singleton dependencies with `vi.mock`. Prefer extracting pure
  helpers and exporting a small `_internals_forTest` namespace over
  driving the full singleton.
- Components: render via `@testing-library/svelte`, assert with the
  jest-dom matchers (`toBeInTheDocument`, `toHaveAttribute`, ...).
- The `test` script is `vitest run`; `test:watch` for development;
  `test:coverage` for v8 coverage. CI runs `test` automatically on
  every PR via the existing `pnpm --dir web run test` lane.

## Brand

The product is "herold". User-facing strings (HTML titles, page
headers, web manifest `name`) say "Herold". The directory
`web/apps/suite/` is named for content (the suite of consumer
apps), not a brand. The pre-import "tabard" name survives only in
git history, in the merge plan filename, and in the ADR filename;
new code does not introduce it.
