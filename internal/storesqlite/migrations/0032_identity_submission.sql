-- 0032_identity_submission.sql — per-Identity external SMTP submission config
-- (REQ-AUTH-EXT-SUBMIT-01..10).
--
-- Each jmap_identities row MAY have one associated identity_submission row
-- carrying the external SMTP endpoint configuration and encrypted credentials.
-- The FK uses ON DELETE CASCADE so removing an Identity drops its submission
-- config automatically (REQ-AUTH-EXT-SUBMIT-08).
--
-- Credential columns (password_ct, oauth_access_ct, oauth_refresh_ct,
-- oauth_client_secret_ct) store AEAD-sealed blobs produced by
-- internal/secrets.Seal; they are opaque to the store layer.
--
-- oauth_client_secret_ct is nullable and reserved for the v1.1 per-Identity
-- OAuth client; v1 uses operator-config providers, so the column is always
-- NULL in v1 deployments.
--
-- refresh_due_us drives the background token-refresh sweeper (Phase 3);
-- the partial index makes ListIdentitySubmissionsDue efficient even in large
-- deployments.
--
-- state and state_at_us carry the per-Identity submission health signal
-- (REQ-AUTH-EXT-SUBMIT-07): 'ok' | 'auth-failed' | 'unreachable'.
--
-- All timestamp columns are microseconds since Unix epoch (consistent with
-- the rest of the schema).
--
-- Forward-only.

CREATE TABLE identity_submission (
  identity_id            TEXT    PRIMARY KEY
                                 REFERENCES jmap_identities(id) ON DELETE CASCADE,
  submit_host            TEXT    NOT NULL,
  submit_port            INTEGER NOT NULL,
  submit_security        TEXT    NOT NULL,
  submit_auth_method     TEXT    NOT NULL,
  password_ct            BLOB,
  oauth_access_ct        BLOB,
  oauth_refresh_ct       BLOB,
  oauth_token_endpoint   TEXT,
  oauth_client_id        TEXT,
  oauth_client_secret_ct BLOB,
  oauth_expires_at_us    INTEGER,
  refresh_due_us         INTEGER,
  state                  TEXT    NOT NULL DEFAULT 'ok',
  state_at_us            INTEGER NOT NULL,
  created_at_us          INTEGER NOT NULL,
  updated_at_us          INTEGER NOT NULL
) STRICT;

CREATE INDEX identity_submission_refresh_due
  ON identity_submission(refresh_due_us)
  WHERE refresh_due_us IS NOT NULL;
