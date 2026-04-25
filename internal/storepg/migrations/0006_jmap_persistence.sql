-- 0006_jmap_persistence.sql — persist JMAP EmailSubmission + Identity.
--
-- Mirrors storesqlite/migrations/0006_jmap_persistence.sql. Two new
-- tables with isomorphic shapes; column types map BIGINT↔INTEGER and
-- BYTEA↔BLOB per the established Wave 2 pattern.

CREATE TABLE jmap_email_submissions (
  id              TEXT    PRIMARY KEY,
  envelope_id     TEXT    NOT NULL,
  principal_id    BIGINT  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  identity_id     TEXT    NOT NULL DEFAULT '',
  email_id        BIGINT  NOT NULL DEFAULT 0,
  thread_id       TEXT    NOT NULL DEFAULT '',
  send_at_us      BIGINT  NOT NULL,
  created_at_us   BIGINT  NOT NULL,
  undo_status     TEXT    NOT NULL DEFAULT '',
  properties      BYTEA   NOT NULL DEFAULT ''::BYTEA
);

CREATE INDEX idx_jmap_email_submissions_principal_send_at
  ON jmap_email_submissions(principal_id, send_at_us);
CREATE INDEX idx_jmap_email_submissions_envelope
  ON jmap_email_submissions(envelope_id);

CREATE TABLE jmap_identities (
  id                TEXT    PRIMARY KEY,
  principal_id      BIGINT  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  name              TEXT    NOT NULL DEFAULT '',
  email             TEXT    NOT NULL,
  reply_to_json     BYTEA   NOT NULL DEFAULT ''::BYTEA,
  bcc_json          BYTEA   NOT NULL DEFAULT ''::BYTEA,
  text_signature    TEXT    NOT NULL DEFAULT '',
  html_signature    TEXT    NOT NULL DEFAULT '',
  may_delete        BOOLEAN NOT NULL DEFAULT TRUE,
  created_at_us     BIGINT  NOT NULL,
  updated_at_us     BIGINT  NOT NULL
);

CREATE INDEX idx_jmap_identities_principal
  ON jmap_identities(principal_id);
