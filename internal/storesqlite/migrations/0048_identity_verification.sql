-- 0048_identity_verification.sql — server-driven email verification for
-- JMAP Identity rows (REQ-IDENT-01..91).
--
-- The verification flow is asynchronous: Identity/set { create } commits
-- the row with verified_at_us = NULL and the server emails a one-time
-- token + 6-digit code. Either input (link redirect on the public
-- listener, or the code entered in the suite) verifies the identity.
--
-- Columns added:
--   verified_at_us                    NULL when unverified; UTC micros
--                                     when verified. The synthesised
--                                     "default" identity is not stored
--                                     here, so default rows do not
--                                     appear; created rows default to
--                                     NULL.
--   verification_token_hash           sha256(raw token bytes). NULL when
--                                     no token is live (post-verify or
--                                     post-expiry GC).
--   verification_code_hash            sha256(raw 6-digit code). NULL
--                                     when no token is live. Identity-
--                                     scoped lookup is intentional: the
--                                     code may repeat across identities
--                                     because the URL carries an id.
--   verification_token_expires_at_us  NULL when no token is live; UTC
--                                     micros 24h from issue time when
--                                     a token is in flight.
--
-- The (verified_at_us, created_at_us) index supports the periodic GC
-- pass (REQ-IDENT-35) that destroys rows whose verified_at_us IS NULL
-- AND created_at_us < now - 7d. The partial form would be marginally
-- smaller but SQLite's planner cannot use a partial index for the
-- WHERE verified_at_us IS NULL predicate as cheaply; the full two-
-- column index keeps the plan deterministic.
--
-- Forward-only. Mirrors storepg 0048.

ALTER TABLE jmap_identities
  ADD COLUMN verified_at_us INTEGER;
ALTER TABLE jmap_identities
  ADD COLUMN verification_token_hash BLOB;
ALTER TABLE jmap_identities
  ADD COLUMN verification_code_hash BLOB;
ALTER TABLE jmap_identities
  ADD COLUMN verification_token_expires_at_us INTEGER;

CREATE INDEX idx_jmap_identities_verified_created
  ON jmap_identities(verified_at_us, created_at_us);
