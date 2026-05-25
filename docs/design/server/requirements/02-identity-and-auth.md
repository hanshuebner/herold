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
- **REQ-AUTH-44** TOTP enrollment MUST be mandatory for principals with the `admin` or `superadmin` role. The admin-login flow MUST refuse to issue an `admin`-scoped session for a principal that does not have TOTP enrolled, returning a step-up error pointing the operator at the enrollment endpoint. The granting-admin path (PATCH `/principals/{id}` to add `admin`) MUST refuse to grant the role until the target principal has TOTP enrolled, OR MUST simultaneously force enrollment on next sign-in via a one-time enrollment ticket (operator choice; default = refuse). The bootstrap superadmin is the sole exception: the password + API key minted by `herold bootstrap` are usable once for the express purpose of enrolling TOTP, after which the same enforcement applies on every subsequent sign-in.
  - Rationale: an admin listener fronted by a reverse proxy on the public internet has no IP allowlist by default; mandatory 2FA is what makes the admin surface safe to expose.
  - Implementation note: the existing login handler already enforces TOTP **when enrolled** (REQ-AUTH-SCOPE-03); this requirement closes the gap where a principal with the admin role but no TOTP can still sign in.

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
- **REQ-AUTH-72** Admin UI sessions use httpOnly + Secure + SameSite=Strict cookies. Default idle timeout 1h, default absolute lifetime 8h, hard ceiling 12h. Both timeouts MUST be operator-configurable under `[server.ui] session.admin_idle_ttl` and `[server.ui] session.admin_absolute_ttl`; configured values above the 12h ceiling are rejected at config-load time. The login handler MUST refuse to issue a session with TTL above the configured `admin_absolute_ttl`.
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

- **REQ-AUTH-JSON-LOGIN**: `POST /api/v1/auth/login` accepts `{email, password, totp_code?}` (unauthenticated, rate-limited per source IP). On success it issues `herold_admin_session` (HttpOnly, Secure, SameSite=Strict, Path=/) and `herold_admin_csrf` (non-HttpOnly, Secure, SameSite=Strict, Path=/) cookies and returns `{principal_id, email, scopes:[...]}`. On bad credentials it returns 401. On a TOTP-enabled principal with missing or wrong `totp_code` it returns 401 with a top-level `step_up_required: true` field in the RFC 7807 problem document (alongside the standard `type`, `title`, `status`, `detail` fields). Failed login attempts MUST be audit-logged with `action="auth.login"`, `outcome=failure`, `subject="email:<attempted-email>"`, and a `message` distinguishing the failure mode (per REQ-ADM-300, REQ-ADM-303); successful logins are audit-logged with `actor=principal/<id>`, `subject="principal:<email>"`, `outcome=success`.

- **REQ-AUTH-JSON-LOGOUT**: `POST /api/v1/auth/logout` (authenticated by cookie or Bearer) clears both cookies by issuing `MaxAge=-1` Set-Cookie headers and returns 204 No Content. Bearer-authenticated callers receive the cookie-clear headers harmlessly (their session was not cookie-based). Sessions are stateless HMAC-signed cookies; logout invalidates the client-side cookies only. Server-side revocation is not implemented; residual sessions on a stolen device expire when the cookie's TTL elapses (per `[server.ui].session_ttl`). Logout MUST be audit-logged with `action="auth.logout"`, `subject="principal:<email>"` of the calling principal, `outcome=success`.

- **REQ-AUTH-COOKIE-PATH**: Session cookies on both the admin and public listeners use `Path=/` so the same browser session accompanies `/api/v1/...`, `/admin/...`, and `/ui/...` requests on the same listener. Cross-listener isolation is enforced by the distinct cookie name (`herold_admin_session` vs `herold_public_session`), not by path scoping. CSRF cookies also use `Path=/`.

- **REQ-AUTH-JSON-WHOAMI**: `GET /api/v1/auth/whoami` (authenticated by cookie or Bearer) returns 200 + `{principal_id, email, scopes:[...]}` for a valid session, or 401 when no valid credential is present. The endpoint is a safe GET method and therefore exempt from CSRF checking (REQ-AUTH-CSRF). The admin SPA calls this endpoint on page load to probe session state without requiring a full server-status round-trip. Additionally, `GET /api/v1/server/status` includes the same `{principal_id, email, scopes}` fields in its response body so the admin SPA's existing bootstrap probe can populate auth state from a single request.

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
- **REQ-AUTH-EXT-SUBMIT-10** Inbound is **not in scope**. Operators arrange forwarding at the external provider (Gmail "Forwarding and POP/IMAP", M365 mailbox forwarding, etc.) so inbound mail still arrives at the local herold mailbox via REQ-FLOW-\*. If an operator later wants bidirectional sync, the deferred broader spec (next section) covers it; this v1 surface does not preclude it but does not deliver it.

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

## External transport identities (deferred)

*(Added 2026-04-29: scopes a future "external mail accounts" feature where a herold principal aggregates one or more external IMAP+SMTP accounts. Spec-only; not scheduled for v1 implementation. Web-side counterpart: `../../web/requirements/02-mail-basics.md` § External mail accounts.)*

The model: an individual principal MAY associate one or more **external mail accounts** with their local principal. Each external account contributes its own JMAP account to the principal's session (RFC 8620 §2), with its own Mailbox tree, Identity set, state strings, and Sieve script. The local principal remains primary — authentication, password, 2FA, and admin authority are unaffected by external accounts.

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

- Fine-grained permissions beyond the three roles.
- Per-tenant identity isolation (non-goal NG3).
- Kerberos / GSSAPI SASL.
- NTLM anything.
- Self-service account registration (public signup forms). Operator creates accounts.
- Acting as an IMAP/SMTP relay on behalf of external accounts (i.e., letting external IMAP clients connect to herold and have herold proxy IMAP to a back-end). External accounts are mirrored into the local store per REQ-AUTH-EXT-04, not proxied live.
