-- 0064_imapimport_migrate_state.sql — complete-migration cutover states
-- (issue #25, REQ-IMAP-IMP-90..96, Wave C).
--
-- The complete-migration cutover adds two lifecycle states to
-- imapimport_account.state: 'migrating' (one-shot complete backfill with
-- authority transferred to herold) and 'migrated' (terminal; herold
-- authoritative, upstream retired). The 0058 CHECK constraint only allowed
-- enabled/disabled/errored, so it must be widened.
--
-- SQLite cannot ALTER or DROP a CHECK constraint, so the STRICT table is
-- rebuilt. imapimport_account is referenced by three child tables with
-- ON DELETE CASCADE (folder_map, folder_cursor, message_state); migrations
-- run inside a transaction with foreign_keys=ON (and the pragma cannot be
-- toggled mid-transaction), so a bare DROP+rename of the parent would
-- cascade-empty those children. To preserve their rows the children are
-- copied into TEMP tables, the parent is rebuilt, and the children are
-- restored from the copies. The children's own schema and indexes are
-- untouched (only their rows are emptied by the cascade and then restored).
--
-- Mirrors storepg/migrations/0064_imapimport_migrate_state.sql (which only
-- needs a constraint swap). Forward-only.

-- Preserve child rows before the parent rebuild cascade-empties them.
CREATE TEMP TABLE _imapimport_folder_map_bak    AS SELECT * FROM imapimport_folder_map;
CREATE TEMP TABLE _imapimport_folder_cursor_bak AS SELECT * FROM imapimport_folder_cursor;
CREATE TEMP TABLE _imapimport_message_state_bak AS SELECT * FROM imapimport_message_state;

-- Rebuild imapimport_account with the widened state CHECK. Column set is the
-- post-0063 shape (0058 base + 0062 identity_id + 0063 provenance_mailbox_id).
CREATE TABLE imapimport_account_new (
  id                    TEXT    NOT NULL PRIMARY KEY,
  principal_id          INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  account_name          TEXT    NOT NULL,
  host                  TEXT    NOT NULL,
  port                  INTEGER NOT NULL,
  tls_mode              TEXT    NOT NULL CHECK(tls_mode IN ('starttls','implicit')),
  username              TEXT    NOT NULL,
  auth_method           TEXT    NOT NULL CHECK(auth_method IN ('password','app_password','xoauth2')),
  backfill_floor_date   INTEGER,
  credential_ct         BLOB    NOT NULL,
  state                 TEXT    NOT NULL DEFAULT 'enabled'
                                CHECK(state IN ('enabled','disabled','errored','migrating','migrated')),
  last_success_at       INTEGER,
  last_error            TEXT    NOT NULL DEFAULT '',
  delete_propagates     INTEGER NOT NULL DEFAULT 1,
  created_at            INTEGER NOT NULL,
  updated_at            INTEGER NOT NULL,
  identity_id           TEXT REFERENCES jmap_identities(id) ON DELETE CASCADE,
  provenance_mailbox_id INTEGER
) STRICT;

INSERT INTO imapimport_account_new
  (id, principal_id, account_name, host, port, tls_mode, username, auth_method,
   backfill_floor_date, credential_ct, state, last_success_at, last_error,
   delete_propagates, created_at, updated_at, identity_id, provenance_mailbox_id)
SELECT
   id, principal_id, account_name, host, port, tls_mode, username, auth_method,
   backfill_floor_date, credential_ct, state, last_success_at, last_error,
   delete_propagates, created_at, updated_at, identity_id, provenance_mailbox_id
  FROM imapimport_account;

DROP TABLE imapimport_account;
ALTER TABLE imapimport_account_new RENAME TO imapimport_account;

-- Restore child rows now that the FK target exists again.
INSERT INTO imapimport_folder_map    SELECT * FROM _imapimport_folder_map_bak;
INSERT INTO imapimport_folder_cursor SELECT * FROM _imapimport_folder_cursor_bak;
INSERT INTO imapimport_message_state SELECT * FROM _imapimport_message_state_bak;

DROP TABLE _imapimport_folder_map_bak;
DROP TABLE _imapimport_folder_cursor_bak;
DROP TABLE _imapimport_message_state_bak;

-- Recreate the parent-table indexes dropped with the old table (0058 + 0062).
CREATE INDEX idx_imapimport_account_principal
  ON imapimport_account(principal_id);
CREATE INDEX idx_imapimport_account_state
  ON imapimport_account(state);
CREATE UNIQUE INDEX idx_imapimport_account_identity
  ON imapimport_account(identity_id)
  WHERE identity_id IS NOT NULL;
