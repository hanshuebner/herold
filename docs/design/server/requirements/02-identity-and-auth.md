# 02 — Identity and authentication

## Principals

A **principal** is the subject of authentication and authorization. Three kinds:

| Kind | Notes |
|---|---|
| **Individual** | A human account. Has one or more email addresses, one password credential, optional 2FA, optional OAuth-linked identities. |
| **Group** | A set of individual principals. Addressable (mail to the group fans out to members). Not authenticatable. |
| **Admin** | A principal with administrative permissions. Can be an individual with an admin role, not a separate object type. |
- **REQ-AUTH-01** Every email address in the system resolves to exactly one principal (individual or group). No floating addresses.
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
- **REQ-AUTH-58** We are a **relying party only**. We do not expose `/.well-known/openid-configuration`, `/authorize`, `/token`, `/userinfo`, `/jwks` endpoints for third parties to consume our identity. Non-goal NG11.

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
- **REQ-AUTH-42** 2FA applies to: admin UI login, JMAP primary-password login. Does NOT apply to: IMAP/SMTP submission with app passwords (by design).
- **REQ-AUTH-43** Recovery codes: ten one-time codes generated on enrollment, hashed like passwords.

### OAuth 2 bearer-token verification (for HTTP surfaces)

- **REQ-AUTH-70** MUST implement OAuth 2 resource-server behavior for JMAP: `Bearer` token check. Tokens are OIDC access tokens issued by an **external** OIDC provider configured per REQ-AUTH-50; verified by fetching the provider's JWKS and validating signature + claims.
- **REQ-AUTH-71** MUST support OAuth 2 Device Authorization Grant (RFC 8628) for CLI / mail client bootstrap flows (clients that can't host a redirect URI, e.g. Thunderbird).
- **REQ-AUTH-72** SMTP/IMAP `SASL XOAUTH2` / `OAUTHBEARER` accept the same provider-issued bearer tokens and validate via the same JWKS path.
- **REQ-AUTH-73** Acting as an OIDC *provider* (issuing tokens for external apps) — **not a goal** (NG11). We are a relying party only.

## Permissions and authorization

Stalwart has a fine-grained permission matrix with ~80 permissions and role inheritance. We simplify.

- **REQ-AUTH-60** A principal has one of: `user`, `admin`, or `superadmin`.
  - `user`: access to own mail, own Sieve script, own identities, own app passwords.
  - `admin`: everything `user` can do, plus account/domain management, queue inspection, spam training.
  - `superadmin`: everything `admin` can do, plus config reload, TLS cert management, server shutdown, directory backend config.
- **REQ-AUTH-61** The first principal created during bootstrap is `superadmin`. There is always exactly one superadmin at minimum; deleting the last superadmin is rejected.
- **REQ-AUTH-62** Admin actions logged to a dedicated audit log (see `09-operations.md`).
- **REQ-AUTH-63** Mailbox ACLs (IMAP RFC 4314) are a *separate* dimension for shared mailboxes — deferred with shared mailboxes.

## Session model

- **REQ-AUTH-70** IMAP and SMTP submission sessions are stateful per-connection. No shared session cache across reconnects.
- **REQ-AUTH-71** JMAP sessions use short-lived bearer tokens (default 1h) with refresh tokens (default 30 days, bound to IP optionally). Refresh tokens are revocable.
- **REQ-AUTH-72** Admin UI sessions use httpOnly + Secure + SameSite=Strict cookies, 1h idle timeout, absolute 12h max.
- **REQ-AUTH-73** All session tokens verifiable offline (signed JWT or signed opaque token). No per-request DB lookup for hot path auth, but revocation list checked once per minute per session.

## Domain ownership and delegated admin

- **REQ-AUTH-80** A domain belongs to exactly one principal (the domain "owner"). Owner can manage their own aliases, catch-alls, DKIM key rotation.
- **REQ-AUTH-81** Domain ownership proof: DNS TXT record matching a server-issued challenge (similar to ACME's DNS-01). Lightweight verification for the admin UI.
- **REQ-AUTH-82** `admin` role can create domains without ownership proof (they're the operator).

## Provisioning and bootstrap

- **REQ-AUTH-90** On first start with no principals, the server writes a one-time bootstrap token to stdout/log and refuses login until the operator uses it to create the first superadmin.
- **REQ-AUTH-91** The admin CLI has a non-interactive mode for bootstrap (`herold admin bootstrap --password-stdin`) for automated provisioning (Ansible, Kubernetes init containers).
- **REQ-AUTH-92** Auto-provisioning of principals from OIDC first-login is opt-in per provider (REQ-AUTH-56). Default is explicit account creation.

## Auth scopes (cookie + API key)

*(Added 2026-04-26 rev 9: closed-enum scope set carried on session cookies and Bearer API keys; mechanically enforces the public/admin listener split per REQ-OPS-ADMIN-LISTENER-01..03.)*

- **REQ-AUTH-SCOPE-01** Suite session cookies and Bearer API keys MUST carry a closed-enum scope set with defined values `end-user`, `admin`, `mail.send`, `mail.receive`, `chat.read`, `chat.write`, `cal.read`, `cal.write`, `contacts.read`, `contacts.write`, `webhook.publish`; the set is operator-extensible only via spec change (not via runtime config -- drift between cookie issuance and handler enforcement creates auth bugs). Cookies issued at suite-login flow get `[end-user, mail.send, mail.receive, chat.*, cal.*, contacts.*]` for the principal's enabled subsystems; admin login (REQ-AUTH-SCOPE-03) adds `[admin]` after a TOTP step-up if the principal has 2FA enabled.
- **REQ-AUTH-SCOPE-02** Every handler MUST check the auth context's scope set against the handler's required scope; mismatch returns 403 with an RFC 7807 problem detail (NOT 401 -- the caller IS authenticated, just not authorised for this scope). The check is performed at handler entry with no scope-implication chains (admin does NOT implicitly grant end-user; an admin must hold both scopes if they want to use end-user surfaces, which is the cookie's default state for principals with 2FA enabled per REQ-AUTH-SCOPE-01).
- **REQ-AUTH-SCOPE-03** Admin scope step-up: when a principal has 2FA enabled (REQ-AUTH-40), the admin listener `/login` flow MUST require a TOTP code in addition to password before issuing a cookie carrying `admin` scope. Principals without 2FA enabled MAY receive admin scope on password alone (operator's call -- discouraged for any deployment exposing the admin listener publicly; see REQ-OPS-ADMIN-LISTENER-01..03 for the network-layer mitigation).
- **REQ-AUTH-SCOPE-04** API key scope is set at creation time and immutable (rotate to change). The `herold apikey create` command (REQ-ADM-03) takes `--scope` as a comma-separated list of allowed values; default is `[mail.send]` (the most common transactional-app shape per REQ-SEND-30). Admin scope on an API key requires `--allow-admin-scope` to acknowledge the operator-side risk; cookies are the recommended path for human admin access.

## REST session and CSRF (phase 2 additions)

*(Added 2026-04-27: JSON login/logout endpoints and cookie-based auth for the admin REST surface, enabling the Svelte admin SPA at /admin/. See docs/design/server/notes/phase-2-protoui-protoadmin-coverage-audit-2026-04-27.md.)*

- **REQ-AUTH-SESSION-REST**: `internal/protoadmin` MUST accept both `Authorization: Bearer hk_...` API keys and the admin listener session cookie (`herold_admin_session` by default) as valid credentials for `/api/v1/...` endpoints. The session cookie is verified against the same HMAC-SHA256 signing key used by the `protoui` admin login flow so cookies minted by the HTML `/login` and by `POST /api/v1/auth/login` are mutually valid. When the signing key is not configured (zero or fewer than 32 bytes), cookie auth is disabled and the endpoint accepts only Bearer keys (backward-compatible with Phase 1 deployments).

- **REQ-AUTH-CSRF**: All mutating `/api/v1/...` requests authenticated by session cookie MUST present an `X-CSRF-Token` header whose value matches the `herold_admin_csrf` cookie value verified by `crypto/subtle.ConstantTimeCompare`. Bearer-authenticated requests are exempt (no ambient credential). GET/HEAD/OPTIONS (RFC 7231 safe methods) are exempt. On mismatch the endpoint returns 403 with an RFC 7807 `application/problem+json` body with type `csrf_mismatch`.

- **REQ-AUTH-JSON-LOGIN**: `POST /api/v1/auth/login` accepts `{email, password, totp_code?}` (unauthenticated, rate-limited per source IP). On success it issues `herold_admin_session` (HttpOnly, Secure, SameSite=Strict, Path=/) and `herold_admin_csrf` (non-HttpOnly, Secure, SameSite=Strict, Path=/) cookies and returns `{principal_id, email, scopes:[...]}`. On bad credentials it returns 401. On a TOTP-enabled principal with missing or wrong `totp_code` it returns 401 with `{step_up_required: true}` in the problem detail extension fields.

- **REQ-AUTH-JSON-LOGOUT**: `POST /api/v1/auth/logout` (authenticated by cookie or Bearer) clears both cookies by issuing `MaxAge=-1` Set-Cookie headers and returns 204 No Content. Bearer-authenticated callers receive the cookie-clear headers harmlessly (their session was not cookie-based).

- **REQ-AUTH-COOKIE-PATH**: Session cookies on both the admin and public listeners use `Path=/` so the same browser session accompanies `/api/v1/...`, `/admin/...`, and `/ui/...` requests on the same listener. Cross-listener isolation is enforced by the distinct cookie name (`herold_admin_session` vs `herold_public_session`), not by path scoping. CSRF cookies also use `Path=/`.

## Out of scope

- Fine-grained permissions beyond the three roles.
- Per-tenant identity isolation (non-goal NG3).
- Kerberos / GSSAPI SASL.
- NTLM anything.
- Self-service account registration (public signup forms). Operator creates accounts.
