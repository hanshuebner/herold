# External SMTP submission — on-call runbook

<!-- TODO docs-writer: This file is a scaffold for on-call engineers. The
     technical facts below are accurate. Expand each section into a
     step-by-step diagnostic and remediation guide. Cross-reference with
     docs/operator/external-smtp-submission.md for operator setup. -->

This runbook covers the external SMTP submission feature
(REQ-AUTH-EXT-SUBMIT-01..10, Phase 6). Use it when:

- A user reports that sent mail is not leaving the server.
- Alerts fire on `herold_external_submission_total{outcome="auth-failed"}`.
- Alerts fire on `herold_external_submission_oauth_refresh_total{outcome="failure"}`.

## Quick diagnostic checklist

<!-- TODO docs-writer: Turn each bullet into a numbered step with the exact
     command or query to run and what the output should look like.

     1. Check submission state for the affected identity:
        GET /api/v1/identities/{id}/submission
        Expected: {"configured": true, "state": "ok"}
        If state == "auth-failed": go to "Auth failure" section.
        If state == "unreachable": go to "Unreachable" section.

     2. Check metrics:
        curl -s http://admin-listener:9090/metrics | grep herold_external_submission
        herold_external_submission_total{outcome="ok"} should be > 0 if any
        successful submission has occurred.

     3. Check logs for the last 15 minutes:
        Look for "extsubmit.sweeper" and "extsubmit" at WARN/ERROR.
        Key log fields:
          identity_id: the affected identity
          category: auth | network | unknown
          correlation_id: links to the audit log entry

     4. Check audit log:
        The action "submission.external.refresh_failure" carries
        category and correlation_id. Never contains the token value.
-->

## Auth failure

<!-- TODO docs-writer: Step-by-step guide for diagnosing and resolving
     auth-failed state.

     Common causes:
     - OAuth access token cannot be refreshed: invalid_grant from token
       endpoint (user revoked access, refresh token expired, provider
       changed scopes).
     - App-specific password revoked at the external provider.
     - Gmail: account exceeded daily sending limits; temporary auth failure.
     - Microsoft 365: conditional access policy blocking the app.

     Resolution steps:
     1. Confirm the identity is in auth-failed state via the admin API.
     2. Check the audit log for the failure category and correlation ID.
     3. For OAuth:
        a. Check the provider console for revoked access or expired tokens.
        b. Ask the user to re-authenticate via Settings > Identities.
        c. After re-auth, the PUT endpoint probes the credentials and
           updates state to ok on success.
     4. For password:
        a. Ask the user to verify the app password at the provider.
        b. Ask the user to re-enter the credential in Settings > Identities.

     Operator-side escalation:
     - If many identities enter auth-failed simultaneously, check whether the
       OAuth application client_secret has been rotated at the provider without
       updating system.toml.
     - Rotate [server.oauth_providers.<name>].client_secret_ref and reload.
-->

## Unreachable

<!-- TODO docs-writer: Step-by-step for network-layer failures.

     Common causes:
     - Firewall blocking outbound 587 / 465 from the herold server.
     - DNS failure resolving submit_host.
     - The external SMTP server is down or rate-limiting.

     Diagnostic steps:
     1. From the herold server: nc -zv smtp.gmail.com 587 (or submit_host:submit_port).
     2. dig smtp.gmail.com from the herold server.
     3. Check herold_external_submission_total{outcome="unreachable"} trend.

     Resolution:
     - Add a firewall allow rule for outbound TCP 587 / 465 to the SMTP host.
     - If the external server is down: nothing to do; state will flip back to ok
       on the next successful submission.
-->

## Sweeper not refreshing tokens

<!-- TODO docs-writer: Diagnosis when tokens are expiring but not being refreshed.

     Indicators:
     - herold_external_submission_oauth_refresh_total{outcome="success"} is 0
       and growing stale.
     - Identities with oauth2 auth method entering auth-failed at expiry.

     Diagnostic steps:
     1. Confirm [server.external_submission].enabled = true.
     2. Confirm [server.secrets].data_key_ref is set and resolves.
     3. Check logs for "extsubmit.sweeper: started".
     4. Check herold_external_submission_active_identities gauge > 0 if
        any OAuth identities are registered.
     5. Check that refresh_due_us is set on the row (non-null) via the
        store debug endpoint (if available) or a direct DB query.
-->

## Credential material in logs

<!-- TODO docs-writer: Incident response for credential material appearing in logs.

     Steps:
     1. Immediately redact the affected log files or segments.
     2. Rotate the affected credential (revoke the OAuth token at the provider
        or change the app-specific password).
     3. Ask the affected user to re-authenticate.
     4. File a bug referencing the DefaultSecretKeys list in
        internal/observe/secret.go — the attribute key containing the token
        was not in the list.
     5. Add the key to DefaultSecretKeys and ship a patch.
     6. Audit other log sinks (OTLP, file sinks) for the same token.
-->

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
