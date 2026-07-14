-- 0094_mailing_list_held_post.sql -- storage for hosted mailing-list
-- moderation (v2 milestone, issue #189, REQ-MLIST-80).
--
-- A `moderated` list holds every post for an explicit owner/moderator
-- approve/reject/discard decision instead of fanning it out immediately;
-- `members-only`/`announce-only` reject a non-conforming post outright
-- (no new row -- see internal/maillist/policy.go). mailing_list_held_post
-- is that held-post row: the raw message bytes are stored via the normal
-- content-addressed blob store, referenced by blob_hash/blob_size and
-- kept alive by a caller-managed blob_refs reference (the same
-- IncRefBlob/DecRefBlob mechanism identity avatar blobs use) for as long
-- as status = 'pending'. A held post surviving a restart is exactly this
-- row existing durably, not an in-memory queue.
--
-- from_address/subject/message_id are denormalised from the held
-- message's headers so the moderation list view needs no blob read.
-- auth_results_json preserves the message's verified mailauth.AuthResults
-- so an approval can ARC-seal with the same "prior" hop the post would
-- have carried had it been allowed immediately.
--
-- idx_mailing_list_held_post_list_status backs the moderation queue read
-- (list_id, status = 'pending', ordered by id) as an index range scan.
--
-- Forward-only.

CREATE TABLE mailing_list_held_post (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  list_id           INTEGER NOT NULL REFERENCES mailing_list(id) ON DELETE CASCADE,
  blob_hash         TEXT    NOT NULL,
  blob_size         INTEGER NOT NULL,
  from_address      TEXT    NOT NULL DEFAULT '',
  subject           TEXT    NOT NULL DEFAULT '',
  message_id        TEXT    NOT NULL DEFAULT '',
  auth_results_json TEXT    NOT NULL DEFAULT '{}',
  reason            TEXT    NOT NULL DEFAULT '',
  status            TEXT    NOT NULL DEFAULT 'pending'
                      CHECK(status IN ('pending', 'approved', 'rejected', 'discarded')),
  held_at_us        INTEGER NOT NULL,
  decided_at_us     INTEGER,
  decided_by        INTEGER REFERENCES principals(id) ON DELETE SET NULL,
  decision_note     TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX idx_mailing_list_held_post_list_status
  ON mailing_list_held_post(list_id, status, id);
