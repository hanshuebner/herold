-- 0032_identity_submission.sql — per-Identity external SMTP submission config
-- (REQ-AUTH-EXT-SUBMIT-01..10). Mirrors storesqlite 0032.
-- Postgres idioms applied: BYTEA instead of BLOB, BIGINT for timestamps,
-- TEXT PRIMARY KEY (jmap_identities.id is TEXT in both backends).
-- Forward-only.

CREATE TABLE identity_submission (
  identity_id            TEXT    PRIMARY KEY
                                 REFERENCES jmap_identities(id) ON DELETE CASCADE,
  submit_host            TEXT    NOT NULL,
  submit_port            INTEGER NOT NULL,
  submit_security        TEXT    NOT NULL,
  submit_auth_method     TEXT    NOT NULL,
  password_ct            BYTEA,
  oauth_access_ct        BYTEA,
  oauth_refresh_ct       BYTEA,
  oauth_token_endpoint   TEXT,
  oauth_client_id        TEXT,
  oauth_client_secret_ct BYTEA,
  oauth_expires_at_us    BIGINT,
  refresh_due_us         BIGINT,
  state                  TEXT    NOT NULL DEFAULT 'ok',
  state_at_us            BIGINT  NOT NULL,
  created_at_us          BIGINT  NOT NULL,
  updated_at_us          BIGINT  NOT NULL
);

CREATE INDEX identity_submission_refresh_due
  ON identity_submission(refresh_due_us)
  WHERE refresh_due_us IS NOT NULL;
