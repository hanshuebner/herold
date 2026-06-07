-- 0058_imap_import.sql — upstream IMAP account mirroring (issue #25,
-- REQ-IMAP-IMP-02, -15..19, -34, -42, -70, -74).
-- Mirrors storesqlite/migrations/0058_imap_import.sql.
-- Postgres idioms: BYTEA for blob, BIGINT for timestamps/integers,
-- BOOLEAN for boolean columns. principal_id is BIGINT (principals.id).
--
-- Forward-only.

CREATE TABLE imapimport_account (
  id                   TEXT    NOT NULL PRIMARY KEY,
  principal_id         BIGINT  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  account_name         TEXT    NOT NULL,
  host                 TEXT    NOT NULL,
  port                 INTEGER NOT NULL,
  tls_mode             TEXT    NOT NULL CHECK(tls_mode IN ('starttls','implicit')),
  username             TEXT    NOT NULL,
  auth_method          TEXT    NOT NULL CHECK(auth_method IN ('password','app_password','xoauth2')),
  backfill_floor_date  BIGINT,               -- unix-micros, NULL = "all / no floor"
  credential_ct        BYTEA   NOT NULL,     -- secrets.Seal output, v1: prefix enforced
  state                TEXT    NOT NULL DEFAULT 'enabled'
                                CHECK(state IN ('enabled','disabled','errored')),
  last_success_at      BIGINT,               -- unix-micros, NULL = never
  last_error           TEXT    NOT NULL DEFAULT '',
  delete_propagates    BOOLEAN NOT NULL DEFAULT TRUE,
  created_at           BIGINT  NOT NULL,
  updated_at           BIGINT  NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_imapimport_account_principal
  ON imapimport_account(principal_id);

CREATE INDEX IF NOT EXISTS idx_imapimport_account_state
  ON imapimport_account(state);

CREATE TABLE imapimport_folder_map (
  account_id          TEXT NOT NULL REFERENCES imapimport_account(id) ON DELETE CASCADE,
  upstream_folder     TEXT NOT NULL,
  herold_mailbox_name TEXT NOT NULL,
  PRIMARY KEY (account_id, upstream_folder)
);

CREATE TABLE imapimport_folder_cursor (
  account_id       TEXT   NOT NULL REFERENCES imapimport_account(id) ON DELETE CASCADE,
  upstream_folder  TEXT   NOT NULL,
  uidvalidity      BIGINT NOT NULL DEFAULT 0,
  uidnext          BIGINT NOT NULL DEFAULT 0,
  low_water_uid    BIGINT NOT NULL DEFAULT 0,
  high_water_uid   BIGINT NOT NULL DEFAULT 0,
  highest_modseq   BIGINT NOT NULL DEFAULT 0,
  PRIMARY KEY (account_id, upstream_folder)
);

CREATE TABLE imapimport_message_state (
  account_id        TEXT   NOT NULL REFERENCES imapimport_account(id) ON DELETE CASCADE,
  upstream_folder   TEXT   NOT NULL,
  upstream_uid      BIGINT NOT NULL,
  herold_message_id BIGINT NOT NULL,
  herold_mailbox_id BIGINT NOT NULL,
  last_synced_flags INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (account_id, upstream_folder, upstream_uid)
);

CREATE INDEX IF NOT EXISTS idx_imapimport_message_state_by_message
  ON imapimport_message_state(account_id, herold_message_id);
