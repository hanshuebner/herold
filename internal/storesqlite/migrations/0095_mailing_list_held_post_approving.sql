-- 0095_mailing_list_held_post_approving.sql -- widens
-- mailing_list_held_post.status to accept 'approving' (issue #189
-- verification fix, REQ-MLIST-80): a two-phase approval so a held post
-- is fanned out AT MOST ONCE under concurrent approve calls, and a
-- claimed-but-not-yet-fanned-out post is never silently lost if the
-- process dies mid-approval.
--
-- ApproveHeldPost now claims the row with an atomic CAS
-- (pending -> approving, ClaimMailingListHeldPostForApproval) BEFORE
-- running fan-out, instead of fanning out first and only then racing to
-- CAS to approved (the bug this migration fixes: two concurrent callers
-- could both pass a pre-fanout status read and both mail every member).
-- Only the caller whose CAS actually flips the row to 'approving' may
-- fan out; every other concurrent caller's CAS affects zero rows and
-- returns ErrConflict before ever calling fanOut. 'approving' is a
-- durable, visible state (not silently lost) if the process dies
-- between the claim and the finalize (approving -> approved via
-- FinalizeMailingListHeldPostApproval, which also releases the blob
-- ref): a later call to ApproveHeldPost on an 'approving' row resumes
-- it rather than erroring, and is itself concurrency-safe because
-- fan-out for a resumed approval uses a per-(held post, member)
-- idempotency key at the queue boundary, so a retried or racing resume
-- cannot double-mail a member.
--
-- SQLite cannot ALTER or DROP a CHECK constraint, so the STRICT table is
-- rebuilt, mirroring 0088_mailing_list_member_awaiting_approval.sql's
-- technique. mailing_list_held_post has no child tables, so no cascade-
-- preserving temp-table dance is needed -- only its own rows are copied
-- across.
--
-- Mirrors storepg/migrations/0095_mailing_list_held_post_approving.sql
-- (a plain constraint swap there). Forward-only.

CREATE TABLE mailing_list_held_post_new (
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
                      CHECK(status IN ('pending', 'approving', 'approved', 'rejected', 'discarded')),
  held_at_us        INTEGER NOT NULL,
  decided_at_us     INTEGER,
  decided_by        INTEGER REFERENCES principals(id) ON DELETE SET NULL,
  decision_note     TEXT    NOT NULL DEFAULT ''
) STRICT;

INSERT INTO mailing_list_held_post_new
  (id, list_id, blob_hash, blob_size, from_address, subject, message_id,
   auth_results_json, reason, status, held_at_us, decided_at_us,
   decided_by, decision_note)
SELECT
   id, list_id, blob_hash, blob_size, from_address, subject, message_id,
   auth_results_json, reason, status, held_at_us, decided_at_us,
   decided_by, decision_note
  FROM mailing_list_held_post;

DROP TABLE mailing_list_held_post;
ALTER TABLE mailing_list_held_post_new RENAME TO mailing_list_held_post;

CREATE INDEX idx_mailing_list_held_post_list_status
  ON mailing_list_held_post(list_id, status, id);
