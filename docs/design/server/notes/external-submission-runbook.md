# External SMTP submission -- on-call runbook

This runbook covers the external SMTP submission feature
(REQ-AUTH-EXT-SUBMIT-01..10, Phase 6). Use it when:

- A user reports that sent mail is not leaving the server.
- Alerts fire on `herold_external_submission_total{outcome="auth-failed"}`.
- Alerts fire on `herold_external_submission_oauth_refresh_total{outcome="failure"}`.

For operator setup (data-key configuration, OAuth provider registration,
manual credential entry), see
`docs/operator/external-smtp-submission.md`.

## Quick diagnostic checklist

Work through these steps in order. Each step narrows the problem to one of the
sections below.

**Step 1. Check the submission state for the affected Identity.**

The endpoint returns the current state without any credential material
(REQ-AUTH-EXT-SUBMIT-04). Obtain the session cookie and CSRF token from the
user's browser (DevTools > Application > Cookies for `herold_admin_session`
and `herold_admin_csrf`) or use an admin API key:

```
curl -s \
  --cookie "herold_admin_session=<value>" \
  -H "X-CSRF-Token: <value>" \
  https://<deployment>/api/v1/identities/<identity-id>/submission
```

Expected response in steady state:

```json
{
  "configured": true,
  "submit_host": "smtp.gmail.com",
  "submit_port": 465,
  "submit_security": "implicit_tls",
  "submit_auth_method": "oauth2",
  "state": "ok"
}
```

If `configured` is `false`, no external submission credentials are stored for
this Identity; the user needs to configure them in Settings. If `state` is
`auth-failed`, go to the Auth failure section. If `state` is `unreachable`,
go to the Unreachable section.

**Step 2. Check the metrics endpoint.**

```
curl -s http://127.0.0.1:9090/metrics | grep herold_external_submission
```

In a deployment that has processed external submissions successfully, the
output should include counter values greater than zero on the `ok` outcome:

```
herold_external_submission_total{outcome="ok"} 47
herold_external_submission_total{outcome="auth-failed"} 0
herold_external_submission_total{outcome="unreachable"} 0
herold_external_submission_oauth_refresh_total{outcome="success"} 12
herold_external_submission_oauth_refresh_total{outcome="failure"} 0
herold_external_submission_active_identities 3
```

A non-zero `auth-failed` or `failure` count that is rising indicates an
ongoing problem. `herold_external_submission_active_identities` shows the
total number of OAuth-configured identities, updated on every sweeper tick.
A value of `0` with OAuth Identities configured means the sweeper cannot
find any rows with `submit_auth_method = 'oauth2'`; see the Sweeper section.

**Step 3. Check the structured logs.**

```
journalctl -u herold | grep -E '(extsubmit|submission\.external)'
```

In normal operation you should see `extsubmit.sweeper: refreshed token` debug
entries every time the sweeper refreshes a token, and no `WARN` or `ERROR`
entries. The sweeper logs one entry per refreshed row at DEBUG level and one
`extsubmit.sweeper: token refresh failed` at WARN level on failure. Key
structured fields:

- `identity_id`: the affected Identity's id.
- `category`: `auth`, `network`, or `unknown`.
- `correlation_id`: links the log entry to the matching audit-log record.

The sweeper also emits `extsubmit.sweeper: started` at INFO level on boot.
Its absence in the most recent startup means the feature flag
`[server.external_submission].enabled` is not `true`.

**Step 4. Correlate with the audit log.**

The audit log entry for a sweeper-triggered failure has
`action = "submission.external.refresh_failure"`. For a submission failure
from a user send, it has `action = "submission.external.failure"`. The
`correlation_id` field in both the slog record and the audit entry is the
same value; use it to match the two across log sinks.

---

## Auth failure

The Identity is in `auth-failed` state when either the SMTP server returned a
535 response to the AUTH command, or the OAuth token endpoint returned a 4xx
response to a refresh request.

**Identify the failure category from the audit log.**

Locate the most recent `submission.external.refresh_failure` or
`submission.external.failure` audit entry for the affected Identity. The
`category` field is one of:

- `auth` -- the token endpoint returned `invalid_grant`, `unauthorized_client`,
  or a similar 4xx code. The refresh token has been invalidated.
- `network` -- the connection to the token endpoint or SMTP server failed.
  Treat as Unreachable.
- `unknown` -- a failure not classified as auth or network.

**For OAuth `invalid_grant` (category: auth):**

The refresh token has been invalidated at the provider. This happens when
the user revokes herold's access in their Google or Microsoft account security
settings, when the OAuth application is suspended in the provider console, or
when a Microsoft 365 tenant conditional access policy blocks the sign-in.

