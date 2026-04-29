-- 0033_email_submission_external.sql — add external flag to jmap_email_submissions
-- (REQ-AUTH-EXT-SUBMIT-05).
--
-- External submissions (those routed through an external SMTP endpoint instead
-- of herold's outbound queue) are distinguished by external = TRUE. Existing rows
-- default to FALSE (internal queue). The flag gates cannotUnsend on destroy and
-- signals /get that no queue rows exist for this submission.
--
-- Forward-only.

ALTER TABLE jmap_email_submissions
  ADD COLUMN external BOOLEAN NOT NULL DEFAULT FALSE;
