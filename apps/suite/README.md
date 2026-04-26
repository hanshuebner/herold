# tabard-suite

The tabard SPA shell: hosts mail, chat, and (eventually) calendar / contacts as routes within a single bundle.

## Local development

Tabard assumes a locally running [herold](../../../herold) instance. The Vite dev server proxies every herold endpoint tabard touches (JMAP, EventSource, chat WebSocket, login, OIDC callbacks, image proxy, send API) so the browser sees everything as coming from `http://localhost:5173`. This emulates the same-origin production deployment.

### Run

```bash
# from the repo root
pnpm install                              # once, after a fresh clone
pnpm dev                                  # starts Vite on :5173
```

By default the proxy points at `http://localhost:8080`. Override:

```bash
HEROLD_URL=http://localhost:9090 pnpm dev
```

### Make sure herold is running

Tabard's auth and JMAP requests will all 502 (bad gateway) if herold isn't up. Start herold first:

```bash
# from /Users/hans/herold
go run ./cmd/herold
```

For dev, a single HTTP listener is fine — TLS is the production concern.

### Production deployment shape

Per herold's spec rev 9 (`REQ-DEPLOY-COLOC-01..05`, `REQ-OPS-ADMIN-LISTENER-01..03`):

- Herold ships tabard's static bundle as embedded assets — one binary deploys the suite.
- **Public listener** (default `0.0.0.0:443`): SPA bundle, JMAP, chat WS, send / call APIs, webhooks, image proxy, login. This is what user browsers reach.
- **Admin listener** (default `127.0.0.1:9443`, loopback): admin REST + admin UI + `/metrics` + `/healthz/*`. Operator-only.

Tabard never touches the admin listener. The Vite dev proxy points at the public listener; herold's admin REST endpoints are out of scope for tabard.

### Cookies on localhost

Herold sets a session cookie on successful login. For the cookie to apply on `localhost:5173`, herold should NOT set `Domain=` on the cookie in dev mode (or set `Domain=localhost`). `HttpOnly`, `Secure`-only-in-prod, `SameSite=Lax` (or Strict) is the typical shape; the dev cookie can drop `Secure`.


### Building for production

```bash
pnpm --filter @tabard/suite build
```

Output: `apps/suite/dist/`. In production this is served by herold under its `/` route alongside the JMAP and other API surfaces (same origin).

## Layout

```
src/
├── App.svelte           top-level component; mounts Shell and current route
├── main.ts              Svelte mount entry
├── app.css              imports @tabard/design-system/index.css
└── lib/
    ├── icons/           inline-SVG icon components (placeholder until a real icon set is picked)
    └── shell/           suite-shell layout primitives (Shell, Rail, GlobalBar, CoachStrip)
```