Resolution: the user must re-authenticate. In the suite, the "Re-authenticate"
button in the compose toast and in the Settings > Identities warning badge both
open the OAuth flow for the affected Identity. After the user completes the
flow, the sweeper stores fresh tokens and the state returns to `ok` on the
next successful submission.

To confirm the re-authentication succeeded without waiting for a send,
re-run Step 1 and verify `state` is `ok`.

For Microsoft 365, check the tenant's sign-in logs. In `portal.azure.com`,
navigate to "Azure Active Directory" > "Sign-in logs" > "Service principal
sign-ins". Filter by the herold application name and by the timestamp of the
first `auth-failed` event. A conditional access failure appears as a blocked
sign-in with a policy name in the "Conditional access" column. Coordinate with
the tenant administrator to adjust the policy or add the herold application to
an exclusion.

**For password auth (535 response):**

An SMTP 535 response to AUTH means the server rejected the credential. The
most common cause is a revoked or expired app-specific password.

Ask the user to check the provider's security settings:
- Gmail: Google Account > Security > 2-Step Verification > App passwords. If
  the app password is missing, it was revoked. Generate a new one and re-enter
  it in Settings > Identities.
- Microsoft 365: if legacy authentication is enabled and an app password was
  in use, check the Microsoft account security settings. Note that Microsoft
  is progressively disabling legacy auth across tenants; if the SMTP
  server starts rejecting credentials tenant-wide, the affected users will
  need to switch to OAuth.

**For operator-level OAuth client rotation:**

If many Identities across different users enter `auth-failed` simultaneously,
the operator-level OAuth client secret was likely rotated at the provider
without updating `system.toml`. Update `client_secret_ref` to reference the
new secret value, reload the configuration with `herold server reload` or
SIGHUP, and ask affected users to re-authenticate. Their stored refresh tokens
reference the old client credentials and will not be usable with the new
secret.

---

## Unreachable

The `unreachable` state means herold could not establish a TCP connection to
`submit_host:submit_port`, could not complete a TLS handshake, or received no
SMTP greeting within the dial timeout (30 seconds, set in
`internal/extsubmit/submitter.go`).

**Identify the host and port from the submission record.**

Use the Step 1 `GET /api/v1/identities/{id}/submission` response to find
`submit_host` and `submit_port`. The `submit_security` field tells you whether
to use implicit TLS or STARTTLS in the diagnostics below.

**Test TCP connectivity from the herold host:**

```
nc -zv <submit_host> <submit_port>
```

For example:

```
nc -zv smtp.gmail.com 465
nc -zv smtp.office365.com 587
```

A successful connection prints `succeeded!` and closes. If `nc` hangs or
prints `refused`, egress is blocked. Check the following:

- The herold host's firewall or security group rules. Cloud providers
  (AWS, GCP, Azure, Hetzner Cloud) commonly default-deny outbound TCP on
  port 25, 465, and 587. Request an egress rule or allowance from the cloud
  provider's support or networking console.
- The on-premises network policy, if applicable.

**Test TLS for STARTTLS endpoints:**

```
openssl s_client -connect smtp.office365.com:587 -starttls smtp
```

Look for `Verify return code: 0 (ok)`. A non-zero return code means the TLS
chain failed verification, which appears in herold's logs as a connection
error. Ensure the herold host's system CA bundle is current. On Debian/Ubuntu:

```
apt-get update && apt-get install --only-upgrade ca-certificates
```

**Check for IP reputation issues:**

If TCP connects but the SMTP greeting is an error response, the herold host's
IP may be on a blocklist at the remote provider. The diagnostic text in the
`submission.external.failure` audit entry contains the SMTP reply code and
text. A response code of 421 or 550 accompanied by a reference to an IP
reputation or block list confirms this. Use the provider's postmaster tools
to request delisting:

- Gmail: `postmaster.google.com`
- Microsoft: `sender.office.com`

Shared-IP cloud deployments are most susceptible; a dedicated IP or a
reputable SMTP relay service resolves the issue without relisting.

---

## Sweeper not refreshing tokens

Indicators: `herold_external_submission_oauth_refresh_total{outcome="success"}`
has not incremented in the past several minutes, and OAuth Identities are
entering `auth-failed` at their natural token expiry rather than being
refreshed 60 seconds before it.

**Step 1. Confirm the feature flag is on:**

```
grep external_submission /etc/herold/system.toml
```

Both `[server.external_submission]` and `enabled = true` must appear. If
`enabled` is absent or false, the sweeper was never started. Enabling it
requires editing `system.toml` and restarting the server (SIGHUP does not
start new goroutines, only re-reads config values that support hot reload).

