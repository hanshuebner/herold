-- 0114_ext_submission_relay_hold.sql -- hold an external submission for its
-- undo-send / sendAt window before dispatching to the relay (re #478).
-- Mirrors storesqlite 0114.

ALTER TABLE jmap_email_submissions
  ADD COLUMN relay_held BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX idx_jmap_email_submissions_relay_held
  ON jmap_email_submissions(send_at_us)
  WHERE relay_held;
