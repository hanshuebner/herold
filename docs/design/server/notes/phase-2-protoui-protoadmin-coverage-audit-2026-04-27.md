# Phase 2 audit: protoui flows vs protoadmin REST coverage

Reconnaissance pass for the merge plan's Phase 2 (Svelte admin SPA at
`web/apps/admin/` to parity with `internal/protoui`). Captured
2026-04-27 by the root agent before dispatching `web-frontend-implementor`
and `http-api-implementor` to land the SPA + the wire-side gap.

The SPA consumes only `/api/v1/...` -- it does NOT reach into the
store / directory / OIDC RP directly the way `internal/protoui` does.
Every protoui flow must therefore have a matching protoadmin endpoint,
or the gap must be filled before the SPA page that needs it ships.

## Pages in dependency order (plan section 5.5)

### 1. Login + session establishment

**Status: real gap.** Protoadmin has no JSON login endpoint. The
admin listener already issues a `herold_admin_session` cookie via the
HTML `/login` flow handled by `internal/protoui/auth_handlers.go` +
`internal/admin/auth_handlers.go`, but:

- The cookie's `Path` is `/ui/` for the admin listener
  (`internal/protoui/session.go:142-147`), so a browser at `/admin/`
  will not send it on either `/admin/...` SPA requests or `/api/v1/...`
  REST calls.
- `protoadmin.requireAuth` only accepts `Authorization: Bearer hk_...`
  API keys (`internal/protoadmin/auth.go:45-72`); the existing
  `TODO(phase2): accept session-cookie tokens here once protoadmin
  ships the UI` (line 43) explicitly punts session cookies to Phase 2.
- There is no JSON `POST /api/v1/auth/login` that an SPA can target.
- TOTP step-up (REQ-AUTH-SCOPE-03) is woven through the HTML form
  flow in `internal/protoui/auth_handlers.go`; it has no REST shape.

**Fill action (http-api-implementor):**
1. New `POST /api/v1/auth/login` accepting `{email, password,
   totp_code?}`, returning `{principal_id, scopes[]}` plus issuing
   the same `herold_admin_session` cookie the HTML flow issues. When
   the principal has TOTP enabled and `totp_code` is missing or
   wrong, return `{"step_up_required": true}` 401.
2. New `POST /api/v1/auth/logout` clearing the cookie.
3. Broaden the admin-listener cookie `Path` from `/ui/` to `/` so
   the same cookie covers `/api/v1/`, `/admin/`, and the legacy
   `/ui/` mount during the dual-mount window.
4. Teach `protoadmin.requireAuth` to accept the session cookie in
   addition to `Bearer hk_...`. The cookie carries the principal
   ID + scopes via the existing `protoui.session` envelope; reuse
   the verification path. When auth happens via cookie, also
   require an `X-CSRF-Token` header that matches the
   `herold_admin_csrf` cookie value (REQ-AUTH-CSRF; verify with
   `crypto/subtle`).
5. Issue the CSRF token alongside the session cookie at login;
   make it available to the SPA via the non-HttpOnly
   `herold_admin_csrf` cookie that already exists
   (`internal/protoui/session.go:181-189`).

This is security-sensitive. `security-reviewer` MUST sign off.

### 2. Dashboard (read-only stats)

**Covered.** `GET /api/v1/queue/stats` + `GET /api/v1/audit` (limit=10,
default sort) + `GET /api/v1/domains` give the same data the
`internal/protoui/dashboard.go` page composes.

No fill action needed. The SPA aggregates with three parallel
fetches.

### 3. Principals list + detail (CRUD)

**Covered.** `GET /api/v1/principals`, `POST /api/v1/principals`,
`GET /api/v1/principals/{pid}`, `PATCH /api/v1/principals/{pid}`,
`DELETE /api/v1/principals/{pid}`, `PUT /api/v1/principals/{pid}/password`.

Same scope split as protoui (admin for list/create/delete,
self-or-admin for read/update/password).

The detail page in protoui inlines API keys, OIDC links, TOTP
status; the SPA must fetch each via its dedicated subresource
endpoint (`/api/v1/principals/{pid}/api-keys`, `.../oidc-links`,
`.../totp/status`). Document this composition in the SPA brief.

