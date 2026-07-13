-- 0088_mailing_list_member_awaiting_approval.sql -- widens
-- mailing_list_member.state to accept 'awaiting-approval' (Stage 3 self-
-- subscription, issue #185, REQ-MLIST-60..63).
--
-- A `request-approval` list's subscriber confirms the double opt-in
-- token first (proving control of the mailbox), then waits for the
-- list owner to approve. 'awaiting-approval' is that second gate, kept
-- distinct from 'pending' (REQ-MLIST-03: "awaiting double-opt-in
-- confirmation") so the operator roster view can tell the two states
-- apart -- the whole point of a request-approval list.
--
-- SQLite cannot ALTER or DROP a CHECK constraint, so the STRICT table is
-- rebuilt, mirroring 0064_imapimport_migrate_state.sql's technique.
-- mailing_list_member has no child tables (nothing references it as a
-- parent), so no cascade-preserving temp-table dance is needed here --
-- only its own rows are copied across.
--
-- Mirrors storepg/migrations/0088_mailing_list_member_awaiting_approval.sql
-- (a plain constraint swap there). Forward-only.

CREATE TABLE mailing_list_member_new (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  list_id           INTEGER NOT NULL REFERENCES mailing_list(id) ON DELETE CASCADE,
  principal_id      INTEGER REFERENCES principals(id) ON DELETE CASCADE,
  external_address  TEXT,
  state             TEXT    NOT NULL DEFAULT 'active'
                            CHECK(state IN ('active', 'suspended', 'unsubscribed', 'pending', 'awaiting-approval')),
  delivery_mode     TEXT    NOT NULL DEFAULT 'each'
                            CHECK(delivery_mode IN ('each', 'nomail')),
  bounce_score      REAL    NOT NULL DEFAULT 0,
  last_bounce_at_us INTEGER,
  added_at_us       INTEGER NOT NULL,
  added_by          INTEGER REFERENCES principals(id) ON DELETE SET NULL,
  CHECK ((principal_id IS NOT NULL AND external_address IS NULL)
      OR (principal_id IS NULL AND external_address IS NOT NULL)),
  CHECK (delivery_mode != 'nomail' OR principal_id IS NOT NULL)
) STRICT;

INSERT INTO mailing_list_member_new
  (id, list_id, principal_id, external_address, state, delivery_mode,
   bounce_score, last_bounce_at_us, added_at_us, added_by)
SELECT
   id, list_id, principal_id, external_address, state, delivery_mode,
   bounce_score, last_bounce_at_us, added_at_us, added_by
  FROM mailing_list_member;

DROP TABLE mailing_list_member;
ALTER TABLE mailing_list_member_new RENAME TO mailing_list_member;

CREATE UNIQUE INDEX idx_mailing_list_member_principal
  ON mailing_list_member(list_id, principal_id) WHERE principal_id IS NOT NULL;
CREATE UNIQUE INDEX idx_mailing_list_member_external
  ON mailing_list_member(list_id, external_address) WHERE external_address IS NOT NULL;
CREATE INDEX idx_mailing_list_member_roster
  ON mailing_list_member(list_id, state, delivery_mode, id);
