-- 0058_imap_import.sql — upstream IMAP account mirroring (issue #25,
-- REQ-IMAP-IMP-02, -15..19, -34, -42, -70, -74).
--
-- Four tables cover the storage foundation for the live-mirror worker:
--
--   imapimport_account        — per-principal upstream account config +
--                              sealed credential.
--   imapimport_folder_map     — folder-name translation table.
--   imapimport_folder_cursor  — per-folder IMAP sync cursors.
--   imapimport_message_state  — upstream-UID → herold-message mapping
--                              for flag/delete write-back.
--
-- Non-secret/secret column split mirrors identity_submission: plaintext
-- config fields alongside a single BLOB credential_ct column that holds
-- the secrets.Seal output (v1:-prefixed ChaCha20-Poly1305 ciphertext).
--
-- Timestamps are unix-microseconds (consistent with the rest of the
-- schema). Nullable timestamps use INTEGER NULL.
--
-- Forward-only. Mirrors storepg 0057.

CREATE TABLE imapimport_account (
  id                   TEXT    NOT NULL PRIMARY KEY,
  principal_id         INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  account_name         TEXT    NOT NULL,
  host                 TEXT    NOT NULL,
  port                 INTEGER NOT NULL,
  tls_mode             TEXT    NOT NULL CHECK(tls_mode IN ('starttls','implicit')),
  username             TEXT    NOT NULL,
  auth_method          TEXT    NOT NULL CHECK(auth_method IN ('password','app_password','xoauth2')),
  backfill_floor_date  INTEGER,            -- unix-micros, NULL = "all / no floor"
  credential_ct        BLOB    NOT NULL,   -- secrets.Seal output, v1: prefix enforced
  state                TEXT    NOT NULL DEFAULT 'enabled'
                                CHECK(state IN ('enabled','disabled','errored')),
  last_success_at      INTEGER,            -- unix-micros, NULL = never
  last_error           TEXT    NOT NULL DEFAULT '',
  delete_propagates    INTEGER NOT NULL DEFAULT 1,  -- 0/1 boolean
  created_at           INTEGER NOT NULL,
  updated_at           INTEGER NOT NULL
) STRICT;

CREATE INDEX idx_imapimport_account_principal
  ON imapimport_account(principal_id);

CREATE INDEX idx_imapimport_account_state
  ON imapimport_account(state);

-- imapimport_folder_map: (account_id, upstream_folder) → herold_mailbox_name.
-- ON DELETE CASCADE from the account so removing an account drops all
-- folder mappings atomically (REQ-IMAP-IMP-10).

CREATE TABLE imapimport_folder_map (
  account_id         TEXT NOT NULL REFERENCES imapimport_account(id) ON DELETE CASCADE,
  upstream_folder    TEXT NOT NULL,
  herold_mailbox_name TEXT NOT NULL,
  PRIMARY KEY (account_id, upstream_folder)
) STRICT;

-- imapimport_folder_cursor: per-folder IMAP sync cursors
-- (REQ-IMAP-IMP-17, -24, -34, -74).

CREATE TABLE imapimport_folder_cursor (
  account_id       TEXT    NOT NULL REFERENCES imapimport_account(id) ON DELETE CASCADE,
  upstream_folder  TEXT    NOT NULL,
  uidvalidity      INTEGER NOT NULL DEFAULT 0,
  uidnext          INTEGER NOT NULL DEFAULT 0,
  low_water_uid    INTEGER NOT NULL DEFAULT 0,
  high_water_uid   INTEGER NOT NULL DEFAULT 0,
  highest_modseq   INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (account_id, upstream_folder)
) STRICT;

-- imapimport_message_state: upstream-UID → herold message mapping
-- (REQ-IMAP-IMP-34 / REQ-IMAP-IMP-42).
-- last_synced_flags is a bitfield: bit0=\Seen, bit1=\Flagged.
--
-- The secondary index on (account_id, herold_message_id) supports the
-- write-back lookup GetIMAPImportMessageStateByMessage.

CREATE TABLE imapimport_message_state (
  account_id        TEXT    NOT NULL REFERENCES imapimport_account(id) ON DELETE CASCADE,
  upstream_folder   TEXT    NOT NULL,
  upstream_uid      INTEGER NOT NULL,
  herold_message_id INTEGER NOT NULL,  -- messages(id) type; no FK so expunge is independent
  herold_mailbox_id INTEGER NOT NULL,  -- mailboxes(id) type; no FK for same reason
  last_synced_flags INTEGER NOT NULL DEFAULT 0,
  PRIMARY KEY (account_id, upstream_folder, upstream_uid)
) STRICT;

CREATE INDEX idx_imapimport_message_state_by_message
  ON imapimport_message_state(account_id, herold_message_id);