Search: protoui filters in-memory; protoadmin has no search query
parameter. SPA does client-side filter on the full list (the data
volume is small enough -- typical operator deployments have
hundreds of principals, not millions).

### 4. Domains list + detail + alias CRUD

**Covered.** `GET /api/v1/domains`, `POST /api/v1/domains`,
`DELETE /api/v1/domains/{name}`, `GET /api/v1/aliases?domain=...`,
`POST /api/v1/aliases`, `DELETE /api/v1/aliases/{id}`.

Minor composition: there is no `GET /api/v1/domains/{name}` -- the
SPA composes the detail view from the list response + the
domain-filtered alias list.

### 5. Queue inspector (list / filter / retry / hold / release / delete)

**Fully covered.** Same filter vocabulary (state, principal_id,
after_id cursor, limit) at `/api/v1/queue`; per-item
`/api/v1/queue/{id}`, `.../retry`, `.../hold`, `.../release`,
`DELETE /api/v1/queue/{id}`. Bonus: `POST /api/v1/queue/flush`
for mass deferred->queued (no protoui equivalent).

Audit logged on every mutation (existing `appendAudit` calls in
`internal/protoadmin/queue.go`).

### 6. Audit log (read-only)

**Fully covered.** `GET /api/v1/audit` with action / principal_id /
since / until / after_id / limit filters. Same vocabulary.

### 7. Email research (sender/recipient lookup)

**Partial.** Protoui's `/ui/research` filters queue items by
sender, recipient, recipient_domain via in-memory substring match
(`internal/protoui/research.go:29-`). Protoadmin's
`GET /api/v1/queue` does not expose sender / recipient /
recipient_domain query parameters.

For Phase 2 the SPA does the same client-side substring match on
the full filtered list (state + principal_id + after_id paginated).
Phase 3 may add a typed search endpoint when the search/scheduler
surface ships; do NOT block the SPA on it.

### 8. OIDC + 2FA + API keys + password change

**Largely covered, one minor gap.**

- OIDC link begin: `POST /api/v1/principals/{pid}/oidc-links/begin`
  (returns auth_url + state). Different shape from protoui's
  redirect-on-GET, but functionally equivalent: SPA hits the
  endpoint, redirects the browser to auth_url, the OIDC callback
  at `/oidc/{provider}/callback` lands, herold reconciles via
  the `state` token. Covered.
- OIDC unlink: `DELETE /api/v1/principals/{pid}/oidc-links/{provider}`.
  Covered.
- TOTP enroll: `POST /api/v1/principals/{pid}/totp/enroll` returns
  the provisioning_uri string but NOT a QR PNG (protoui rendered
  one server-side via `principals.go:301`). The SPA must include
  a client-side QR encoder (qrcode-svg or similar npm dep). Minor
  fill on the SPA side; no server change required.
- TOTP confirm: `POST /api/v1/principals/{pid}/totp/confirm`. Covered.
- TOTP disable: `DELETE /api/v1/principals/{pid}/totp` (requires
  `current_password`). Covered.
- API keys list (admin): `GET /api/v1/principals/{pid}/api-keys`. Covered.
- API keys list (self-service): `GET /api/v1/api-keys`. Covered.
- API keys create: `POST /api/v1/principals/{pid}/api-keys` with
  optional `scope[]` and `allow_admin_scope` flag (REQ-AUTH-SCOPE-04).
  Returns the plaintext key once. Covered; protoadmin is stricter
  than protoui (which never exposed scope).
- API keys revoke: `DELETE /api/v1/api-keys/{id}` for self-service,
  same path admin-only when targeting another principal. Covered.
- Password change: `PUT /api/v1/principals/{pid}/password` with
  self-and-admin split (self requires `current_password`, admin
  scope can override). Covered.

## Summary

The only true wire gap is **Login + session establishment**, which
is also the most security-sensitive thing on the list. Filling that
gap unlocks the rest of the SPA work; everything else is composable
from existing `/api/v1/*` endpoints.

`http-api-implementor` owns the auth gap.
`web-frontend-implementor` owns the SPA scaffold + page work.
`security-reviewer` MUST sign off on the cookie+CSRF design before
the SPA mount lights up at `/admin/` (Phase 3 cutover).
