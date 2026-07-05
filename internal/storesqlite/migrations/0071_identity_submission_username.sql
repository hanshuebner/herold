-- 0071_identity_submission_username.sql
-- Adds submit_username to identity_submission so the SMTP AUTH (SASL)
-- username can differ from the identity's email address. Empty string
-- (the default) means "fall back to the identity email at auth time".
-- Not used for oauth2 (XOAUTH2 user stays the email).
--
-- Forward-only.

ALTER TABLE identity_submission ADD COLUMN submit_username TEXT NOT NULL DEFAULT '';
