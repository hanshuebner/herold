-- 0095_mailing_list_held_post_approving.sql -- widens
-- mailing_list_held_post.status to accept 'approving' (issue #189
-- verification fix, REQ-MLIST-80). Mirrors
-- internal/storesqlite/migrations/0095_mailing_list_held_post_approving.sql;
-- see that file for the full rationale. Postgres only needs to drop and
-- re-add the auto-named single-column CHECK constraint.
--
-- Forward-only.

ALTER TABLE mailing_list_held_post
  DROP CONSTRAINT mailing_list_held_post_status_check;

ALTER TABLE mailing_list_held_post
  ADD CONSTRAINT mailing_list_held_post_status_check
  CHECK (status IN ('pending', 'approving', 'approved', 'rejected', 'discarded'));