**Step 2. Confirm the sweeper started:**

```
journalctl -u herold | grep "extsubmit.sweeper: started"
```

If the line is present in the most recent startup, the sweeper is running.
If it is absent and the feature flag is on, look for an earlier startup error
related to the data key or external submission configuration.

**Step 3. Confirm the data key resolved at startup:**

If the data key reference resolves to an empty value or shorter than 32 bytes,
the feature is disabled at startup even when `enabled = true`. Check the
startup log for any `sysconfig:` error lines.

**Step 4. Check the active-identities gauge:**

```
curl -s http://127.0.0.1:9090/metrics | grep herold_external_submission_active_identities
```

The gauge counts all rows with `submit_auth_method = 'oauth2'`, regardless of
whether they are currently due for a refresh. It is updated on every sweeper
tick (default 60 s). If the gauge is `0` but OAuth Identities have been
configured, the rows may not have been written with `submit_auth_method =
'oauth2'`. Query the store directly:

SQLite:

```
sqlite3 /var/lib/herold/herold.db \
  "SELECT identity_id, state, refresh_due_us FROM identity_submission;"
```

Postgres:

```
psql "$HEROLD_DB_URL" \
  -c "SELECT identity_id, state, refresh_due_us FROM identity_submission;"
```

Rows with `refresh_due_us` as null were created from an OAuth flow where the
token response did not include `expires_in`. The sweeper only picks up rows
with a non-null `refresh_due_us`. In that case the token cannot be refreshed
proactively; the user must re-authenticate when the token expires.

**Step 5. Check sweeper concurrency:**

`[server.external_submission].sweeper_workers` defaults to 4
(`internal/extsubmit/sweeper.go` `defaultWorkerCount`). Raising it above 4
is useful only when a large number of OAuth Identities are due for refresh
simultaneously and the `herold_external_submission_oauth_refresh_total`
counter shows that failure counts are climbing due to refresh timeouts, not
auth errors. Do not increase this value beyond 16 without profiling; the
token-endpoint HTTP calls are the bottleneck, not CPU.

---

## Credential material in logs

Treat any appearance of a password, access token, or refresh token in log
output as a security incident. Follow these steps without delay.

**1. Rotate the exposed credential at the provider immediately.**

Do not wait for the investigation to complete. For OAuth tokens: revoke the
affected application's access in the provider's security console (Google:
account security > third-party app access; Microsoft: app permissions in
the account or Azure AD). For passwords: change or revoke the app password.
Ask the affected user to re-authenticate in Settings > Identities after
rotation.

**2. Identify the attribute key.**

Find the log record and note the slog attribute key under which the credential
appeared. The key is the field name immediately before the credential value in
the structured output.

**3. Check `DefaultSecretKeys`.**

Open `internal/observe/secret.go`. The `DefaultSecretKeys` slice covers:
`password`, `token`, `access_token`, `refresh_token`, `xoauth2_token`,
`bearer_token`, `api_key`, `secret`, `client_secret`, `authorization`,
`cookie`, `set-cookie`. If the key that leaked is not in that list, the
redaction filter did not cover it.

**4. File a bug.**

Open an issue with the attribute key name. The fix is one line: add the key
to `DefaultSecretKeys` in `internal/observe/secret.go`. The PR requires a
`security-reviewer` approval. The change must ship before the next release.

**5. Check the logger construction path.**

If the attribute key is already in `DefaultSecretKeys` and the leak still
occurred, the logger that emitted the record was not constructed through
`observe.NewLogger`. In that construction path, `NewRedactHandler` is wrapped
outermost in the handler chain; a logger constructed any other way does not
carry the redaction filter. Identify the call site in the code and ensure it
obtains its logger through the standard path.

**6. Audit all log sinks.**

Check every configured log destination (OTLP exporters, file sinks, external
log aggregators) for the same credential value and purge or redact the affected
records. The exposure window runs from the time of the first log record
containing the credential to the time the affected logs are purged.

---

## Metrics reference

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `herold_external_submission_total` | Counter | `outcome` | Submission attempts by outcome |
| `herold_external_submission_duration_seconds` | Histogram | `outcome` | Submit call wall-clock time |
| `herold_external_submission_oauth_refresh_total` | Counter | `outcome` | OAuth refresh attempts |
| `herold_external_submission_active_identities` | Gauge | (none) | OAuth identity count |

Outcome values for `herold_external_submission_total`:
`ok`, `auth-failed`, `unreachable`, `permanent`, `transient`.

Outcome values for `herold_external_submission_oauth_refresh_total`:
`success`, `failure`.
