# 02 — Identity and authentication

## Principals

A **principal** is the subject of authentication and authorization. Three kinds:

| Kind | Notes |
|---|---|
| **Individual** | A human account. Has one or more email addresses, one password credential, optional 2FA, optional OAuth-linked identities. |
| **Group** | A set of individual principals. Addressable (mail to the group fans out to members). Not authenticatable. |
| **Admin** | A principal with administrative permissions. Can be an individual with an admin role, not a separate object type. |
| **Sub-principal** | A mail container owned by an individual principal, carrying its own Mailbox tree, Identity set, Sieve scripts, and JMAP state strings. Addressable. Not authenticatable. Backs the sub-account model (§ Sub-accounts). |
- **REQ-AUTH-01** Every email address in the system resolves to exactly one principal (individual, group, or sub-principal). No floating addresses.
- **REQ-AUTH-02** An individual principal MAY have multiple addresses: one canonical + N aliases. Aliases MAY be on different domains.
- **REQ-AUTH-03** A principal MAY have a catch-all address per domain (e.g. `*@example.com`), but only if the principal also owns the domain. Limit one catch-all per domain.
- **REQ-AUTH-04** Principal names (login handles) are case-insensitive ASCII. Internal IDs are opaque (UUID or 64-bit snowflake).

## Directory and identity backends

*(Revised 2026-04-24: external OIDC is per-user federation, not a directory; we do not act as an OIDC issuer.)*

Where principal records live, and how external identity providers are federated in.

| Backend | v1? | Purpose |
|---|---|---|
| **Internal** (built-in) | yes | **Sole directory.** Principal records live in the main store. Password + TOTP 2FA. Full CRUD. |
| **External OIDC federation (per user)** | yes | Any number of external OIDC providers (Google, Microsoft, GitHub, corporate Okta, etc.) configured at system level. Each *user* may link one or more external identities to their local principal. External-provider email does NOT need to match the local email. Orthogonal to the directory — an auth method, not a storage layer. |
| **LDAP** | **no** | Out of scope. Operators backing user identity with LDAP front it with an OIDC IdP (common) or provision via admin API. |
| **SQL-table directory** | **no** | Out of scope. |
| **SCIM 2.0** | deferred | Phase 3 candidate for automated HR-system-driven provisioning. |

- **REQ-AUTH-10** MUST support the internal directory backend end-to-end (create/read/update/delete, password set, alias add/remove, 2FA enrollment). This is the **only** built-in directory; there is no pluggable directory back-end in v1 beyond the optional directory-adapter plugin type (REQ-PLUG).
- **REQ-AUTH-12** MUST support external-OIDC federation on a **per-user, per-principal** basis (REQ-AUTH-50+).

## Per-user external OIDC federation

The model: local identity is primary. A user MAY associate 0–N external OIDC identities with their local principal. Sign-in via an associated external IdP authenticates the local principal.

