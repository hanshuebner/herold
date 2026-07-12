-- 0085_mailing_lists.sql -- storage foundation for hosted mailing lists,
-- Stage 1 (epic #183, docs/design/server/requirements/28-mailing-lists.md
-- REQ-MLIST-01..12). Mirrors
-- internal/storesqlite/migrations/0085_mailing_lists.sql; see that file
-- for the full rationale (S2-S4 columns included now per the requirements
-- doc's stated intent; domain denormalisation; roster uniqueness and
-- streaming-scan index).

CREATE TABLE mailing_list (
  id                     BIGINT  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  principal_id           BIGINT  NOT NULL REFERENCES principals(id) ON DELETE CASCADE,
  posting_address        TEXT    NOT NULL,
  domain                 TEXT    NOT NULL,
  display_name           TEXT    NOT NULL DEFAULT '',
  owner_id               BIGINT  NOT NULL REFERENCES principals(id) ON DELETE RESTRICT,
  subject_tag            TEXT,
  arc_seal               BOOLEAN NOT NULL DEFAULT TRUE,
  posting_policy         TEXT    NOT NULL DEFAULT 'open',
  subscribe_policy       TEXT    NOT NULL DEFAULT 'closed',
  bounce_policy_json     TEXT    NOT NULL DEFAULT '{}',
  archive_mailbox_id     BIGINT  REFERENCES mailboxes(id) ON DELETE SET NULL,
  max_message_size_bytes BIGINT  NOT NULL DEFAULT 0,
  created_at_us          BIGINT  NOT NULL,
  updated_at_us          BIGINT  NOT NULL
);

CREATE UNIQUE INDEX idx_mailing_list_principal ON mailing_list(principal_id);
CREATE UNIQUE INDEX idx_mailing_list_posting_address ON mailing_list(posting_address);
CREATE INDEX idx_mailing_list_domain ON mailing_list(domain);

CREATE TABLE mailing_list_member (
  id                BIGINT  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  list_id           BIGINT  NOT NULL REFERENCES mailing_list(id) ON DELETE CASCADE,
  principal_id      BIGINT  REFERENCES principals(id) ON DELETE CASCADE,
  external_address  TEXT,
  state             TEXT    NOT NULL DEFAULT 'active'
                            CHECK(state IN ('active', 'suspended', 'unsubscribed', 'pending')),
  delivery_mode     TEXT    NOT NULL DEFAULT 'each'
                            CHECK(delivery_mode IN ('each', 'nomail')),
  bounce_score      DOUBLE PRECISION NOT NULL DEFAULT 0,
  last_bounce_at_us BIGINT,
  added_at_us       BIGINT  NOT NULL,
  added_by          BIGINT  REFERENCES principals(id) ON DELETE SET NULL,
  CHECK ((principal_id IS NOT NULL AND external_address IS NULL)
      OR (principal_id IS NULL AND external_address IS NOT NULL)),
  CHECK (delivery_mode != 'nomail' OR principal_id IS NOT NULL)
);

CREATE UNIQUE INDEX idx_mailing_list_member_principal
  ON mailing_list_member(list_id, principal_id) WHERE principal_id IS NOT NULL;
CREATE UNIQUE INDEX idx_mailing_list_member_external
  ON mailing_list_member(list_id, external_address) WHERE external_address IS NOT NULL;
CREATE INDEX idx_mailing_list_member_roster
  ON mailing_list_member(list_id, state, delivery_mode, id);
