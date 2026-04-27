# @herold/suite

The end-user SPA shell: hosts mail, chat, and (eventually) calendar
/ contacts as routes within a single bundle. Mounted at `/` on
herold's public listener.

## Local development

The suite assumes a locally running herold backend. The Vite dev
server proxies every herold endpoint the suite touches (JMAP,
EventSource, chat WebSocket, login, OIDC callbacks, image proxy,
send API) so the browser sees everything as coming from
`http://localhost:5173`. This emulates the same-origin production
deployment.

### Run

```bash
# from the repo root
pnpm -C web install                       # once, after a fresh clone
pnpm -C web dev --filter @herold/suite    # starts Vite on :5173
```

By default the proxy points at `http://localhost:8080`. Override:

```bash
HEROLD_URL=http://localhost:9090 pnpm -C web dev --filter @herold/suite
```

### Make sure herold is running

JMAP and auth requests will all 502 (bad gateway) if the herold
backend isn't up. Start it first from the repo root:

```bash
go run ./cmd/herold
```

For dev, a single HTTP listener is fine — TLS is the production
concern.

### Production deployment shape

Per herold's spec rev 9 (`REQ-DEPLOY-COLOC-01..05`,
`REQ-OPS-ADMIN-LISTENER-01..03`):

- Herold embeds this SPA bundle as static assets — one binary
  deploys the suite.
- **Public listener** (default `0.0.0.0:443`): SPA bundle, JMAP,
  chat WS, send / call APIs, webhooks, image proxy, login. This is
  what user browsers reach.
- **Admin listener** (default `127.0.0.1:9443`, loopback): admin
  REST + operator UI + `/metrics` + `/healthz/*`. Operator-only.

The suite never touches the admin listener. The Vite dev proxy
points at the public listener; herold's admin REST endpoints are
out of scope for the suite.

### Cookies on localhost

Herold sets a session cookie on successful login. For the cookie
to apply on `localhost:5173`, herold should NOT set `Domain=` on
the cookie in dev mode (or set `Domain=localhost`). `HttpOnly`,
`Secure`-only-in-prod, `SameSite=Lax` (or Strict) is the typical
shape; the dev cookie can drop `Secure`.

### Building for production

```bash
pnpm -C web --filter @herold/suite build
```

Output: `web/apps/suite/dist/`. `scripts/build-web.sh` copies it
into `internal/webspa/dist/suite/` so the next `go build` embeds
the result. In production herold serves it at `/` on the public
listener alongside the JMAP and other API surfaces (same origin).

## Layout

```
src/
├── App.svelte           top-level component; mounts Shell and current route
├── main.ts              Svelte mount entry
├── app.css              imports @herold/design-system/index.css
└── lib/
    ├── icons/           inline-SVG icon components (placeholder until a real icon set is picked)
    └── shell/           suite-shell layout primitives (Shell, Rail, GlobalBar, CoachStrip)
```
