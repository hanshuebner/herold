# @herold/admin

Svelte 5 admin SPA for the Herold operator interface.

This app is Phase 2 of the merge plan documented at
`docs/design/server/notes/plan-tabard-merge-and-admin-rewrite.md`.
It will reach feature parity with `internal/protoui` (the server-rendered
HTMX admin UI) over a series of follow-up commits, then replace it
entirely in Phase 3.

## Dev loop

Start a local herold instance first (defaults to port 8080), then:

```
pnpm --filter @herold/admin dev
```

The Vite dev server runs on port 5174. It proxies `/api`, `/login`,
`/logout`, and `/oidc` to `http://localhost:8080` so cookies attach as
if served from the same origin. Set `HEROLD_URL` to point at a
different herold instance.

To run from the workspace root:

```
pnpm --dir web --filter @herold/admin dev
```

## Production embed flow

1. `make build-web` runs `pnpm --dir web install --frozen-lockfile` and
   then builds both apps.
2. The suite build output (`web/apps/suite/dist/`) is copied to
   `internal/webspa/dist/suite/`.
3. The admin build output (`web/apps/admin/dist/`) is copied to
   `internal/webspa/dist/admin/`.
4. A subsequent `go build ./cmd/herold` (without `-tags nofrontend`) bakes
   both SPA dists into the binary via the `//go:embed dist` directive in
   `internal/webspa/embed_default.go`.

The placeholder `index.html` files under `internal/webspa/dist/` are
committed to source control so the `//go:embed` directive resolves on a
fresh checkout without running `make build-web`. They are overwritten by
the build script and are not served in production builds.

## Parity target

The parity target is `internal/protoui`. See the audit at
`docs/design/server/notes/phase-2-protoui-protoadmin-coverage-audit-2026-04-27.md`
for the full page-by-page breakdown of which `/api/v1/...` endpoints each
admin page consumes.

Pages currently implemented:
- Login (`/login`) -- full-bleed form targeting `POST /api/v1/auth/login`
- Dashboard (`/dashboard`) -- placeholder stub; real implementation deferred

Pages deferred to follow-up commits:
- Principals list + detail (`/principals`)
- Domains list + detail + alias CRUD (`/domains`)
- Queue inspector (`/queue`)
- Audit log (`/audit`)
- Email research
- OIDC link/unlink, 2FA enroll/confirm/disable, API keys, password change

## Type checking

```
pnpm --filter @herold/admin check
```
