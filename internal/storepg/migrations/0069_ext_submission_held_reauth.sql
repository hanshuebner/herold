-- 0068_ext_submission_held_reauth.sql -- hold-and-retry for external submissions
-- (re #70, REQ-AUTH-EXT-SUBMIT-05).
-- Mirrors storesqlite 0068.

ALTER TABLE jmap_email_submissions
  ADD COLUMN held_for_reauth BOOLEAN NOT NULL DEFAULT FALSE;

ALTER TABLE jmap_email_submissions
  ADD COLUMN hold_deadline_us BIGINT;

CREATE INDEX idx_jmap_email_submissions_held_reauth
  ON jmap_email_submissions(identity_id, hold_deadline_us)
  WHERE held_for_reauth;
