-- 0114_ext_submission_relay_hold.sql -- hold an external submission for its
-- undo-send / sendAt window before dispatching to the relay (re #478).
--
-- relay_held  INTEGER NOT NULL DEFAULT 0
--   1 while an External=true row is scheduled for a future SendAtUs but has
--   not yet been claimed for relay dispatch. EmailSubmission/set destroy
--   cancels the row while this is 1 (CancelExternalRelay); the relay
--   scheduler claims it once SendAtUs elapses (ClaimExternalRelay). Both are
--   a single atomic UPDATE ... WHERE relay_held = 1, so exactly one of
--   "canceled" or "relayed" ever happens for a given row. 0 for every
--   non-External row and for an External row whose sendAt was absent or
--   already past at creation (dispatches immediately, unchanged from before
--   this migration).
--
-- The partial index lets the relay scheduler find due rows in O(held) time
-- without a full table scan.
--
-- Forward-only. Mirrors storepg 0114.

ALTER TABLE jmap_email_submissions
  ADD COLUMN relay_held INTEGER NOT NULL DEFAULT 0;

CREATE INDEX idx_jmap_email_submissions_relay_held
  ON jmap_email_submissions(send_at_us)
  WHERE relay_held = 1;
