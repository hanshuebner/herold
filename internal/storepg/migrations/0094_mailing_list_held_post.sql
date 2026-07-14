-- 0094_mailing_list_held_post.sql -- storage for hosted mailing-list
-- moderation (v2 milestone, issue #189, REQ-MLIST-80). Mirrors
-- internal/storesqlite/migrations/0094_mailing_list_held_post.sql; see
-- that file for the full rationale.
--
-- Forward-only.

CREATE TABLE mailing_list_held_post (
  id                BIGINT  GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  list_id           BIGINT  NOT NULL REFERENCES mailing_list(id) ON DELETE CASCADE,
  blob_hash         TEXT    NOT NULL,
  blob_size         BIGINT  NOT NULL,
  from_address      TEXT    NOT NULL DEFAULT '',
  subject           TEXT    NOT NULL DEFAULT '',
  message_id        TEXT    NOT NULL DEFAULT '',
  auth_results_json TEXT    NOT NULL DEFAULT '{}',
  reason            TEXT    NOT NULL DEFAULT '',
  status            TEXT    NOT NULL DEFAULT 'pending'
                      CHECK(status IN ('pending', 'approved', 'rejected', 'discarded')),
  held_at_us        BIGINT  NOT NULL,
  decided_at_us     BIGINT,
  decided_by        BIGINT  REFERENCES principals(id) ON DELETE SET NULL,
  decision_note     TEXT    NOT NULL DEFAULT ''
);

CREATE INDEX idx_mailing_list_held_post_list_status
  ON mailing_list_held_post(list_id, status, id);
