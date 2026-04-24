-- 0001_init.sql — initial SQLite schema for the metadata store.
--
-- This schema covers the Wave 1 interface in internal/store. Shape is
-- aligned with docs/architecture/02-storage-architecture.md §Schema and
-- kept isomorphic to the Postgres migration of the same number so the
-- migration tool (Phase 2, REQ-STORE-83) can copy row-by-row. Types:
-- INTEGER PRIMARY KEY for identity columns; TEXT for strings and hex
-- hashes; INTEGER timestamps stored as Unix micros (deterministic,
-- portable, idiomatic for modernc.org/sqlite).

CREATE TABLE principals (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  kind             INTEGER NOT NULL,
  canonical_email  TEXT    NOT NULL UNIQUE,
  display_name     TEXT    NOT NULL DEFAULT '',
  password_hash    TEXT    NOT NULL DEFAULT '',
  totp_secret      BLOB,
  quota_bytes      INTEGER NOT NULL DEFAULT 0,
  flags            INTEGER NOT NULL DEFAULT 0,
  used_bytes       INTEGER NOT NULL DEFAULT 0,
  created_at_us    INTEGER NOT NULL,
  updated_at_us    INTEGER NOT NULL
) STRICT;

CREATE TABLE domains (
  name          TEXT    PRIMARY KEY,
  is_local      INTEGER NOT NULL,
  created_at_us INTEGER NOT NULL
) STRICT;

CREATE TABLE aliases (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  local_part        TEXT    NOT NULL,
  domain            TEXT    NOT NULL,
  target_principal  INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  expires_at_us     INTEGER,
  created_at_us     INTEGER NOT NULL,
  UNIQUE(local_part, domain)
) STRICT;

CREATE INDEX idx_aliases_target ON aliases(target_principal);

CREATE TABLE oidc_providers (
  name              TEXT    PRIMARY KEY,
  issuer_url        TEXT    NOT NULL,
  client_id         TEXT    NOT NULL,
  client_secret_ref TEXT    NOT NULL,
  scopes_csv        TEXT    NOT NULL DEFAULT '',
  auto_provision    INTEGER NOT NULL DEFAULT 0,
  created_at_us     INTEGER NOT NULL
) STRICT;

CREATE TABLE oidc_links (
  principal_id       INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  provider_name      TEXT    NOT NULL REFERENCES oidc_providers(name) ON DELETE CASCADE,
  subject            TEXT    NOT NULL,
  email_at_provider  TEXT    NOT NULL DEFAULT '',
  linked_at_us       INTEGER NOT NULL,
  PRIMARY KEY (provider_name, subject)
) STRICT;

CREATE INDEX idx_oidc_links_principal ON oidc_links(principal_id);

CREATE TABLE api_keys (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  principal_id    INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  hash            TEXT    NOT NULL UNIQUE,
  name            TEXT    NOT NULL DEFAULT '',
  created_at_us   INTEGER NOT NULL,
  last_used_at_us INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE TABLE mailboxes (
  id               INTEGER PRIMARY KEY AUTOINCREMENT,
  principal_id     INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  parent_id        INTEGER NOT NULL DEFAULT 0,
  name             TEXT    NOT NULL,
  attributes       INTEGER NOT NULL DEFAULT 0,
  uidvalidity      INTEGER NOT NULL,
  uidnext          INTEGER NOT NULL DEFAULT 1,
  highest_modseq   INTEGER NOT NULL DEFAULT 0,
  created_at_us    INTEGER NOT NULL,
  updated_at_us    INTEGER NOT NULL,
  UNIQUE(principal_id, name)
) STRICT;

CREATE TABLE messages (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  mailbox_id      INTEGER NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
  uid             INTEGER NOT NULL,
  modseq          INTEGER NOT NULL,
  flags           INTEGER NOT NULL DEFAULT 0,
  keywords_csv    TEXT    NOT NULL DEFAULT '',
  internal_date_us INTEGER NOT NULL,
  received_at_us  INTEGER NOT NULL,
  size            INTEGER NOT NULL,
  blob_hash       TEXT    NOT NULL,
  blob_size       INTEGER NOT NULL,
  thread_id       INTEGER NOT NULL DEFAULT 0,
  env_subject     TEXT    NOT NULL DEFAULT '',
  env_from        TEXT    NOT NULL DEFAULT '',
  env_to          TEXT    NOT NULL DEFAULT '',
  env_cc          TEXT    NOT NULL DEFAULT '',
  env_bcc         TEXT    NOT NULL DEFAULT '',
  env_reply_to    TEXT    NOT NULL DEFAULT '',
  env_message_id  TEXT    NOT NULL DEFAULT '',
  env_in_reply_to TEXT    NOT NULL DEFAULT '',
  env_date_us     INTEGER NOT NULL DEFAULT 0,
  UNIQUE(mailbox_id, uid)
) STRICT;

CREATE INDEX idx_messages_mailbox_modseq ON messages(mailbox_id, modseq);
CREATE INDEX idx_messages_blob_hash ON messages(blob_hash);

CREATE TABLE blob_refs (
  hash          TEXT    PRIMARY KEY,
  size          INTEGER NOT NULL,
  ref_count     INTEGER NOT NULL,
  last_change_us INTEGER NOT NULL
) STRICT;

CREATE TABLE state_changes (
  id             INTEGER PRIMARY KEY AUTOINCREMENT,
  principal_id   INTEGER NOT NULL,
  seq            INTEGER NOT NULL,
  kind           INTEGER NOT NULL,
  mailbox_id     INTEGER NOT NULL DEFAULT 0,
  message_id     INTEGER NOT NULL DEFAULT 0,
  message_uid    INTEGER NOT NULL DEFAULT 0,
  produced_at_us INTEGER NOT NULL,
  UNIQUE(principal_id, seq)
) STRICT;

CREATE INDEX idx_state_changes_principal_seq
  ON state_changes(principal_id, seq);

CREATE INDEX idx_state_changes_global_id
  ON state_changes(id);
