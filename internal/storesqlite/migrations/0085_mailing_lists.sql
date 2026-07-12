-- 0085_mailing_lists.sql -- storage foundation for hosted mailing lists,
-- Stage 1 (epic #183, docs/design/server/requirements/28-mailing-lists.md
-- REQ-MLIST-01..12; docs/design/server/architecture/14-mailing-lists.md
-- "Storage").
--
-- A list is a Group principal (REQ-MLIST-01) plus list configuration
-- (mailing_list) and a membership roster (mailing_list_member). Only Stage 1
-- semantics are exercised today (static admin-maintained roster, fan-out
-- with List-* headers); the S2-S4 columns below are included now so the
-- data model is chosen once per the requirements doc's stated intent:
--   bounce_score, last_bounce_at_us          -- S2 bounce scoring
--   subscribe_policy, member.state=pending   -- S3 self-subscription
--   archive_mailbox_id, delivery_mode=nomail -- S4 archive + non-delivery
--
-- mailing_list.domain is a denormalised, lowercased copy of the address-spec
-- domain part of posting_address, maintained by the store (never trusted
-- from the caller) so REQ-MLIST-05's domain-scoped operator listing
-- (ListMailingLists by domain) is an indexed equality lookup rather than a
-- LIKE-based suffix scan.
--
-- mailing_list_member enforces REQ-MLIST-02 in the schema, not only in Go:
--   - CHECK constraint: exactly one of principal_id / external_address.
--   - CHECK constraint: delivery_mode = 'nomail' requires principal_id
--     (REQ-MLIST-04 -- nomail members must be internal, authenticatable
--     principals).
--   - Two partial unique indexes give "a given address appears at most once
--     per list": one on (list_id, principal_id) where principal_id is set,
--     one on (list_id, external_address) where external_address is set.
-- idx_mailing_list_member_roster backs the paged, streamed roster read
-- fan-out uses (REQ-MLIST-12): a covering index on
-- (list_id, state, delivery_mode, id) makes the active+each keyset scan an
-- index range scan, never a whole-table materialisation.
--
-- Forward-only.

CREATE TABLE mailing_list (
  id                     INTEGER PRIMARY KEY AUTOINCREMENT,
  principal_id           INTEGER NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  posting_address        TEXT    NOT NULL,
  domain                 TEXT    NOT NULL,
  display_name           TEXT    NOT NULL DEFAULT '',
  owner_id               INTEGER NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
  subject_tag            TEXT,
  arc_seal               INTEGER NOT NULL DEFAULT 1,
  posting_policy         TEXT    NOT NULL DEFAULT 'open',
  subscribe_policy       TEXT    NOT NULL DEFAULT 'closed',
  bounce_policy_json     TEXT    NOT NULL DEFAULT '{}',
  archive_mailbox_id     INTEGER REFERENCES mailboxes(id) ON DELETE SET NULL,
  max_message_size_bytes INTEGER NOT NULL DEFAULT 0,
  created_at_us          INTEGER NOT NULL,
  updated_at_us          INTEGER NOT NULL
) STRICT;

CREATE UNIQUE INDEX idx_mailing_list_principal ON mailing_list(principal_id);
CREATE UNIQUE INDEX idx_mailing_list_posting_address ON mailing_list(posting_address);
CREATE INDEX idx_mailing_list_domain ON mailing_list(domain);

CREATE TABLE mailing_list_member (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  list_id           INTEGER NOT NULL REFERENCES mailing_list(id) ON DELETE CASCADE,
  principal_id      INTEGER REFERENCES principals(id) ON DELETE CASCADE,
  external_address  TEXT,
  state             TEXT    NOT NULL DEFAULT 'active'
                            CHECK(state IN ('active', 'suspended', 'unsubscribed', 'pending')),
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

CREATE UNIQUE INDEX idx_mailing_list_member_principal
  ON mailing_list_member(list_id, principal_id) WHERE principal_id IS NOT NULL;
CREATE UNIQUE INDEX idx_mailing_list_member_external
  ON mailing_list_member(list_id, external_address) WHERE external_address IS NOT NULL;
CREATE INDEX idx_mailing_list_member_roster
  ON mailing_list_member(list_id, state, delivery_mode, id);
