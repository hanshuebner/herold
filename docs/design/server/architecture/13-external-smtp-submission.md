# 13 -- External SMTP submission per Identity

How herold routes outbound mail through an external SMTP server on a
per-Identity basis, seals and unseals credential material, refreshes OAuth
tokens in the background, and enforces self-only access to submission
credentials. Behavioural requirements in REQ-AUTH-EXT-SUBMIT-01..10 and
REQ-MAIL-SUBMIT-02..06.

For operator setup (data-key configuration, OAuth provider registration,
manual credential entry) see the admin manual chapter
`external-smtp-submission` (served at `/admin/#/help/external-smtp-submission`).

For on-call diagnostics (metrics, structured-log fields, sweeper debugging,
security-incident response) see
`docs/design/server/notes/external-submission-runbook.md`.

---

## Per-Identity routing

External SMTP submission is scoped to a single JMAP Identity (RFC 8621
section 6). Each Identity carries its own submission endpoint, authentication
method, and sealed credential independently of every other Identity the same
principal owns. A principal with three Identities can use herold's outbound
queue for two and route the third through a personal Gmail account.

The routing decision is made at submission time in
`internal/extsubmit/submitter.go` (`Submit`), which is invoked by the JMAP
EmailSubmission method handler in
`internal/protojmap/mail/emailsubmission/methods.go`. The handler checks
whether the selected Identity has a row in the `identity_submission` table
(added in database migration 0032 on both SQLite and Postgres backends).

- If no row exists, the message follows the normal path: herold's outbound
  queue, DKIM signing, and the delivery machinery in `internal/queue`.
- If a row exists, `Submit` opens a TLS connection to the configured
  `submit_host:submit_port`, authenticates with the sealed credential, and
  submits the message via SMTP.

There is no deployment-wide switch that forces all mail through an external
server; the choice is made Identity by Identity at submission time.

## Credential sealing

Credentials (passwords and OAuth tokens) are never stored in plaintext.
The sealing path is `internal/secrets/aead.go`, which implements
ChaCha20-Poly1305 authenticated encryption. The data key is a 32-byte
symmetric key referenced by `[server.secrets].data_key_ref`.

When a user submits credentials via the Settings form:

1. The plaintext credential is received by the REST handler.
2. `aead.Seal` encrypts it under the data key and prepends a `v1:` version
   prefix. The prefix allows a future rotation tool to identify ciphertexts
   produced under a specific key version.
3. The ciphertext is written to `identity_submission.sealed_credential`.
4. The plaintext is discarded and not written to any log sink.

At submission or token-refresh time:

1. `aead.Unseal` decrypts the credential in memory.
2. The plaintext is used for one submission attempt or one OAuth token
   exchange.
3. The plaintext is zeroed after use.

The slog redaction handler in `internal/observe/secret.go` (`NewRedactHandler`,
`DefaultSecretKeys`) filters any slog attribute whose key matches the list
before the record reaches any configured sink. Keys covered:
`password`, `token`, `access_token`, `refresh_token`, `xoauth2_token`,
`bearer_token`, `api_key`, `secret`, `client_secret`, `authorization`,
`cookie`, `set-cookie`. Matching is case-insensitive, exact against the full
attribute key name.

Key rotation is not implemented in v1. The `v1:` prefix on every ciphertext
means a future rotation tool can enumerate affected rows without a full scan.
Until rotation support ships, re-entering all external submission credentials
is required after a data-key rotation; affected Identities enter `auth-failed`
state when the old key is withdrawn.

## Authentication methods

Two methods are supported.

**Password authentication** uses SASL AUTH PLAIN or AUTH LOGIN after TLS.
`internal/extsubmit/submitter.go` prefers PLAIN when the server advertises it
and falls back to LOGIN automatically. Gmail requires an app-specific password
for accounts with two-factor authentication enabled; Microsoft 365 tenant
policy determines whether legacy auth or app passwords are allowed.

**OAuth 2.0** uses SASL XOAUTH2 (RFC 7628). The access token is obtained
during the initial OAuth authorization code flow. The background sweeper
(`internal/extsubmit/sweeper.go`) maintains the token without user interaction.

A live probe (`internal/extsubmit/probe.go`) is run before any credentials
are persisted. The probe opens an AUTH-only SMTP session, verifies the
credentials are accepted, and returns an error category (`auth`, `network`,
`unknown`) on failure. No message is sent during the probe.

## OAuth refresh sweeper

The sweeper (`internal/extsubmit/sweeper.go`) runs as a goroutine within the
herold process. It is started when `[server.external_submission].enabled = true`
and the data key resolves successfully. It cannot be started or stopped by
a configuration reload (SIGHUP); a full restart is required to change its
running state.

Tick cadence: 60 seconds.

On each tick:

