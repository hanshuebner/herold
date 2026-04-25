-- 0006_jmap_persistence.sql — persist JMAP EmailSubmission + Identity.
--
-- Wave 2.2.5 lifts the JMAP EmailSubmission metaTable and the Identity
-- in-process overlay onto disk so /get, /query, and /set survive a
-- server restart. Two additive tables; per-row schemas mirror the
-- Postgres 0006 migration so the cross-backend migration tool keeps
-- copying row-by-row. Forward-only; no DROP / ALTER on existing tables.
--
-- jmap_email_submissions: one row per JMAP EmailSubmission. The id
-- column is the EnvelopeID stringified (TEXT); the EnvelopeID column
-- itself is preserved so the queue → submission join is one indexed
-- lookup. principal_id, identity_id, email_id, thread_id carry the
-- metadata EmailSubmission/get renders. send_at_us is the scheduled
-- (or actual) send instant; the default sort key. undo_status carries
-- the most recent /set update transition. properties is a small JSON
-- blob for any RFC 8621 fields we do not (yet) model with their own
-- column.
CREATE TABLE jmap_email_submissions (
  id              TEXT    PRIMARY KEY,
  envelope_id     TEXT    NOT NULL,
  principal_id    INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  identity_id     TEXT    NOT NULL DEFAULT '',
  email_id        INTEGER NOT NULL DEFAULT 0,
  thread_id       TEXT    NOT NULL DEFAULT '',
  send_at_us      INTEGER NOT NULL,
  created_at_us   INTEGER NOT NULL,
  undo_status     TEXT    NOT NULL DEFAULT '',
  properties      BLOB    NOT NULL DEFAULT x''
) STRICT;

CREATE INDEX idx_jmap_email_submissions_principal_send_at
  ON jmap_email_submissions(principal_id, send_at_us);
CREATE INDEX idx_jmap_email_submissions_envelope
  ON jmap_email_submissions(envelope_id);

-- jmap_identities: persistent JMAP Identity overlay. The default
-- identity (synthesised from the principal's canonical email) is NOT
-- stored here; only operator- and user-explicitly-created rows. Custom
-- ids are the wire-form decimal strings the overlay assigned at
-- create-time. reply_to_json / bcc_json carry the optional EmailAddress
-- arrays; empty / absent means the field is unset.
CREATE TABLE jmap_identities (
  id                TEXT    PRIMARY KEY,
  principal_id      INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  name              TEXT    NOT NULL DEFAULT '',
  email             TEXT    NOT NULL,
  reply_to_json     BLOB    NOT NULL DEFAULT x'',
  bcc_json          BLOB    NOT NULL DEFAULT x'',
  text_signature    TEXT    NOT NULL DEFAULT '',
  html_signature    TEXT    NOT NULL DEFAULT '',
  may_delete        INTEGER NOT NULL DEFAULT 1,
  created_at_us     INTEGER NOT NULL,
  updated_at_us     INTEGER NOT NULL
) STRICT;

CREATE INDEX idx_jmap_identities_principal
  ON jmap_identities(principal_id);