- **REQ-AUTH-50** Operators configure one or more **OIDC providers** at the system/application level: name, issuer URL, client ID, client secret, scopes. Discovery via `.well-known/openid-configuration`.
- **REQ-AUTH-51** A user links their principal to an external identity via the self-service flow: `GET /auth/oidc/<provider>/link?principal=<id>` → OAuth redirect → callback creates an association `{principal_id, provider_name, subject_claim, email_claim, linked_at}`. Stored per REQ-STORE-*.
- **REQ-AUTH-52** **Emails need not match.** The external-IdP email (`email` claim) and local principal email are independent. Matching is via the `sub` claim (provider-unique subject identifier), not email.
- **REQ-AUTH-53** A principal MAY have multiple external associations across different providers.
- **REQ-AUTH-54** A principal MAY be "external-only" — no local password. Login only via a linked IdP. Operator-configurable; default allows both.
- **REQ-AUTH-55** Unlink: user or admin removes the association. If the principal is external-only and has only one association, unlink requires simultaneously setting a local password.
- **REQ-AUTH-56** Auto-provisioning from OIDC first login: **opt-in per provider** (off by default). When enabled + first login for a `sub` that doesn't exist: create a new local principal with a generated local email or a config-specified template; store the association. When off: first-time unknown `sub` is rejected.
- **REQ-AUTH-57** Logout from herold does NOT log the user out of the external IdP (single-sign-out is out of scope v1).
- **REQ-AUTH-58** We are a **relying party only**. We do not expose `/.well-known/openid-configuration`, `/authorize`, `/token`, `/userinfo`, `/jwks` endpoints for third parties to consume our identity. Non-goal NG11. **[Extended by `07-access-control.md` (REQ-AC-60..65): RP-only / NG11 is preserved for token *issuance*, but a provider's group/role *claims* MAY now be mapped to herold grants for authorization.]**

### Protocols where external OIDC login applies

- Admin web UI (phase 2) and JMAP HTTP surface: interactive redirect flow supported.
- JMAP over Bearer: MAY use OIDC-issued access tokens from configured providers, validated against the provider's JWKS.
- IMAP/SMTP submission: SASL XOAUTH2 / OAUTHBEARER accept provider-issued tokens, validated same way.
- The same `sub` claim in a token → same local principal as the one linked via interactive flow.

## Credentials

### Passwords

- **REQ-AUTH-20** MUST hash passwords with **Argon2id** (default params: m=64 MiB, t=3, p=1) or **scrypt** as a second option. No bcrypt, no PBKDF2, no MD5/SHA-1.
- **REQ-AUTH-21** MUST support verification of hashes written in standard `$argon2id$…` / `$scrypt$…` / `{SSHA}…` / `{SHA512-CRYPT}…` encoded formats (for import / migration compatibility from existing mail systems). Rehash on successful login if the stored hash is not Argon2id.
- **REQ-AUTH-22** MUST enforce a minimum password length (default 12) and reject passwords present in a local compiled breach-password list (small, embedded — e.g. top 10k from HIBP). No online breach checks.
- **REQ-AUTH-23** Rate-limit authentication attempts per account and per source IP. Lockout thresholds configurable; default: 10 failures / 5 min / IP; 20 failures / 1 h / account with exponential backoff, no hard lock.

### Application passwords

- **REQ-AUTH-30** An individual principal MAY create named **application passwords** (per-device tokens) separate from the main password. App passwords bypass 2FA (because IMAP clients can't do interactive MFA).
- **REQ-AUTH-31** App passwords MUST be revocable independently and listed with last-used timestamp + IP.
- **REQ-AUTH-32** App passwords MAY be scoped to a single protocol (e.g. "IMAP only" or "SMTP submission only") — configurable but not required in v1.

### Two-factor authentication

- **REQ-AUTH-40** MUST support **TOTP** (RFC 6238) as the primary second factor.
- **REQ-AUTH-41** SHOULD support **WebAuthn / FIDO2** as a second factor for the admin UI (phase 2; v1 may be TOTP-only).
- **REQ-AUTH-42** TOTP (REQ-AUTH-40) is required at session creation for principals who have it enrolled (REQ-AUTH-JSON-LOGIN): a web session is not issued without a valid code. A freshly created session begins elevated (REQ-AUTH-74), and TOTP is also the credential used to re-elevate a session for admin and destructive operations after elevation expires. IMAP/SMTP submission with app passwords bypasses TOTP by design (REQ-AUTH-30).
- **REQ-AUTH-43** Recovery codes: ten one-time codes generated on enrollment, hashed like passwords.
- **REQ-AUTH-44** TOTP enrollment MUST be mandatory for principals with the `admin` or `superadmin` role. Without TOTP enrolled, a principal cannot complete the step-up required to perform admin or destructive operations (REQ-AUTH-74); the 403 step-up error carries `enroll_required: true` pointing to the enrollment endpoint. The granting-admin path (PATCH `/principals/{id}` to add `admin`) MUST refuse to grant the role until the target principal has TOTP enrolled, OR MUST simultaneously force enrollment on next sign-in via a one-time enrollment ticket (operator choice; default = refuse). The bootstrap superadmin is the sole exception: the password + API key minted by `herold bootstrap` are usable once for the express purpose of enrolling TOTP, after which the same enforcement applies on every subsequent step-up attempt.
  - Rationale: an admin listener fronted by a reverse proxy on the public internet has no IP allowlist by default; mandatory 2FA is what makes the admin surface safe to expose.
  - Implementation note: the step-up handler (REQ-AUTH-74) checks enrollment before accepting a TOTP code; the enrollment check is mandatory for every elevation attempt.

### OAuth 2 bearer-token verification (for HTTP surfaces)

- **REQ-AUTH-70** MUST implement OAuth 2 resource-server behavior for JMAP: `Bearer` token check. Tokens are OIDC access tokens issued by an **external** OIDC provider configured per REQ-AUTH-50; verified by fetching the provider's JWKS and validating signature + claims.
- **REQ-AUTH-71** MUST support OAuth 2 Device Authorization Grant (RFC 8628) for CLI / mail client bootstrap flows (clients that can't host a redirect URI, e.g. Thunderbird).
- **REQ-AUTH-72** SMTP/IMAP `SASL XOAUTH2` / `OAUTHBEARER` accept the same provider-issued bearer tokens and validate via the same JWKS path.
- **REQ-AUTH-73** Acting as an OIDC *provider* (issuing tokens for external apps) — **not a goal** (NG11). We are a relying party only.

## Permissions and authorization

Stalwart has a fine-grained permission matrix with ~80 permissions and role inheritance. We simplify.

- **REQ-AUTH-60** A principal has one of: `user`, `admin`, or `superadmin`. **[Superseded by `07-access-control.md` (REQ-AC-01): roles are emergent from resource grants; `superadmin` survives as `server:superadmin`, while `admin`/`user` cease to be stored attributes. The tiers below remain a useful capability summary of what those grants confer.]**
  - `user`: access to own mail, own Sieve script, own identities, own app passwords.
  - `admin`: everything `user` can do, plus account/domain management, queue inspection, spam training.
  - `superadmin`: everything `admin` can do, plus config reload, TLS cert management, server shutdown, directory backend config.
- **REQ-AUTH-61** The first principal created during bootstrap is `superadmin`. There is always exactly one superadmin at minimum; deleting the last superadmin is rejected.
- **REQ-AUTH-62** Admin actions logged to a dedicated audit log (see `09-operations.md`).
- **REQ-AUTH-63** Mailbox ACLs (IMAP RFC 4314) are a *separate* dimension for shared mailboxes — deferred with shared mailboxes. **[Realised by `07-access-control.md` (REQ-AC-50): mailbox ACLs are `mailbox:read`/`write`/`admin` grants in the unified model, no longer a separate deferred dimension; phase-2 timing (REQ-PROTO-33) unchanged.]**

## Session model

- **REQ-AUTH-70** IMAP and SMTP submission sessions are stateful per-connection. No shared session cache across reconnects.
- **REQ-AUTH-71** JMAP sessions use short-lived bearer tokens (default 1h) with refresh tokens (default 30 days, bound to IP optionally). Refresh tokens are revocable.
- **REQ-AUTH-72** Web sessions (Suite and admin UI) use httpOnly + Secure + SameSite=Strict cookies. Session lifetime is governed by an **idle timeout** (default 7 days); no absolute lifetime cap is applied by default. A session's idle deadline is extended on every authenticated request; a session unused for the full idle window expires and requires re-login. Both the idle timeout and an optional absolute cap are operator-configurable: `[server.ui] session.idle_ttl` (default `"168h"`) and `[server.ui] session.absolute_ttl` (default `""`, disabled). The effective expiry deadline is embedded in the session token so the server enforces it independently of cookie `Max-Age`. Idle enforcement applies uniformly to every web session regardless of the principal's role.

  *(Session-model redesign 2026-06-28: the prior model applied a 1h idle / 8h absolute TTL to admin cookies only and no idle enforcement to normal sessions, causing admins doing ordinary Suite work to be logged out after idle. The new model uses uniform idle-only enforcement; elevated admin operations are authorized per REQ-AUTH-74 rather than by a short-lived admin-scoped cookie.)*

- **REQ-AUTH-73** Session tokens are HMAC-signed and offline-verifiable without a per-request DB lookup on the hot auth path. The server also persists a **session record** `{session_id, principal_id, created_at, last_seen_at, last_seen_ip, user_agent, idle_deadline}` for every active web session. Session records are updated on each request (rolling `last_seen_at` and `idle_deadline`). A revoked session is tombstoned in the session record; the revocation is checked at most once per minute per session on the hot path. Bearer API keys do not create session records.

- **REQ-AUTH-74** Session elevation for admin and destructive operations. An elevation record `{session_id, principal_id, elevated_at, idle_deadline, absolute_deadline}` authorizes admin-scoped and destructive requests. An elevation is created two ways: (a) **at login**, for a principal who submits a valid TOTP code (REQ-AUTH-JSON-LOGIN) — a fresh session begins elevated; (b) by a correct TOTP code submitted to `POST /api/v1/auth/step-up` (body: `{totp_code}`, rate-limited per session and per source IP, CSRF-checked). Either path sets `elevated_at = now`, `idle_deadline = now + elevation_idle_ttl`, and `absolute_deadline = now + elevation_absolute_ttl`. An elevation is **active** while `now < idle_deadline AND now < absolute_deadline`. Any request requiring elevation with no active elevation returns `403` with `{"type": "step_up_required", "elevation_scope": "admin"}` in the RFC 7807 body. On every request that passes the active-elevation check, the server extends `idle_deadline` to `now + elevation_idle_ttl`, clamped to never exceed `absolute_deadline`; the write is best-effort (log at warn on failure, do not reject the in-flight request), mirroring `UpdateSessionLastSeen`. Elevation therefore expires after `elevation_idle_ttl` of inactivity, and unconditionally at `absolute_deadline` regardless of activity. The two intervals are runtime-configurable (REQ-AUTH-ELEV-CONFIG). A wrong TOTP code returns `401`; after five consecutive wrong codes within a 5-minute window the session's elevation attempts are locked for 5 minutes. Revoking a session (REQ-AUTH-77) also invalidates all elevation records for that session. Elevation grants and failures are audit-logged with `action="auth.step_up"`, `outcome=success|failure`, `actor=principal/<id>`.

- **REQ-AUTH-75** The server surfaces effective session expiry to the client. `GET /api/v1/auth/whoami` MUST include `{..., roles: [<role>...], session_idle_deadline: <ISO-8601 UTC>, elevation_expires_at: <ISO-8601 UTC | null>}` in its response body. `session_idle_deadline` is the current rolling idle deadline; it advances on every authenticated request. `elevation_expires_at` is the effective elevation deadline — the earlier of the elevation's `idle_deadline` and `absolute_deadline` (REQ-AUTH-74) — or null when no elevation is active; because the idle deadline slides on each elevated request, this value moves later over an active session, so clients MUST re-arm any elevation-expiry timer from each fresh value (web REQ-AS-24). `roles` is the calling principal's role set (REQ-AUTH-60: `end-user`, `admin`, `superadmin`); because `admin` is no longer carried as a cookie scope (REQ-AUTH-SCOPE-01), `roles` is how the client learns the principal is admin-capable and gates the admin entry point's visibility (web REQ-AS-26). Clients use these fields to proactively trigger forced re-login before a 401 arrives (web REQ-AS-10, REQ-AS-11) and to display elevation-countdown UI (web REQ-AS-24).

- **REQ-AUTH-76** Expired-session response. When an authenticated request arrives with an expired session (idle deadline elapsed or absolute cap exceeded), the server returns `401` with `{"type": "session_expired"}` in the RFC 7807 body. A tombstoned (revoked) session returns `401` with `{"type": "session_revoked"}`. These responses MUST be recognized globally by the client regardless of which endpoint emits them (web REQ-AS-10). A `session_revoked` response also causes any active elevation record for the session to be invalidated.

- **REQ-AUTH-77** Session list and per-session revocation. A principal can enumerate and revoke their own active web sessions.
  - `GET /api/v1/auth/sessions` (authenticated, CSRF-exempt safe GET) returns the calling principal's session records: `[{session_id, created_at, last_seen_at, last_seen_ip, user_agent, is_current}]`. `is_current` marks the session making the request.
  - `DELETE /api/v1/auth/sessions/{session_id}` (authenticated, CSRF-checked) immediately tombstones the named session and all its elevation records. When `session_id` matches the calling session, the response additionally issues `MaxAge=-1` Set-Cookie headers for the session and CSRF cookies. Returns 204 on success; 404 if the session does not belong to the calling principal.
  - Admin principals MAY list and revoke sessions for any principal via `GET /api/v1/admin/principals/{id}/sessions` and `DELETE /api/v1/admin/principals/{id}/sessions/{session_id}` (admin-listener only, requires valid step-up elevation per REQ-AUTH-74).
  - All revocation actions are audit-logged with `action="auth.session.revoke"`, `actor=principal/<id>`, `subject=session/<session_id>`, `outcome=success`.

- **REQ-AUTH-78** Step-up for sensitive self-service operations. Independent of admin authorization (REQ-AUTH-74 covers admin scope), a defined set of self-service operations a principal performs on their own account requires a valid, unexpired elevation record **when the calling principal has TOTP enrolled**. The set: creating an API key (REQ-AUTH-SCOPE-04), creating an app password, creating or updating external-submission credentials (REQ-AUTH-EXT-SUBMIT-04), changing the account password, and disabling TOTP. Behavior:
  - When the principal **has TOTP enrolled** and no active elevation exists, the handler returns `403` with `{"type": "step_up_required", "elevation_scope": "self-service"}`. The client satisfies it via the same `POST /api/v1/auth/step-up` TOTP flow and retries the operation (web REQ-AS-20, REQ-AS-22). A single per-session elevation record (REQ-AUTH-74) satisfies both admin and self-service step-up; it is created, TTL'd, locked-out, and invalidated-on-revocation identically.
  - When the principal has **no TOTP enrolled**, these operations proceed **without** elevation. The step-up cannot be satisfied (no authenticator), enrollment is **not** forced for self-service operations, and the `403`/`enroll_required` path (REQ-AUTH-44) does **not** apply here — that path is specific to admin operations. Operators who want these operations universally protected enforce org-wide TOTP enrollment by other means.
  - Grants and failures are audit-logged consistently with REQ-AUTH-74 (`action="auth.step_up"`).

- **REQ-AUTH-ELEV-CONFIG** The elevation idle and absolute intervals are DB-backed application config (`internal/appconfig`), editable at runtime by a superadmin without a restart: `elevation.idle_ttl` (default `15m`, range 1m–1h) and `elevation.absolute_ttl` (default `8h`, range 15m–24h). A change takes effect for elevations granted after it; elevation records already in flight keep the deadlines fixed at their grant time (REQ-AUTH-74). This supersedes the former static `[server.ui] session.elevation_ttl` sysconfig key, which is removed: system config is never mutated at runtime, so a runtime-tunable security interval belongs in the DB-backed application config. Both values are surfaced and edited through the admin config surface; changes are audit-logged with `action="config.set"`.

## Domain ownership and delegated admin

- **REQ-AUTH-80** A domain belongs to exactly one principal (the domain "owner"). Owner can manage their own aliases, catch-alls, DKIM key rotation. **[Restated by `07-access-control.md` (REQ-AC-31) as exactly one `domain:owner` grant per domain; the day-to-day management powers here are `domain:operator` (REQ-AC-30).]**
- **REQ-AUTH-81** Domain ownership proof: DNS TXT record matching a server-issued challenge (similar to ACME's DNS-01). Lightweight verification for the admin UI.
- **REQ-AUTH-82** `admin` role can create domains without ownership proof (they're the operator).

## Provisioning and bootstrap

- **REQ-AUTH-90** On first start with no principals, the server writes a one-time bootstrap token to stdout/log and refuses login until the operator uses it to create the first superadmin.
- **REQ-AUTH-91** The admin CLI has a non-interactive mode for bootstrap (`herold admin bootstrap --password-stdin`) for automated provisioning (Ansible, Kubernetes init containers).
- **REQ-AUTH-92** Auto-provisioning of principals from OIDC first-login is opt-in per provider (REQ-AUTH-56). Default is explicit account creation.

## Auth scopes (cookie + API key)

*(Added 2026-04-26 rev 9: closed-enum scope set carried on session cookies and Bearer API keys; mechanically enforces the public/admin listener split per REQ-OPS-ADMIN-LISTENER-01..03.)*

- **REQ-AUTH-SCOPE-01** Suite session cookies and Bearer API keys MUST carry a closed-enum scope set with defined values `end-user`, `admin`, `mail.send`, `mail.receive`, `chat.read`, `chat.write`, `cal.read`, `cal.write`, `contacts.read`, `contacts.write`, `webhook.publish`; the set is operator-extensible only via spec change (not via runtime config — drift between cookie issuance and handler enforcement creates auth bugs). Session cookies issued at login carry `[end-user, mail.send, mail.receive, chat.*, cal.*, contacts.*]` for the principal's enabled subsystems. `admin` scope is never baked into a session cookie; access to admin and destructive operations is authorized by a separate time-limited elevation record (REQ-AUTH-74). A single session-cookie shape is issued at login regardless of the principal's role.

  *(Scope-model redesign 2026-06-28: the prior model issued a combined admin+end-user cookie after a TOTP step-up at login, taking the short admin session TTL. The new model issues uniform end-user cookies and moves admin authorization to per-operation step-up elevations.)*
- **REQ-AUTH-SCOPE-02** Every handler MUST check the auth context's scope set against the handler's required scope; mismatch returns 403 with an RFC 7807 problem detail (NOT 401 — the caller IS authenticated, just not authorised for this scope). For operations requiring admin authorization, the handler additionally verifies a valid, unexpired elevation record for the current session (REQ-AUTH-74); absence returns 403 with `{"type": "step_up_required"}`. The cookie's scope set never carries `admin`; the elevation record is the sole authorization for admin operations.
- **REQ-AUTH-SCOPE-03** Admin elevation: entering the admin UI or issuing any mutating admin request within a session that lacks an active elevation record causes the endpoint to return 403 with `step_up_required` (REQ-AUTH-74). The client presents a TOTP-only modal; a correct TOTP code creates an elevation record bounded by the configured idle and absolute intervals (REQ-AUTH-74, REQ-AUTH-ELEV-CONFIG). Principals without TOTP enrolled (REQ-AUTH-44) cannot complete the step-up; the 403 body carries `enroll_required: true`. The session login flow (`POST /api/v1/auth/login`) issues only end-user-scoped cookies regardless of the principal's role; TOTP is required at login for enrolled principals (REQ-AUTH-JSON-LOGIN), and a successful enrolled login yields an initially elevated session (REQ-AUTH-74).
- **REQ-AUTH-SCOPE-04** API key scope is set at creation time and immutable (rotate to change). The `herold apikey create` command (REQ-ADM-03) takes `--scope` as a comma-separated list of allowed values; default is `[mail.send]` (the most common transactional-app shape per REQ-SEND-30). Admin scope on an API key requires `--allow-admin-scope` to acknowledge the operator-side risk; cookies are the recommended path for human admin access.

## REST session and CSRF (phase 2 additions)

*(Added 2026-04-27: JSON login/logout endpoints and cookie-based auth for the admin REST surface, enabling the Svelte admin SPA at /admin/. See docs/design/server/notes/phase-2-protoui-protoadmin-coverage-audit-2026-04-27.md.)*

- **REQ-AUTH-SESSION-REST**: `internal/protoadmin` MUST accept both `Authorization: Bearer hk_...` API keys and the admin listener session cookie (`herold_admin_session` by default) as valid credentials for `/api/v1/...` endpoints. The session cookie is verified against the same HMAC-SHA256 signing key used by the `protoui` admin login flow so cookies minted by the HTML `/login` and by `POST /api/v1/auth/login` are mutually valid. When the signing key is not configured (zero or fewer than 32 bytes), cookie auth is disabled and the endpoint accepts only Bearer keys (backward-compatible with Phase 1 deployments).

- **REQ-AUTH-CSRF**: All mutating `/api/v1/...` requests authenticated by session cookie MUST present an `X-CSRF-Token` header whose value matches the `herold_admin_csrf` cookie value verified by `crypto/subtle.ConstantTimeCompare`. Bearer-authenticated requests are exempt (no ambient credential). GET/HEAD/OPTIONS (RFC 7231 safe methods) are exempt. On mismatch the endpoint returns 403 with an RFC 7807 `application/problem+json` body with type `csrf_mismatch`.

- **REQ-AUTH-JSON-LOGIN**: `POST /api/v1/auth/login` accepts `{email, password, totp_code?}` (unauthenticated, rate-limited per source IP). On success it issues `herold_admin_session` (HttpOnly, Secure, SameSite=Strict, Path=/) and `herold_admin_csrf` (non-HttpOnly, Secure, SameSite=Strict, Path=/) cookies and returns `{principal_id, email, roles:[...], scopes:[...], session_idle_deadline: <ISO-8601 UTC>, elevation_expires_at: <ISO-8601 UTC | null>}`. `roles` (REQ-AUTH-75) lets the client render the admin entry point immediately after login without a second `whoami` round-trip. The issued session carries only end-user scopes (REQ-AUTH-SCOPE-01); admin authorization is carried by the elevation record, not the cookie. **TOTP is evaluated at login:** for a principal with TOTP enrolled, the endpoint returns `401` with `{"type": "step_up_required"}` and issues no session when `totp_code` is absent or incorrect, and issues the session only after a valid code. A successful login for an enrolled principal creates an initial elevation record (REQ-AUTH-74), so `elevation_expires_at` is populated and the fresh session performs elevated operations without an immediate second prompt; a principal with no TOTP enrolled logs in with password alone and receives `elevation_expires_at: null`. On bad credentials the endpoint returns 401. Failed login attempts MUST be audit-logged with `action="auth.login"`, `outcome=failure`, `subject="email:<attempted-email>"`, and a `message` distinguishing the failure mode (per REQ-ADM-300, REQ-ADM-303); successful logins are audit-logged with `actor=principal/<id>`, `subject="principal:<email>"`, `outcome=success`.

- **REQ-AUTH-JSON-LOGOUT**: `POST /api/v1/auth/logout` (authenticated by cookie or Bearer) tombstones the server-side session record (REQ-AUTH-73), invalidating it immediately for the per-minute revocation check; it also clears both cookies by issuing `MaxAge=-1` Set-Cookie headers and returns 204 No Content. Bearer-authenticated callers receive the cookie-clear headers harmlessly (their session was not cookie-based and has no session record). Residual sessions on stolen devices are immediately revocable via REQ-AUTH-77. Logout MUST be audit-logged with `action="auth.logout"`, `subject="principal:<email>"` of the calling principal, `outcome=success`.

- **REQ-AUTH-COOKIE-PATH**: Session cookies on both the admin and public listeners use `Path=/` so the same browser session accompanies `/api/v1/...`, `/admin/...`, and `/ui/...` requests on the same listener. Cross-listener isolation is enforced by the distinct cookie name (`herold_admin_session` vs `herold_public_session`), not by path scoping. CSRF cookies also use `Path=/`.

- **REQ-AUTH-JSON-WHOAMI**: `GET /api/v1/auth/whoami` (authenticated by cookie or Bearer) returns 200 + `{principal_id, email, roles:[...], scopes:[...], session_idle_deadline: <ISO-8601 UTC | null>, elevation_expires_at: <ISO-8601 UTC | null>}` for a valid session, or 401 when no valid credential is present. `roles` is the principal's role set (REQ-AUTH-75); the client gates admin-entry-point visibility on it (web REQ-AS-26). `session_idle_deadline` is the current rolling idle deadline (REQ-AUTH-75); it is null for Bearer-key sessions that have no session record. `elevation_expires_at` is the active step-up elevation expiry (REQ-AUTH-74) or null when no elevation is in effect. The endpoint is a safe GET method and therefore exempt from CSRF checking (REQ-AUTH-CSRF). The admin SPA calls this endpoint on page load to probe session state without requiring a full server-status round-trip. Additionally, `GET /api/v1/server/status` includes the same fields (including `roles`) in its response body so the admin SPA's existing bootstrap probe can populate auth state from a single request.

## External SMTP submission per Identity (v1)

*(Added 2026-04-29: a narrow v1 surface where each `Identity` MAY carry credentials for an external SMTP submission endpoint. Outbound for that Identity goes via the external endpoint instead of herold's outbound queue. Inbound is out of scope for this section — operators arrange forwarding at the external provider so inbound mail still arrives at the local mailbox via REQ-FLOW-\*. The broader "external mail accounts" feature with bidirectional IMAP mirror is a strict superset and lives in § External transport identities (deferred), below. Web-side counterpart: `../../web/requirements/02-mail-basics.md` § External SMTP submission per Identity.)*

The v1 use case: an operator who wants herold to send mail through an existing Gmail / Microsoft 365 / Fastmail / corporate SMTP relay account, using that account's deliverability posture, while still using herold for everything else (storage, search, JMAP, suite UI, admin). Pre-production deployments without owning DKIM / SPF / DMARC for the sending domain.

Scope boundary against the deferred broader spec (next section): this section adds **submission-only credentials per `Identity`** — one local JMAP account, no inbound mirror, no extra `accounts[]` in the session descriptor, no IMAP IDLE worker. Inbound continues to flow through whatever forwarding the operator arranges at the external provider.

- **REQ-AUTH-EXT-SUBMIT-01** Each `Identity` (RFC 8621 §6) MAY carry an external submission config: `{submit_host, submit_port, submit_security ∈ {implicit_tls, starttls, none}, submit_auth_method ∈ {password, oauth2}, credential_ref}`. Absent submission config means the existing default — outbound for this Identity goes through herold's outbound queue (REQ-FLOW-\*).
- **REQ-AUTH-EXT-SUBMIT-02** Credentials are stored encrypted at rest with the server-managed data key already used for other secrets (REQ-STORE-\*). For `password`, the at-rest record is the encrypted password (or app-specific password). For `oauth2`, the at-rest record is `{access_token, refresh_token, expires_at, token_endpoint, client_credentials_ref}`; herold refreshes the access token before expiry on a background timer. Refresh failure sets the Identity submission state to `auth-failed` (REQ-AUTH-EXT-SUBMIT-07). Credential plaintext exists in memory only during a single submission attempt or the OAuth refresh round-trip; it is zeroed on completion.
- **REQ-AUTH-EXT-SUBMIT-03** Provider auto-detection (MAY ship in v1; deferral to v1.1 acceptable). When the Identity's email domain is `gmail.com` / `googlemail.com` or any Google Workspace MX, herold offers a one-click OAuth flow against Google's OAuth server. Operator configures the OAuth client at the system level — same shape as REQ-AUTH-50 but issuing tokens scoped for SMTP submission (`https://mail.google.com/`). Same shape applies for `outlook.com` / `hotmail.com` / Microsoft 365 hosted domains. Manual entry is always available as a fallback. When auto-detect is not configured at the system level, the user enters host, port, security mode, and credential by hand.
- **REQ-AUTH-EXT-SUBMIT-04** Submission credentials are managed via an admin-style REST surface, not on the JMAP wire — credentials never appear in JMAP responses. Endpoints (mounted on the public listener at the self-service prefix used for API keys, REQ-AUTH-SCOPE-04 et al.):
  - `GET /api/v1/identities/{id}/submission` → `{configured: bool, submit_host, submit_port, submit_security, submit_auth_method, state}`. No credential material.
  - `PUT /api/v1/identities/{id}/submission` → set or replace. The body carries the credential payload (password or the result of a completed OAuth flow). The body is consumed once; the server stores the encrypted form and discards the plaintext.
  - `DELETE /api/v1/identities/{id}/submission` → remove the configuration; subsequent submissions for the Identity revert to herold's outbound queue.
  - All three are scoped to the principal that owns the Identity (REQ-AUTH-SCOPE-01); admins MAY NOT view or set submission credentials for other principals (no impersonation in v1).
- **REQ-AUTH-EXT-SUBMIT-05** When a JMAP `EmailSubmission/set` selects an Identity that has submission credentials configured, the server submits the message through the configured external endpoint instead of enqueueing it on the local outbound queue:
  - Connect to `submit_host:submit_port` with the configured security mode (`implicit_tls` opens the connection inside TLS; `starttls` upgrades after `EHLO`; `none` is for test-only fixtures).
  - SMTP `EHLO`, then `AUTH` per the configured method: `AUTH PLAIN` / `AUTH LOGIN` for `password`; `AUTH XOAUTH2` for `oauth2`.
  - Issue `MAIL FROM`, `RCPT TO` for every envelope recipient (including Bcc), `DATA`, `.`, `QUIT`.
  - The external server's response (positive 2xx → submission accepted; transient 4xx → soft-fail; permanent 5xx → hard-fail) is mapped to the JMAP `EmailSubmission` state and surfaced back to the client. There is **no local retry** — the external server's queue is authoritative for retries from this point forward.
- **REQ-AUTH-EXT-SUBMIT-06** Local DKIM signing (REQ-DKIM-\*) is **skipped** for messages submitted via an external endpoint. The external server is responsible for signing under its own DKIM key for its own domain. Re-signing locally would either fail DMARC alignment at the receiver or duplicate signatures uselessly. Operators are responsible for ensuring the external provider accepts the chosen `From:` address (e.g. Gmail's "Send mail as" verification, Microsoft 365 send-as permissions) — this is a deployment concern, not a herold concern.
- **REQ-AUTH-EXT-SUBMIT-07** Per-Identity submission state is one of: `ok` (last submission succeeded, or no submission has been attempted yet), `auth-failed` (last submission attempt got 535, or an OAuth refresh failed), `unreachable` (network or DNS failure on connect, last attempt). State changes emit a JMAP push event on the principal's EventSource feed so the suite can prompt the user to re-authenticate. State is read by the suite via `GET /api/v1/identities/{id}/submission`.
- **REQ-AUTH-EXT-SUBMIT-08** Removal of an Identity also drops its submission credentials (the foreign key uses `ON DELETE CASCADE`).
- **REQ-AUTH-EXT-SUBMIT-09** Audit. Every `PUT` and `DELETE` against the submission endpoint emits an audit-log entry tagged `identity.submission.{set,delete}` with the principal id, the Identity id, and the auth method (never the credential value). Every external-submission failure emits a `submission.external.failure` audit event with the failure category (`auth`, `transport`, `permanent`) and an opaque correlation id matching the JMAP `EmailSubmission` id.
- **REQ-AUTH-EXT-SUBMIT-10** Inbound is **not in scope of this section**. Operators arrange forwarding at the external provider (Gmail "Forwarding and POP/IMAP", M365 mailbox forwarding, etc.) so inbound mail still arrives at the local herold mailbox via REQ-FLOW-\*. The optional inbound mirror is delivered by `19-imap-import.md` (IMAP import, re-scoped per-identity by its decision 10) — the two compose into the per-identity external-transport identity described in REQ-AUTH-EXT-SUBMIT-12.
- **REQ-AUTH-EXT-SUBMIT-11** **(Added 2026-06-27 — verify before finish.)** Submission setup is probe-gated. `PUT /api/v1/identities/{id}/submission` MUST NOT persist a submission config until herold has performed a successful **probe submission** against the supplied endpoint: connect with the chosen security mode, `EHLO`, `AUTH` per the chosen method, then validate send capability (a `MAIL FROM` + `RCPT TO` to the identity's own address followed by `RSET`, or a full empty-message `DATA` where `RSET`-probe is unsupported). A failed probe returns the categorised error (`auth-failed` / `unreachable` / `permanent-reject`, REQ-AUTH-EXT-SUBMIT-07) and the config is rejected, so an identity can never be left carrying unverified or broken submission credentials. The web wizard cannot finish setup on a failed probe (web REQ-MAIL-SUBMIT-03).
- **REQ-AUTH-EXT-SUBMIT-12** **(Added 2026-06-27 — mandatory send path; per-identity transport.)** Every `Identity` MUST have a working send path before it can be used to send. A **hosted-domain** identity (REQ-IDENT-21) sends natively through herold's outbound queue — no configuration is needed and none is offered. An **external-domain** identity MUST carry a probe-verified external submission config (REQ-AUTH-EXT-SUBMIT-11); until it does, the identity is send-disabled (REQ-IDENT-62 / web REQ-MAIL-SUBMIT-05c). The external submission config (send, mandatory) and the **optional** IMAP import config (receive, `19-imap-import.md` REQ-IMAP-IMP-01) together constitute the per-identity "external transport identity"; both are edited under the one Identity in the suite (web REQ-SET-IDENT-10 + REQ-SET-IMAPIMP-\*). IMAP import is offered only on external-domain identities (a hosted identity's mail is already local).

### Migration to the broader deferred spec

When the broader "external mail accounts" feature (next section) lands, every existing `Identity` with submission credentials is migrated by the deployment to the corresponding external account, and its `submit_*` fields move under `account.smtp_submission`. The migration is one-way and idempotent. v1 implementations need not consider the migration — it is the deferred feature's job to write it.

## Identity creation and verification (v1)

*(Added 2026-05-11: a v1 surface for user-driven creation of additional `Identity` rows on the principal's local account. Every newly-created Identity passes through an email-verification step before it can be used to send, regardless of whether the email's domain is hosted on this herold. Web-side counterpart: `../../web/requirements/20-settings.md` § Identity maintenance (v1).)*

Scope: this section covers creating, verifying, listing, and removing `Identity` rows on the principal's existing local JMAP account. It does NOT cover external transport (the deferred § External transport identities below) — that is a separate feature with its own JMAP-account boundary. An Identity created here MAY later acquire external SMTP submission credentials (§ External SMTP submission per Identity); the two surfaces compose.

The synthesised default identity (id `"default"`, derived from the principal row's canonical email) is implicitly verified at provisioning time and is unaffected by the requirements below — it cannot be unverified, re-verified, or destroyed.

### Identity record extensions

- **REQ-IDENT-01** Every persisted `jmap_identities` row gains a verification trio: `{verified_at_us: int64 | NULL, verification_token_hash: bytes | NULL, verification_token_expires_at_us: int64 | NULL}`. `verified_at_us` is the wall-clock instant the identity was verified; NULL means unverified. The token columns are NULL when no verification is in flight. Stored per REQ-STORE-*. Schema migration is forward-only and additive.
- **REQ-IDENT-02** The synthesised default identity (id `"default"`) is treated as verified-by-construction: code paths that read `verifiedAt` for a synthesised default return the principal's `created_at` (or any non-NULL sentinel) and writes are rejected. No row, no token, no JMAP `Identity/set { update }` against the default's verification fields.

### JMAP wire surface

- **REQ-IDENT-10** The JMAP `Identity` object gains one herold-namespaced extension property: `verifiedAt: UTCDate | null`. Present on every `Identity/get` response and every `Identity/changes` row.
- **REQ-IDENT-11** A new server-level capability `https://netzhansa.com/jmap/identity-verification` is advertised in the JMAP session descriptor when `[server.identity_creation].enabled = true` (default true). Clients ignoring the capability MUST tolerate the `verifiedAt` property; it is additive and never blocks normal field access.
- **REQ-IDENT-12** `Identity/set { create }` creates the row with `verified_at_us = NULL` and triggers the server-driven verification flow (REQ-IDENT-30+). The server-side response carries the freshly-created `Identity` with `verifiedAt = null`; the suite is responsible for surfacing the pending state. There is NO synchronous "verify now" path on the wire — verification is always asynchronous, gated by the email round-trip.
- **REQ-IDENT-13** `Identity/set { update }` MAY NOT toggle `verifiedAt`: any client attempt is rejected with `invalidProperties { verifiedAt }`. Verification transitions only via the email-link callback or the admin CLI (REQ-IDENT-50).
- **REQ-IDENT-14** `Identity/set { destroy }` is permitted on unverified Identities at any time. For verified Identities, the existing JMAP-level rules apply (default cannot be destroyed; see REQ-AUTH-EXT-SUBMIT-08 for cascade behaviour with submission credentials).

### Domain policy (operator-side gate)

- **REQ-IDENT-20** Operators MAY restrict which email domains a principal can create new Identities for via `[server.identity_creation].external_domains` in `system.toml`. Values: `allow_all` (default), `allowlist`, or `deny_all`. In `allowlist` mode, `[server.identity_creation].external_domain_allowlist` is a list of bare domain names (e.g. `["gmail.com", "company.com"]`).
- **REQ-IDENT-21** Domain classification: an email's domain is "hosted" if it appears in the server's domain list (the same set surfaced by `Meta().ListLocalDomains`); otherwise "external". Hosted-domain Identity creation is always permitted regardless of the `external_domains` knob; the policy applies only to external domains.
- **REQ-IDENT-22** A rejected creation (policy or any other reason) MUST surface as a JMAP `setError { type: "forbiddenFrom" | "invalidProperties", description }` with enough context for the suite to render a precise error. Audit-log every rejection per REQ-IDENT-90.
- **REQ-IDENT-23** **(Added 2026-06-27.)** Adding a domain to the hosted set reclassifies existing `Identity` rows for that domain from external to hosted (REQ-IDENT-21 is evaluated live), but this MUST NOT auto-convert them into native **delivery** addresses: a self-asserted external identity confers no claim on the hosted namespace. Converting an external identity into a native delivery address — dropping its external SMTP submission, finalizing any IMAP mirror, and assigning the address to the principal — happens only through the operator-driven domain-cutover process (`26-domain-cutover.md` REQ-CUTOVER-30..43), never as a side effect of `herold domain add`. Until conversion, a reclassified-but-unconverted identity is send-disabled the same as any verified-without-transport identity would be, and delivery of `user@domain` mail is governed solely by the domain's principals and aliases.

### Verification email flow

- **REQ-IDENT-30** On `Identity/set { create }`, after the row is committed in the unverified state, the server enqueues one outbound verification message to the new Identity's email address. Sender (envelope MAIL FROM + header From) is `[server.identity_creation].verifier_from`; default `postmaster@<canonical hosted domain>` per RFC 5321 §4.5.1. Operator MAY override to `noreply@<host>` or any locally-hosted address. The verification message is queued via the same outbound queue as user mail (REQ-FLOW-*); deliverability follows the operator's normal posture for the canonical domain.
- **REQ-IDENT-31** Token shape: 32 random bytes generated from a CSPRNG, encoded base64url (43 ASCII characters, no padding). Stored on the row as `verification_token_hash = SHA-256(token)`; the raw token exists in memory only between generation and email-write. A separate 6-digit numeric code is derived from the same generation (REQ-IDENT-32); both are independent inputs that resolve to the same verification.
- **REQ-IDENT-32** Code shape: 6 decimal digits, ASCII, leading zeros preserved. Stored as `verification_code_hash = SHA-256(code)` on the row. The numeric code is included in the email body for users whose mail clients break the click-link (text-mode readers, link-rewriting MTAs). Either input (link or code) verifies the Identity; one consumes the other (token rows are single-use).
- **REQ-IDENT-33** Email body. Plain-text + HTML alternative. Subject: localised "Verify your email address" (the principal's preferred-locale, falling back to operator default). Body: a short explanatory paragraph, the click-link (`https://<host>/verify-identity?token=<token>`), and the 6-digit code on its own line. The body identifies the requesting principal (`Initiated by: <principal canonical email>`) so the recipient can detect an unsolicited verification. Final composition is plain RFC 5322 mail, DKIM-signed by the canonical-domain key (REQ-DKIM-*), and dispatched via the standard outbound queue.
- **REQ-IDENT-34** Token TTL: 24 hours from issue. After expiry, both link and code are inert; the verification row remains but the suite renders the Identity as unverified with a "Resend" affordance (REQ-IDENT-41).
- **REQ-IDENT-35** Unverified-identity cleanup: a periodic GC pass (default every 6 hours) destroys any Identity row whose `verified_at_us IS NULL` AND `created_at_us < now - 7d`. Token columns are purged on a tighter cadence (any token whose `verification_token_expires_at_us < now` is wiped at the next GC pass even if the Identity row is retained). The 7-day window is configurable via `[server.identity_creation].unverified_purge_after`.
- **REQ-IDENT-36** Resend rate-limit. A user-initiated resend on the same Identity is rejected if any of: (a) the most recent token issue was less than 60s ago; (b) the count of token issues for this Identity in the trailing 24 h ≥ 5. The hard daily limit is per-Identity, NOT per-principal, so a user with multiple in-flight Identities is independently rate-limited on each. Limits are configurable via `[server.identity_creation].resend_cooldown_seconds` (default 60) and `[server.identity_creation].resend_daily_cap` (default 5). Rejected resends surface as `tooManyRequests` with `Retry-After` set.
- **REQ-IDENT-37** Resend rotates the token. Every successful resend invalidates the previous link and code by overwriting the stored hashes; the new token also resets `verification_token_expires_at_us` to `now + 24h`. The user's most recently delivered email is always the only one that works.

### Verification callback

- **REQ-IDENT-40** `GET /verify-identity?token=<token>` is mounted on the **public listener** (port serving the suite SPA). The handler:
  - Hash the supplied token; look up the matching Identity row by `verification_token_hash`.
  - Reject if not found, expired, or already consumed: 400 with a server-rendered HTML page ("This verification link is invalid or has expired. Please request a new one from Settings.").
  - On success: set `verified_at_us = now`, NULL the token columns, commit, and **302 redirect** to `/#/settings`. The suite reads the freshly-verified Identity via the JMAP `Identity` state push and surfaces a toast on arrival.
- **REQ-IDENT-41** Code entry: `POST /api/v1/identities/{id}/verify` on the public listener (CSRF-checked, self-only). Body: `{code: "123456"}`. Same successful-verification semantics as the link callback; same error shape on failure. Used by the suite's "have a code" input.
- **REQ-IDENT-42** Verification is idempotent on a verified Identity: a second valid token/code redeem returns 200 / 304 (no-op) so the user can refresh the link page safely. An invalid token on an already-verified row returns 400 (the row is verified, but this token is not the active token; treat as "the most recently delivered link is the only valid one"). Distinguishing the two cases is for diagnostics, not for the user-visible page.
- **REQ-IDENT-43** Audit. Every callback (success and failure) emits an audit-log entry tagged `identity.verify.{success,failure}` with the Identity id, the requesting principal, the result class, and the user-agent / source IP. Tokens are never logged (the row stores only the hash).

### Admin CLI

- **REQ-IDENT-50** `herold identity verify <identity-id>` immediately sets `verified_at_us = now` and clears any pending token. The CLI ignores the verification email round-trip; intended for incident recovery, bootstrap of import flows, or operator-assisted setup when email delivery to the target is broken. Audit-logged as `identity.verify.admin` with the operator's principal id.
- **REQ-IDENT-51** `herold identity unverify <identity-id>` reverts a verified Identity to unverified (sets `verified_at_us = NULL`); used to revoke a compromised identity. Audit-logged. Refused for the synthesised default identity.
- **REQ-IDENT-52** `herold identity list [--principal <id>]` lists Identities with verification status. Useful for operators to inspect pending verifications.
- `herold identity reissue-code <identity-id>` is a testing aid: it generates a fresh verification token + 6-digit code for an unverified Identity, persists the new sha256 hashes and a fresh 24h expiry (via `ResetIdentityVerificationToken`), and prints the plaintext code, token, and `/verify-identity?token=...` link to stdout. Because the live flow stores only the hashes (migration 0048), an in-flight code can never be recovered, so the command always re-issues. Audit-logged as `identity.reissue-code.admin`. Refused on the synthesised default identity and on an already-verified Identity (unverify it first to re-test).

### Send-side gating

- **REQ-IDENT-60** When a JMAP `EmailSubmission/set` references an Identity whose `verified_at_us IS NULL`, the server rejects with a `SetError { type: "forbiddenFrom", description: "identity is not verified", properties: ["identityId"] }`. The submission is dropped; no message enters the outbound queue, no DKIM signing happens, no external SMTP attempt happens.
- **REQ-IDENT-61** The suite's compose UI MUST surface unverified Identities visibly so the user does not discover the rejection at send time. The wire-level rejection in REQ-IDENT-60 is a defence-in-depth gate, not the primary UX path.
- **REQ-IDENT-62** For external-domain Identities (REQ-IDENT-21) that are verified but lack external SMTP submission credentials (REQ-AUTH-EXT-SUBMIT-01), the server still rejects `EmailSubmission/set` with a distinct error: `forbiddenFrom { description: "external identity requires submission configuration" }`. Rationale: sending via herold's outbound queue with herold's DKIM signature would fail DMARC alignment for the external domain. The suite reflects this in the From picker by disabling the row with a "Configure external SMTP" prompt (REQ-MAIL-SUBMIT-05c, web side).

### Configuration knobs

```toml
[server.identity_creation]
# Master switch. When false, Identity/set { create } returns
# forbidden and the suite hides the "Add identity" affordance.
enabled = true

# Sender address for verification messages. Default postmaster on
# the canonical hosted domain. MUST be locally hosted so DKIM signs
# under a key herold controls.
verifier_from = "postmaster@example.local"

# External domain policy.
external_domains = "allow_all"  # allow_all | allowlist | deny_all
external_domain_allowlist = []  # only consulted when mode = "allowlist"

# Lifecycles.
unverified_purge_after = "7d"
resend_cooldown_seconds = 60
resend_daily_cap = 5
```

### Audit and observability

- **REQ-IDENT-90** Every state transition emits a structured audit event: `identity.create`, `identity.verify.success`, `identity.verify.failure`, `identity.verify.admin`, `identity.unverify`, `identity.destroy`, `identity.resend`, `identity.purge`. Each carries the principal id, the Identity id, the Identity email (canonicalised), and the result class. Verification tokens are never logged.
- **REQ-IDENT-91** Metrics: counters for `identity_create_total{domain_class=hosted|external}`, `identity_verify_total{result=success|expired|invalid}`, `identity_resend_total`, `identity_purge_total`. Histograms for verification time-to-verify (created → verified). Surfaces on the operator metrics endpoint.

### Migration to deferred external accounts

When the broader "external mail accounts" feature lands (next section), Identities created by this v1 flow continue to live on the local JMAP account. External accounts add their own JMAP-account-scoped Identities orthogonally; no migration of v1 Identities is required. The user MAY end up with overlapping Identities (e.g. `hans@gmail.com` on both the local account via this flow + the deferred external Gmail account); the deferred feature is responsible for surfacing the overlap to the user, not for collapsing the rows.

## Sub-accounts

A principal's mail MAY be split across more than one JMAP account. Two features want this: a **separated local identity** (an imported or secondary address the user keeps out of the shared inbox) and an **external mail account** (a mirrored Gmail or Microsoft 365 mailbox, § External transport identities). Both are the same object — a sub-account — and differ only in transport.

A sub-account is a **sub-principal**. Mailbox trees, Identity sets, Sieve scripts, and JMAP state strings are already keyed by principal, so a sub-principal carries its own by construction, and the session's existing secondary-account surface (REQ-PROTO-33, built for shared mailboxes) already knows how to advertise an account the caller does not own.

Web-side counterpart: `../../web/requirements/02-mail-basics.md` § Sub-accounts: separated identities.

- **REQ-SUBACCT-01** A sub-account is a principal owned by exactly one individual principal (its **parent**). It carries its own Mailbox tree, Identity set, Sieve scripts, and JMAP state strings.
- **REQ-SUBACCT-02** A sub-principal is not authenticatable. No credential may be set on it, and it is rejected on every auth path: session-cookie login, device token, the OAuth2 authorization-code grant, IMAP, SMTP submission, and ManageSieve. A user reaches their sub-accounts only by authenticating as the parent.
- **REQ-SUBACCT-03** The JMAP session descriptor enumerates each of the caller's sub-accounts in `accounts` (RFC 8620 §2), each with its own `accountCapabilities` and its own state-string namespace. `primaryAccounts` continues to name the parent's own account.
- **REQ-SUBACCT-04** Isolation. Mailboxes, Threads, Emails, and Identities never cross a JMAP-account boundary. A query scoped to the parent account never returns a sub-account's mail, and vice versa; the two accounts' state strings advance independently.
- **REQ-SUBACCT-05** Quota. A sub-account's mail counts against its parent's quota. A principal cannot enlarge its storage allowance by separating an identity.
- **REQ-SUBACCT-06** Admin surface. Sub-principals are not users: they do not appear in admin principal lists or user counts, and are surfaced as sub-accounts of their parent. Deleting the parent deletes its sub-accounts and their mail.
- **REQ-SUBACCT-07** Transport. Each sub-account has an inbound and an outbound transport. Inbound is local delivery, IMAP import (`19-imap-import.md`), or an external IMAP mirror (REQ-AUTH-EXT-04). Outbound is herold's queue or an external SMTP endpoint (REQ-AUTH-EXT-SUBMIT-*). One account model serves all of them; a new transport does not add a new account model.
- **REQ-SUBACCT-08** Outbound transport is a property of the Identity and separation a property of the account, and the two compose without interaction: an Identity moved into a sub-account keeps whatever external-submission config it carries.
- **REQ-SUBACCT-09** Promotion. Separating an existing Identity creates a sub-principal, moves that Identity into it, and migrates the mail already attributed to that Identity into the new account (REQ-IMAP-IMP-106). Promotion is idempotent and crash-safe: a restart mid-migration completes it rather than stranding mail across two accounts.
- **REQ-SUBACCT-10** Removal. Deleting a sub-account carries an explicit keep-or-purge choice for its mail, on the model of REQ-IMAP-IMP-102: keep moves the mail back to the parent account, purge destroys it. Neither path modifies an external server's mailbox.
- **REQ-SUBACCT-11** Capability gating. Sub-account support is advertised as `https://netzhansa.com/jmap/sub-accounts` at the JMAP session level. Absent the capability, a client surfaces no account switcher and no separation affordance.

## External transport identities (deferred)

*(Added 2026-04-29: scopes a future "external mail accounts" feature where a herold principal aggregates one or more external IMAP+SMTP accounts. Spec-only; not scheduled for v1 implementation. Web-side counterpart: `../../web/requirements/02-mail-basics.md` § External mail accounts.)*

The model: an individual principal MAY associate one or more **external mail accounts** with their local principal. Each external account is a sub-account (§ Sub-accounts) whose inbound transport is an external IMAP mirror and whose outbound transport is an external SMTP endpoint. It contributes its own JMAP account to the principal's session (RFC 8620 §2), with its own Mailbox tree, Identity set, state strings, and Sieve script. The local principal remains primary — authentication, password, 2FA, and admin authority are unaffected by external accounts.

External accounts are orthogonal to OIDC federation (REQ-AUTH-50+): OIDC federates *authentication* (an external IdP can log the user in to herold), while external accounts federate *transport* (herold acts as a client to an external mail server on the user's behalf). A user MAY use both in any combination.

- **REQ-AUTH-EXT-01** A principal MAY register one or more external mail accounts, each defined by `{display_name, primary_email, imap: {host, port, security, auth_method}, smtp_submission: {host, port, security, auth_method}, credential_ref}`. Stored per REQ-STORE-*.
- **REQ-AUTH-EXT-02** Credentials for external accounts are stored encrypted at rest with a server-managed data key. Supported `auth_method` values: `password` (encrypted at rest, decrypted in-memory for use), `oauth2` (an OAuth 2.0 access token plus refresh token; tokens are refreshed by the server on a background timer; refresh failure sets the account to `authentication-failed`).
- **REQ-AUTH-EXT-03** Common providers (Google, Microsoft 365) are recognised by domain heuristics and offered a one-click OAuth flow using OAuth client credentials configured at the system level (mirroring the OIDC provider config of REQ-AUTH-50, but issuing tokens scoped for IMAP/SMTP rather than identity). Provider config is operator-side; the user-facing flow is self-service.
- **REQ-AUTH-EXT-04** Per external account, the server maintains a long-lived IMAP IDLE session and mirrors mailboxes and messages into the principal's local store, tagged with the source account id. Mirroring is bidirectional for read state, flag state, and mailbox membership; deletion semantics are TBD when this is implemented (proposal: mirror-side delete propagates to the external server, with a per-account opt-out).
- **REQ-AUTH-EXT-05** Per external account, outbound submission for an Identity bound to that account uses the configured external SMTP submission endpoint and is **not** routed through herold's outbound queue (REQ-FLOW-*). The external transport's deliverability posture governs delivery; herold does not re-sign or rewrite.
- **REQ-AUTH-EXT-06** Each external account contributes one JMAP account to the principal's session descriptor. The local account remains the principal's `accounts[<primary>]`; external accounts are added with their own `accountCapabilities`, their own `accountId`, and their own state-string namespace. Mailboxes, Threads, Emails, and Identities never cross JMAP-account boundaries.
- **REQ-AUTH-EXT-07** The session descriptor advertises `https://netzhansa.com/jmap/external-accounts` as a server-level capability when external accounts are enabled in deployment config. Operator-side disable hides the surface from the suite (REQ-MAIL-EXT-14 fallback applies).
- **REQ-AUTH-EXT-08** Per-account state surface (read via the `Account/get` extension or an admin-style endpoint, TBD): `connected`, `connecting`, `authentication-failed`, `degraded` (fetch or submit is failing), `disabled` (user-paused). State changes emit JMAP push events on the principal's EventSource feed so the suite can update its status surface (REQ-MAIL-EXT-08).
- **REQ-AUTH-EXT-09** Removal of an external account: drops credentials and OAuth tokens, terminates the IDLE session, and removes the JMAP account from subsequent session descriptors. Mirrored mail is retained by default (read-only archive) and purged only on explicit user request. The external server's mailbox is never modified by removal.
- **REQ-AUTH-EXT-10** Permission scope is per local principal: a `user` role principal may add/remove/manage their own external accounts; an `admin` role principal may not act on another principal's external accounts (no impersonation in v1). External-account credentials are not exposed via the admin API.
- **REQ-AUTH-EXT-11** Auth scopes (REQ-AUTH-SCOPE-01) interact with external accounts as follows: the suite's session cookie's `mail.send` and `mail.receive` scopes apply uniformly across all JMAP accounts in the session (local and external). API keys (REQ-AUTH-SCOPE-04) MAY be scoped per JMAP account at creation time; without an `--account` constraint, an API key applies to all of the principal's accounts.

## Out of scope

- Fine-grained permissions beyond the three roles. **[Amended by `07-access-control.md`: authorization is now grant-per-resource (server/domain/list/mailbox), still deliberately bounded — a small level set per resource kind, not a per-operation permission matrix.]**
- Per-tenant identity isolation (non-goal NG3).
- Kerberos / GSSAPI SASL.
- NTLM anything.
- Self-service account registration (public signup forms). Operator creates accounts.
- Acting as an IMAP/SMTP relay on behalf of external accounts (i.e., letting external IMAP clients connect to herold and have herold proxy IMAP to a back-end). External accounts are mirrored into the local store per REQ-AUTH-EXT-04, not proxied live.