1. The sweeper queries `identity_submission` for rows where
   `submit_auth_method = 'oauth2'` and `refresh_due_us` is in the past.
2. Up to `sweeper_workers` (default: 4, configurable via
   `[server.external_submission].sweeper_workers`) concurrent refresh
   attempts are dispatched using `internal/extsubmit/oauth.go`.
3. On success: the new sealed access token is written back; `refresh_due_us`
   is set to 80% of the new token's `expires_in` lifetime.
4. On failure: `state` is set to `auth-failed`; `refresh_due_us` is left
   unchanged. The sweeper retries every 60 seconds until the user
   re-authenticates.

`refresh_due_us` is null for tokens whose OAuth response did not include
`expires_in` (non-standard providers). Null rows are not picked up by the
sweeper; those tokens must be refreshed manually by the user re-authenticating.

Metrics emitted by the sweeper:

| Metric | Type | Description |
|--------|------|-------------|
| `herold_external_submission_active_identities` | Gauge | OAuth Identity rows seen on last tick |
| `herold_external_submission_oauth_refresh_total{outcome}` | Counter | Refresh attempts by outcome |

## Self-only authorization

REST endpoints for Identity submission credentials (REQ-AUTH-EXT-SUBMIT-04)
are gated by a `requireSelfOnly` check in the handler. Only the principal who
owns the Identity may call `GET`, `PUT`, or `DELETE` on
`/api/v1/identities/{id}/submission`. Principals with the `admin` role are not
exempt; there is no impersonation path in v1.

`GET` returns the configuration and state without any credential material
(sealed ciphertext is never returned to the client).

## DKIM skip

Messages submitted via an external endpoint skip local DKIM signing
(REQ-AUTH-EXT-SUBMIT-06). The external server is responsible for signing
under its own DKIM key for its own domain. The skip is applied in
`internal/extsubmit/submitter.go` before the message is handed to the external
SMTP connection; the outbound queue DKIM path is never entered.

Operators must ensure the external provider accepts the chosen `From:` address
before users start sending. Gmail requires "Send mail as" verification in Gmail
settings; Microsoft 365 requires send-as permissions on the mailbox or an
Exchange Online shared mailbox.

## OAuth provider registration (system.toml)

Registering a provider at the operator level enables the one-click "Sign in
with Google" or "Sign in with Microsoft" OAuth flow in the suite's Identity
settings. The `[server.oauth_providers.<name>]` block in `system.toml` carries:

- `client_id` -- the OAuth application client ID from the provider console.
- `client_secret_ref` -- a reference to the client secret (env var or file;
  never inline).
- `auth_url`, `token_url` -- provider-specific OAuth 2.0 endpoints.
- `scopes` -- scope list. M365 requires `offline_access` in the list;
  without it the token endpoint does not issue a refresh token and the
  sweeper cannot maintain the session.

The fixed redirect URI for all providers and all Identity flows on a
deployment is:

```
https://<herold-hostname>/api/v1/oauth/external-submission/callback
```

Herold carries the Identity id in the OAuth state token, not in the redirect
URI, so one provider registration covers every user.

When the OAuth callback handler receives a successful authorization code
response, it uses the provider name (`gmail`, `m365`) to set SMTP host
defaults automatically:

- `gmail`: `smtp.gmail.com`, port 465, implicit TLS.
- `m365`: `smtp.office365.com`, port 465, implicit TLS.

No manual SMTP host configuration is needed when using the OAuth flow with
these provider names.

## Submission state model

The `state` column of `identity_submission` drives the suite badge display
(REQ-MAIL-SUBMIT-04):

| State | Meaning | Suite display |
|-------|---------|---------------|
| `ok` | Last submission or token refresh succeeded | "External" badge (neutral) |
| `auth-failed` | Last AUTH attempt was rejected (SMTP 535 or OAuth 4xx) | Warning badge; compose toast offers one-click "Re-authenticate" |
| `unreachable` | TCP connect or TLS handshake failed | Warning badge |

Submission is not retried after a failure. Subsequent JMAP EmailSubmission
calls for the same Identity also fail until the user intervenes.

## Data-key / config design

`[server.external_submission].enabled = true` requires
`[server.secrets].data_key_ref` to resolve to a value of at least 32 bytes.
The server refuses to start if the dependency is unmet, emitting:

```
sysconfig: [server.external_submission] enabled requires [server.secrets].data_key_ref; set [server.secrets].data_key_ref to enable [server.external_submission]
```

Two reference forms are accepted for `data_key_ref`. Inline values are
rejected at startup (STANDARDS section 9):

- `$ENV_VAR` -- reads from the named environment variable.
- `file:/path/to/key.hex` -- reads from the named file.

The value must be a 64-character hexadecimal string encoding a 32-byte key.
