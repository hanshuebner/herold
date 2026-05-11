-- 0048_identity_verification.sql — server-driven email verification for
-- JMAP Identity rows (REQ-IDENT-01..91).
--
-- The verification flow is asynchronous: Identity/set { create } commits
-- the row with verified_at_us = NULL and the server emails a one-time
-- token + 6-digit code. Either input (link redirect on the public
-- listener, or the code entered in the suite) verifies the identity.
--
-- Mirrors storesqlite/migrations/0048_identity_verification.sql. Column
-- types map BIGINT <-> INTEGER and BYTEA <-> BLOB per the established
-- pattern.
--
-- Forward-only.

ALTER TABLE jmap_identities
  ADD COLUMN verified_at_us BIGINT;
ALTER TABLE jmap_identities
  ADD COLUMN verification_token_hash BYTEA;
ALTER TABLE jmap_identities
  ADD COLUMN verification_code_hash BYTEA;
ALTER TABLE jmap_identities
  ADD COLUMN verification_token_expires_at_us BIGINT;

CREATE INDEX IF NOT EXISTS idx_jmap_identities_verified_created
  ON jmap_identities(verified_at_us, created_at_us);
