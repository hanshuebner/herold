-- 0068_ext_submission_held_reauth.sql -- hold-and-retry for external submissions
-- (re #70, REQ-AUTH-EXT-SUBMIT-05).
--
-- External submissions that fail with auth-failed are now parked rather than
-- immediately marked final.  Two new columns on jmap_email_submissions drive
-- the park/retry lifecycle:
--
-- held_for_reauth  INTEGER NOT NULL DEFAULT 0
--   1 when the row is parked waiting for auth recovery.  JMAP /get renders
--   undoStatus=pending and deliveryStatus.delivered=queued while this is 1.
--   Cleared by the extsubmit.Retryer when the identity returns to ok (or when
--   the hold window expires).
--
-- hold_deadline_us  INTEGER (nullable)
--   Unix-micros expiry of the hold window.  When NULL the row is not held.
--   After this instant the Retryer abandons the submission: sets held_for_reauth
--   to 0, undoStatus to 'final', delivered to 'no', and appends a diagnostic.
--   Default hold window: 72 hours from initial submission.
--
-- The partial index on (identity_id) WHERE held_for_reauth = 1 lets the
-- Retryer find all parked submissions for a given identity in O(held) time
-- without a full table scan.
--
-- Forward-only.  Mirrors storepg 0068.

ALTER TABLE jmap_email_submissions
  ADD COLUMN held_for_reauth INTEGER NOT NULL DEFAULT 0;

ALTER TABLE jmap_email_submissions
  ADD COLUMN hold_deadline_us INTEGER;

CREATE INDEX idx_jmap_email_submissions_held_reauth
  ON jmap_email_submissions(identity_id, hold_deadline_us)
  WHERE held_for_reauth = 1;
