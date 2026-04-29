# External SMTP submission per Identity

<!-- TODO docs-writer: This file is a scaffold. All sections marked with TODO
     need real prose before the operator manual is published. The technical
     facts in each TODO are accurate; the docs-writer should expand them into
     readable, operator-facing documentation. -->

This document explains how to configure herold to submit outbound mail through
an external SMTP server on a per-Identity basis (REQ-AUTH-EXT-SUBMIT-01..10).

The v1 use case: send through Gmail, Microsoft 365, Fastmail, or a corporate
SMTP relay while using herold for storage, search, JMAP, and the suite UI.
Inbound mail continues to arrive at herold via forwarding configured at the
external provider.

## Overview

<!-- TODO docs-writer: Write 3-5 paragraphs covering:
     - What the feature does: per-Identity external SMTP submission.
     - What it does NOT do (inbound is out of scope; operators arrange
       forwarding at the external provider).
     - The two auth methods: password (app-specific password) and oauth2
       (server-mediated OAuth flow via configured providers).
     - The credential lifecycle: credentials are encrypted at rest with
       the server data key; the plaintext exists only during an active
       submission attempt or an OAuth refresh round-trip.
     - The background token refresh sweeper: runs every 60 seconds,
       refreshes tokens 60 seconds before expiry, flips state to
       auth-failed on failure.
     - State surface: the suite shows "External" badge on Identities with
       submission credentials; auth-failed / unreachable surfaces as a
       warning badge.
-->

## Data-key setup

<!-- TODO docs-writer: Write a step-by-step guide covering:
     - Why a data key is required: credential encryption at rest.
     - How to generate a 32-byte key (e.g. `openssl rand -hex 32`).
     - How to set [server.secrets].data_key_ref in system.toml pointing
       to the key via $ENV_VAR or file:/path.
     - That setting [server.external_submission].enabled = true without
       a data_key_ref causes the server to refuse to start (boot-time
       hard-fail).
     - Key rotation: not supported in v1; credentials must be re-entered
       after a key rotation.

Example system.toml snippet:

  [server.secrets]
  data_key_ref = "$HEROLD_DATA_KEY"

  [server.external_submission]
  enabled = true
  sweeper_workers = 4  # optional, default 4
-->

## OAuth provider registration

<!-- TODO docs-writer: Write a step-by-step guide for each supported provider.
     Include:

     ### Gmail / Google Workspace

     Steps:
     1. Create an OAuth 2.0 client ID in Google Cloud Console
        (Application type: Web application).
     2. Add the herold OAuth callback redirect URI:
        https://<your-herold-hostname>/api/v1/submission/oauth/callback
     3. Enable the Gmail API for the project.
     4. Add to system.toml:

          [server.oauth_providers.gmail]
          client_id     = "123456789012-abc.apps.googleusercontent.com"
          client_secret_ref = "$HEROLD_GMAIL_CLIENT_SECRET"
          auth_url      = "https://accounts.google.com/o/oauth2/v2/auth"
          token_url     = "https://oauth2.googleapis.com/token"
          scopes        = ["https://mail.google.com/"]

     5. Set HEROLD_GMAIL_CLIENT_SECRET in the server environment.
     6. Reload: `herold server reload` or send SIGHUP.

     ### Microsoft 365

     Steps (similar shape to Gmail):
     - Register an application in Azure AD (Entra ID).
     - Add redirect URI.
     - Required scopes: https://outlook.office.com/SMTP.Send offline_access
     - provider name: "microsoft365"
     - auth_url / token_url from the tenant's OAuth 2.0 endpoints.

     ### Manual entry (any SMTP server)

     When no provider is configured for the identity's email domain, the
     settings dialog falls back to manual entry: host, port, security mode,
     auth method, and credential. The PUT endpoint validates credentials via
     a live probe before persisting.
-->

## Troubleshooting

<!-- TODO docs-writer: Write a troubleshooting guide covering the following
     failure modes and their resolution steps.

     ### auth-failed state

     - The Identity badge in settings shows a warning.
     - Cause: the stored credential was rejected by the external server
       (535 response to AUTH), or an OAuth access token could not be
       refreshed (invalid_grant from the token endpoint).
     - Resolution: open the Identity settings, click the warning badge,
       and re-authenticate (OAuth) or re-enter the app-specific password.
     - For OAuth: check that the OAuth application in the provider console
       is still active and has not been revoked.
     - For password: verify the app-specific password has not been revoked
       in the external provider's security settings.

     ### unreachable state

     - Cause: DNS or TCP failure connecting to submit_host:submit_port.
     - Resolution: check network connectivity from the herold server to the
       external SMTP server. Check firewall rules for outbound port 587 / 465.

     ### OAuth token refresh not happening

     - Check that [server.external_submission].enabled = true.
     - Check that sweeper is running: `herold diag collect` includes a
       sweeper-status section (TODO: add to diag collect).
     - Check /metrics: herold_external_submission_oauth_refresh_total should
       increment on each refresh attempt.
     - Check logs for "extsubmit.sweeper:" entries at WARN or ERROR level.

     ### Credentials appearing in logs

     - Should not happen. The redaction handler (DefaultSecretKeys) covers
       access_token, refresh_token, password, token, secret, authorization.
     - If you see credential material: file a bug and redact the log file.

     ### DKIM and DMARC

     - Local DKIM signing is skipped for external-submission Identities.
     - The external server signs under its own DKIM key.
     - Operators are responsible for ensuring the external provider accepts
       the chosen From: address (Gmail "Send mail as" verification,
       Microsoft 365 send-as permissions).
-->
