-- 0001_init.sql — initial Postgres schema for the metadata store.
--
-- Kept isomorphic to storesqlite/migrations/0001_init.sql so the
-- SQLite <-> Postgres migration tool (REQ-STORE-83) can copy
-- row-by-row. Postgres idioms applied where useful: BIGINT identity
-- columns via GENERATED ALWAYS AS IDENTITY, BOOLEAN where SQLite uses
-- INTEGER, and TIMESTAMPTZ for timestamps. Timestamps are stored both
-- as TIMESTAMPTZ (for operator-facing inspection) and as Unix micros
-- in _us columns (for parity with SQLite payloads crossing the
-- migration tool without lossy conversion). Only the _us columns are
-- queried by the application layer; the TIMESTAMPTZ shadow is a
-- convenience for ad-hoc queries and backups.
--
-- Hashes stored hex for parity with SQLite (keeps the migration tool
-- single-path).

CREATE TABLE principals (
  id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  kind             INTEGER NOT NULL,
  canonical_email  TEXT    NOT NULL UNIQUE,
  display_name     TEXT    NOT NULL DEFAULT '',
  password_hash    TEXT    NOT NULL DEFAULT '',
  totp_secret      BYTEA,
  quota_bytes      BIGINT  NOT NULL DEFAULT 0,
  flags            BIGINT  NOT NULL DEFAULT 0,
  used_bytes       BIGINT  NOT NULL DEFAULT 0,
  created_at_us    BIGINT  NOT NULL,
  updated_at_us    BIGINT  NOT NULL
);

CREATE TABLE domains (
  name          TEXT    PRIMARY KEY,
  is_local      BOOLEAN NOT NULL,
  created_at_us BIGINT  NOT NULL
);

CREATE TABLE aliases (
  id                BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  local_part        TEXT    NOT NULL,
  domain            TEXT    NOT NULL,
  target_principal  BIGINT  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  expires_at_us     BIGINT,
  created_at_us     BIGINT  NOT NULL,
  UNIQUE(local_part, domain)
);

CREATE INDEX idx_aliases_target ON aliases(target_principal);

CREATE TABLE oidc_providers (
  name              TEXT    PRIMARY KEY,
  issuer_url        TEXT    NOT NULL,
  client_id         TEXT    NOT NULL,
  client_secret_ref TEXT    NOT NULL,
  scopes_csv        TEXT    NOT NULL DEFAULT '',
  auto_provision    BOOLEAN NOT NULL DEFAULT FALSE,
  created_at_us     BIGINT  NOT NULL
);

CREATE TABLE oidc_links (
  principal_id       BIGINT  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  provider_name      TEXT    NOT NULL REFERENCES oidc_providers(name) ON DELETE CASCADE,
  subject            TEXT    NOT NULL,
  email_at_provider  TEXT    NOT NULL DEFAULT '',
  linked_at_us       BIGINT  NOT NULL,
  PRIMARY KEY (provider_name, subject)
);

CREATE INDEX idx_oidc_links_principal ON oidc_links(principal_id);

CREATE TABLE api_keys (
  id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  principal_id    BIGINT  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  hash            TEXT    NOT NULL UNIQUE,
  name            TEXT    NOT NULL DEFAULT '',
  created_at_us   BIGINT  NOT NULL,
  last_used_at_us BIGINT  NOT NULL DEFAULT 0
);

CREATE TABLE mailboxes (
  id               BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  principal_id     BIGINT  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  parent_id        BIGINT  NOT NULL DEFAULT 0,
  name             TEXT    NOT NULL,
  attributes       BIGINT  NOT NULL DEFAULT 0,
  uidvalidity      BIGINT  NOT NULL,
  uidnext          BIGINT  NOT NULL DEFAULT 1,
  highest_modseq   BIGINT  NOT NULL DEFAULT 0,
  created_at_us    BIGINT  NOT NULL,
  updated_at_us    BIGINT  NOT NULL,
  UNIQUE(principal_id, name)
);

CREATE TABLE messages (
  id              BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  mailbox_id      BIGINT  NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
  uid             BIGINT  NOT NULL,
  modseq          BIGINT  NOT NULL,
  flags           BIGINT  NOT NULL DEFAULT 0,
  keywords_csv    TEXT    NOT NULL DEFAULT '',
  internal_date_us BIGINT NOT NULL,
  received_at_us  BIGINT  NOT NULL,
  size            BIGINT  NOT NULL,
  blob_hash       TEXT    NOT NULL,
  blob_size       BIGINT  NOT NULL,
  thread_id       BIGINT  NOT NULL DEFAULT 0,
  env_subject     TEXT    NOT NULL DEFAULT '',
  env_from        TEXT    NOT NULL DEFAULT '',
  env_to          TEXT    NOT NULL DEFAULT '',
  env_cc          TEXT    NOT NULL DEFAULT '',
  env_bcc         TEXT    NOT NULL DEFAULT '',
  env_reply_to    TEXT    NOT NULL DEFAULT '',
  env_message_id  TEXT    NOT NULL DEFAULT '',
  env_in_reply_to TEXT    NOT NULL DEFAULT '',
  env_date_us     BIGINT  NOT NULL DEFAULT 0,
  UNIQUE(mailbox_id, uid)
);

CREATE INDEX idx_messages_mailbox_modseq ON messages(mailbox_id, modseq);
CREATE INDEX idx_messages_blob_hash ON messages(blob_hash);

CREATE TABLE blob_refs (
  hash          TEXT    PRIMARY KEY,
  size          BIGINT  NOT NULL,
  ref_count     BIGINT  NOT NULL,
  last_change_us BIGINT NOT NULL
);

CREATE TABLE state_changes (
  id             BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  principal_id   BIGINT  NOT NULL,
  seq            BIGINT  NOT NULL,
  kind           INTEGER NOT NULL,
  mailbox_id     BIGINT  NOT NULL DEFAULT 0,
  message_id     BIGINT  NOT NULL DEFAULT 0,
  message_uid    BIGINT  NOT NULL DEFAULT 0,
  produced_at_us BIGINT  NOT NULL,
  UNIQUE(principal_id, seq)
);

CREATE INDEX idx_state_changes_principal_seq
  ON state_changes(principal_id, seq);

CREATE INDEX idx_state_changes_global_id
  ON state_changes(id);
